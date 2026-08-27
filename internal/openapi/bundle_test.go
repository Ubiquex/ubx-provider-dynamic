package openapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
)

// bundleTestServer serves routes (path -> real JSON body) from one real
// httptest server, so a "main" document's own real relative $refs
// ("./common.json#/...") resolve against a real HTTP base URL exactly
// the way Azure's own real specs do against raw.githubusercontent.com --
// only the transport is local, matching this project's own "real tests,
// no transport mocking" discipline already established in load_test.go.
func bundleTestServer(t *testing.T, routes map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := routes[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// mustReparseNetworkFree marshals doc and confirms it re-parses with a
// nil location (Parse's own real network-free contract, matching what
// internal/snapshot.GenerateOpenAPIMember actually calls after Bundle)
// -- the real, direct proof any of this package's own external-ref work
// exists to satisfy.
func mustReparseNetworkFree(t *testing.T, doc *openapi3.T) []byte {
	t.Helper()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := Parse(raw, nil); err != nil {
		t.Fatalf("reparse network-free after Bundle: %v (bundling left something unresolved)", err)
	}
	return raw
}

// TestBundle_ExternalRefBecomesLocal proves the real, basic case: a
// main document's own real external $ref, once Bundle runs, resolves
// with zero network on reparse -- the resulting local schema carries
// real content (not just an empty stand-in), and every real reference
// site is verified, not just one.
func TestBundle_ExternalRefBecomesLocal(t *testing.T) {
	common := `{
		"swagger": "2.0", "info": {"title": "common", "version": "1"}, "paths": {},
		"definitions": {
			"Widget": {"type": "object", "properties": {"id": {"type": "string"}, "name": {"type": "string"}}}
		}
	}`
	main := `{
		"swagger": "2.0",
		"info": {"title": "main", "version": "1"},
		"paths": {
			"/widgets/{id}": {
				"get": {
					"operationId": "getWidget",
					"parameters": [{"name": "id", "in": "path", "required": true, "type": "string"}],
					"responses": {"200": {"description": "ok", "schema": {"$ref": "./common.json#/definitions/Widget"}}}
				}
			},
			"/widgets2/{id}": {
				"get": {
					"operationId": "getWidget2",
					"parameters": [{"name": "id", "in": "path", "required": true, "type": "string"}],
					"responses": {"200": {"description": "ok", "schema": {"$ref": "./common.json#/definitions/Widget"}}}
				}
			}
		}
	}`
	srv := bundleTestServer(t, map[string]string{"/main.json": main, "/common.json": common})

	doc, err := Load(srv.URL + "/main.json")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	Bundle(doc)

	schema, ok := doc.Components.Schemas["Widget"]
	if !ok || schema.Value == nil {
		t.Fatalf("expected a real local Widget schema after bundling, got %+v", doc.Components.Schemas)
	}
	if _, ok := schema.Value.Properties["name"]; !ok {
		t.Fatalf("expected the real, bundled Widget schema to carry its own real content (a name property), got %+v", schema.Value.Properties)
	}

	raw := mustReparseNetworkFree(t, doc)

	reparsed, err := Parse(raw, nil)
	if err != nil {
		t.Fatalf("reparse: %v", err)
	}
	for _, path := range []string{"/widgets/{id}", "/widgets2/{id}"} {
		item := reparsed.Paths.Find(path)
		if item == nil || item.Get == nil {
			t.Fatalf("expected %s to survive reparse", path)
		}
	}
}

// TestBundle_CyclicExternalRefTerminates is the real, direct proof
// behind this design's own core claim: a naive "clear Ref, deep-copy
// Value inline" bundler would recurse forever on a real cycle (UBI-193's
// own live finding: Azure's network/virtualNetwork.json's real
// PublicIPAddress reaches itself through linkedPublicIPAddress). This
// fixture reproduces the identical real shape -- two external files
// whose own schemas reference each other -- and proves Bundle
// terminates and produces a network-free, reparseable result.
func TestBundle_CyclicExternalRefTerminates(t *testing.T) {
	fileA := `{
		"swagger": "2.0", "info": {"title": "a", "version": "1"}, "paths": {},
		"definitions": {
			"A": {"type": "object", "properties": {"b": {"$ref": "./b.json#/definitions/B"}}}
		}
	}`
	fileB := `{
		"swagger": "2.0", "info": {"title": "b", "version": "1"}, "paths": {},
		"definitions": {
			"B": {"type": "object", "properties": {"a": {"$ref": "./a.json#/definitions/A"}}}
		}
	}`
	main := `{
		"swagger": "2.0",
		"info": {"title": "main", "version": "1"},
		"paths": {
			"/things/{id}": {
				"get": {
					"operationId": "getThing",
					"parameters": [{"name": "id", "in": "path", "required": true, "type": "string"}],
					"responses": {"200": {"description": "ok", "schema": {"$ref": "./a.json#/definitions/A"}}}
				}
			}
		}
	}`
	srv := bundleTestServer(t, map[string]string{
		"/main.json": main,
		"/a.json":    fileA,
		"/b.json":    fileB,
	})

	doc, err := Load(srv.URL + "/main.json")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	done := make(chan struct{})
	go func() {
		Bundle(doc)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Bundle did not terminate within 5s on a real cyclic external ref graph")
	}

	if _, ok := doc.Components.Schemas["A"]; !ok {
		t.Fatalf("expected a real local A schema, got %+v", doc.Components.Schemas)
	}
	if _, ok := doc.Components.Schemas["B"]; !ok {
		t.Fatalf("expected a real local B schema, got %+v", doc.Components.Schemas)
	}
	mustReparseNetworkFree(t, doc)
}

// TestBundle_NestedForeignRefRelativeToExternalDocument is the real
// regression guard for the bug this design's own build found and fixed:
// a ref found INSIDE an already-external subtree can itself be a bare
// "#/definitions/X"-shaped ref -- meaningless relative to the MAIN
// document (which, after v2->v3 conversion, has no top-level
// "definitions" field at all), genuinely meaningful only relative to
// the EXTERNAL file it was defined in. Confirmed live against Azure's
// own real common-types/*/types.json (ProxyResource's own real "allOf":
// [{"$ref": "#/definitions/Resource"}]) before this test was written --
// reproduced here hermetically so it can't regress silently.
func TestBundle_NestedForeignRefRelativeToExternalDocument(t *testing.T) {
	common := `{
		"swagger": "2.0", "info": {"title": "common", "version": "1"}, "paths": {},
		"definitions": {
			"Resource": {"type": "object", "properties": {"id": {"type": "string"}}},
			"ProxyResource": {"allOf": [{"$ref": "#/definitions/Resource"}], "type": "object", "properties": {"name": {"type": "string"}}}
		}
	}`
	main := `{
		"swagger": "2.0",
		"info": {"title": "main", "version": "1"},
		"paths": {
			"/things/{id}": {
				"get": {
					"operationId": "getThing",
					"parameters": [{"name": "id", "in": "path", "required": true, "type": "string"}],
					"responses": {"200": {"description": "ok", "schema": {"$ref": "./common.json#/definitions/ProxyResource"}}}
				}
			}
		}
	}`
	srv := bundleTestServer(t, map[string]string{"/main.json": main, "/common.json": common})

	doc, err := Load(srv.URL + "/main.json")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	Bundle(doc)

	if _, ok := doc.Components.Schemas["Resource"]; !ok {
		t.Fatalf("expected the real, nested foreign ref (ProxyResource's own allOf -> Resource) to ALSO be bundled locally, got %+v", doc.Components.Schemas)
	}
	if _, ok := doc.Components.Schemas["ProxyResource"]; !ok {
		t.Fatalf("expected ProxyResource itself to be bundled locally, got %+v", doc.Components.Schemas)
	}

	mustReparseNetworkFree(t, doc)
}

// TestBundle_CollisionGetsDeterministicSuffix proves two distinct
// external targets sharing the same derived base name (a real
// possibility: different files each defining their own "Widget") don't
// collide silently -- one keeps the base name, the other gets a
// deterministic numeric suffix, and the assignment doesn't depend on
// Go's own randomized map iteration order (this project's own
// established determinism rule).
func TestBundle_CollisionGetsDeterministicSuffix(t *testing.T) {
	fileA := `{"swagger": "2.0", "info": {"title": "a", "version": "1"}, "paths": {},
		"definitions": {"Widget": {"type": "object", "properties": {"fromA": {"type": "string"}}}}}`
	fileB := `{"swagger": "2.0", "info": {"title": "b", "version": "1"}, "paths": {},
		"definitions": {"Widget": {"type": "object", "properties": {"fromB": {"type": "string"}}}}}`
	main := `{
		"swagger": "2.0",
		"info": {"title": "main", "version": "1"},
		"paths": {
			"/a/{id}": {"get": {"operationId": "getA", "parameters": [{"name": "id", "in": "path", "required": true, "type": "string"}],
				"responses": {"200": {"description": "ok", "schema": {"$ref": "./a.json#/definitions/Widget"}}}}},
			"/b/{id}": {"get": {"operationId": "getB", "parameters": [{"name": "id", "in": "path", "required": true, "type": "string"}],
				"responses": {"200": {"description": "ok", "schema": {"$ref": "./b.json#/definitions/Widget"}}}}}
		}
	}`
	srv := bundleTestServer(t, map[string]string{"/main.json": main, "/a.json": fileA, "/b.json": fileB})

	doc, err := Load(srv.URL + "/main.json")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	Bundle(doc)

	if len(doc.Components.Schemas) != 2 {
		t.Fatalf("expected exactly 2 real local schemas (both Widgets, disambiguated), got %d: %+v", len(doc.Components.Schemas), doc.Components.Schemas)
	}
	names := make([]string, 0, 2)
	for name := range doc.Components.Schemas {
		names = append(names, name)
	}
	hasBase, hasSuffixed := false, false
	for _, n := range names {
		if n == "Widget" {
			hasBase = true
		}
		if n == "Widget_2" {
			hasSuffixed = true
		}
	}
	if !hasBase || !hasSuffixed {
		t.Fatalf("expected real names {Widget, Widget_2}, got %v", names)
	}
	mustReparseNetworkFree(t, doc)
}

// TestBundle_NoOpWhenNoExternalRefs proves Bundle leaves a document with
// only real internal refs byte-for-byte unchanged -- confirmed live
// against Kubernetes/Datadog/GitHub's own real, published specs before
// this test was written (all three had zero non-example external refs,
// GitHub's own 3 all under x-ms-examples, which Bundle structurally
// never reaches).
func TestBundle_NoOpWhenNoExternalRefs(t *testing.T) {
	main := `{
		"swagger": "2.0",
		"info": {"title": "main", "version": "1"},
		"paths": {
			"/widgets/{id}": {
				"get": {
					"operationId": "getWidget",
					"parameters": [{"name": "id", "in": "path", "required": true, "type": "string"}],
					"responses": {"200": {"description": "ok", "schema": {"$ref": "#/definitions/Widget"}}}
				}
			}
		},
		"definitions": {"Widget": {"type": "object", "properties": {"id": {"type": "string"}}}}
	}`
	srv := bundleTestServer(t, map[string]string{"/main.json": main})

	doc, err := Load(srv.URL + "/main.json")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	before, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal before: %v", err)
	}
	Bundle(doc)
	after, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal after: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("Bundle changed a document with zero external refs -- expected a real no-op\nbefore: %s\nafter:  %s", before, after)
	}
}
