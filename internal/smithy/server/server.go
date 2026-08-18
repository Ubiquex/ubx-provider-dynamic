// Package server is a real tfprotov6.ProviderServer for Smithy-sourced
// resources -- UBI-158 Phase 4 Checkpoint 2's own "serving a Smithy-backed
// resource through the real tfplugin RPC surface" deliverable, the piece
// build.go's own doc comment (Checkpoint 1) explicitly deferred to this
// checkpoint.
//
// Deliberately a separate, smaller server from dynserver.Server (Phase
// 1-3's OpenAPI-sourced provider), not a generalization of it: dynserver's
// own CRUD fields (ReadPath, CreateMethod, PathParams, ...) are REST-path-
// shaped, meaningful only for restexec.Client's own single, fixed "JSON
// body over an OpenAPI path template" protocol. A Smithy-sourced resource's
// real execution instead dispatches through wireexec.Client.Do, keyed by
// operation shapeID, protocol-generic by construction (see wireexec's own
// doc comment) -- reusing dynserver's own real CRUD RPC methods verbatim
// would need retrofitting every one of them to accept a pluggable
// invocation seam, a deeper refactor than this checkpoint's own explicit
// scope ("wire protocols + SigV4") calls for. What IS reused, unchanged: the
// wire.go tftypes<->JSON conversion Phase 1 already built, and
// restexec.IsNotFound/IsTerminal for error classification -- Phase 3's own
// real ambiguous-vs-terminal contract, unchanged regardless of which real
// wire protocol produced the failure.
//
// Real, deliberate scope narrowing versus dynserver.Server, flagged here
// rather than silently matched: this server does not (yet) implement
// Phase 3's own async-operation polling or field-level drift/normalize
// policy for Smithy resources -- neither was part of this checkpoint's own
// explicit scope (wire protocols + SigV4), and every real verification
// target (SQS's own real CRUD operations) completes synchronously. A future
// checkpoint can add both by threading a wireexec-based invoker through the
// identical async.go/policy.go logic dynserver.Server already has, once a
// real Smithy-sourced async AWS operation is the concrete target to build
// against.
package server

import (
	"context"
	"fmt"
	"sort"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/ubiquex/ubx-provider-dynamic/internal/restexec"
	"github.com/ubiquex/ubx-provider-dynamic/internal/smithy"
	"github.com/ubiquex/ubx-provider-dynamic/internal/smithy/wireexec"
	"github.com/ubiquex/ubx-provider-dynamic/internal/wire"
)

// Server is the real tfprotov6.ProviderServer this binary serves when
// schema_source = "smithy", wired to Build's own discovered/translated
// resources (build.go) and a real wireexec.Client.
type Server struct {
	ProviderName string
	Resources    map[string]*smithy.BuiltResource
	Model        *smithy.Model
	Wire         *wireexec.Client
}

var plannedPrivateMarker = []byte("ubx-provider-dynamic-smithy-planned")

var _ tfprotov6.ProviderServer = (*Server)(nil)

func (s *Server) resourceType(typeName string) (*smithy.BuiltResource, error) {
	rt, ok := s.Resources[typeName]
	if !ok {
		return nil, fmt.Errorf("unknown resource type %q", typeName)
	}
	return rt, nil
}

func diagError(summary, detail string) []*tfprotov6.Diagnostic {
	return []*tfprotov6.Diagnostic{{Severity: tfprotov6.DiagnosticSeverityError, Summary: summary, Detail: detail}}
}

// classifyError mirrors dynserver's own identical real classifyRESTError
// (Phase 3) -- see its own doc comment for the full rationale. Restated
// here (not imported from dynserver) because dynserver's version is
// unexported and this server's own real error shape is the identical
// restexec error type regardless of which real wire protocol produced it
// (wireexec.Client.Do always returns errors through restexec's own
// *APIError/network-error wrapping, unchanged from Phase 1-3).
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

	idFields := wireexec.InputMemberNames(s.Model, rt.ReadOperationID)
	params, err := splitImportID(req.ID, idFields)
	if err != nil {
		return &tfprotov6.ImportResourceStateResponse{Diagnostics: diagError("invalid import ID", err.Error())}, nil
	}

	newVal, diags, ambiguous := s.readFromAPI(ctx, rt, params)
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
	params, err := stateAsMap(current)
	if err != nil {
		return &tfprotov6.ReadResourceResponse{Diagnostics: diagError("read resource", err.Error())}, nil
	}

	newVal, diags, ambiguous := s.readFromAPI(ctx, rt, params)
	if ambiguous != nil {
		return nil, ambiguous
	}
	if diags != nil {
		return &tfprotov6.ReadResourceResponse{Diagnostics: diags}, nil
	}
	if newVal == nil {
		return &tfprotov6.ReadResourceResponse{}, nil
	}

	merged, err := mergeCarryForward(*newVal, current, wireexec.InputMemberNames(s.Model, rt.ReadOperationID))
	if err != nil {
		return &tfprotov6.ReadResourceResponse{Diagnostics: diagError("merge read result", err.Error())}, nil
	}
	dv, err := tfprotov6.NewDynamicValue(rt.ObjectType, merged)
	if err != nil {
		return &tfprotov6.ReadResourceResponse{Diagnostics: diagError("encode new state", err.Error())}, nil
	}
	return &tfprotov6.ReadResourceResponse{NewState: &dv}, nil
}

// readFromAPI performs the real read, mirroring dynserver's own identical
// real (nil,nil,nil)/(nil,diags,nil)/(nil,nil,err) contract -- see
// dynserver.Server.readFromAPI's own doc comment for why these three
// outcomes are kept distinct.
func (s *Server) readFromAPI(ctx context.Context, rt *smithy.BuiltResource, params map[string]any) (*tftypes.Value, []*tfprotov6.Diagnostic, error) {
	_, body, _, err := s.Wire.Do(ctx, rt.ReadOperationID, params)
	if err != nil {
		if restexec.IsNotFound(err) {
			return nil, nil, nil
		}
		diags, ambiguous := classifyError("read resource", err)
		return nil, diags, ambiguous
	}
	v, err := wire.FromJSON(body, rt.ObjectType)
	if err != nil {
		return nil, diagError("decode API response", err.Error()), nil
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

func (s *Server) applyCreate(ctx context.Context, rt *smithy.BuiltResource, req *tfprotov6.ApplyResourceChangeRequest) (*tfprotov6.ApplyResourceChangeResponse, error) {
	planned, err := req.PlannedState.Unmarshal(rt.ObjectType)
	if err != nil {
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diagError("decode planned state", err.Error())}, nil
	}
	params, err := stateAsMap(planned)
	if err != nil {
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diagError("create resource", err.Error())}, nil
	}

	_, body, _, err := s.Wire.Do(ctx, rt.CreateOperationID, params)
	if err != nil {
		diags, ambiguous := classifyError("create resource", err)
		if ambiguous != nil {
			return nil, ambiguous
		}
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diags}, nil
	}

	newVal, err := wire.FromJSON(body, rt.ObjectType)
	if err != nil {
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diagError("decode create response", err.Error())}, nil
	}
	merged, err := mergeCarryForward(newVal, planned, wireexec.InputMemberNames(s.Model, rt.CreateOperationID))
	if err != nil {
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diagError("merge create result", err.Error())}, nil
	}
	dv, err := tfprotov6.NewDynamicValue(rt.ObjectType, merged)
	if err != nil {
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diagError("encode new state", err.Error())}, nil
	}
	return &tfprotov6.ApplyResourceChangeResponse{NewState: &dv}, nil
}

func (s *Server) applyUpdate(ctx context.Context, rt *smithy.BuiltResource, req *tfprotov6.ApplyResourceChangeRequest) (*tfprotov6.ApplyResourceChangeResponse, error) {
	if rt.UpdateOperationID == "" {
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diagError(
			"resource has no update operation",
			fmt.Sprintf("%s was discovered with no Update/Modify/Put/Set-shaped operation -- this resource is create/delete-only", rt.HashiCorpName),
		)}, nil
	}
	planned, err := req.PlannedState.Unmarshal(rt.ObjectType)
	if err != nil {
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diagError("decode planned state", err.Error())}, nil
	}
	params, err := stateAsMap(planned)
	if err != nil {
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diagError("update resource", err.Error())}, nil
	}

	_, body, _, err := s.Wire.Do(ctx, rt.UpdateOperationID, params)
	if err != nil {
		diags, ambiguous := classifyError("update resource", err)
		if ambiguous != nil {
			return nil, ambiguous
		}
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diags}, nil
	}

	var newVal tftypes.Value
	if body != nil {
		newVal, err = wire.FromJSON(body, rt.ObjectType)
		if err != nil {
			return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diagError("decode update response", err.Error())}, nil
		}
	} else {
		// A real, common AWS convention (e.g. SQS's own SetQueueAttributes):
		// an update operation returns no body at all -- planned is already
		// the real, correct new state in that case.
		newVal = planned
	}
	merged, err := mergeCarryForward(newVal, planned, wireexec.InputMemberNames(s.Model, rt.UpdateOperationID))
	if err != nil {
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diagError("merge update result", err.Error())}, nil
	}
	dv, err := tfprotov6.NewDynamicValue(rt.ObjectType, merged)
	if err != nil {
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diagError("encode new state", err.Error())}, nil
	}
	return &tfprotov6.ApplyResourceChangeResponse{NewState: &dv}, nil
}

func (s *Server) applyDestroy(ctx context.Context, rt *smithy.BuiltResource, req *tfprotov6.ApplyResourceChangeRequest) (*tfprotov6.ApplyResourceChangeResponse, error) {
	if len(req.PlannedPrivate) == 0 {
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diagError(
			"destroy called without PlannedPrivate",
			"a real destroy requires a prior PlanResourceChange call -- this provider refuses to silently no-op",
		)}, nil
	}
	if rt.DeleteOperationID == "" {
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diagError(
			"resource has no delete operation",
			fmt.Sprintf("%s was discovered with no Delete-shaped operation -- this resource cannot be destroyed through this provider", rt.HashiCorpName),
		)}, nil
	}
	prior, err := req.PriorState.Unmarshal(rt.ObjectType)
	if err != nil {
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diagError("decode prior state", err.Error())}, nil
	}
	params, err := stateAsMap(prior)
	if err != nil {
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diagError("destroy resource", err.Error())}, nil
	}

	_, _, _, err = s.Wire.Do(ctx, rt.DeleteOperationID, params)
	if err != nil && !restexec.IsNotFound(err) {
		diags, ambiguous := classifyError("destroy resource", err)
		if ambiguous != nil {
			return nil, ambiguous
		}
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diags}, nil
	}
	return &tfprotov6.ApplyResourceChangeResponse{}, nil
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
