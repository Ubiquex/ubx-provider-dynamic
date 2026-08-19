// Package server is a real tfprotov6.ProviderServer for
// schema_source = "cloudformation" resources -- execution via AWS Cloud
// Control API (internal/cloudformation/ccapi), mirroring
// internal/smithy/server's own identical real precedent (a genuinely
// different execution model gets its own small server, not shoehorned
// into dynserver.Server's REST-path-shaped CRUD -- see that package's
// own doc comment for the full rationale, which applies here unchanged).
//
// Real, deliberate scope narrowing, flagged here rather than silently
// matched to dynserver.Server: no field-level drift/normalize policy
// (internal/dynserver/policy.go) for CFN resources yet -- not part of
// this checkpoint's own explicit scope (schema + CCAPI execution). Update
// only ever emits a real, top-level-only RFC 6902 JSON Patch (buildPatch
// below) -- a nested object's own inner field changing produces a
// top-level "replace" of that whole object, not a deep per-field patch;
// CCAPI's own real handlers treat a replace of an object property as a
// normal update in every real resource this checkpoint verified against,
// so this is a real, working, if not maximally minimal, patch.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/ubiquex/ubx-provider-dynamic/internal/cloudformation"
	"github.com/ubiquex/ubx-provider-dynamic/internal/cloudformation/ccapi"
	"github.com/ubiquex/ubx-provider-dynamic/internal/restexec"
	"github.com/ubiquex/ubx-provider-dynamic/internal/wire"
)

// Server is the real tfprotov6.ProviderServer this binary serves when
// schema_source = "cloudformation", wired to Build's own discovered/
// translated resources (internal/cloudformation) and a real ccapi.Client.
type Server struct {
	ProviderName string
	Resources    map[string]*cloudformation.BuiltResource
	CCAPI        *ccapi.Client

	// PollInterval/PollTimeout govern AwaitTerminal's own real poll loop
	// (ccapi.Client.AwaitTerminal) -- sane, fixed defaults (5s/10m) are
	// applied by New if left zero; a real, future refinement could make
	// these configurable per [dynamic_providers.<name>.timeouts], the
	// identical real config surface internal/dynserver already exposes,
	// not done this checkpoint (named, not hidden).
	PollInterval time.Duration
	PollTimeout  time.Duration
}

// New returns a ready Server with real, sane poll defaults applied.
func New(providerName string, resources map[string]*cloudformation.BuiltResource, client *ccapi.Client) *Server {
	return &Server{
		ProviderName: providerName,
		Resources:    resources,
		CCAPI:        client,
		PollInterval: 5 * time.Second,
		PollTimeout:  10 * time.Minute,
	}
}

var plannedPrivateMarker = []byte("ubx-provider-dynamic-cloudformation-planned")

var _ tfprotov6.ProviderServer = (*Server)(nil)

func (s *Server) resourceType(typeName string) (*cloudformation.BuiltResource, error) {
	rt, ok := s.Resources[typeName]
	if !ok {
		return nil, fmt.Errorf("unknown resource type %q", typeName)
	}
	return rt, nil
}

func diagError(summary, detail string) []*tfprotov6.Diagnostic {
	return []*tfprotov6.Diagnostic{{Severity: tfprotov6.DiagnosticSeverityError, Summary: summary, Detail: detail}}
}

// classifyError mirrors dynserver's and smithy/server's own identical
// real classifyRESTError/classifyError -- restated here (the identical
// restexec error type regardless of which real wire protocol produced
// it) rather than importing an unexported helper across a package
// boundary.
func classifyError(op string, err error) (diags []*tfprotov6.Diagnostic, ambiguous error) {
	if restexec.IsTerminal(err) {
		return diagError(op, err.Error()), nil
	}
	return nil, fmt.Errorf("%s: %w", op, err)
}

// --- Provider-level RPCs ---

func (s *Server) GetMetadata(context.Context, *tfprotov6.GetMetadataRequest) (*tfprotov6.GetMetadataResponse, error) {
	names := make([]string, 0, len(s.Resources))
	for name := range s.Resources {
		names = append(names, name)
	}
	sort.Strings(names)
	meta := make([]tfprotov6.ResourceMetadata, 0, len(names))
	for _, name := range names {
		meta = append(meta, tfprotov6.ResourceMetadata{TypeName: name})
	}
	return &tfprotov6.GetMetadataResponse{Resources: meta}, nil
}

func (s *Server) GetProviderSchema(context.Context, *tfprotov6.GetProviderSchemaRequest) (*tfprotov6.GetProviderSchemaResponse, error) {
	resourceSchemas := make(map[string]*tfprotov6.Schema, len(s.Resources))
	for name, rt := range s.Resources {
		resourceSchemas[name] = rt.Schema
	}
	return &tfprotov6.GetProviderSchemaResponse{
		Provider:        &tfprotov6.Schema{Version: 1, Block: &tfprotov6.SchemaBlock{}},
		ResourceSchemas: resourceSchemas,
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

// --- Resource RPCs ---

func (s *Server) ValidateResourceConfig(_ context.Context, req *tfprotov6.ValidateResourceConfigRequest) (*tfprotov6.ValidateResourceConfigResponse, error) {
	if _, err := s.resourceType(req.TypeName); err != nil {
		return &tfprotov6.ValidateResourceConfigResponse{Diagnostics: diagError("unknown resource type", err.Error())}, nil
	}
	return &tfprotov6.ValidateResourceConfigResponse{}, nil
}

func (s *Server) UpgradeResourceState(_ context.Context, req *tfprotov6.UpgradeResourceStateRequest) (*tfprotov6.UpgradeResourceStateResponse, error) {
	rt, err := s.resourceType(req.TypeName)
	if err != nil {
		return &tfprotov6.UpgradeResourceStateResponse{Diagnostics: diagError("unknown resource type", err.Error())}, nil
	}
	v, err := req.RawState.UnmarshalWithOpts(rt.ObjectType, tfprotov6.UnmarshalOpts{ValueFromJSONOpts: tftypes.ValueFromJSONOpts{IgnoreUndefinedAttributes: true}})
	if err != nil {
		return &tfprotov6.UpgradeResourceStateResponse{Diagnostics: diagError("upgrade resource state", err.Error())}, nil
	}
	dv, err := tfprotov6.NewDynamicValue(rt.ObjectType, v)
	if err != nil {
		return &tfprotov6.UpgradeResourceStateResponse{Diagnostics: diagError("upgrade resource state", err.Error())}, nil
	}
	return &tfprotov6.UpgradeResourceStateResponse{UpgradedState: &dv}, nil
}

func (s *Server) ImportResourceState(ctx context.Context, req *tfprotov6.ImportResourceStateRequest) (*tfprotov6.ImportResourceStateResponse, error) {
	rt, err := s.resourceType(req.TypeName)
	if err != nil {
		return &tfprotov6.ImportResourceStateResponse{Diagnostics: diagError("unknown resource type", err.Error())}, nil
	}

	newVal, diags, ambiguous := s.readFromAPI(ctx, rt, req.ID)
	if ambiguous != nil {
		return nil, ambiguous
	}
	if diags != nil {
		return &tfprotov6.ImportResourceStateResponse{Diagnostics: diags}, nil
	}
	if newVal == nil {
		return &tfprotov6.ImportResourceStateResponse{Diagnostics: diagError("resource not found", fmt.Sprintf("no %s found for import ID %q", req.TypeName, req.ID))}, nil
	}
	dv, err := tfprotov6.NewDynamicValue(rt.ObjectType, *newVal)
	if err != nil {
		return &tfprotov6.ImportResourceStateResponse{Diagnostics: diagError("encode imported state", err.Error())}, nil
	}
	return &tfprotov6.ImportResourceStateResponse{
		ImportedResources: []*tfprotov6.ImportedResource{{TypeName: req.TypeName, State: &dv}},
	}, nil
}

func (s *Server) ReadResource(ctx context.Context, req *tfprotov6.ReadResourceRequest) (*tfprotov6.ReadResourceResponse, error) {
	rt, err := s.resourceType(req.TypeName)
	if err != nil {
		return &tfprotov6.ReadResourceResponse{Diagnostics: diagError("unknown resource type", err.Error())}, nil
	}
	current, err := req.CurrentState.Unmarshal(rt.ObjectType)
	if err != nil {
		return &tfprotov6.ReadResourceResponse{Diagnostics: diagError("decode current state", err.Error())}, nil
	}
	identifier, err := extractIdentifier(current, rt)
	if err != nil {
		return &tfprotov6.ReadResourceResponse{Diagnostics: diagError("read resource", err.Error())}, nil
	}

	newVal, diags, ambiguous := s.readFromAPI(ctx, rt, identifier)
	if ambiguous != nil {
		return nil, ambiguous
	}
	if diags != nil {
		return &tfprotov6.ReadResourceResponse{Diagnostics: diags}, nil
	}
	if newVal == nil {
		return &tfprotov6.ReadResourceResponse{}, nil
	}

	dv, err := tfprotov6.NewDynamicValue(rt.ObjectType, *newVal)
	if err != nil {
		return &tfprotov6.ReadResourceResponse{Diagnostics: diagError("encode new state", err.Error())}, nil
	}
	return &tfprotov6.ReadResourceResponse{NewState: &dv}, nil
}

// readFromAPI performs a real GetResource call and decodes its real
// Properties JSON into a tftypes.Value shaped by rt's own real
// ObjectType -- mirrors dynserver's and smithy/server's own identical
// real (nil,nil,nil)/(nil,diags,nil)/(nil,nil,err) contract.
func (s *Server) readFromAPI(ctx context.Context, rt *cloudformation.BuiltResource, identifier string) (*tftypes.Value, []*tfprotov6.Diagnostic, error) {
	propsJSON, err := s.CCAPI.GetResource(ctx, rt.TypeName, identifier)
	if err != nil {
		if restexec.IsNotFound(err) {
			return nil, nil, nil
		}
		diags, ambiguous := classifyError("read resource", err)
		return nil, diags, ambiguous
	}
	var raw any
	dec := json.NewDecoder(strings.NewReader(string(propsJSON)))
	dec.UseNumber()
	if err := dec.Decode(&raw); err != nil {
		return nil, diagError("decode CCAPI response", err.Error()), nil
	}
	snakeRaw := rekeyToSnake(raw, rt.WireNames)
	v, err := wire.FromJSON(snakeRaw, rt.ObjectType)
	if err != nil {
		return nil, diagError("decode CCAPI response into schema", err.Error()), nil
	}
	return &v, nil, nil
}

func (s *Server) PlanResourceChange(_ context.Context, req *tfprotov6.PlanResourceChangeRequest) (*tfprotov6.PlanResourceChangeResponse, error) {
	if _, err := s.resourceType(req.TypeName); err != nil {
		return &tfprotov6.PlanResourceChangeResponse{Diagnostics: diagError("unknown resource type", err.Error())}, nil
	}
	return &tfprotov6.PlanResourceChangeResponse{
		PlannedState:   req.ProposedNewState,
		PlannedPrivate: plannedPrivateMarker,
	}, nil
}

func (s *Server) ApplyResourceChange(ctx context.Context, req *tfprotov6.ApplyResourceChangeRequest) (*tfprotov6.ApplyResourceChangeResponse, error) {
	rt, err := s.resourceType(req.TypeName)
	if err != nil {
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diagError("unknown resource type", err.Error())}, nil
	}
	priorNull, err := req.PriorState.IsNull()
	if err != nil {
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diagError("decode prior state", err.Error())}, nil
	}
	plannedNull, err := req.PlannedState.IsNull()
	if err != nil {
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diagError("decode planned state", err.Error())}, nil
	}
	switch {
	case plannedNull:
		return s.applyDestroy(ctx, rt, req)
	case priorNull:
		return s.applyCreate(ctx, rt, req)
	default:
		return s.applyUpdate(ctx, rt, req)
	}
}

func (s *Server) applyCreate(ctx context.Context, rt *cloudformation.BuiltResource, req *tfprotov6.ApplyResourceChangeRequest) (*tfprotov6.ApplyResourceChangeResponse, error) {
	planned, err := req.PlannedState.Unmarshal(rt.ObjectType)
	if err != nil {
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diagError("decode planned state", err.Error())}, nil
	}
	desiredJSON, err := desiredStateJSON(planned, rt)
	if err != nil {
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diagError("create resource", err.Error())}, nil
	}

	pe, err := s.CCAPI.CreateResource(ctx, rt.TypeName, desiredJSON)
	if err != nil {
		diags, ambiguous := classifyError("create resource", err)
		if ambiguous != nil {
			return nil, ambiguous
		}
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diags}, nil
	}

	final, diags, ambiguous := s.finalizeAfterWrite(ctx, rt, pe, planned)
	if ambiguous != nil {
		return nil, ambiguous
	}
	if diags != nil {
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diags}, nil
	}
	dv, err := tfprotov6.NewDynamicValue(rt.ObjectType, final)
	if err != nil {
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diagError("encode new state", err.Error())}, nil
	}
	return &tfprotov6.ApplyResourceChangeResponse{NewState: &dv}, nil
}

func (s *Server) applyUpdate(ctx context.Context, rt *cloudformation.BuiltResource, req *tfprotov6.ApplyResourceChangeRequest) (*tfprotov6.ApplyResourceChangeResponse, error) {
	prior, err := req.PriorState.Unmarshal(rt.ObjectType)
	if err != nil {
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diagError("decode prior state", err.Error())}, nil
	}
	planned, err := req.PlannedState.Unmarshal(rt.ObjectType)
	if err != nil {
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diagError("decode planned state", err.Error())}, nil
	}
	identifier, err := extractIdentifier(prior, rt)
	if err != nil {
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diagError("update resource", err.Error())}, nil
	}
	patchJSON, err := buildPatch(prior, planned, rt)
	if err != nil {
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diagError("update resource", err.Error())}, nil
	}
	if patchJSON == "[]" {
		dv, err := tfprotov6.NewDynamicValue(rt.ObjectType, planned)
		if err != nil {
			return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diagError("encode new state", err.Error())}, nil
		}
		return &tfprotov6.ApplyResourceChangeResponse{NewState: &dv}, nil
	}

	pe, err := s.CCAPI.UpdateResource(ctx, rt.TypeName, identifier, patchJSON)
	if err != nil {
		diags, ambiguous := classifyError("update resource", err)
		if ambiguous != nil {
			return nil, ambiguous
		}
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diags}, nil
	}

	final, diags, ambiguous := s.finalizeAfterWrite(ctx, rt, pe, planned)
	if ambiguous != nil {
		return nil, ambiguous
	}
	if diags != nil {
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diags}, nil
	}
	dv, err := tfprotov6.NewDynamicValue(rt.ObjectType, final)
	if err != nil {
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diagError("encode new state", err.Error())}, nil
	}
	return &tfprotov6.ApplyResourceChangeResponse{NewState: &dv}, nil
}

func (s *Server) applyDestroy(ctx context.Context, rt *cloudformation.BuiltResource, req *tfprotov6.ApplyResourceChangeRequest) (*tfprotov6.ApplyResourceChangeResponse, error) {
	if len(req.PlannedPrivate) == 0 {
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diagError(
			"destroy called without PlannedPrivate",
			"a real destroy requires a prior PlanResourceChange call -- this provider refuses to silently no-op",
		)}, nil
	}
	prior, err := req.PriorState.Unmarshal(rt.ObjectType)
	if err != nil {
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diagError("decode prior state", err.Error())}, nil
	}
	identifier, err := extractIdentifier(prior, rt)
	if err != nil {
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diagError("destroy resource", err.Error())}, nil
	}

	pe, err := s.CCAPI.DeleteResource(ctx, rt.TypeName, identifier)
	if err != nil {
		diags, ambiguous := classifyError("destroy resource", err)
		if ambiguous != nil {
			return nil, ambiguous
		}
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diags}, nil
	}
	if pe != nil && pe.RequestToken != "" {
		final, err := s.CCAPI.AwaitTerminal(ctx, pe.RequestToken, s.PollInterval, s.PollTimeout)
		if err != nil {
			return nil, fmt.Errorf("destroy resource: %w", err)
		}
		if final.OperationStatus == "FAILED" {
			return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diagError("destroy resource failed", final.StatusMessage)}, nil
		}
	}
	return &tfprotov6.ApplyResourceChangeResponse{}, nil
}

// finalizeAfterWrite polls pe's own real RequestToken to a real terminal
// status (ccapi.Client.AwaitTerminal), then performs one real GetResource
// read for the final, authoritative property values -- CCAPI's own real
// ProgressEvent.ResourceModel is sometimes present but not guaranteed
// complete (confirmed against the real botocore docs: "may be available
// before the resource operation has reached SUCCESS"), so a real read
// afterward, not the progress event's own body, is this package's real
// source of truth, mirroring dynserver's and smithy/server's own
// identical real "read after write" discipline.
func (s *Server) finalizeAfterWrite(ctx context.Context, rt *cloudformation.BuiltResource, pe *ccapi.ProgressEvent, planned tftypes.Value) (tftypes.Value, []*tfprotov6.Diagnostic, error) {
	if pe == nil || pe.RequestToken == "" {
		return planned, nil, nil
	}
	final, err := s.CCAPI.AwaitTerminal(ctx, pe.RequestToken, s.PollInterval, s.PollTimeout)
	if err != nil {
		return tftypes.Value{}, nil, err
	}
	if final.OperationStatus == "FAILED" {
		return tftypes.Value{}, diagError("resource operation failed", final.StatusMessage), nil
	}

	identifier := final.Identifier
	if identifier == "" {
		var err error
		identifier, err = extractIdentifier(planned, rt)
		if err != nil {
			return tftypes.Value{}, diagError("resolve identifier after write", err.Error()), nil
		}
	}
	newVal, diags, ambiguous := s.readFromAPI(ctx, rt, identifier)
	if ambiguous != nil {
		return tftypes.Value{}, nil, ambiguous
	}
	if diags != nil {
		return tftypes.Value{}, diags, nil
	}
	if newVal == nil {
		return tftypes.Value{}, diagError("resource operation completed", "the resource could not be found by a real read afterward"), nil
	}
	return *newVal, nil, nil
}

// --- Not supported (advertised capabilities are all false/zero) ---

func (s *Server) MoveResourceState(context.Context, *tfprotov6.MoveResourceStateRequest) (*tfprotov6.MoveResourceStateResponse, error) {
	return &tfprotov6.MoveResourceStateResponse{Diagnostics: diagError("not supported", "this provider does not support MoveResourceState")}, nil
}

func (s *Server) UpgradeResourceIdentity(context.Context, *tfprotov6.UpgradeResourceIdentityRequest) (*tfprotov6.UpgradeResourceIdentityResponse, error) {
	return &tfprotov6.UpgradeResourceIdentityResponse{Diagnostics: diagError("not supported", "this provider does not use resource identity")}, nil
}

func (s *Server) GenerateResourceConfig(context.Context, *tfprotov6.GenerateResourceConfigRequest) (*tfprotov6.GenerateResourceConfigResponse, error) {
	return &tfprotov6.GenerateResourceConfigResponse{Diagnostics: diagError("not supported", "this provider does not support GenerateResourceConfig")}, nil
}

func (s *Server) ValidateDataResourceConfig(context.Context, *tfprotov6.ValidateDataResourceConfigRequest) (*tfprotov6.ValidateDataResourceConfigResponse, error) {
	return &tfprotov6.ValidateDataResourceConfigResponse{Diagnostics: diagError("not supported", "this provider defines no data sources")}, nil
}

func (s *Server) ReadDataSource(context.Context, *tfprotov6.ReadDataSourceRequest) (*tfprotov6.ReadDataSourceResponse, error) {
	return &tfprotov6.ReadDataSourceResponse{Diagnostics: diagError("not supported", "this provider defines no data sources")}, nil
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
