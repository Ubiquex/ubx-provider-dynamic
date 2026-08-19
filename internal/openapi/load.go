// Package openapi loads and parses a real OpenAPI document (schema_source
// = "openapi"), from either an http(s) URL or a local file path -- both
// GitHub's and Datadog's own real, published specs are plain https URLs, so
// that's the primary path Phase 1 validates, with a local-file fallback for
// tests and for any schema_url an operator has already vendored.
package openapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/getkin/kin-openapi/openapi2"
	"github.com/getkin/kin-openapi/openapi2conv"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/oasdiff/yaml"
)

// httpClient bounds a real spec fetch -- real specs can be large (Kubernetes'
// own real swagger.json is several MB), never http.DefaultClient's unbounded
// wait.
var httpClient = &http.Client{Timeout: 60 * time.Second}

// versionProbe sniffs which real OpenAPI generation a document declares,
// without committing to parsing it as either yet -- both "swagger" (2.0) and
// "openapi" (3.x) are real, required, top-level fields their own respective
// specs mandate, so a plain JSON unmarshal into this tiny struct is enough
// to decide, no heuristic guessing needed.
type versionProbe struct {
	Swagger string `json:"swagger"`
	OpenAPI string `json:"openapi"`
}

// Load fetches and parses source (an http(s) URL or local file path) as a
// real OpenAPI document, following internal $refs (component schemas) and
// external $refs (other files/URLs a spec may split itself across -- most
// real specs this session has loaded are single-file, but this isn't
// assumed). Two real generations are handled, per the document's own
// declared version, never guessed from the source URL's own shape:
//
//   - OpenAPI 3.x ("openapi": "3.x") -- parsed directly via kin-openapi's
//     own v3 loader, unchanged from Phase 1.
//   - Swagger 2.0 ("swagger": "2.0") -- real, confirmed finding
//     (onboarding-pipeline naming-derivation work): Kubernetes' own real,
//     official API surface has no single-file OpenAPI 3.x document at all
//     (github.com/kubernetes/kubernetes's own api/openapi-spec/ splits v3
//     into 65+ separate per-API-group files; only the real, complete,
//     single-file document is v2 -- api/openapi-spec/swagger.json).
//     Converted to v3 via kin-openapi's own real, already-vendored
//     openapi2conv.ToV3 (the same module this package already depends on
//     for v3 parsing itself) -- not a hand-rolled conversion, and not a
//     new dependency.
//
// Deliberately does not call doc.Validate: real, currently-published specs
// (GitHub's own included) are known to fail kin-openapi's strict validator
// on details irrelevant to schema translation (e.g. example/default value
// mismatches) -- refusing to load over that would make this loader less
// honest about real-world OpenAPI, not more, so parsing and $ref resolution
// succeeding is the real bar, not full spec-conformance validation.
func Load(source string) (*openapi3.T, error) {
	raw, err := fetch(source)
	if err != nil {
		return nil, fmt.Errorf("load OpenAPI spec from %s: %w", source, err)
	}

	// Real, confirmed regression caught by this session's own live test
	// suite before it ever reached a caller: Datadog's own real, published
	// spec is YAML-encoded, not JSON -- oasdiff/yaml (already a real,
	// vendored transitive dependency of kin-openapi itself, the same
	// library kin-openapi's own v3 loader uses internally for YAML
	// support) converts YAML to JSON first; a JSON source round-trips
	// through this unchanged (JSON is valid YAML 1.2), so this is safe
	// for every real source this package has ever loaded, not just the
	// new Swagger 2.0 case.
	jsonRaw, err := yaml.YAMLToJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("load OpenAPI spec from %s: not valid JSON or YAML: %w", source, err)
	}

	var probe versionProbe
	if err := json.Unmarshal(jsonRaw, &probe); err != nil {
		return nil, fmt.Errorf("load OpenAPI spec from %s: %w", source, err)
	}

	if probe.Swagger != "" {
		var doc2 openapi2.T
		if err := json.Unmarshal(jsonRaw, &doc2); err != nil {
			return nil, fmt.Errorf("parse Swagger %s document from %s: %w", probe.Swagger, source, err)
		}
		doc3, err := openapi2conv.ToV3(&doc2)
		if err != nil {
			return nil, fmt.Errorf("convert Swagger %s document from %s to OpenAPI 3: %w", probe.Swagger, source, err)
		}
		return doc3, nil
	}

	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = true
	// LoadFromDataWithPath, not LoadFromData: it sets the same document-path
	// context LoadFromURI/LoadFromFile establish internally, so a real
	// spec's own relative external $refs (a real, if rare, shape neither
	// GitHub's nor Datadog's own current spec exercises, but not assumed
	// impossible for a future real source) still resolve correctly -- the
	// version probe above already consumed the fetch, so this reuses raw
	// rather than fetching source a second time.
	var location *url.URL
	if u, err := url.Parse(source); err == nil && (u.Scheme == "http" || u.Scheme == "https") {
		location = u
	} else if abs, err := filepath.Abs(source); err == nil {
		location = &url.URL{Scheme: "file", Path: abs}
	}
	doc, err := loader.LoadFromDataWithPath(raw, location)
	if err != nil {
		return nil, fmt.Errorf("parse OpenAPI %s document from %s: %w", probe.OpenAPI, source, err)
	}
	return doc, nil
}

// fetch retrieves source's own raw bytes -- an http(s) URL or a local file
// path, the identical two real source shapes Phase 1's own loader already
// supported, just factored out here so both the v2 and v3 parse paths can
// share one real fetch instead of the version probe needing its own,
// separate request.
func fetch(source string) ([]byte, error) {
	u, err := url.Parse(source)
	if err == nil && (u.Scheme == "http" || u.Scheme == "https") {
		resp, err := httpClient.Get(source)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			return nil, fmt.Errorf("HTTP %d fetching %s", resp.StatusCode, source)
		}
		return io.ReadAll(resp.Body)
	}
	return os.ReadFile(source)
}
