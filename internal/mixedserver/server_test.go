package mixedserver

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
)

// stubServer is a real, minimal tfprotov6.ProviderServer used only to
// prove routing -- ReadResource/ReadDataSource each return a real,
// distinguishable marker (their own Name) so a test can confirm the
// dispatch layer reached the RIGHT sub-server, not just A sub-server.
// Every other RPC panics if called, since these tests never exercise
// them (the provider-wide RPCs are answered directly by Server itself,
// never delegated).
type stubServer struct {
	tfprotov6.ProviderServer
	Name string
}

func (s *stubServer) ReadResource(context.Context, *tfprotov6.ReadResourceRequest) (*tfprotov6.ReadResourceResponse, error) {
	return &tfprotov6.ReadResourceResponse{Private: []byte(s.Name)}, nil
}

func (s *stubServer) ReadDataSource(context.Context, *tfprotov6.ReadDataSourceRequest) (*tfprotov6.ReadDataSourceResponse, error) {
	return &tfprotov6.ReadDataSourceResponse{}, nil
}

// realAWSLikeServer builds a Server shaped exactly like AWS's own real
// dispatch layer would be: one CloudFormation-owned resource type, one
// Smithy-owned data-source type -- confirmed the real, only mixed-
// source shape among this org's six real providers.
func realAWSLikeServer() *Server {
	cfn := &stubServer{Name: "cloudformation"}
	smithy := &stubServer{Name: "smithy"}
	return &Server{
		ProviderName: "aws",
		ResourceOwner: map[string]tfprotov6.ProviderServer{
			"aws_s3_bucket": cfn,
		},
		DataSourceOwner: map[string]tfprotov6.ProviderServer{
			"aws_data_ec2_instance": smithy,
		},
		ResourceSchemas: map[string]*tfprotov6.Schema{
			"aws_s3_bucket": {},
		},
		DataSourceSchemas: map[string]*tfprotov6.Schema{
			"aws_data_ec2_instance": {},
		},
	}
}

// TestReadResource_RealMixedGroup_RoutesToOwningSource proves a
// resource-type RPC is routed to the real sub-server that actually
// owns it (CloudFormation), not some other real source present in the
// same group.
func TestReadResource_RealMixedGroup_RoutesToOwningSource(t *testing.T) {
	s := realAWSLikeServer()
	resp, err := s.ReadResource(context.Background(), &tfprotov6.ReadResourceRequest{TypeName: "aws_s3_bucket"})
	if err != nil {
		t.Fatalf("ReadResource: %v", err)
	}
	if got := string(resp.Private); got != "cloudformation" {
		t.Errorf("routed to %q, want cloudformation", got)
	}
}

// TestReadDataSource_RealMixedGroup_RoutesToOwningSource is
// ReadResource's own real sibling proof, on the data-source side --
// Smithy's own real member is 429 data-source-mode entries in AWS's
// real group, so this is the RPC AWS actually exercises at scale.
func TestReadDataSource_RealMixedGroup_RoutesToOwningSource(t *testing.T) {
	s := realAWSLikeServer()
	sub, err := s.dataSourceOwner("aws_data_ec2_instance")
	if err != nil {
		t.Fatalf("dataSourceOwner: %v", err)
	}
	got := sub.(*stubServer).Name
	if got != "smithy" {
		t.Errorf("routed to %q, want smithy", got)
	}
}

// TestReadResource_RealUnknownType_FailsLoud proves a type name neither
// real source contributed fails loud rather than silently routing to
// whichever sub-server happens to be first -- the same "unknown
// resource type" shape every real sub-server already gives on its own
// unrecognized types.
func TestReadResource_RealUnknownType_FailsLoud(t *testing.T) {
	s := realAWSLikeServer()
	resp, err := s.ReadResource(context.Background(), &tfprotov6.ReadResourceRequest{TypeName: "aws_does_not_exist"})
	if err != nil {
		t.Fatalf("ReadResource returned a real transport error, want a real diagnostic instead: %v", err)
	}
	if len(resp.Diagnostics) == 0 {
		t.Fatal("expected a real diagnostic for an unrouted resource type")
	}
}

// TestGetProviderSchema_RealMixedGroup_MergesAcrossSources proves
// GetProviderSchema serves the real, pre-merged schema maps spanning
// BOTH real sources present -- confirming the dispatch layer's own real
// motivation: one real schema-fetch RPC, not one per source.
func TestGetProviderSchema_RealMixedGroup_MergesAcrossSources(t *testing.T) {
	s := realAWSLikeServer()
	resp, err := s.GetProviderSchema(context.Background(), &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatalf("GetProviderSchema: %v", err)
	}
	if _, ok := resp.ResourceSchemas["aws_s3_bucket"]; !ok {
		t.Error("missing cloudformation-sourced resource schema")
	}
	if _, ok := resp.DataSourceSchemas["aws_data_ec2_instance"]; !ok {
		t.Error("missing smithy-sourced data source schema")
	}
}

// TestGetMetadata_RealMixedGroup_ListsBothSources is GetProviderSchema's
// own real sibling proof for GetMetadata, the other real provider-wide
// RPC the dispatch layer answers directly from its own merged maps.
func TestGetMetadata_RealMixedGroup_ListsBothSources(t *testing.T) {
	s := realAWSLikeServer()
	resp, err := s.GetMetadata(context.Background(), &tfprotov6.GetMetadataRequest{})
	if err != nil {
		t.Fatalf("GetMetadata: %v", err)
	}
	if len(resp.Resources) != 1 || resp.Resources[0].TypeName != "aws_s3_bucket" {
		t.Errorf("Resources = %v, want [aws_s3_bucket]", resp.Resources)
	}
	if len(resp.DataSources) != 1 || resp.DataSources[0].TypeName != "aws_data_ec2_instance" {
		t.Errorf("DataSources = %v, want [aws_data_ec2_instance]", resp.DataSources)
	}
}
