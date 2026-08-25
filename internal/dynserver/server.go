package dynserver

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/ubiquex/ubx-provider-dynamic/internal/restexec"
)

// withOperationTimeout bounds one real HTTP call to d, independent of
// ubx core's own ambient --ship deadline (docs/executor.md: core sets no
// per-RPC timeout of its own around ApplyResourceChange/ReadResource --
// this is UBI-158 Phase 3's own answer to that gap). d<=0 (Timeouts
// resolved from a zero-value config.TimeoutsConfig, e.g. in a test that
// constructs a Server directly rather than via Build) means "no
// additional bound," deferring entirely to ctx's own existing deadline.
func withOperationTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, d)
}

// Server is the real tfprotov6.ProviderServer this binary serves over
// tf6server.Serve -- the tfplugin server layer (layer 1), wired to the
// resource types Build produced (layers 2-3) and a real restexec.Client
// (layer 4/6).
type Server struct {
	ProviderName string
	Resources    map[string]*ResourceType
	// DataSources is UBI-186's own real data-source-serving field,
	// populated only by a [dynamic_providers.<name>] entry with
	// data_sources = true (main.go's own discoverydoc dispatch --
	// shared unchanged by the openapi dispatch below it, both funnel
	// through this same Server). Always nil/empty for an ordinary
	// resource-serving entry. GetProviderSchema is the only real RPC
	// this field is ever read by; every entry here is typically a
	// zero-valued embedded resourcemap.Resource with only Schema set
	// (mirroring how a real *ResourceType built for the RESOURCE branch
	// already gets constructed the identical way, since that RPC reads
	// ONLY .Schema, confirmed by direct inspection, nothing else).
	DataSources map[string]*ResourceType
	Client      *restexec.Client

	// PlannedPrivateMarker is echoed by PlanResourceChange and required
	// (non-empty) by ApplyResourceChange on a destroy -- see
	// PlanResourceChange's own doc comment. Not a real secret, just a
	// presence sentinel, matching the exact real-provider requirement
	// ubiquex's own fakeprovider fixture was built to enforce (UBI-30:
	// skipping PlanResourceChange makes a real provider's shim silently
	// no-op a destroy instead of actually deleting anything).
}

var plannedPrivateMarker = []byte("ubx-provider-dynamic-planned")

var _ tfprotov6.ProviderServer = (*Server)(nil)

func (s *Server) resourceType(typeName string) (*ResourceType, error) {
	rt, ok := s.Resources[typeName]
	if !ok {
		return nil, fmt.Errorf("unknown resource type %q", typeName)
	}
	return rt, nil
}

func diagError(summary, detail string) []*tfprotov6.Diagnostic {
	return []*tfprotov6.Diagnostic{{Severity: tfprotov6.DiagnosticSeverityError, Summary: summary, Detail: detail}}
}

// classifyRESTError translates a restexec.Client.Do error into exactly one
// of: a terminal Diagnostic (a real, structured rejection -- safe to
// report, since ubx core's own docs/executor.md taxonomy never retries a
// Diagnostic-bearing response within one ship invocation, cli/stateadapter.go's
// real implementation of that rule), or a plain, propagated Go error
// (ambiguous -- returned as-is so THIS RPC method's own (nil, err) return
// reaches ubx core exactly the way a dropped connection or context
// deadline would, triggering reconcile-by-query rather than a guess).
//
// This is the load-bearing seam UBI-158 Phase 3 exists to get right: every
// earlier Phase 1/2 call site wrapped every restexec failure as a
// Diagnostic unconditionally, which meant a merely transient failure (a
// 503, a dropped connection) got classified exactly like a real, certain
// rejection -- ubx core would never retry it, never verify ground truth,
// just mark the resource failed on a guess. See restexec.IsTerminal's own
// doc comment for why "not terminal" is the deliberate default whenever
// genuinely uncertain.
func classifyRESTError(op string, err error) (diags []*tfprotov6.Diagnostic, ambiguous error) {
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

	dsNames := make([]string, 0, len(s.DataSources))
	for name := range s.DataSources {
		dsNames = append(dsNames, name)
	}
	sort.Strings(dsNames)
	dsMeta := make([]tfprotov6.DataSourceMetadata, 0, len(dsNames))
	for _, name := range dsNames {
		dsMeta = append(dsMeta, tfprotov6.DataSourceMetadata{TypeName: name})
	}

	return &tfprotov6.GetMetadataResponse{Resources: meta, DataSources: dsMeta}, nil
}

func (s *Server) GetProviderSchema(context.Context, *tfprotov6.GetProviderSchemaRequest) (*tfprotov6.GetProviderSchemaResponse, error) {
	resourceSchemas := make(map[string]*tfprotov6.Schema, len(s.Resources))
	for name, rt := range s.Resources {
		resourceSchemas[name] = rt.Schema
	}
	dataSourceSchemas := make(map[string]*tfprotov6.Schema, len(s.DataSources))
	for name, rt := range s.DataSources {
		dataSourceSchemas[name] = rt.Schema
	}
	return &tfprotov6.GetProviderSchemaResponse{
		// Empty provider-level config block: this binary's own real
		// configuration comes from .ubx/config at process startup (see
		// internal/config's own doc comment on why GetProviderSchema
		// can't wait for a ConfigureProvider-delivered config to exist),
		// not from an HCL provider block Terraform/ubx would populate
		// here.
		Provider:          &tfprotov6.Schema{Version: 1, Block: &tfprotov6.SchemaBlock{}},
		ResourceSchemas:   resourceSchemas,
		DataSourceSchemas: dataSourceSchemas,
	}, nil
}

func (s *Server) GetResourceIdentitySchemas(context.Context, *tfprotov6.GetResourceIdentitySchemasRequest) (*tfprotov6.GetResourceIdentitySchemasResponse, error) {
	return &tfprotov6.GetResourceIdentitySchemasResponse{}, nil
}

func (s *Server) ValidateProviderConfig(_ context.Context, req *tfprotov6.ValidateProviderConfigRequest) (*tfprotov6.ValidateProviderConfigResponse, error) {
	return &tfprotov6.ValidateProviderConfigResponse{PreparedConfig: req.Config}, nil
}

func (s *Server) ConfigureProvider(context.Context, *tfprotov6.ConfigureProviderRequest) (*tfprotov6.ConfigureProviderResponse, error) {
	// Real auth wiring is Phase 2 (UBI-158's own explicit scope) -- this
	// binary is already fully configured from .ubx/config by the time
	// main.go builds the Server, so there is nothing left to do here.
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

// UpgradeResourceState is a no-op: every schema this binary ever produces
// is Version 1 (translated fresh from the OpenAPI spec's own current
// shape each run, with no schema history of its own to migrate between) --
// this exists only to satisfy the interface, matching Version: 1 in
// build.go.
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

	// Real, documented convention (Phase 1's own choice, no precedent to
	// follow since read paths carry an arbitrary number of params): the
	// import ID is every read-path param value, joined by "/" in the same
	// order BuildPath expects them -- "owner/repo" for a resource read at
	// /repos/{owner}/{repo}. A real, common shape for composite-key
	// Terraform resources, not invented whole-cloth for this provider.
	params, err := splitImportID(req.ID, rt.PathParams)
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

	// Known, real Phase 1 gap, not silently papered over: a create-only
	// path attribute (e.g. "org" on a resource created at
	// /orgs/{org}/repos but read at /repos/{owner}/{repo}) has no source
	// to carry forward from on import -- there is no prior state, and the
	// read response never mentions it either. It comes back null here,
	// even though the schema marks it Required. Resources whose Create
	// and Read paths share every param (the common case) are unaffected.
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

	params, err := extractStringAttrs(current, rt.PathParams, rt.PathParamAttr)
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
		// Genuinely gone -- real provider convention: ReadResource
		// returns an empty (no NewState) response, matching
		// tfprotov6.ReadResourceResponse's own documented not-found shape.
		return &tfprotov6.ReadResourceResponse{}, nil
	}

	normalized, err := applyNormalizers(*newVal, rt.Drift.Normalize)
	if err != nil {
		return &tfprotov6.ReadResourceResponse{Diagnostics: diagError("normalize read result", err.Error())}, nil
	}

	// carryForwardFields, not just rt.PathParams: a create-only path
	// attribute (e.g. "org" on a resource read at /repos/{owner}/{repo}
	// but created at /orgs/{org}/repos) is never part of any read
	// response either -- it must keep being carried forward from state
	// on every read, not just at create/update, or it would silently go
	// null the first time this resource is refreshed. Drift.Ignore fields
	// ride the identical mechanism (UBI-158 Phase 3).
	merged, err := mergeCarryForward(normalized, current, carryForwardFields(rt))
	if err != nil {
		return &tfprotov6.ReadResourceResponse{Diagnostics: diagError("merge read result", err.Error())}, nil
	}
	dv, err := tfprotov6.NewDynamicValue(rt.ObjectType, merged)
	if err != nil {
		return &tfprotov6.ReadResourceResponse{Diagnostics: diagError("encode new state", err.Error())}, nil
	}
	return &tfprotov6.ReadResourceResponse{NewState: &dv}, nil
}

// readFromAPI performs the real GET, returning (nil, nil, nil) for a
// genuine 404 (not-found is not an error here, it's ReadResource's own
// real "this resource is gone" signal), (nil, diags, nil) for a terminal
// failure, and (nil, nil, err) for an ambiguous one -- see
// classifyRESTError's own doc comment for why these are kept distinct
// rather than all becoming Diagnostics.
func (s *Server) readFromAPI(ctx context.Context, rt *ResourceType, params map[string]string) (*tftypes.Value, []*tfprotov6.Diagnostic, error) {
	ctx, cancel := withOperationTimeout(ctx, rt.Timeouts.Read)
	defer cancel()

	path, err := restexec.BuildPath(rt.ReadPath, params)
	if err != nil {
		return nil, diagError("build read request", err.Error()), nil
	}
	_, body, _, err := s.Client.Do(ctx, "GET", path, nil)
	if err != nil {
		if restexec.IsNotFound(err) {
			return nil, nil, nil
		}
		diags, ambiguous := classifyRESTError("read resource", err)
		return nil, diags, ambiguous
	}
	v, err := valueFromResponse(body, rt.ObjectType)
	if err != nil {
		return nil, diagError("decode API response", err.Error()), nil
	}
	return &v, nil, nil
}

// PlanResourceChange is deliberately minimal, matching the same real
// contract fakeprovider's own PlanResourceChange fixture (ubiquex's
// provider/internal/fakeprovider, UBI-30) was built to enforce against
// real provider binaries: echo ProposedNewState back unchanged (this
// provider computes nothing extra at plan time -- Computed attributes
// resolve to their real value only once ApplyResourceChange actually calls
// the API) and always set PlannedPrivate to a non-empty marker, since a
// real destroy's own ApplyResourceChange call requires it non-empty --
// skipping this is exactly the bug UBI-30 found live against a real AWS
// provider (a destroy silently no-oped instead of deleting anything).
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

func (s *Server) applyCreate(ctx context.Context, rt *ResourceType, req *tfprotov6.ApplyResourceChangeRequest) (*tfprotov6.ApplyResourceChangeResponse, error) {
	planned, err := req.PlannedState.Unmarshal(rt.ObjectType)
	if err != nil {
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diagError("decode planned state", err.Error())}, nil
	}

	params, err := extractStringAttrs(planned, rt.CreatePathParams, rt.CreatePathParamAttr)
	if err != nil {
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diagError("create resource", fmt.Sprintf("resolving %s: %s", rt.CreatePath, err.Error()))}, nil
	}
	path, err := restexec.BuildPath(rt.CreatePath, params)
	if err != nil {
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diagError("build create request", err.Error())}, nil
	}

	body, err := requestBody(planned, resolveAttrNames(rt.CreatePathParams, rt.CreatePathParamAttr))
	if err != nil {
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diagError("encode create request body", err.Error())}, nil
	}

	doCtx, cancel := withOperationTimeout(ctx, rt.Timeouts.Create)
	_, respBody, respHeader, err := s.Client.Do(doCtx, rt.CreateMethod, path, body)
	cancel()
	if err != nil {
		diags, ambiguous := classifyRESTError("create resource", err)
		if ambiguous != nil {
			// Real, load-bearing consequence of ship.go's own
			// retryCreateOnDependencyNotVisible (docs/executor.md, UBI-92):
			// that mechanism only ever fires on a TERMINAL, not-found-shaped
			// diagnostic -- an ambiguous error here (this branch) instead
			// leaves the resource at unknown_post_timeout for a human or a
			// future `ubx ship` re-run to resolve, exactly like ship.go's
			// own documented "create has no lookup key to reconcile against
			// yet" limitation already accepts for every other ambiguous
			// create failure. This provider does not try to do better than
			// core already does here.
			return nil, ambiguous
		}
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diags}, nil
	}

	newVal, err := valueFromResponse(respBody, rt.ObjectType)
	if err != nil {
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diagError("decode create response", err.Error())}, nil
	}
	normalized, err := applyNormalizers(newVal, rt.Drift.Normalize)
	if err != nil {
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diagError("normalize create result", err.Error())}, nil
	}
	merged, err := mergeCarryForward(normalized, planned, carryForwardFields(rt))
	if err != nil {
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diagError("merge create result", err.Error())}, nil
	}

	final, diags, err := s.finalizeAfterWrite(ctx, rt, respBody, respHeader, merged)
	if err != nil {
		return nil, err
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

func (s *Server) applyUpdate(ctx context.Context, rt *ResourceType, req *tfprotov6.ApplyResourceChangeRequest) (*tfprotov6.ApplyResourceChangeResponse, error) {
	if rt.UpdateOperation == nil {
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diagError(
			"resource has no update operation",
			fmt.Sprintf("%s was discovered with no PATCH/PUT on %s -- this resource is create/delete-only in the source OpenAPI spec (see resourcemap's own Notes); an in-place update was requested anyway", rt.TypeName, rt.ReadPath),
		)}, nil
	}

	planned, err := req.PlannedState.Unmarshal(rt.ObjectType)
	if err != nil {
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diagError("decode planned state", err.Error())}, nil
	}

	params, err := extractStringAttrs(planned, rt.PathParams, rt.PathParamAttr)
	if err != nil {
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diagError("update resource", err.Error())}, nil
	}
	path, err := restexec.BuildPath(rt.ReadPath, params)
	if err != nil {
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diagError("build update request", err.Error())}, nil
	}

	body, err := requestBody(planned, resolveAttrNames(rt.PathParams, rt.PathParamAttr))
	if err != nil {
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diagError("encode update request body", err.Error())}, nil
	}

	doCtx, cancel := withOperationTimeout(ctx, rt.Timeouts.Update)
	_, respBody, respHeader, err := s.Client.Do(doCtx, rt.UpdateMethod, path, body)
	cancel()
	if err != nil {
		diags, ambiguous := classifyRESTError("update resource", err)
		if ambiguous != nil {
			return nil, ambiguous
		}
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diags}, nil
	}

	newVal, err := valueFromResponse(respBody, rt.ObjectType)
	if err != nil {
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diagError("decode update response", err.Error())}, nil
	}
	normalized, err := applyNormalizers(newVal, rt.Drift.Normalize)
	if err != nil {
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diagError("normalize update result", err.Error())}, nil
	}
	merged, err := mergeCarryForward(normalized, planned, carryForwardFields(rt))
	if err != nil {
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diagError("merge update result", err.Error())}, nil
	}

	final, diags, err := s.finalizeAfterWrite(ctx, rt, respBody, respHeader, merged)
	if err != nil {
		return nil, err
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

func (s *Server) applyDestroy(ctx context.Context, rt *ResourceType, req *tfprotov6.ApplyResourceChangeRequest) (*tfprotov6.ApplyResourceChangeResponse, error) {
	// Matches the real requirement PlanResourceChange's own doc comment
	// describes (UBI-30) -- ubx's own fakeprovider fixture refuses a
	// destroy with empty PlannedPrivate for the identical real reason.
	if len(req.PlannedPrivate) == 0 {
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diagError(
			"destroy called without PlannedPrivate",
			"a real destroy requires a prior PlanResourceChange call -- this provider refuses to silently no-op",
		)}, nil
	}
	if rt.DeleteOperation == nil {
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diagError(
			"resource has no delete operation",
			fmt.Sprintf("%s was discovered with no DELETE on %s -- this resource cannot be destroyed through this provider", rt.TypeName, rt.ReadPath),
		)}, nil
	}

	prior, err := req.PriorState.Unmarshal(rt.ObjectType)
	if err != nil {
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diagError("decode prior state", err.Error())}, nil
	}
	params, err := extractStringAttrs(prior, rt.PathParams, rt.PathParamAttr)
	if err != nil {
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diagError("destroy resource", err.Error())}, nil
	}
	path, err := restexec.BuildPath(rt.ReadPath, params)
	if err != nil {
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diagError("build delete request", err.Error())}, nil
	}

	doCtx, cancel := withOperationTimeout(ctx, rt.Timeouts.Delete)
	_, _, _, err = s.Client.Do(doCtx, "DELETE", path, nil)
	cancel()
	if err != nil && !restexec.IsNotFound(err) {
		// Already gone (IsNotFound) is a real, honest destroy, not a lie
		// (docs/executor.md's own UBI-44 finding: a lying destroy is one
		// that reports success without the delete ever having taken
		// effect; a 404 on delete means it already had) -- falls through
		// to the clean success below, same as an ordinary successful
		// DELETE.
		//
		// Everything else genuinely failed to delete. A terminal
		// rejection (403/409/...) is reported as a Diagnostic and, per
		// ship.go's own documented asymmetry (UBI-44: "terminal Apply
		// errors get no read-back added"), core marks this failed
		// immediately -- correct, since a real, structured rejection IS
		// certainty. An ambiguous failure (network error, 5xx exhausted)
		// is returned as a plain error instead: core's own
		// reconcileDestroyLoop then verifies via a real ReadResource
		// whether the delete secretly landed anyway, exactly the
		// discipline this provider must not try to reimplement itself.
		diags, ambiguous := classifyRESTError("destroy resource", err)
		if ambiguous != nil {
			return nil, ambiguous
		}
		return &tfprotov6.ApplyResourceChangeResponse{Diagnostics: diags}, nil
	}
	// A clean response here (success, or already-gone) is NOT the final
	// word for a destroy -- core's own ReadResource-based post-destroy
	// read-back (UBI-44, docs/executor.md) verifies it independently
	// before ever recording `destroyed`. This provider does not need its
	// own read-back: returning a clean response is exactly what triggers
	// core's universal one.
	return &tfprotov6.ApplyResourceChangeResponse{}, nil
}

// --- Not supported in this provider (advertised capabilities are all
// false/zero, so real Terraform/ubx never issues these) ---

func (s *Server) MoveResourceState(context.Context, *tfprotov6.MoveResourceStateRequest) (*tfprotov6.MoveResourceStateResponse, error) {
	return &tfprotov6.MoveResourceStateResponse{Diagnostics: diagError("not supported", "this provider does not support MoveResourceState")}, nil
}

func (s *Server) UpgradeResourceIdentity(context.Context, *tfprotov6.UpgradeResourceIdentityRequest) (*tfprotov6.UpgradeResourceIdentityResponse, error) {
	return &tfprotov6.UpgradeResourceIdentityResponse{Diagnostics: diagError("not supported", "this provider does not use resource identity")}, nil
}

func (s *Server) GenerateResourceConfig(context.Context, *tfprotov6.GenerateResourceConfigRequest) (*tfprotov6.GenerateResourceConfigResponse, error) {
	return &tfprotov6.GenerateResourceConfigResponse{Diagnostics: diagError("not supported", "this provider does not support GenerateResourceConfig")}, nil
}

// --- Data sources / functions / ephemeral resources: genuinely out of
// scope (this provider advertises none in GetProviderSchema, so real
// Terraform/ubx never calls these; a real caller reaching them anyway is
// a caller bug, answered with a clear error rather than a silent empty
// success). ---

func (s *Server) ValidateDataResourceConfig(context.Context, *tfprotov6.ValidateDataResourceConfigRequest) (*tfprotov6.ValidateDataResourceConfigResponse, error) {
	return &tfprotov6.ValidateDataResourceConfigResponse{Diagnostics: diagError("not supported", "this provider defines no data sources")}, nil
}

// ReadDataSource is deliberately never real, even once s.DataSources is
// populated -- see smithy/server's own identical ReadDataSource doc
// comment for the full account: a real, live data-source read goes
// through ReadResource, keyed by the data source's own WireType, not
// this RPC.
func (s *Server) ReadDataSource(context.Context, *tfprotov6.ReadDataSourceRequest) (*tfprotov6.ReadDataSourceResponse, error) {
	return &tfprotov6.ReadDataSourceResponse{Diagnostics: diagError("not supported", "data source reads go through ReadResource, keyed by WireType, not this RPC")}, nil
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

// carryForwardFields is every attribute name ReadResource/ApplyResourceChange's
// own NewState construction must always echo back from prior/planned
// state rather than whatever the API's fresh response says: read- and
// create-path params (never part of the API's own JSON representation at
// all -- see ensurePathParamsPresent) union'd with rt.Drift.Ignore (UBI-158
// Phase 3: a field a config author has declared should never be reported
// as drift, regardless of what the API returns for it -- the identical
// mechanism, just a second real reason to use it).
func carryForwardFields(rt *ResourceType) []string {
	seen := map[string]bool{}
	var out []string
	add := func(fields []string) {
		for _, f := range fields {
			if !seen[f] {
				seen[f] = true
				out = append(out, f)
			}
		}
	}
	// resolveAttrNames, not the raw literal template names: a renamed
	// path-only attribute (e.g. "owner_path", when the URL template's own
	// "owner" collided with a real, differently-typed response attribute
	// of the same name -- see ResourceType.PathParamAttr's own doc
	// comment) is the one that never appears in any real API response and
	// needs carrying forward; the ORIGINAL "owner" is real response data,
	// already legitimately (re)populated by every fresh read, and carrying
	// IT forward instead would be both wrong and pointless.
	add(resolveAttrNames(rt.PathParams, rt.PathParamAttr))
	add(resolveAttrNames(rt.CreatePathParams, rt.CreatePathParamAttr))
	add(rt.Drift.Ignore)
	return out
}
