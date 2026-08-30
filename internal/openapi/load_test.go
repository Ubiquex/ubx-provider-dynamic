package openapi

import (
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
)

// TestLoadWithRedoclyBundle_MissingNpx_NamesFlagAndProvider proves UBI-217's
// own real requirement: a human hitting this locally (Node.js is not
// guaranteed present the way a CI runner image guarantees it) needs to
// know immediately why Node is needed and which config entry asked for
// it, not a bare "npx: command not found" with no link back to the real
// cause. Forces the real "npx not found" path by pointing PATH at an
// empty directory rather than mocking exec.LookPath -- this package's
// own "real tests, no transport mocking" discipline.
func TestLoadWithRedoclyBundle_MissingNpx_NamesFlagAndProvider(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := exec.LookPath("npx"); err == nil {
		t.Fatal("test setup broken: npx still resolves with PATH cleared")
	}

	_, err := LoadWithRedoclyBundle("https://example.invalid/spec.yaml", "digitalocean")
	if err == nil {
		t.Fatal("expected an error when npx is not in PATH")
	}
	for _, want := range []string{"digitalocean", "redocly_bundle", "npx", "PATH", "nodejs.org"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q -- should name both the flag and the provider so someone hitting this locally knows immediately why Node is needed", err.Error(), want)
		}
	}
}

// TestLoadWithRedoclyBundle_RealBundle proves the real, live bundling path
// end to end against DigitalOcean's own real, published spec -- the exact
// real document UBI-217 was filed against, confirmed live: kin-openapi's
// own plain Load fails outright on this document (a Tag Object's own
// "description" field expressed as $ref, which kin-openapi refuses to
// unmarshal into Tag.description's own plain string field), and
// LoadWithRedoclyBundle resolves it correctly. Gated behind
// UBX_LIVE_VALIDATION like this package's other live tests, since it
// needs both real network access and a real Node.js install.
func TestLoadWithRedoclyBundle_RealBundle(t *testing.T) {
	requireLive(t)
	if _, err := exec.LookPath("npx"); err != nil {
		t.Skip("npx not found in PATH, skipping the real bundling test")
	}
	const url = "https://raw.githubusercontent.com/digitalocean/openapi/main/specification/DigitalOcean-public.v2.yaml"

	if _, plainErr := Load(url); plainErr == nil {
		t.Fatal("expected the real, unbundled Load to fail (a Tag Object's own \"description\" field expressed as $ref, which kin-openapi cannot unmarshal into a plain string) -- UBI-217's own real finding may no longer hold")
	}

	doc, err := LoadWithRedoclyBundle(url, "digitalocean")
	if err != nil {
		t.Fatalf("LoadWithRedoclyBundle: %v", err)
	}
	if doc.Paths == nil || doc.Paths.Len() == 0 {
		t.Fatal("expected real paths to survive bundling, got none")
	}
	t.Logf("real DigitalOcean spec, bundled: %d paths", doc.Paths.Len())
}

// TestLoad_JSONSurrogatePairEscapeSurvives proves UBI-217's own real,
// live-found bug stays fixed: Linode's real, published OpenAPI 3.0.1 spec
// is valid JSON containing a UTF-16 surrogate-pair Unicode escape (an
// emoji, JSON-legal per RFC 8259) inside a field description.
// oasdiff/yaml, the shared YAML lib Parse used to route every source
// through unconditionally, rejects this exact escape outright ("found
// invalid Unicode character escape code") even though encoding/json
// parses the identical bytes correctly -- confirmed live before this fix
// landed. A genuinely JSON source (checked via json.Valid) now skips the
// YAML library entirely, since it never needed YAML-to-JSON conversion in
// the first place.
func TestLoad_JSONSurrogatePairEscapeSurvives(t *testing.T) {
	raw := []byte("{\"openapi\": \"3.0.1\", \"info\": {\"title\": \"test\", \"version\": \"1\"}, " +
		"\"paths\": {\"/widgets\": {\"get\": {\"description\": \"book \\uD83D\\uDCD8 icon\", " +
		"\"responses\": {\"200\": {\"description\": \"ok\"}}}}}}")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(raw)
	}))
	defer srv.Close()

	doc, err := Load(srv.URL)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	path := doc.Paths.Find("/widgets")
	if path == nil || path.Get == nil {
		t.Fatalf("expected the real GET /widgets operation to survive parsing")
	}
	const want = "book \U0001F4D8 icon"
	if path.Get.Description != want {
		t.Fatalf("description = %q, want %q (the decoded surrogate pair)", path.Get.Description, want)
	}
}

// TestLoad_SwaggerV2ConvertsToV3 proves the real Swagger 2.0 -> OpenAPI 3
// conversion path against a small, real-shaped (not live) fixture -- a
// real local httptest server, not a mocked openapi3.T, so Load's own real
// fetch+probe+convert sequence is exercised end to end, matching this
// project's own "real tests, no transport mocking" discipline (only the
// SERVER side is local here, the code under test performs a real HTTP
// round trip against it).
func TestLoad_SwaggerV2ConvertsToV3(t *testing.T) {
	const swagger2 = `{
		"swagger": "2.0",
		"info": {"title": "test", "version": "1"},
		"paths": {
			"/widgets/{id}": {
				"get": {
					"operationId": "getWidget",
					"parameters": [{"name": "id", "in": "path", "required": true, "type": "string"}],
					"responses": {
						"200": {
							"description": "ok",
							"schema": {"$ref": "#/definitions/Widget"}
						}
					}
				}
			}
		},
		"definitions": {
			"Widget": {
				"type": "object",
				"properties": {
					"id": {"type": "string"},
					"name": {"type": "string"}
				}
			}
		}
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(swagger2))
	}))
	defer srv.Close()

	doc, err := Load(srv.URL)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if doc.OpenAPI == "" {
		t.Fatalf("expected a real OpenAPI 3.x version string after conversion, got %+v", doc.OpenAPI)
	}
	schema, ok := doc.Components.Schemas["Widget"]
	if !ok || schema.Value == nil {
		t.Fatalf("expected the real Widget schema to survive conversion, got %+v", doc.Components.Schemas)
	}
	if _, ok := schema.Value.Properties["name"]; !ok {
		t.Fatalf("expected Widget.name to survive conversion, got %+v", schema.Value.Properties)
	}
	path := doc.Paths.Find("/widgets/{id}")
	if path == nil || path.Get == nil {
		t.Fatalf("expected the real GET /widgets/{id} operation to survive conversion")
	}
}

// TestLoad_OpenAPIv3StillWorks proves the pre-existing v3 path is
// unaffected by the new version-sniffing logic -- a real regression guard,
// not just a Swagger2-only proof.
func TestLoad_OpenAPIv3StillWorks(t *testing.T) {
	const v3 = `{
		"openapi": "3.0.0",
		"info": {"title": "test", "version": "1"},
		"paths": {},
		"components": {"schemas": {"Widget": {"type": "object", "properties": {"id": {"type": "string"}}}}}
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(v3))
	}))
	defer srv.Close()

	doc, err := Load(srv.URL)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if doc.OpenAPI != "3.0.0" {
		t.Fatalf("OpenAPI = %q, want 3.0.0", doc.OpenAPI)
	}
}
