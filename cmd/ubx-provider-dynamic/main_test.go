package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ubiquex/ubx-provider-dynamic/internal/config"
	"github.com/ubiquex/ubx-provider-dynamic/internal/snapshot"
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

// buildRealMember generates a real MemberSnapshot the same way
// production code does (GenerateOpenAPIMember against a real server),
// for use as a prev-snapshot fixture in the tests below -- not a
// hand-built struct literal, so these tests exercise the exact real
// shape generateGroupMembersFromSnapshot has to reconstruct a
// config.Provider from.
func buildRealMember(t *testing.T, name, url string) *snapshot.MemberSnapshot {
	t.Helper()
	member, _, _, err := snapshot.GenerateOpenAPIMember(name, name, url, snapshot.ModeResource, config.Provider{BaseURL: "https://api.widgetco.example"}, nil)
	if err != nil {
		t.Fatalf("buildRealMember(%q): %v", name, err)
	}
	return member
}

// TestGenerateGroupMembersFromSnapshot_AllSucceed_RealNoChangeLevel is
// UBI-229's own real proof that a group can regenerate from nothing
// but its own committed snapshot -- every input generateOneMember
// needs (schema_source, schema_url, base_url, ...) comes back out of
// the MemberSnapshot itself, with no config.Provider ever hand-built
// from a separate, live config. Re-serving the identical spec at the
// same recorded URL and diffing against itself must land on NoChange,
// the same real signal a clean hash-watch run needs to report.
func TestGenerateGroupMembersFromSnapshot_AllSucceed_RealNoChangeLevel(t *testing.T) {
	url := serveSpec(t, widgetSpec)
	m := buildRealMember(t, "widgetco", url)
	prev := &snapshot.Snapshot{
		SchemaFormat: snapshot.CurrentSchemaFormat,
		Provider:     "widgetco",
		Version:      "1.0.0",
		Members:      map[string]*snapshot.MemberSnapshot{"widgetco": m},
	}

	members, levels, err := generateGroupMembersFromSnapshot(prev)
	if err != nil {
		t.Fatalf("generateGroupMembersFromSnapshot: %v", err)
	}
	if _, ok := members["widgetco"]; !ok {
		t.Fatalf("expected members[%q] to be present", "widgetco")
	}
	if got := levels["widgetco"]; got != snapshot.NoChange {
		t.Errorf("expected NoChange regenerating the identical spec from its own committed snapshot, got %v", got)
	}
}

// TestGenerateGroupMembersFromSnapshot_MovedPaths_NamesEveryOneInOnePass
// is the exact real UBI-229 shape: a group whose own committed
// snapshot carries members whose upstream schema_url has since moved
// -- the real azure_newrelic incident, reproduced generically. Two of
// three members' own recorded URLs now 404; the aggregate error must
// name both, not just the first, the same collect-all-errors guarantee
// generateGroupMembers already has to.
func TestGenerateGroupMembersFromSnapshot_MovedPaths_NamesEveryOneInOnePass(t *testing.T) {
	goodURL := serveSpec(t, widgetSpec)
	good := buildRealMember(t, "good", goodURL)

	movedURL1 := serveSpec(t, widgetSpec)
	moved1 := buildRealMember(t, "moved1", movedURL1)
	dead1 := serve404(t)
	moved1.SchemaURL = dead1 // upstream moved since this was committed

	movedURL2 := serveSpec(t, widgetSpec)
	moved2 := buildRealMember(t, "moved2", movedURL2)
	dead2 := serve404(t)
	moved2.SchemaURL = dead2

	prev := &snapshot.Snapshot{
		SchemaFormat: snapshot.CurrentSchemaFormat,
		Provider:     "widgetco",
		Version:      "1.0.0",
		Members: map[string]*snapshot.MemberSnapshot{
			"good":   good,
			"moved1": moved1,
			"moved2": moved2,
		},
	}

	members, levels, err := generateGroupMembersFromSnapshot(prev)
	if err == nil {
		t.Fatal("expected a real error, got nil")
	}
	if members != nil || levels != nil {
		t.Errorf("expected nil members/levels on any failure, got members=%v levels=%v", members, levels)
	}
	msg := err.Error()
	for _, want := range []string{"moved1", "moved2"} {
		if !strings.Contains(msg, want) {
			t.Errorf("expected the aggregate error to name %q, got: %s", want, msg)
		}
	}
}
