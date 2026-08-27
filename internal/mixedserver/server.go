// Package mixedserver is UBI-193's own real dispatch layer: a real
// tfprotov6.ProviderServer that routes each RPC to whichever REAL
// sub-server (dynserver.Server, cfnserver.Server, smithyserver.Server)
// actually owns the resource or data-source type in question, instead
// of merging their internals into one shared representation.
//
// Confirmed directly before building, not assumed: dynserver.Server,
// cfnserver.Server (internal/cloudformation/server), and
// smithyserver.Server (internal/smithy/server) already implement the
// IDENTICAL tfprotov6.ProviderServer interface, method-for-method --
// the "shared abstraction" a dispatch layer needs already exists, it's
// the interface itself. Every provider-wide RPC (ValidateProviderConfig,
// ConfigureProvider, StopProvider, GetResourceIdentitySchemas,
// GenerateResourceConfig, CallFunction, GetFunctions, the four
// ephemeral-resource RPCs) is confirmed byte-identical (modulo one real
// comment) across all three real server types today -- this package
// answers those directly, with the identical real response, rather than
// delegating to an arbitrarily-chosen sub-server.
//
// Real, live motivation, not hypothetical: AWS's own real group
// (repo_name "aws") is the ONLY real group among this org's six real
// providers whose own members span more than one real SchemaSource (one
// CloudFormation resource member, 429 Smithy data-source members) --
// confirmed by checking every real group's own member composition
// directly before building this. GroupSchemaSource's own real refusal
// (ErrMixedSchemaSourceGroup) is correct and stays -- it protects every
// existing Merge<Source>Group from ever needing to know about mixing;
// this package is what a caller reaches for INSTEAD, once it already
// has one real sub-server per real source built via
// Snapshot.SubsetBySource + the matching Merge<Source>Group.
package mixedserver

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
)

func diagError(summary, detail string) []*tfprotov6.Diagnostic {
	return []*tfprotov6.Diagnostic{{Severity: tfprotov6.DiagnosticSeverityError, Summary: summary, Detail: detail}}
}

// Server is the real dispatch layer. ResourceOwner/DataSourceOwner are
// real, pre-built routing tables (typeName -> the real sub-server that
// actually owns it) -- built once, at construction, from the SAME real
// collision-checked merge (Snapshot.MergeMixedSourceSchemas) that also
// produces ResourceSchemas/DataSourceSchemas, so a type can never be
// routed to a different sub-server than the one whose schema is being
// served for it.
type Server struct {
	ProviderName string

	ResourceOwner     map[string]tfprotov6.ProviderServer
	DataSourceOwner   map[string]tfprotov6.ProviderServer
	ResourceSchemas   map[string]*tfprotov6.Schema
	DataSourceSchemas map[string]*tfprotov6.Schema
}

var _ tfprotov6.ProviderServer = (*Server)(nil)

func (s *Server) resourceOwner(typeName string) (tfprotov6.ProviderServer, error) {
	sub, ok := s.ResourceOwner[typeName]
	if !ok {
		return nil, fmt.Errorf("unknown resource type %q", typeName)
	}
	return sub, nil
}

func (s *Server) dataSourceOwner(typeName string) (tfprotov6.ProviderServer, error) {
	sub, ok := s.DataSourceOwner[typeName]
	if !ok {
		return nil, fmt.Errorf("unknown data source type %q", typeName)
	}
	return sub, nil
}

// --- Provider-level RPCs: answered directly, the SAME real, trivial
// response every one of the three real sub-server types already gives
// (confirmed identical before building this, not assumed) -- no
// delegation, no ambiguity about which sub-server would even own these. ---

func (s *Server) GetMetadata(context.Context, *tfprotov6.GetMetadataRequest) (*tfprotov6.GetMetadataResponse, error) {
	resources := make([]tfprotov6.ResourceMetadata, 0, len(s.ResourceSchemas))
	for typeName := range s.ResourceSchemas {
		resources = append(resources, tfprotov6.ResourceMetadata{TypeName: typeName})
	}
	dataSources := make([]tfprotov6.DataSourceMetadata, 0, len(s.DataSourceSchemas))
	for typeName := range s.DataSourceSchemas {
		dataSources = append(dataSources, tfprotov6.DataSourceMetadata{TypeName: typeName})
	}
	return &tfprotov6.GetMetadataResponse{Resources: resources, DataSources: dataSources}, nil
}

func (s *Server) GetProviderSchema(context.Context, *tfprotov6.GetProviderSchemaRequest) (*tfprotov6.GetProviderSchemaResponse, error) {
	return &tfprotov6.GetProviderSchemaResponse{
		Provider:          &tfprotov6.Schema{Version: 1, Block: &tfprotov6.SchemaBlock{}},
		ResourceSchemas:   s.ResourceSchemas,
		DataSourceSchemas: s.DataSourceSchemas,
	}, nil
}

func (s *Server) GetResourceIdentitySchemas(context.Context, *tfprotov6.GetResourceIdentitySchemasRequest) (*tfprotov6.GetResourceIdentitySchemasResponse, error) {
	return &tfprotov6.GetResourceIdentitySchemasResponse{}, nil
}

func (s *Server) ValidateProviderConfig(_ context.Context, req *tfprotov6.ValidateProviderConfigRequest) (*tfprotov6.ValidateProviderConfigResponse, error) {
	return &tfprotov6.ValidateProviderConfigResponse{PreparedConfig: req.Config}, nil
}

func (s *Server) ConfigureProvider(context.Context, *tfprotov6.ConfigureProviderRequest) (*tfprotov6.ConfigureProviderResponse, error) {
	return &tfprotov6.ConfigureProviderResponse{}, nil
}

func (s *Server) StopProvider(context.Context, *tfprotov6.StopProviderRequest) (*tfprotov6.StopProviderResponse, error) {
	return &tfprotov6.StopProviderResponse{}, nil
}

func (s *Server) GenerateResourceConfig(context.Context, *tfprotov6.GenerateResourceConfigRequest) (*tfprotov6.GenerateResourceConfigResponse, error) {
	return &tfprotov6.GenerateResourceConfigResponse{Diagnostics: diagError("not supported", "this provider does not support GenerateResourceConfig")}, nil
}

func (s *Server) CallFunction(context.Context, *tfprotov6.CallFunctionRequest) (*tfprotov6.CallFunctionResponse, error) {
	return &tfprotov6.CallFunctionResponse{Error: &tfprotov6.FunctionError{Text: "this provider defines no functions"}}, nil
}

func (s *Server) GetFunctions(context.Context, *tfprotov6.GetFunctionsRequest) (*tfprotov6.GetFunctionsResponse, error) {
	return &tfprotov6.GetFunctionsResponse{}, nil
}

func (s *Server) ValidateEphemeralResourceConfig(context.Context, *tfprotov6.ValidateEphemeralResourceConfigRequest) (*tfprotov6.ValidateEphemeralResourceConfigResponse, error) {
	return &tfprotov6.ValidateEphemeralResourceConfigResponse{Diagnostics: diagError("not supported", "this provider defines no ephemeral resources")}, nil
}

func (s *Server) OpenEphemeralResource(context.Context, *tfprotov6.OpenEphemeralResourceRequest) (*tfprotov6.OpenEphemeralResourceResponse, error) {
	return &tfprotov6.OpenEphemeralResourceResponse{Diagnostics: diagError("not supported", "this provider defines no ephemeral resources")}, nil
}

func (s *Server) RenewEphemeralResource(context.Context, *tfprotov6.RenewEphemeralResourceRequest) (*tfprotov6.RenewEphemeralResourceResponse, error) {
	return &tfprotov6.RenewEphemeralResourceResponse{Diagnostics: diagError("not supported", "this provider defines no ephemeral resources")}, nil
}

func (s *Server) CloseEphemeralResource(context.Context, *tfprotov6.CloseEphemeralResourceRequest) (*tfprotov6.CloseEphemeralResourceResponse, error) {
	return &tfprotov6.CloseEphemeralResourceResponse{Diagnostics: diagError("not supported", "this provider defines no ephemeral resources")}, nil
}

// --- Per-type RPCs: routed to whichever real sub-server actually owns
// req.TypeName, via the SAME real routing table GetProviderSchema's own
// ResourceSchemas/DataSourceSchemas were built alongside -- a type this
// dispatch layer doesn't recognize at all fails loud here, the same
// "unknown resource type" shape every real sub-server already gives on
// its own unrecognized types. ---

func (s *Server) ValidateResourceConfig(ctx context.Context, req *tfprotov6.ValidateResourceConfigRequest) (*tfprotov6.ValidateResourceConfigResponse, error) {
	sub, err := s.resourceOwner(req.TypeName)
	if err != nil {
		return &tfprotov6.ValidateResourceConfigResponse{Diagnostics: diagError("routing", err.Error())}, nil
	}
	return sub.ValidateResourceConfig(ctx, req)
}

func (s *Server) UpgradeResourceState(ctx context.Context, req *tfprotov6.UpgradeResourceStateRequest) (*tfprotov6.UpgradeResourceStateResponse, error) {
	sub, err := s.resourceOwner(req.TypeName)
	if err != nil {
		return &tfprotov6.UpgradeResourceStateResponse{Diagnostics: diagError("routing", err.Error())}, nil
	}
	return sub.UpgradeResourceState(ctx, req)
}

func (s *Server) ImportResourceState(ctx context.Context, req *tfprotov6.ImportResourceStateRequest) (*tfprotov6.ImportResourceStateResponse, error) {
	sub, err := s.resourceOwner(req.TypeName)
	if err != nil {
		return &tfprotov6.ImportResourceStateResponse{Diagnostics: diagError("routing", err.Error())}, nil
	}
	return sub.ImportResourceState(ctx, req)
}

func (s *Server) ReadResource(ctx context.Context, req *tfprotov6.ReadResourceRequest) (*tfprotov6.ReadResourceResponse, error) {
	sub, err := s.resourceOwner(req.TypeName)
	if err != nil {
		return &tfprotov6.ReadResourceResponse{Diagnostics: diagError("routing", err.Error())}, nil
	}
	return sub.ReadResource(ctx, req)
}

func (s *Server) PlanResourceChange(ctx context.Context, req *tfprotov6.PlanResourceChangeRequest) (*tfprotov6.PlanResourceChangeResponse, error) {
	sub, err := s.resourceOwner(req.TypeName)
	if err != nil {
		return &tfprotov6.PlanResourceChangeResponse{Diagnostics: diagError("routing", err.Error())}, nil
	}
	return sub.PlanResourceChange(ctx, req)
}

func (s *Server) ApplyResourceChange(ctx context.Context, req *tfprotov6.ApplyResourceChangeRequest) (*tfprotov6.ApplyResourceChangeResponse, error) {
	sub, err := s.resourceOwner(req.TypeName)
	if err != nil {
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diagError("routing", err.Error())}, nil
	}
	return sub.ApplyResourceChange(ctx, req)
}

func (s *Server) MoveResourceState(ctx context.Context, req *tfprotov6.MoveResourceStateRequest) (*tfprotov6.MoveResourceStateResponse, error) {
	sub, err := s.resourceOwner(req.TargetTypeName)
	if err != nil {
		return &tfprotov6.MoveResourceStateResponse{Diagnostics: diagError("routing", err.Error())}, nil
	}
	return sub.MoveResourceState(ctx, req)
}

func (s *Server) UpgradeResourceIdentity(ctx context.Context, req *tfprotov6.UpgradeResourceIdentityRequest) (*tfprotov6.UpgradeResourceIdentityResponse, error) {
	sub, err := s.resourceOwner(req.TypeName)
	if err != nil {
		return &tfprotov6.UpgradeResourceIdentityResponse{Diagnostics: diagError("routing", err.Error())}, nil
	}
	return sub.UpgradeResourceIdentity(ctx, req)
}

func (s *Server) ValidateDataResourceConfig(ctx context.Context, req *tfprotov6.ValidateDataResourceConfigRequest) (*tfprotov6.ValidateDataResourceConfigResponse, error) {
	sub, err := s.dataSourceOwner(req.TypeName)
	if err != nil {
		return &tfprotov6.ValidateDataResourceConfigResponse{Diagnostics: diagError("routing", err.Error())}, nil
	}
	return sub.ValidateDataResourceConfig(ctx, req)
}

func (s *Server) ReadDataSource(ctx context.Context, req *tfprotov6.ReadDataSourceRequest) (*tfprotov6.ReadDataSourceResponse, error) {
	sub, err := s.dataSourceOwner(req.TypeName)
	if err != nil {
		return &tfprotov6.ReadDataSourceResponse{Diagnostics: diagError("routing", err.Error())}, nil
	}
	return sub.ReadDataSource(ctx, req)
}
