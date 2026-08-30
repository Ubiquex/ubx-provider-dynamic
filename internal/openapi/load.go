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
	"os/exec"
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

// noLocation is Parse's own real placeholder for a genuinely absent
// document location -- see Parse's own doc comment for why loc=nil can't
// be passed straight through to kin-openapi's own loader.
var noLocation = &url.URL{Scheme: "ubx-no-location", Path: "/"}

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
	return load(source, "", false)
}

// LoadWithRedoclyBundle is Load's own real counterpart for a provider
// whose [dynamic_providers.<name>] table sets redocly_bundle = true
// (config.Provider.RedoclyBundle's own doc comment has the full real
// reason, UBI-217). Runs source through the real, already-correct
// `npx @redocly/cli bundle` first, resolving Redocly-only $ref
// conventions real OpenAPI 3.0 does not define before kin-openapi's own
// Load ever sees the bytes.
//
// providerName is used only to name the exact [dynamic_providers.<name>]
// entry in the error this produces if npx is not found in PATH, so
// someone hitting it locally (Node.js is not guaranteed present the way
// a CI runner image guarantees it) knows immediately why Node is needed
// and which config entry asked for it, rather than a bare "npx: command
// not found" with no link back to the real cause.
func LoadWithRedoclyBundle(source, providerName string) (*openapi3.T, error) {
	return load(source, providerName, true)
}

func load(source, providerName string, redoclyBundle bool) (*openapi3.T, error) {
	var raw []byte
	var err error
	if redoclyBundle {
		raw, err = bundleViaRedocly(source, providerName)
	} else {
		raw, err = fetch(source)
	}
	if err != nil {
		return nil, fmt.Errorf("load OpenAPI spec from %s: %w", source, err)
	}
	doc, err := Parse(raw, location(source))
	if err != nil {
		return nil, fmt.Errorf("load OpenAPI spec from %s: %w", source, err)
	}
	return doc, nil
}

// bundleViaRedocly shells out to the real, already-correct
// `npx @redocly/cli bundle` rather than reimplementing a bundler in Go
// (UBI-217's own real finding: path rewriting during content hoisting is
// exactly what makes a bundler non-trivial, and a partial Go
// reimplementation risks silently mishandling a real relative $ref a
// substituted file itself contains). Passes source straight through
// (an http(s) URL or local file path, the identical two real shapes
// Load itself accepts) -- Redocly's own bundler does its own fetching
// for a URL, so this package's own fetch() is never called for a
// bundled source at all.
//
// Bundles to JSON explicitly (--ext json), not source's own original
// format, so the result is always valid JSON -- Parse's own json.Valid
// check then skips oasdiff/yaml entirely for it, the identical fast
// path a genuinely JSON source already takes (UBI-217's own Linode fix).
func bundleViaRedocly(source, providerName string) ([]byte, error) {
	npx, err := exec.LookPath("npx")
	if err != nil {
		return nil, fmt.Errorf(
			"dynamic_providers.%s sets redocly_bundle = true (its own real, published spec needs Redocly's bundler to resolve non-standard $ref conventions kin-openapi cannot follow), but \"npx\" was not found in PATH -- install Node.js (https://nodejs.org) to onboard or regenerate this provider",
			providerName,
		)
	}

	tmp, err := os.CreateTemp("", "ubx-redocly-bundle-*.json")
	if err != nil {
		return nil, fmt.Errorf("bundle %s via @redocly/cli: create temp file: %w", source, err)
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)

	cmd := exec.Command(npx, "--yes", "@redocly/cli", "bundle", source, "--output", tmpPath, "--ext", "json")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("bundle %s via @redocly/cli: %w: %s", source, err, output)
	}

	bundled, err := os.ReadFile(tmpPath)
	if err != nil {
		return nil, fmt.Errorf("bundle %s via @redocly/cli: read bundled output: %w", source, err)
	}
	return bundled, nil
}

// location derives Parse's own loc argument from source (an http(s) URL or
// a local file path) -- factored out of Load so ParseSnapshot (a real,
// already-fetched-elsewhere raw document, no live source string to derive
// a location from) can still supply one when it has a real, remembered
// origin worth preserving for relative-$ref resolution, or nil when it
// doesn't.
func location(source string) *url.URL {
	if u, err := url.Parse(source); err == nil && (u.Scheme == "http" || u.Scheme == "https") {
		return u
	}
	if abs, err := filepath.Abs(source); err == nil {
		return &url.URL{Scheme: "file", Path: abs}
	}
	return nil
}

// Parse is Load's own real parsing half, split out so a real, frozen
// snapshot (internal/snapshot) can re-run the identical parse logic
// against already-in-hand bytes, with no network fetch at all -- the
// exact same real code path Load uses when it does have live network
// access, never a second, drifting reimplementation. loc is the document's
// own real origin (Load's own location() helper derives one from a live
// source string; a snapshot reload passes whatever origin was recorded at
// snapshot-generation time, or nil for a document with no real relative
// external $refs to resolve).
//
// Real, confirmed regression caught by this session's own live test suite
// before it ever reached a caller: Datadog's own real, published spec is
// YAML-encoded, not JSON -- oasdiff/yaml (already a real, vendored
// transitive dependency of kin-openapi itself, the same library
// kin-openapi's own v3 loader uses internally for YAML support) converts
// YAML to JSON first; a JSON source round-trips through this unchanged
// (JSON is valid YAML 1.2), so this is safe for every real source this
// package has ever loaded, not just the new Swagger 2.0 case.
//
// UBI-217: that round-trip is not actually safe for every real JSON
// source. Linode's own real, published OpenAPI 3.0.1 spec is valid JSON
// containing a UTF-16 surrogate-pair Unicode escape (an emoji, JSON-legal
// per RFC 8259) inside a field description -- oasdiff/yaml rejects it
// outright ("found invalid Unicode character escape code") even though
// encoding/json parses the identical bytes correctly, confirmed live via
// json.Valid/json.Unmarshal against the exact same raw bytes. A genuinely
// JSON source never needs YAML-to-JSON conversion in the first place
// (that step exists only for genuinely YAML-encoded sources like
// Datadog's), so checking json.Valid first and skipping the YAML library
// entirely for real JSON sidesteps the whole class of YAML-parser-only
// escape rejections, not just this one emoji.
func Parse(raw []byte, loc *url.URL) (*openapi3.T, error) {
	// Real, found-in-review bug, caught only when internal/snapshot's own
	// network-free reload path became the first real caller to ever pass
	// loc=nil (every prior caller derives a real, non-nil location from a
	// real source string): kin-openapi's own loadFromDataWithPathInternal
	// dereferences path unconditionally, regardless of whether the
	// document has any real external ref that would ever need it --
	// confirmed live, a genuine nil-pointer panic, not a clean error.
	// noLocation is a real, deliberately unresolvable placeholder (an
	// invented scheme no real fetcher understands) rather than something
	// that could ever coincidentally resolve: any real external $ref this
	// document turns out to have fails loud and immediate against it,
	// exactly the same real, honest refusal a genuinely nil location was
	// always meant to produce.
	if loc == nil {
		loc = noLocation
	}
	jsonRaw := raw
	if !json.Valid(raw) {
		var err error
		jsonRaw, err = yaml.YAMLToJSON(raw)
		if err != nil {
			return nil, fmt.Errorf("not valid JSON or YAML: %w", err)
		}
	}

	var probe versionProbe
	if err := json.Unmarshal(jsonRaw, &probe); err != nil {
		return nil, err
	}

	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = true

	if probe.Swagger != "" {
		var doc2 openapi2.T
		if err := json.Unmarshal(jsonRaw, &doc2); err != nil {
			return nil, fmt.Errorf("parse Swagger %s document: %w", probe.Swagger, err)
		}
		// ToV3WithLoader, not the plain ToV3 wrapper: ToV3 constructs its
		// own internal loader with IsExternalRefsAllowed left at its real,
		// safe-by-default false -- confirmed live against Azure's own
		// real, published Swagger 2.0 specs (a genuinely different real
		// shape from Kubernetes' own single-file spec): Azure's own real
		// per-resource-provider files reference shared external
		// "common-types" definition files by real relative path
		// ("../../common-types/v1/common.json#/definitions/SubResource"),
		// which ToV3's own default loader refuses outright
		// ("encountered disallowed external reference"). Passing this
		// package's own loader (already carrying IsExternalRefsAllowed
		// and, from here on, the real document location too) through
		// lets those real external refs resolve exactly the way the v3
		// path below already does.
		doc3, err := openapi2conv.ToV3WithLoader(&doc2, loader, loc)
		if err != nil {
			return nil, fmt.Errorf("convert Swagger %s document to OpenAPI 3: %w", probe.Swagger, err)
		}
		return doc3, nil
	}

	doc, err := loader.LoadFromDataWithPath(raw, loc)
	if err != nil {
		return nil, fmt.Errorf("parse OpenAPI %s document: %w", probe.OpenAPI, err)
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
