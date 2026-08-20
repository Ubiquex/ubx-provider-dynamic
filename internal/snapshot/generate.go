package snapshot

import (
	"encoding/json"
	"fmt"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"

	"github.com/ubiquex/ubx-provider-dynamic/internal/config"
	"github.com/ubiquex/ubx-provider-dynamic/internal/dynserver"
	"github.com/ubiquex/ubx-provider-dynamic/internal/openapi"
)

// GenerateOpenAPI fetches provider's real, live schema_source = "openapi"
// spec ONE real time (the one real network call this whole package ever
// makes), verifies it needs no further network access to re-parse, and
// returns a real, complete Snapshot -- version mechanically derived
// against prev (nil for a provider's first-ever snapshot; DiffLevel/
// NextVersion's own doc comments cover why that case can never come out
// Major).
//
// The real, live verification step matters, not a formality: kin-openapi
// resolves a real spec's own $refs into its own in-memory Value fields as
// it parses (openapi.Load, called here with real network access to
// resolve them), but SchemaRef's own MarshalJSON always re-emits a bare
// {"$ref": "..."} string whenever Ref is set, NEVER the already-resolved
// Value alongside it -- confirmed live before this package was written,
// not assumed. For a spec whose own $refs are entirely internal
// (#/components/schemas/... -- confirmed live for every real
// schema_source = "openapi" provider this session has onboarded:
// Datadog, GitHub, Kubernetes), that's harmless: the ref target lives in
// the SAME document being snapshotted, so re-parsing needs nothing
// external. For a spec with real EXTERNAL refs (confirmed live only for
// Azure's own real, published Swagger 2.0 specs, a genuinely different
// shape -- "../../common-types/v1/common.json#/definitions/Foo") the
// marshaled snapshot would still carry that external path, and a later,
// network-free reload would fail to resolve it. Rather than silently
// producing a snapshot that looks complete but isn't, this function
// proves the real, marshaled RawSpec re-parses with zero network access
// (openapi.Parse(raw, nil) -- no location, so a relative external ref
// fails fast and loud instead of attempting a real fetch) before ever
// returning a Snapshot, and returns ErrExternalRefsUnsupported,
// unmistakably, if it doesn't. Real, explicit, named follow-up work, not
// attempted this session: a real "bundling" pass (rewriting external
// refs into the document's own Components.Schemas so they become
// internal) would close this gap for Azure-shaped specs -- see the
// package's own doc comment for the full real scope statement.
func GenerateOpenAPI(providerName, schemaURL string, execCfg config.Provider, prev *Snapshot) (*Snapshot, error) {
	doc, err := openapi.Load(schemaURL)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", schemaURL, err)
	}

	rawSpec, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("marshal fetched spec: %w", err)
	}

	if _, err := openapi.Parse(rawSpec, nil); err != nil {
		return nil, fmt.Errorf("%w: %s's own real spec has at least one $ref this snapshot can't yet make network-free (real bundling support for external refs is named, explicit, unstarted follow-up work, not a silent gap): %v",
			ErrExternalRefsUnsupported, schemaURL, err)
	}

	newResources, _, err := dynserver.Build(doc, providerName, execCfg)
	if err != nil {
		return nil, fmt.Errorf("build resource schemas: %w", err)
	}
	newSchemas := make(map[string]*tfprotov6.Schema, len(newResources))
	for typeName, rt := range newResources {
		newSchemas[typeName] = rt.Schema
	}

	version := ""
	if prev != nil {
		oldSchemas, err := schemasFromSnapshot(providerName, prev)
		if err != nil {
			return nil, fmt.Errorf("reconstruct prior snapshot's own real schema for diffing: %w", err)
		}
		level := DiffLevel(oldSchemas, newSchemas)
		version, err = NextVersion(prev.Version, level)
		if err != nil {
			return nil, fmt.Errorf("derive next version from %s's own prior version %q: %w", providerName, prev.Version, err)
		}
	} else {
		version, err = NextVersion("", Minor)
		if err != nil {
			return nil, err
		}
	}

	return &Snapshot{
		SchemaFormat: CurrentSchemaFormat,
		Provider:     providerName,
		Version:      version,
		SchemaSource: SchemaSourceOpenAPI,
		Auth:         execCfg.Auth,
		BaseURL:      execCfg.BaseURL,
		Retry:        execCfg.Retry,
		Timeouts:     execCfg.Timeouts,
		Resources:    execCfg.Resources,
		RawSpec:      rawSpec,
	}, nil
}

// LoadOpenAPI is Save's own real, network-free counterpart: re-derives
// snap's own complete, real resource map (schemas, CRUD paths/methods,
// everything dynserver.Server needs to actually serve real RPCs) purely
// from RawSpec -- zero network calls, the SAME real translation
// (openapi.Parse + dynserver.Build) the binary already runs at live-fetch
// time, just fed frozen bytes instead of a fresh HTTP GET. The one real,
// deliberate design choice worth naming: this RE-TRANSLATES on every
// call rather than caching the result inside Snapshot itself, because
// re-translating is what makes SchemaFormat's own real promise
// ("the binary's own translation logic can evolve independently of a
// provider's own frozen spec") actually true -- a newer binary build
// reading an older snapshot gets that build's own, possibly-improved
// translation of the SAME real, unchanged spec, not a stale, pre-
// translated artifact frozen at generation time.
func LoadOpenAPI(providerName string, snap *Snapshot) (map[string]*dynserver.ResourceType, error) {
	if snap.SchemaSource != SchemaSourceOpenAPI {
		return nil, fmt.Errorf("snapshot's own schema_source %q is not openapi", snap.SchemaSource)
	}
	doc, err := openapi.Parse(snap.RawSpec, nil)
	if err != nil {
		return nil, fmt.Errorf("parse snapshot's own raw_spec: %w", err)
	}
	execCfg := config.Provider{
		BaseURL:  snap.BaseURL,
		Auth:     snap.Auth,
		Retry:    snap.Retry,
		Timeouts: snap.Timeouts,
	}
	resources, _, err := dynserver.Build(doc, providerName, execCfg)
	if err != nil {
		return nil, fmt.Errorf("rebuild snapshot's own resource schemas: %w", err)
	}
	return resources, nil
}

// schemasFromSnapshot is GenerateOpenAPI's own diff-step helper: the same
// real LoadOpenAPI, narrowed to just the Schema every resource carries --
// DiffLevel only ever needs the schema tree, never CRUD paths/methods.
func schemasFromSnapshot(providerName string, snap *Snapshot) (map[string]*tfprotov6.Schema, error) {
	resources, err := LoadOpenAPI(providerName, snap)
	if err != nil {
		return nil, err
	}
	out := make(map[string]*tfprotov6.Schema, len(resources))
	for typeName, rt := range resources {
		out[typeName] = rt.Schema
	}
	return out, nil
}

// ErrExternalRefsUnsupported is GenerateOpenAPI's own real, named
// sentinel for the one real, explicit scope gap this function refuses to
// paper over -- see its own doc comment.
var ErrExternalRefsUnsupported = fmt.Errorf("spec has external $refs this snapshot format can't yet make network-free")
