package snapshot

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/ubiquex/ubx-provider-dynamic/internal/config"
)

// widgetSpecV1/V2/V3Breaking are real, minimal, self-contained OpenAPI 3
// specs (internal $refs only, matching the real shape confirmed live for
// every schema_source = "openapi" provider this project has actually
// onboarded -- Datadog, GitHub, Kubernetes) -- V2 adds a real, purely
// additive field on top of V1; V3Breaking removes a required field V1
// declared, a real, breaking change. Each also carries one real,
// UNCLAIMED GET (/widget-summary, no matching create) -- a real
// resourcemap.DiscoverDataSources candidate, alongside the claimed
// /widgets{id}+POST/widgets resource pair -- so the same fixture proves
// both Mode branches against the identical live document.
const widgetSpecV1 = `{
  "openapi": "3.0.0",
  "info": {"title": "widgetco", "version": "1"},
  "paths": {
    "/widgets/{id}": {
      "get": {"operationId": "getWidget", "parameters": [{"name": "id", "in": "path", "required": true, "schema": {"type": "string"}}],
        "responses": {"200": {"description": "ok", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Widget"}}}}}}
    },
    "/widgets": {
      "post": {"operationId": "createWidget",
        "requestBody": {"content": {"application/json": {"schema": {"$ref": "#/components/schemas/Widget"}}}},
        "responses": {"201": {"description": "created", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Widget"}}}}}}
    },
    "/widget-summary": {
      "get": {"operationId": "listWidgetSummary",
        "responses": {"200": {"description": "ok", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Widget"}}}}}}
    }
  },
  "components": {"schemas": {"Widget": {"type": "object", "required": ["name"],
    "properties": {"id": {"type": "string", "readOnly": true}, "name": {"type": "string"}}}}}
}`

const widgetSpecV2AddsField = `{
  "openapi": "3.0.0",
  "info": {"title": "widgetco", "version": "2"},
  "paths": {
    "/widgets/{id}": {
      "get": {"operationId": "getWidget", "parameters": [{"name": "id", "in": "path", "required": true, "schema": {"type": "string"}}],
        "responses": {"200": {"description": "ok", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Widget"}}}}}}
    },
    "/widgets": {
      "post": {"operationId": "createWidget",
        "requestBody": {"content": {"application/json": {"schema": {"$ref": "#/components/schemas/Widget"}}}},
        "responses": {"201": {"description": "created", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Widget"}}}}}}
    },
    "/widget-summary": {
      "get": {"operationId": "listWidgetSummary",
        "responses": {"200": {"description": "ok", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Widget"}}}}}}
    }
  },
  "components": {"schemas": {"Widget": {"type": "object", "required": ["name"],
    "properties": {"id": {"type": "string", "readOnly": true}, "name": {"type": "string"}, "color": {"type": "string"}}}}}
}`

const widgetSpecV3RemovesRequiredField = `{
  "openapi": "3.0.0",
  "info": {"title": "widgetco", "version": "3"},
  "paths": {
    "/widgets/{id}": {
      "get": {"operationId": "getWidget", "parameters": [{"name": "id", "in": "path", "required": true, "schema": {"type": "string"}}],
        "responses": {"200": {"description": "ok", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Widget"}}}}}}
    },
    "/widgets": {
      "post": {"operationId": "createWidget",
        "requestBody": {"content": {"application/json": {"schema": {"$ref": "#/components/schemas/Widget"}}}},
        "responses": {"201": {"description": "created", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Widget"}}}}}}
    },
    "/widget-summary": {
      "get": {"operationId": "listWidgetSummary",
        "responses": {"200": {"description": "ok", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Widget"}}}}}}
    }
  },
  "components": {"schemas": {"Widget": {"type": "object",
    "properties": {"id": {"type": "string", "readOnly": true}}}}}
}`

func serveSpec(t *testing.T, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestGenerateOpenAPIMember_FirstEverMember_RealMinorLevel(t *testing.T) {
	url := serveSpec(t, widgetSpecV1)
	member, schemas, level, err := GenerateOpenAPIMember("widgetco", "widgetco", url, ModeResource, config.Provider{BaseURL: "https://api.widgetco.example"}, nil)
	if err != nil {
		t.Fatalf("GenerateOpenAPIMember: %v", err)
	}
	if level != Minor {
		t.Errorf("first-ever member level = %s, want minor", level)
	}
	if member.SchemaSource != SchemaSourceOpenAPI || member.Mode != ModeResource {
		t.Errorf("identity fields wrong: %+v", member)
	}
	if len(schemas) == 0 {
		t.Fatal("zero translated resource schemas")
	}
	var probe map[string]any
	if err := json.Unmarshal(member.RawSpec, &probe); err != nil {
		t.Fatalf("RawSpec is not valid JSON: %v", err)
	}
}

func TestGenerateOpenAPIMember_AdditiveChange_RealMinorLevel(t *testing.T) {
	execCfg := config.Provider{BaseURL: "https://api.widgetco.example"}
	prevMember, _, _, err := GenerateOpenAPIMember("widgetco", "widgetco", serveSpec(t, widgetSpecV1), ModeResource, execCfg, nil)
	if err != nil {
		t.Fatalf("first generation: %v", err)
	}

	_, _, level, err := GenerateOpenAPIMember("widgetco", "widgetco", serveSpec(t, widgetSpecV2AddsField), ModeResource, execCfg, prevMember)
	if err != nil {
		t.Fatalf("second generation: %v", err)
	}
	if level != Minor {
		t.Errorf("real additive change level = %s, want minor", level)
	}
}

func TestGenerateOpenAPIMember_BreakingChange_RealMajorLevel(t *testing.T) {
	execCfg := config.Provider{BaseURL: "https://api.widgetco.example"}
	prevMember, _, _, err := GenerateOpenAPIMember("widgetco", "widgetco", serveSpec(t, widgetSpecV1), ModeResource, execCfg, nil)
	if err != nil {
		t.Fatalf("first generation: %v", err)
	}

	_, _, level, err := GenerateOpenAPIMember("widgetco", "widgetco", serveSpec(t, widgetSpecV3RemovesRequiredField), ModeResource, execCfg, prevMember)
	if err != nil {
		t.Fatalf("second generation: %v", err)
	}
	if level != Major {
		t.Errorf("real breaking change level = %s, want major", level)
	}
}

func TestGenerateOpenAPIMember_NoChange_RealNoChangeLevel(t *testing.T) {
	execCfg := config.Provider{BaseURL: "https://api.widgetco.example"}
	prevMember, _, _, err := GenerateOpenAPIMember("widgetco", "widgetco", serveSpec(t, widgetSpecV1), ModeResource, execCfg, nil)
	if err != nil {
		t.Fatalf("first generation: %v", err)
	}

	_, _, level, err := GenerateOpenAPIMember("widgetco", "widgetco", serveSpec(t, widgetSpecV1), ModeResource, execCfg, prevMember)
	if err != nil {
		t.Fatalf("second generation: %v", err)
	}
	if level != NoChange {
		t.Errorf("real no-op regeneration level = %s, want none", level)
	}
}

// TestGenerateOpenAPIMember_DataSourceMode_RealDataSourceCandidate proves
// ModeDataSource actually runs resourcemap.BuildDataSources, not
// dynserver.Build -- against the SAME real fixture's own unclaimed
// /widget-summary GET (never a resource, since it has no matching
// create), the exact real gap this session found: before this change, a
// data-source-mode member could not be generated at all for openapi.
func TestGenerateOpenAPIMember_DataSourceMode_RealDataSourceCandidate(t *testing.T) {
	url := serveSpec(t, widgetSpecV1)
	member, schemas, level, err := GenerateOpenAPIMember("widgetco", "widgetco", url, ModeDataSource, config.Provider{BaseURL: "https://api.widgetco.example"}, nil)
	if err != nil {
		t.Fatalf("GenerateOpenAPIMember (data source): %v", err)
	}
	if member.Mode != ModeDataSource {
		t.Errorf("Mode = %q, want data_source", member.Mode)
	}
	if level != Minor {
		t.Errorf("first-ever data-source member level = %s, want minor", level)
	}
	if len(schemas) == 0 {
		t.Fatal("zero translated data-source schemas -- /widget-summary should have been a real, unclaimed candidate")
	}

	// Real, network-free reload must come back as DATA SOURCES, never
	// resources -- the exact real distinction this session's own gap
	// finding was about.
	resources, dataSources, err := LoadOpenAPIMember("widgetco", member)
	if err != nil {
		t.Fatalf("LoadOpenAPIMember: %v", err)
	}
	if len(resources) != 0 {
		t.Errorf("a data-source-mode member reloaded %d RESOURCES, want zero", len(resources))
	}
	if len(dataSources) == 0 {
		t.Fatal("a data-source-mode member reloaded zero data sources")
	}
}

// TestGenerateOpenAPIMember_WireNameOverride_RealDistinctTypeNames is a
// real regression guard for a real, live-found bug this session's own
// merge-group work caught before it could ship: GenerateOpenAPIMember
// originally used name for BOTH identity/diffing AND wire-type
// translation, silently ignoring a distinct wireName the way
// GenerateSmithyMember already correctly did -- a data-source-mode
// member keyed under a distinct table name (kubernetes_ds, matching
// Kubernetes' own real config) generated wire type names prefixed
// "kubernetes_ds_" instead of the intended, shared "kubernetes_" prefix
// -- confirmed live against the already-generated, already-published
// real kubernetes_ds member before this fix landed, not assumed. Proves
// directly here that a member keyed "widgetco_ds" but given wireName
// "widgetco" produces type names carrying the "widgetco_" prefix, not
// "widgetco_ds_".
func TestGenerateOpenAPIMember_WireNameOverride_RealDistinctTypeNames(t *testing.T) {
	member, schemas, _, err := GenerateOpenAPIMember("widgetco_ds", "widgetco", serveSpec(t, widgetSpecV1), ModeDataSource, config.Provider{BaseURL: "https://api.widgetco.example"}, nil)
	if err != nil {
		t.Fatalf("GenerateOpenAPIMember: %v", err)
	}
	if member.WireName != "widgetco" {
		t.Fatalf("WireName = %q, want %q (not stored, or lost)", member.WireName, "widgetco")
	}
	for typeName := range schemas {
		if !strings.HasPrefix(typeName, "widgetco_") {
			t.Errorf("type name %q does not carry the real wireName prefix \"widgetco_\" -- the table key \"widgetco_ds\" leaked into translation instead", typeName)
		}
		if strings.HasPrefix(typeName, "widgetco_ds_") {
			t.Errorf("type name %q carries the table key's own \"widgetco_ds_\" prefix -- wireName was ignored", typeName)
		}
	}

	// LoadOpenAPIMember must reproduce the IDENTICAL real type names from
	// the stored WireName alone, with no separate hint from the caller.
	_, dataSources, err := LoadOpenAPIMember("widgetco_ds", member)
	if err != nil {
		t.Fatalf("LoadOpenAPIMember: %v", err)
	}
	for typeName := range dataSources {
		if !strings.HasPrefix(typeName, "widgetco_") || strings.HasPrefix(typeName, "widgetco_ds_") {
			t.Errorf("reloaded type name %q does not match the real generated wireName prefix", typeName)
		}
	}
}

// TestGenerateOpenAPIMember_UnknownMode_RealFailLoud proves the fail-loud
// requirement directly: an unrecognized Mode must error immediately, not
// silently fall through to resource-shaped output.
func TestGenerateOpenAPIMember_UnknownMode_RealFailLoud(t *testing.T) {
	_, _, _, err := GenerateOpenAPIMember("widgetco", "widgetco", serveSpec(t, widgetSpecV1), Mode("not-a-real-mode"), config.Provider{BaseURL: "https://api.widgetco.example"}, nil)
	if err == nil {
		t.Fatal("expected a real error for an unrecognized Mode")
	}
	if !errors.Is(err, ErrUnsupportedMode) {
		t.Errorf("error doesn't wrap ErrUnsupportedMode: %v", err)
	}
}

// TestGenerateOpenAPIMember_ExternalRefs_RealRefusal is a real, live test
// (gated behind UBX_LIVE_VALIDATION, the same convention
// internal/openapi's own TestLive_AzureSwaggerWithExternalRefs already
// uses) proving GenerateOpenAPIMember genuinely refuses, loudly, rather
// than silently shipping a member that would fail to reload without
// network.
func TestGenerateOpenAPIMember_ExternalRefs_RealRefusal(t *testing.T) {
	if os.Getenv("UBX_LIVE_VALIDATION") == "" {
		t.Skip("set UBX_LIVE_VALIDATION=1 to run live validation against real, published specs")
	}
	const url = "https://raw.githubusercontent.com/Azure/azure-rest-api-specs/main/specification/compute/resource-manager/Microsoft.Compute/Compute/stable/2026-04-01/ComputeRP.json"
	_, _, _, err := GenerateOpenAPIMember("azure", "azure", url, ModeResource, config.Provider{BaseURL: "https://management.azure.com"}, nil)
	if err == nil {
		t.Fatal("GenerateOpenAPIMember accepted a real spec with external $refs -- should have refused")
	}
	if !errors.Is(err, ErrExternalRefsUnsupported) {
		t.Errorf("error doesn't wrap ErrExternalRefsUnsupported: %v", err)
	}
}

// ---------------------------------------------------------------------
// group container: AssembleGroup, Save/Load/Member round trip
// ---------------------------------------------------------------------

func TestAssembleGroup_FirstEverGroup_RealVersion100(t *testing.T) {
	resourceMember, _, _, err := GenerateOpenAPIMember("widgetco", "widgetco", serveSpec(t, widgetSpecV1), ModeResource, config.Provider{BaseURL: "https://api.widgetco.example"}, nil)
	if err != nil {
		t.Fatalf("generate resource member: %v", err)
	}
	dsMember, _, _, err := GenerateOpenAPIMember("widgetco_ds", "widgetco_ds", serveSpec(t, widgetSpecV1), ModeDataSource, config.Provider{BaseURL: "https://api.widgetco.example"}, nil)
	if err != nil {
		t.Fatalf("generate data-source member: %v", err)
	}

	members := map[string]*MemberSnapshot{"widgetco": resourceMember, "widgetco_ds": dsMember}
	levels := map[string]ChangeLevel{"widgetco": Minor, "widgetco_ds": Minor}
	group, err := AssembleGroup("widgetco", nil, members, levels, nil)
	if err != nil {
		t.Fatalf("AssembleGroup: %v", err)
	}
	if group.Version != "1.0.0" {
		t.Errorf("first-ever group version = %q, want 1.0.0", group.Version)
	}
	if group.SchemaFormat != CurrentSchemaFormat {
		t.Errorf("SchemaFormat = %d, want %d", group.SchemaFormat, CurrentSchemaFormat)
	}
	if len(group.Members) != 2 {
		t.Fatalf("group has %d members, want 2", len(group.Members))
	}
}

func TestAssembleGroup_OneMemberMajorChange_RealMajorGroupBump(t *testing.T) {
	execCfg := config.Provider{BaseURL: "https://api.widgetco.example"}
	prevResource, _, _, err := GenerateOpenAPIMember("widgetco", "widgetco", serveSpec(t, widgetSpecV1), ModeResource, execCfg, nil)
	if err != nil {
		t.Fatalf("prev resource member: %v", err)
	}
	prevDS, _, _, err := GenerateOpenAPIMember("widgetco_ds", "widgetco_ds", serveSpec(t, widgetSpecV1), ModeDataSource, execCfg, nil)
	if err != nil {
		t.Fatalf("prev data-source member: %v", err)
	}
	prevGroup, err := AssembleGroup("widgetco", nil, map[string]*MemberSnapshot{"widgetco": prevResource, "widgetco_ds": prevDS}, map[string]ChangeLevel{"widgetco": Minor, "widgetco_ds": Minor}, nil)
	if err != nil {
		t.Fatalf("AssembleGroup (prev): %v", err)
	}

	// Only the resource member breaks; the data-source member is
	// unchanged (NoChange level) -- the GROUP's own real version must
	// still bump Major, since AssembleGroup takes the max across every
	// member, not an average or a per-member vote.
	nextResource, _, resourceLevel, err := GenerateOpenAPIMember("widgetco", "widgetco", serveSpec(t, widgetSpecV3RemovesRequiredField), ModeResource, execCfg, prevResource)
	if err != nil {
		t.Fatalf("next resource member: %v", err)
	}
	nextDS, _, dsLevel, err := GenerateOpenAPIMember("widgetco_ds", "widgetco_ds", serveSpec(t, widgetSpecV1), ModeDataSource, execCfg, prevDS)
	if err != nil {
		t.Fatalf("next data-source member: %v", err)
	}
	if resourceLevel != Major {
		t.Fatalf("resource member level = %s, want major (test setup)", resourceLevel)
	}
	if dsLevel != NoChange {
		t.Fatalf("data-source member level = %s, want none (test setup)", dsLevel)
	}

	nextGroup, err := AssembleGroup("widgetco", prevGroup, map[string]*MemberSnapshot{"widgetco": nextResource, "widgetco_ds": nextDS}, map[string]ChangeLevel{"widgetco": resourceLevel, "widgetco_ds": dsLevel}, nil)
	if err != nil {
		t.Fatalf("AssembleGroup (next): %v", err)
	}
	if nextGroup.Version != "2.0.0" {
		t.Errorf("group version after one member's own major change = %q, want 2.0.0 (max across all members)", nextGroup.Version)
	}
}

func TestAssembleGroup_MemberRemoved_RealMajorBumpUnconditional(t *testing.T) {
	execCfg := config.Provider{BaseURL: "https://api.widgetco.example"}
	resourceMember, _, _, err := GenerateOpenAPIMember("widgetco", "widgetco", serveSpec(t, widgetSpecV1), ModeResource, execCfg, nil)
	if err != nil {
		t.Fatalf("resource member: %v", err)
	}
	dsMember, _, _, err := GenerateOpenAPIMember("widgetco_ds", "widgetco_ds", serveSpec(t, widgetSpecV1), ModeDataSource, execCfg, nil)
	if err != nil {
		t.Fatalf("data-source member: %v", err)
	}
	prevGroup, err := AssembleGroup("widgetco", nil, map[string]*MemberSnapshot{"widgetco": resourceMember, "widgetco_ds": dsMember}, map[string]ChangeLevel{"widgetco": Minor, "widgetco_ds": Minor}, nil)
	if err != nil {
		t.Fatalf("AssembleGroup (prev): %v", err)
	}

	// The next real generation only re-generates "widgetco" -- "widgetco_ds"
	// is gone from the group entirely, an unconditional Major regardless
	// of what its own content used to look like.
	nextResource, _, resourceLevel, err := GenerateOpenAPIMember("widgetco", "widgetco", serveSpec(t, widgetSpecV1), ModeResource, execCfg, resourceMember)
	if err != nil {
		t.Fatalf("next resource member: %v", err)
	}
	nextGroup, err := AssembleGroup("widgetco", prevGroup, map[string]*MemberSnapshot{"widgetco": nextResource}, map[string]ChangeLevel{"widgetco": resourceLevel}, nil)
	if err != nil {
		t.Fatalf("AssembleGroup (next): %v", err)
	}
	if nextGroup.Version != "2.0.0" {
		t.Errorf("group version after a real member removal = %q, want 2.0.0 (unconditional major), got real member-level %s", nextGroup.Version, resourceLevel)
	}
}

func TestSnapshotSaveLoad_GroupContainer_RealRoundTrip(t *testing.T) {
	execCfg := config.Provider{BaseURL: "https://api.widgetco.example"}
	resourceMember, _, _, err := GenerateOpenAPIMember("widgetco", "widgetco", serveSpec(t, widgetSpecV1), ModeResource, execCfg, nil)
	if err != nil {
		t.Fatalf("resource member: %v", err)
	}
	dsMember, _, _, err := GenerateOpenAPIMember("widgetco_ds", "widgetco_ds", serveSpec(t, widgetSpecV1), ModeDataSource, execCfg, nil)
	if err != nil {
		t.Fatalf("data-source member: %v", err)
	}
	group, err := AssembleGroup("widgetco", nil, map[string]*MemberSnapshot{"widgetco": resourceMember, "widgetco_ds": dsMember}, map[string]ChangeLevel{"widgetco": Minor, "widgetco_ds": Minor}, nil)
	if err != nil {
		t.Fatalf("AssembleGroup: %v", err)
	}

	path := t.TempDir() + "/snapshot.json"
	if err := Save(path, group); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Version != group.Version || loaded.Provider != group.Provider {
		t.Fatalf("round trip mismatch: got %+v, want %+v", loaded, group)
	}

	resourceMemberLoaded, err := loaded.Member("widgetco")
	if err != nil {
		t.Fatalf("Member(widgetco): %v", err)
	}
	if resourceMemberLoaded.Mode != ModeResource {
		t.Errorf("reloaded widgetco member Mode = %q, want resource", resourceMemberLoaded.Mode)
	}
	dsMemberLoaded, err := loaded.Member("widgetco_ds")
	if err != nil {
		t.Fatalf("Member(widgetco_ds): %v", err)
	}
	if dsMemberLoaded.Mode != ModeDataSource {
		t.Errorf("reloaded widgetco_ds member Mode = %q, want data_source", dsMemberLoaded.Mode)
	}

	if _, err := loaded.Member("no-such-member"); err == nil {
		t.Fatal("Member(no-such-member) should have failed loud, not returned a zero value")
	}
}

func TestCheckFormat_RejectsFormat2_RealBreak(t *testing.T) {
	// UBI-182's own real, accepted break: the OLD, single-member format
	// (2) is not readable by this build at all -- confirmed here, not
	// assumed, since this is exactly the founder's own explicit decision
	// (no compatibility shim) this session made real.
	if err := CheckFormat(2); err == nil {
		t.Fatal("CheckFormat(2) should refuse the old, single-member format now that Min/Max are both 3")
	} else if !errors.Is(err, ErrUnsupportedSchemaFormat) {
		t.Errorf("error doesn't wrap ErrUnsupportedSchemaFormat: %v", err)
	}
}
