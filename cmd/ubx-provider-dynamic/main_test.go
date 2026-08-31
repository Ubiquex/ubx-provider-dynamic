package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ubiquex/ubx-provider-dynamic/internal/config"
)

// widgetSpec is a real, minimal, self-contained OpenAPI 3 spec, the
// same shape generate_test.go's own widgetSpecV1 already uses in the
// snapshot package -- kept small and local here since main is a
// different package and cannot import that unexported fixture.
const widgetSpec = `{
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

func serveSpec(t *testing.T, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func serve404(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestGenerateGroupMembers_AllSucceed_RealMembersAndLevels(t *testing.T) {
	url := serveSpec(t, widgetSpec)
	allProviders := map[string]config.Provider{
		"widgetco": {Name: "widgetco", SchemaSource: config.SchemaSourceOpenAPI, SchemaURL: url, BaseURL: "https://api.widgetco.example"},
	}

	members, levels, err := generateGroupMembers(allProviders, []string{"widgetco"}, nil)
	if err != nil {
		t.Fatalf("generateGroupMembers: %v", err)
	}
	if _, ok := members["widgetco"]; !ok {
		t.Errorf("expected members[%q] to be present", "widgetco")
	}
	if _, ok := members["widgetco_ds"]; !ok {
		t.Errorf("expected members[%q] to be present (default openapi mode expansion is resource+data_source)", "widgetco_ds")
	}
	if len(levels) != 2 {
		t.Errorf("expected 2 real levels (resource+data_source), got %d", len(levels))
	}
}

// TestGenerateGroupMembers_MultipleBadMembers_NamesEveryOneInOnePass is
// UBI-229's own real regression guard: before this change, the first
// bad member aborted the whole run and the caller never learned
// whether a second, unrelated member was also broken. This member set
// carries three real problems at once -- a missing config table, and
// two distinct members whose own real schema_url 404s -- and expects
// every one of them named in the single returned error, not just the
// first encountered.
func TestGenerateGroupMembers_MultipleBadMembers_NamesEveryOneInOnePass(t *testing.T) {
	goodURL := serveSpec(t, widgetSpec)
	dead1 := serve404(t)
	dead2 := serve404(t)
	allProviders := map[string]config.Provider{
		"good":  {Name: "good", SchemaSource: config.SchemaSourceOpenAPI, SchemaURL: goodURL, BaseURL: "https://api.widgetco.example"},
		"dead1": {Name: "dead1", SchemaSource: config.SchemaSourceOpenAPI, SchemaURL: dead1, BaseURL: "https://api.widgetco.example"},
		"dead2": {Name: "dead2", SchemaSource: config.SchemaSourceOpenAPI, SchemaURL: dead2, BaseURL: "https://api.widgetco.example"},
	}

	_, _, err := generateGroupMembers(allProviders, []string{"good", "dead1", "missing", "dead2"}, nil)
	if err == nil {
		t.Fatal("expected a real error, got nil")
	}
	msg := err.Error()
	for _, want := range []string{"dead1", "dead2", "missing"} {
		if !strings.Contains(msg, want) {
			t.Errorf("expected the aggregate error to name %q, got: %s", want, msg)
		}
	}
}

func TestGenerateGroupMembers_AnyFailure_ReturnsNilMembersAndLevels(t *testing.T) {
	allProviders := map[string]config.Provider{}
	members, levels, err := generateGroupMembers(allProviders, []string{"missing"}, nil)
	if err == nil {
		t.Fatal("expected a real error, got nil")
	}
	if members != nil || levels != nil {
		t.Errorf("expected nil members/levels on any failure (never a partial, silently-incomplete group), got members=%v levels=%v", members, levels)
	}
}
