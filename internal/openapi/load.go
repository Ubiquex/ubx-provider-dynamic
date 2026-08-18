// Package openapi loads and parses a real OpenAPI 3.x document (schema_source
// = "openapi"), from either an http(s) URL or a local file path -- both
// GitHub's and Datadog's own real, published specs are plain https URLs, so
// that's the primary path Phase 1 validates, with a local-file fallback for
// tests and for any schema_url an operator has already vendored.
package openapi

import (
	"fmt"
	"net/url"

	"github.com/getkin/kin-openapi/openapi3"
)

// Load fetches and parses source (an http(s) URL or local file path) as an
// OpenAPI 3.x document, following internal $refs (component schemas) and
// external $refs (other files/URLs a spec may split itself across -- both
// GitHub's and Datadog's real specs are single-file, but this isn't assumed).
//
// Deliberately does not call doc.Validate: real, currently-published specs
// (GitHub's own included) are known to fail kin-openapi's strict validator
// on details irrelevant to schema translation (e.g. example/default value
// mismatches) -- refusing to load over that would make this loader less
// honest about real-world OpenAPI, not more, so parsing and $ref resolution
// succeeding is the real bar, not full spec-conformance validation.
func Load(source string) (*openapi3.T, error) {
	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = true

	u, err := url.Parse(source)
	if err == nil && (u.Scheme == "http" || u.Scheme == "https") {
		doc, err := loader.LoadFromURI(u)
		if err != nil {
			return nil, fmt.Errorf("load OpenAPI spec from %s: %w", source, err)
		}
		return doc, nil
	}

	doc, err := loader.LoadFromFile(source)
	if err != nil {
		return nil, fmt.Errorf("load OpenAPI spec from file %s: %w", source, err)
	}
	return doc, nil
}
