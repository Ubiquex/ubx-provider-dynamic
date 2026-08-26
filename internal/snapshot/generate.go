package snapshot

import (
	"encoding/json"
	"fmt"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"

	"github.com/ubiquex/ubx-provider-dynamic/internal/cloudformation"
	"github.com/ubiquex/ubx-provider-dynamic/internal/config"
	"github.com/ubiquex/ubx-provider-dynamic/internal/discoverydoc"
	"github.com/ubiquex/ubx-provider-dynamic/internal/dynserver"
	"github.com/ubiquex/ubx-provider-dynamic/internal/openapi"
	"github.com/ubiquex/ubx-provider-dynamic/internal/smithy"
)

// generateFromSchemas is the real, shared core every Generate<Source>
// below funnels through once its own native fetch+Build has produced the
// SAME real converged type every source's own Build pipeline already
// produces -- diffing (DiffLevel), semver derivation (NextVersion), and
// Snapshot construction are identical regardless of which real format
// rawSpec came from. See the package doc comment for why this is scoped
// here (post-translation) rather than forced onto the fetch/Build step,
// which genuinely differs per source.
//
// oldSchemas is nil for a provider's first-ever snapshot (prev == nil);
// NextVersion's own doc comment covers why that case always yields
// "1.0.0" regardless of level, so the level computed in that branch is
// never actually load-bearing -- kept as an explicit, mirrored branch
// (not relying on DiffLevel(nil, newSchemas) happening to behave) to
// match GenerateOpenAPI's own original, already-proven structure exactly.
func generateFromSchemas(providerName string, source SchemaSource, rawSpec json.RawMessage, newSchemas map[string]*tfprotov6.Schema, execCfg config.Provider, prev *Snapshot, oldSchemas map[string]*tfprotov6.Schema) (*Snapshot, error) {
	var version string
	var err error
	if prev != nil {
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
		SchemaSource: source,
		Auth:         execCfg.Auth,
		BaseURL:      execCfg.BaseURL,
		Retry:        execCfg.Retry,
		Timeouts:     execCfg.Timeouts,
		Resources:    execCfg.Resources,
		RawSpec:      rawSpec,
	}, nil
}

// ---------------------------------------------------------------------
// openapi
// ---------------------------------------------------------------------

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

	var oldSchemas map[string]*tfprotov6.Schema
	if prev != nil {
		oldResources, err := LoadOpenAPI(providerName, prev)
		if err != nil {
			return nil, fmt.Errorf("reconstruct prior snapshot's own real schema for diffing: %w", err)
		}
		oldSchemas = make(map[string]*tfprotov6.Schema, len(oldResources))
		for typeName, rt := range oldResources {
			oldSchemas[typeName] = rt.Schema
		}
	}

	return generateFromSchemas(providerName, SchemaSourceOpenAPI, rawSpec, newSchemas, execCfg, prev, oldSchemas)
}

// LoadOpenAPI is GenerateOpenAPI's own real, network-free counterpart:
// re-derives snap's own complete, real resource map (schemas, CRUD
// paths/methods, everything dynserver.Server needs to actually serve
// real RPCs) purely from RawSpec -- zero network calls, the SAME real
// translation (openapi.Parse + dynserver.Build) the binary already runs
// at live-fetch time, just fed frozen bytes instead of a fresh HTTP GET.
// The one real, deliberate design choice worth naming: this
// RE-TRANSLATES on every call rather than caching the result inside
// Snapshot itself, because re-translating is what makes SchemaFormat's
// own real promise ("the binary's own translation logic can evolve
// independently of a provider's own frozen spec") actually true -- a
// newer binary build reading an older snapshot gets that build's own,
// possibly-improved translation of the SAME real, unchanged spec, not a
// stale, pre-translated artifact frozen at generation time.
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

// ErrExternalRefsUnsupported is GenerateOpenAPI's own real, named
// sentinel for the one real, explicit scope gap this function refuses to
// paper over -- see its own doc comment.
var ErrExternalRefsUnsupported = fmt.Errorf("spec has external $refs this snapshot format can't yet make network-free")

// ---------------------------------------------------------------------
// cloudformation
// ---------------------------------------------------------------------

// GenerateCloudFormation mirrors GenerateOpenAPI exactly, for AWS's real
// CloudFormation resource-provider schema registry. RawSpec here is the
// whole real map[string]*cloudformation.ResourceSchema the registry zip
// fetch produces (one file per real "AWS::<Namespace>::<Type>" resource
// type), not a single document -- confirmed safe to round-trip through
// plain encoding/json (ResourceSchema carries ordinary JSON tags, none
// of kin-openapi's SchemaRef marshal-loses-the-resolved-Value behavior
// GenerateOpenAPI's own reparse check exists to catch), so no
// ErrExternalRefsUnsupported-style network-free-reparse gate is needed
// here -- verified by actually generating and reloading a real snapshot
// (internal/snapshot/generate_test.go), not assumed from the struct
// tags alone.
func GenerateCloudFormation(providerName, schemaURL string, execCfg config.Provider, prev *Snapshot) (*Snapshot, error) {
	files, err := cloudformation.Fetch(schemaURL)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", schemaURL, err)
	}

	rawSpec, err := json.Marshal(files)
	if err != nil {
		return nil, fmt.Errorf("marshal fetched spec: %w", err)
	}

	newBuilt, _, err := cloudformation.Build(files, smithy.DefaultKnownNames())
	if err != nil {
		return nil, fmt.Errorf("build resource schemas: %w", err)
	}
	newSchemas := make(map[string]*tfprotov6.Schema, len(newBuilt))
	for typeName, rt := range newBuilt {
		newSchemas[typeName] = rt.Schema
	}

	var oldSchemas map[string]*tfprotov6.Schema
	if prev != nil {
		oldBuilt, err := LoadCloudFormation(prev)
		if err != nil {
			return nil, fmt.Errorf("reconstruct prior snapshot's own real schema for diffing: %w", err)
		}
		oldSchemas = make(map[string]*tfprotov6.Schema, len(oldBuilt))
		for typeName, rt := range oldBuilt {
			oldSchemas[typeName] = rt.Schema
		}
	}

	return generateFromSchemas(providerName, SchemaSourceCloudFormation, rawSpec, newSchemas, execCfg, prev, oldSchemas)
}

// LoadCloudFormation is GenerateCloudFormation's own real, network-free
// counterpart -- same real re-translation discipline LoadOpenAPI already
// documents (re-runs cloudformation.Build fresh on every call, never a
// frozen pre-translated artifact). providerName is deliberately not a
// parameter here: cloudformation.Build takes no providerName at all
// (confirmed against its real signature) -- each real CFN resource's own
// type name is already fully qualified ("AWS::<Namespace>::<Type>").
func LoadCloudFormation(snap *Snapshot) (map[string]*cloudformation.BuiltResource, error) {
	if snap.SchemaSource != SchemaSourceCloudFormation {
		return nil, fmt.Errorf("snapshot's own schema_source %q is not cloudformation", snap.SchemaSource)
	}
	var files map[string]*cloudformation.ResourceSchema
	if err := json.Unmarshal(snap.RawSpec, &files); err != nil {
		return nil, fmt.Errorf("parse snapshot's own raw_spec: %w", err)
	}
	resources, _, err := cloudformation.Build(files, smithy.DefaultKnownNames())
	if err != nil {
		return nil, fmt.Errorf("rebuild snapshot's own resource schemas: %w", err)
	}
	return resources, nil
}

// ---------------------------------------------------------------------
// smithy
// ---------------------------------------------------------------------

// GenerateSmithy mirrors GenerateOpenAPI exactly, for AWS's real Smithy
// service models (schema_source = "smithy", used today for the ~430
// per-service AWS data-source entries -- see cmd/ubx-provider-dynamic's
// own real live-fetch branch). wireName, not providerName, is what gets
// baked into every generated resource type name (config.Provider.WireName's
// own doc comment has the full real reason these can differ) -- matches
// main.go's own real live-fetch call (smithy.Build(doc, wireName, ...)),
// not the table key alone. Both wireName and targetPrefix are stored
// onto the returned Snapshot (WireName/TargetPrefix, SchemaFormat 2) --
// necessary once a pinned [providers.<name>] entry is the only config a
// caller has: LoadSmithy has no live [dynamic_providers.<name>] table
// left to read either value from otherwise. No reparse-verification
// gate: smithy.Model's own real JSON shape has no equivalent to
// kin-openapi's SchemaRef marshal-loses-the-resolved-Value quirk --
// verified live, not assumed (internal/snapshot/generate_test.go).
func GenerateSmithy(providerName, wireName, schemaURL, targetPrefix string, execCfg config.Provider, prev *Snapshot) (*Snapshot, error) {
	doc, err := smithy.Load(schemaURL)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", schemaURL, err)
	}

	rawSpec, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("marshal fetched spec: %w", err)
	}

	newBuilt, _, err := smithy.Build(doc, wireName, smithy.DefaultKnownNames())
	if err != nil {
		return nil, fmt.Errorf("build resource schemas: %w", err)
	}
	newSchemas := make(map[string]*tfprotov6.Schema, len(newBuilt))
	for typeName, rt := range newBuilt {
		newSchemas[typeName] = rt.Schema
	}

	var oldSchemas map[string]*tfprotov6.Schema
	if prev != nil {
		oldBuilt, err := LoadSmithy(prev)
		if err != nil {
			return nil, fmt.Errorf("reconstruct prior snapshot's own real schema for diffing: %w", err)
		}
		oldSchemas = make(map[string]*tfprotov6.Schema, len(oldBuilt))
		for typeName, rt := range oldBuilt {
			oldSchemas[typeName] = rt.Schema
		}
	}

	snap, err := generateFromSchemas(providerName, SchemaSourceSmithy, rawSpec, newSchemas, execCfg, prev, oldSchemas)
	if err != nil {
		return nil, err
	}
	snap.WireName = wireName
	snap.TargetPrefix = targetPrefix
	return snap, nil
}

// LoadSmithy is GenerateSmithy's own real, network-free counterpart --
// same real re-translation discipline LoadOpenAPI already documents.
// Reads WireName from the snapshot itself (falls back to Provider when
// empty, matching config.Provider.WireName's own "defaults to name"
// convention) rather than taking it as a separate parameter -- see the
// Snapshot struct's own doc comment for why.
func LoadSmithy(snap *Snapshot) (map[string]*smithy.BuiltResource, error) {
	if snap.SchemaSource != SchemaSourceSmithy {
		return nil, fmt.Errorf("snapshot's own schema_source %q is not smithy", snap.SchemaSource)
	}
	wireName := snap.WireName
	if wireName == "" {
		wireName = snap.Provider
	}
	var doc smithy.Model
	if err := json.Unmarshal(snap.RawSpec, &doc); err != nil {
		return nil, fmt.Errorf("parse snapshot's own raw_spec: %w", err)
	}
	resources, _, err := smithy.Build(&doc, wireName, smithy.DefaultKnownNames())
	if err != nil {
		return nil, fmt.Errorf("rebuild snapshot's own resource schemas: %w", err)
	}
	return resources, nil
}

// ---------------------------------------------------------------------
// discovery_docs
// ---------------------------------------------------------------------

// GenerateDiscoveryDoc mirrors GenerateOpenAPI exactly, for GCP's real
// Discovery Documents. versionQualifier is stored onto the returned
// Snapshot (VersionQualifier, SchemaFormat 2) -- necessary once a
// pinned [providers.<name>] entry is the only config a caller has:
// LoadDiscoveryDoc has no live [dynamic_providers.<name>] table left to
// read it from otherwise. No reparse-verification gate:
// discoverydoc.Document's own real JSON shape has no equivalent to
// kin-openapi's SchemaRef marshal-loses-the-resolved-Value quirk --
// verified live, not assumed (internal/snapshot/generate_test.go).
func GenerateDiscoveryDoc(providerName, schemaURL, versionQualifier string, execCfg config.Provider, prev *Snapshot) (*Snapshot, error) {
	doc, err := discoverydoc.Load(schemaURL)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", schemaURL, err)
	}

	rawSpec, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("marshal fetched spec: %w", err)
	}

	newBuilt, _, err := discoverydoc.Build(doc, providerName, versionQualifier)
	if err != nil {
		return nil, fmt.Errorf("build resource schemas: %w", err)
	}
	newSchemas := make(map[string]*tfprotov6.Schema, len(newBuilt))
	for typeName, rt := range newBuilt {
		newSchemas[typeName] = rt.Schema
	}

	var oldSchemas map[string]*tfprotov6.Schema
	if prev != nil {
		oldBuilt, err := LoadDiscoveryDoc(prev)
		if err != nil {
			return nil, fmt.Errorf("reconstruct prior snapshot's own real schema for diffing: %w", err)
		}
		oldSchemas = make(map[string]*tfprotov6.Schema, len(oldBuilt))
		for typeName, rt := range oldBuilt {
			oldSchemas[typeName] = rt.Schema
		}
	}

	snap, err := generateFromSchemas(providerName, SchemaSourceDiscoveryDoc, rawSpec, newSchemas, execCfg, prev, oldSchemas)
	if err != nil {
		return nil, err
	}
	snap.VersionQualifier = versionQualifier
	return snap, nil
}

// LoadDiscoveryDoc is GenerateDiscoveryDoc's own real, network-free
// counterpart -- same real re-translation discipline LoadOpenAPI already
// documents. Reads VersionQualifier from the snapshot itself rather than
// taking it as a separate parameter -- see the Snapshot struct's own doc
// comment for why. Provider (not a separate providerName parameter) is
// what discoverydoc.Build's own providerName argument needs -- Snapshot.Provider
// already carries the identical value GenerateDiscoveryDoc was called
// with, so a second, separate parameter here would only ever be able to
// disagree with it, never usefully differ.
func LoadDiscoveryDoc(snap *Snapshot) (map[string]*discoverydoc.BuiltResource, error) {
	if snap.SchemaSource != SchemaSourceDiscoveryDoc {
		return nil, fmt.Errorf("snapshot's own schema_source %q is not discovery_docs", snap.SchemaSource)
	}
	var doc discoverydoc.Document
	if err := json.Unmarshal(snap.RawSpec, &doc); err != nil {
		return nil, fmt.Errorf("parse snapshot's own raw_spec: %w", err)
	}
	resources, _, err := discoverydoc.Build(&doc, snap.Provider, snap.VersionQualifier)
	if err != nil {
		return nil, fmt.Errorf("rebuild snapshot's own resource schemas: %w", err)
	}
	return resources, nil
}
