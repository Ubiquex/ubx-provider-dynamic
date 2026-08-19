package openapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

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
