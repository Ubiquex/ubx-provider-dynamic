package snapshot

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/ubiquex/ubx-provider-dynamic/internal/config"
)

// widgetSpecV1/V2/V3Breaking are real, minimal, self-contained OpenAPI 3
// specs (internal $refs only, matching the real shape confirmed live for
// every schema_source = "openapi" provider this project has actually
// onboarded -- Datadog, GitHub, Kubernetes) -- V2 adds a real, purely
// additive field on top of V1; V3Breaking removes a required field V1
// declared, a real, breaking change.
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

func TestGenerateOpenAPI_FirstEverSnapshot_Real010(t *testing.T) {
	url := serveSpec(t, widgetSpecV1)
	snap, err := GenerateOpenAPI("widgetco", url, config.Provider{BaseURL: "https://api.widgetco.example"}, nil)
	if err != nil {
		t.Fatalf("GenerateOpenAPI: %v", err)
	}
	if snap.Version != "0.1.0" {
		t.Errorf("first-ever snapshot version = %q, want 0.1.0", snap.Version)
	}
	if snap.SchemaFormat != CurrentSchemaFormat {
		t.Errorf("SchemaFormat = %d, want %d", snap.SchemaFormat, CurrentSchemaFormat)
	}
	if snap.Provider != "widgetco" || snap.SchemaSource != SchemaSourceOpenAPI {
		t.Errorf("identity fields wrong: %+v", snap)
	}
	var probe map[string]any
	if err := json.Unmarshal(snap.RawSpec, &probe); err != nil {
		t.Fatalf("RawSpec is not valid JSON: %v", err)
	}
}

func TestGenerateOpenAPI_AdditiveChange_RealMinorBump(t *testing.T) {
	execCfg := config.Provider{BaseURL: "https://api.widgetco.example"}
	prev, err := GenerateOpenAPI("widgetco", serveSpec(t, widgetSpecV1), execCfg, nil)
	if err != nil {
		t.Fatalf("first generation: %v", err)
	}

	next, err := GenerateOpenAPI("widgetco", serveSpec(t, widgetSpecV2AddsField), execCfg, prev)
	if err != nil {
		t.Fatalf("second generation: %v", err)
	}
	if next.Version != "0.2.0" {
		t.Errorf("real minor bump: version = %q, want 0.2.0", next.Version)
	}
}

func TestGenerateOpenAPI_BreakingChange_RealMajorBump(t *testing.T) {
	execCfg := config.Provider{BaseURL: "https://api.widgetco.example"}
	prev, err := GenerateOpenAPI("widgetco", serveSpec(t, widgetSpecV1), execCfg, nil)
	if err != nil {
		t.Fatalf("first generation: %v", err)
	}
	// Force prev to a real, non-0.x version so a major bump is visibly
	// distinct from a minor one in this test's own assertion.
	prev.Version = "1.0.0"

	next, err := GenerateOpenAPI("widgetco", serveSpec(t, widgetSpecV3RemovesRequiredField), execCfg, prev)
	if err != nil {
		t.Fatalf("second generation: %v", err)
	}
	if next.Version != "2.0.0" {
		t.Errorf("real major bump: version = %q, want 2.0.0", next.Version)
	}
}

func TestGenerateOpenAPI_NoChange_RealSameVersion(t *testing.T) {
	execCfg := config.Provider{BaseURL: "https://api.widgetco.example"}
	prev, err := GenerateOpenAPI("widgetco", serveSpec(t, widgetSpecV1), execCfg, nil)
	if err != nil {
		t.Fatalf("first generation: %v", err)
	}
	prev.Version = "1.0.0"

	next, err := GenerateOpenAPI("widgetco", serveSpec(t, widgetSpecV1), execCfg, prev)
	if err != nil {
		t.Fatalf("second generation: %v", err)
	}
	if next.Version != "1.0.0" {
		t.Errorf("real no-op regeneration: version = %q, want unchanged 1.0.0", next.Version)
	}
}

// TestGenerateOpenAPI_ExternalRefs_RealRefusal is a real, live test
// (gated behind UBX_LIVE_VALIDATION, the same convention
// internal/openapi's own TestLive_AzureSwaggerWithExternalRefs already
// uses) proving GenerateOpenAPI genuinely refuses, loudly, rather than
// silently shipping a snapshot that would fail to reload without
// network -- against the exact same real, live, external-ref-carrying
// Azure spec that test already confirms parses fine on the FETCH side.
func TestGenerateOpenAPI_ExternalRefs_RealRefusal(t *testing.T) {
	if os.Getenv("UBX_LIVE_VALIDATION") == "" {
		t.Skip("set UBX_LIVE_VALIDATION=1 to run live validation against real, published specs")
	}
	const url = "https://raw.githubusercontent.com/Azure/azure-rest-api-specs/main/specification/compute/resource-manager/Microsoft.Compute/Compute/stable/2026-04-01/ComputeRP.json"
	_, err := GenerateOpenAPI("azure", url, config.Provider{BaseURL: "https://management.azure.com"}, nil)
	if err == nil {
		t.Fatal("GenerateOpenAPI accepted a real spec with external $refs -- should have refused")
	}
	if !errors.Is(err, ErrExternalRefsUnsupported) {
		t.Errorf("error doesn't wrap ErrExternalRefsUnsupported: %v", err)
	}
}
