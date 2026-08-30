package snapshot

import (
	"encoding/json"
	"fmt"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"

	"github.com/ubiquex/ubx-provider-dynamic/internal/cloudformation"
	"github.com/ubiquex/ubx-provider-dynamic/internal/config"
	"github.com/ubiquex/ubx-provider-dynamic/internal/discoverydoc"
	"github.com/ubiquex/ubx-provider-dynamic/internal/dynserver"
	"github.com/ubiquex/ubx-provider-dynamic/internal/openapi"
	"github.com/ubiquex/ubx-provider-dynamic/internal/resourcemap"
	"github.com/ubiquex/ubx-provider-dynamic/internal/smithy"
)

// AssembleGroup combines already-generated member snapshots into one
// real, versioned group container. The group's own Version is derived
// mechanically: the MAX real ChangeLevel found across every member
// (memberLevels, one entry per member in members -- ModeResource members
// diff against their own prior translated schema, ModeDataSource members
// the same, both using the identical real DiffLevel one level down), plus
// Major, unconditionally, for any member prev.Members had that members
// does not -- a whole member disappearing is always a real, breaking
// change to the group's own published surface, regardless of what that
// member's own content used to look like. prev == nil means the group's
// own first-ever snapshot (every member is new content, Minor at most,
// matching NextVersion("", ...)'s own real, existing single-member
// discipline one level up -- never hand-picked here either).
//
// exclude is UBI-182's own real precedence record -- which real member's
// own copy of a colliding wire type name loses, keyed by member name.
// Recorded on the returned Snapshot verbatim (never inferred, never
// computed here): this is the SAME real judgment ubiquex's own
// [dynamic_provider_groups.<x>.exclude] table already made for codegen
// (e.g. Datadog's own real v1/v2 collisions -- v1's richer version
// wins), passed through by whatever real caller already has it (the
// group's own hash-watch.yml, via --group-exclude), not invented fresh
// here. May be nil for a group with no known real collisions.
func AssembleGroup(repoName string, prev *Snapshot, members map[string]*MemberSnapshot, memberLevels map[string]ChangeLevel, exclude map[string][]string) (*Snapshot, error) {
	var version string
	var err error
	if prev == nil {
		version, err = NextVersion("", Minor)
		if err != nil {
			return nil, err
		}
	} else {
		level := NoChange
		for _, l := range memberLevels {
			if l > level {
				level = l
			}
		}
		for name := range prev.Members {
			if _, stillPresent := members[name]; !stillPresent {
				level = Major
			}
		}
		// UBI-194: a real MinBinaryVersion transition (the committed
		// snapshot's own prior value differs from this build's real
		// BinaryVersion -- most commonly absent -> present, for the six
		// snapshots published before this field existed) must force at
		// least a Patch-level bump even when no member's own translated
		// content changed at all. Without this, NextVersion(prev.Version,
		// NoChange) returns prev.Version unmodified, the caller's own
		// "is this newer than what's committed" gate (every real
		// hash-watch.yml in this org) sees no change, and the real,
		// freshly-stamped MinBinaryVersion is silently discarded instead
		// of committed -- the exact real, live failure this comment exists
		// to prevent (confirmed against a genuine run: Kubernetes'
		// swagger.json is unchanged, memberLevels reported "none" for
		// both real members, and the assembled group was thrown away
		// with no PR opened, even though it now carried a real
		// MinBinaryVersion the committed manifest.json still lacks).
		if level == NoChange && prev.MinBinaryVersion != BinaryVersion {
			level = Patch
		}
		version, err = NextVersion(prev.Version, level)
		if err != nil {
			return nil, fmt.Errorf("derive next group version for %q: %w", repoName, err)
		}
	}

	return &Snapshot{
		SchemaFormat:     CurrentSchemaFormat,
		Provider:         repoName,
		Version:          version,
		Members:          members,
		Exclude:          exclude,
		MinBinaryVersion: BinaryVersion,
	}, nil
}

// memberChangeLevel is every Generate<Source>Member's own shared "how
// much did THIS member change against its own prior real content"
// helper -- prevMember == nil (a genuinely new member, whether this is
// the group's first-ever snapshot or a member added to an existing
// group) always contributes Minor at most, the identical real reasoning
// DiffLevel's own doc comment already gives for a nil old.
func memberChangeLevel(prevMember *MemberSnapshot, oldSchemas, newSchemas map[string]*tfprotov6.Schema) ChangeLevel {
	if prevMember == nil {
		return Minor
	}
	return DiffLevel(oldSchemas, newSchemas)
}

// ---------------------------------------------------------------------
// openapi
// ---------------------------------------------------------------------

// GenerateOpenAPIMember fetches name's real, live schema_url ONE real
// time (the one real network call this function makes), verifies it
// needs no further network access to re-parse, builds it in the real
// pipeline mode selects (ModeResource: dynserver.Build; ModeDataSource:
// resourcemap.BuildDataSources -- UBI-186's own real data-source
// discovery, unchanged, the identical pipeline run()'s own live-fetch
// branch already uses when cfg.DataSources is true), and returns a real
// MemberSnapshot plus its own translated schema (for AssembleGroup's own
// cross-member diff) and real ChangeLevel against prevMember.
//
// The real, live verification step matters, not a formality: kin-openapi
// resolves a real spec's own $refs into its own in-memory Value fields as
// it parses (openapi.Load, called here with real network access to
// resolve them), but SchemaRef's own MarshalJSON always re-emits a bare
// {"$ref": "..."} string whenever Ref is set, never the already-resolved
// Value alongside it. Confirmed live, not assumed, for every real
// schema_source = "openapi" provider onboarded so far: Datadog (v1 and
// v2) and Kubernetes have zero external refs. GitHub has exactly 3, all
// three under x-ms-examples (see below), resolved correctly and
// completely by kin-openapi's own real Loader with zero network needed
// on reparse -- GitHub's own already-published snapshot is genuinely
// complete, not silently missing anything (an earlier version of this
// comment claimed GitHub was "entirely internal," which was wrong; left
// here corrected rather than silently fixed, since the wrong claim once
// shipped). Azure is the one real, live-confirmed exception: its own
// real Swagger 2.0 specs split themselves across real, shared files by
// real relative path (sampled live across 16 diverse Azure specs, 100%
// relative, 0% absolute URLs) -- openapi.Bundle (called below, before
// marshaling) rewrites every one of those into a real, local, network-
// free component before this function's own real reparse-verification
// step ever runs, so this function proves the real, marshaled RawSpec
// re-parses with zero network access before ever returning, and returns
// ErrExternalRefsUnsupported, unmistakably, if it still doesn't (a real
// external ref Bundle's own named scope boundary doesn't cover -- see
// its own doc comment -- rather than a silently incomplete snapshot).
//
// x-ms-examples (Azure's own real Swagger vendor extension for
// per-operation example payloads) is real and sizeable -- 23.4% of
// every external ref sampled live across this package's own 16-spec
// Azure sample -- but is deliberately never bundled: it lives entirely
// inside an Operation's own Extensions map, never read by
// internal/schema.Translator's own BuildTopLevel (schema content only),
// so bundling it would only grow the snapshot for zero real benefit.
// GitHub's own 3 external refs happen to be this exact same shape
// (x-ms-examples, confirmed live) -- which is why they resolve cleanly
// on reparse despite Bundle never touching them: kin-openapi's own Load
// already resolves an Operation-level x-ms-examples $ref against real
// network access the identical way it resolves a real schema ref, and
// (unlike SchemaRef) ExampleRef's own MarshalJSON does not re-emit a
// dangling pointer once resolved -- confirmed live before writing this,
// not assumed from kin-openapi's own source.
// wireName, not name, is what gets baked into every generated resource
// or data-source type name (config.Provider.WireName's own doc comment
// has the full real reason these can differ) -- matches run()'s own
// real live-fetch call exactly (dynserver.Build(doc, wireName, cfg) for
// BOTH the resource and data-source branches; UBI-182's own real,
// live-found bug, caught before this session's own merge-group work
// could even validate correctly against it: this function originally
// used name for translation, silently ignoring wire_name entirely,
// producing "kubernetes_ds_"-prefixed data source type names instead of
// the intended, shared "kubernetes_" prefix -- confirmed live against
// the already-generated kubernetes_ds member before landing this fix,
// not assumed).
func GenerateOpenAPIMember(name, wireName, schemaURL string, mode Mode, execCfg config.Provider, prevMember *MemberSnapshot) (*MemberSnapshot, map[string]*tfprotov6.Schema, ChangeLevel, error) {
	var doc *openapi3.T
	var err error
	if execCfg.RedoclyBundle {
		doc, err = openapi.LoadWithRedoclyBundle(schemaURL, execCfg.Name)
	} else {
		doc, err = openapi.Load(schemaURL)
	}
	if err != nil {
		return nil, nil, NoChange, fmt.Errorf("fetch %s: %w", schemaURL, err)
	}
	openapi.Bundle(doc)

	rawSpec, err := json.Marshal(doc)
	if err != nil {
		return nil, nil, NoChange, fmt.Errorf("marshal fetched spec: %w", err)
	}

	if _, err := openapi.Parse(rawSpec, nil); err != nil {
		return nil, nil, NoChange, fmt.Errorf("%w: %s's own real spec has at least one $ref openapi.Bundle's own named scope boundary doesn't cover (its own doc comment has the real list): %v",
			ErrExternalRefsUnsupported, schemaURL, err)
	}

	newSchemas, err := buildOpenAPISchemasForMode(doc, wireName, mode, execCfg)
	if err != nil {
		return nil, nil, NoChange, err
	}

	member := &MemberSnapshot{
		SchemaSource:      SchemaSourceOpenAPI,
		Mode:              mode,
		Auth:              execCfg.Auth,
		BaseURL:           execCfg.BaseURL,
		Retry:             execCfg.Retry,
		Timeouts:          execCfg.Timeouts,
		Resources:         execCfg.Resources,
		WireName:          wireName,
		NamespaceFromTags: execCfg.NamespaceFromTags,
		RawSpec:           rawSpec,
	}

	var oldSchemas map[string]*tfprotov6.Schema
	if prevMember != nil {
		oldSchemas, err = LoadOpenAPIMemberSchemas(name, prevMember)
		if err != nil {
			return nil, nil, NoChange, fmt.Errorf("reconstruct prior member %q's own real schema for diffing: %w", name, err)
		}
	}
	level := memberChangeLevel(prevMember, oldSchemas, newSchemas)

	return member, newSchemas, level, nil
}

func buildOpenAPISchemasForMode(doc *openapi3.T, wireName string, mode Mode, execCfg config.Provider) (map[string]*tfprotov6.Schema, error) {
	switch mode {
	case ModeResource:
		built, _, err := dynserver.Build(doc, wireName, execCfg)
		if err != nil {
			return nil, fmt.Errorf("build resource schemas: %w", err)
		}
		schemas := make(map[string]*tfprotov6.Schema, len(built))
		for typeName, rt := range built {
			schemas[typeName] = rt.Schema
		}
		return schemas, nil
	case ModeDataSource:
		built, _, err := resourcemap.BuildDataSources(doc, wireName)
		if err != nil {
			return nil, fmt.Errorf("build data source schemas: %w", err)
		}
		schemas := make(map[string]*tfprotov6.Schema, len(built))
		for typeName, ds := range built {
			schemas[typeName] = ds.Schema
		}
		return schemas, nil
	default:
		return nil, fmt.Errorf("%w: openapi member (wire_name %q) requested mode %q", ErrUnsupportedMode, wireName, mode)
	}
}

// LoadOpenAPIMember is GenerateOpenAPIMember's own real, network-free
// counterpart: re-derives member's own complete, real resource-or-data-
// source map (mode-aware) purely from RawSpec -- zero network calls, the
// SAME real translation the binary already runs at live-fetch time, fed
// frozen bytes instead of a fresh HTTP GET. Deliberately re-translates on
// every call rather than caching the result inside MemberSnapshot itself
// -- see the package doc comment's own account of why (SchemaFormat's
// real promise that a newer binary's own improved translation applies to
// an old, frozen spec unchanged). Reads WireName from member itself
// (falls back to name when empty, matching config.Provider.WireName's
// own "defaults to name" convention and LoadSmithyMember's own identical
// real fallback).
func LoadOpenAPIMember(name string, member *MemberSnapshot) (resources map[string]*dynserver.ResourceType, dataSources map[string]*resourcemap.BuiltDataSource, err error) {
	if member.SchemaSource != SchemaSourceOpenAPI {
		return nil, nil, fmt.Errorf("member %q's own schema_source %q is not openapi", name, member.SchemaSource)
	}
	wireName := member.WireName
	if wireName == "" {
		wireName = name
	}
	doc, err := openapi.Parse(member.RawSpec, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("parse member %q's own raw_spec: %w", name, err)
	}
	execCfg := config.Provider{
		BaseURL:  member.BaseURL,
		Auth:     member.Auth,
		Retry:    member.Retry,
		Timeouts: member.Timeouts,
	}
	switch member.Mode {
	case ModeResource:
		resources, _, err = dynserver.Build(doc, wireName, execCfg)
		if err != nil {
			return nil, nil, fmt.Errorf("rebuild member %q's own resource schemas: %w", name, err)
		}
	case ModeDataSource:
		dataSources, _, err = resourcemap.BuildDataSources(doc, wireName)
		if err != nil {
			return nil, nil, fmt.Errorf("rebuild member %q's own data source schemas: %w", name, err)
		}
	default:
		return nil, nil, fmt.Errorf("%w: openapi member %q has mode %q", ErrUnsupportedMode, name, member.Mode)
	}
	return resources, dataSources, nil
}

// LoadOpenAPIMemberSchemas is LoadOpenAPIMember's own thin adapter for
// AssembleGroup's own diffing needs, which only ever wants the converged
// map[string]*tfprotov6.Schema regardless of mode -- avoids every real
// caller of GenerateOpenAPIMember (the diff path specifically) needing to
// know which of resources/dataSources LoadOpenAPIMember actually
// populated.
func LoadOpenAPIMemberSchemas(name string, member *MemberSnapshot) (map[string]*tfprotov6.Schema, error) {
	resources, dataSources, err := LoadOpenAPIMember(name, member)
	if err != nil {
		return nil, err
	}
	if member.Mode == ModeDataSource {
		schemas := make(map[string]*tfprotov6.Schema, len(dataSources))
		for typeName, ds := range dataSources {
			schemas[typeName] = ds.Schema
		}
		return schemas, nil
	}
	schemas := make(map[string]*tfprotov6.Schema, len(resources))
	for typeName, rt := range resources {
		schemas[typeName] = rt.Schema
	}
	return schemas, nil
}

// ErrExternalRefsUnsupported is GenerateOpenAPIMember's own real, named
// sentinel for the one real, explicit scope gap this function refuses to
// paper over -- see its own doc comment.
var ErrExternalRefsUnsupported = fmt.Errorf("spec has external $refs this snapshot format can't yet make network-free")

// ---------------------------------------------------------------------
// cloudformation
// ---------------------------------------------------------------------

// GenerateCloudFormationMember mirrors GenerateOpenAPIMember for AWS's
// real CloudFormation resource-provider schema registry. RawSpec here is
// the whole real map[string]*cloudformation.ResourceSchema the registry
// zip fetch produces, not a single document -- confirmed safe to
// round-trip through plain encoding/json (no kin-openapi-style
// marshal-loses-the-resolved-Value quirk), so no
// ErrExternalRefsUnsupported-style reparse gate is needed here.
//
// mode must be ModeResource: CloudFormation has no real data-source
// concept at all (confirmed directly: zero BuildDataSources/DataSource
// references anywhere in internal/cloudformation) -- ModeDataSource
// fails loud, immediately, before any network access.
func GenerateCloudFormationMember(name, schemaURL string, mode Mode, execCfg config.Provider, prevMember *MemberSnapshot) (*MemberSnapshot, map[string]*tfprotov6.Schema, ChangeLevel, error) {
	if mode != ModeResource {
		return nil, nil, NoChange, fmt.Errorf("%w: cloudformation member %q requested mode %q -- cloudformation has no data-source concept", ErrUnsupportedMode, name, mode)
	}

	files, err := cloudformation.Fetch(schemaURL)
	if err != nil {
		return nil, nil, NoChange, fmt.Errorf("fetch %s: %w", schemaURL, err)
	}

	rawSpec, err := json.Marshal(files)
	if err != nil {
		return nil, nil, NoChange, fmt.Errorf("marshal fetched spec: %w", err)
	}

	newBuilt, _, err := cloudformation.Build(files, smithy.DefaultKnownNames())
	if err != nil {
		return nil, nil, NoChange, fmt.Errorf("build resource schemas: %w", err)
	}
	newSchemas := make(map[string]*tfprotov6.Schema, len(newBuilt))
	for typeName, rt := range newBuilt {
		newSchemas[typeName] = rt.Schema
	}

	member := &MemberSnapshot{
		SchemaSource: SchemaSourceCloudFormation,
		Mode:         ModeResource,
		Auth:         execCfg.Auth,
		BaseURL:      execCfg.BaseURL,
		Retry:        execCfg.Retry,
		Timeouts:     execCfg.Timeouts,
		Resources:    execCfg.Resources,
		RawSpec:      rawSpec,
	}

	var oldSchemas map[string]*tfprotov6.Schema
	if prevMember != nil {
		oldBuilt, err := LoadCloudFormationMember(name, prevMember)
		if err != nil {
			return nil, nil, NoChange, fmt.Errorf("reconstruct prior member %q's own real schema for diffing: %w", name, err)
		}
		oldSchemas = make(map[string]*tfprotov6.Schema, len(oldBuilt))
		for typeName, rt := range oldBuilt {
			oldSchemas[typeName] = rt.Schema
		}
	}
	level := memberChangeLevel(prevMember, oldSchemas, newSchemas)

	return member, newSchemas, level, nil
}

// LoadCloudFormationMember is GenerateCloudFormationMember's own real,
// network-free counterpart. name is deliberately unused for the real
// build itself (cloudformation.Build takes no providerName -- each real
// CFN resource's own type name is already fully qualified), kept as a
// parameter only so this function's own signature matches every other
// Load<Source>Member's, and so its own error messages can name which
// member failed.
func LoadCloudFormationMember(name string, member *MemberSnapshot) (map[string]*cloudformation.BuiltResource, error) {
	if member.SchemaSource != SchemaSourceCloudFormation {
		return nil, fmt.Errorf("member %q's own schema_source %q is not cloudformation", name, member.SchemaSource)
	}
	if member.Mode != ModeResource {
		return nil, fmt.Errorf("%w: cloudformation member %q has mode %q -- cloudformation has no data-source concept", ErrUnsupportedMode, name, member.Mode)
	}
	var files map[string]*cloudformation.ResourceSchema
	if err := json.Unmarshal(member.RawSpec, &files); err != nil {
		return nil, fmt.Errorf("parse member %q's own raw_spec: %w", name, err)
	}
	resources, _, err := cloudformation.Build(files, smithy.DefaultKnownNames())
	if err != nil {
		return nil, fmt.Errorf("rebuild member %q's own resource schemas: %w", name, err)
	}
	return resources, nil
}

// ---------------------------------------------------------------------
// smithy
// ---------------------------------------------------------------------

// GenerateSmithyMember mirrors GenerateOpenAPIMember for AWS's real
// Smithy service models. wireName, not name, is what gets baked into
// every generated resource type name (config.Provider.WireName's own doc
// comment). ModeDataSource uses smithy.BuildDataSources, the identical
// pipeline run()'s own live-fetch branch already uses -- namespaceOverride
// is config.Provider.DataSourceNamespace, plumbed through unchanged, and
// (like the live path) requires re-deriving the model's own *Service via
// smithy.FindService before BuildDataSources can run.
func GenerateSmithyMember(name, wireName, schemaURL, targetPrefix string, mode Mode, namespaceOverride string, execCfg config.Provider, prevMember *MemberSnapshot) (*MemberSnapshot, map[string]*tfprotov6.Schema, ChangeLevel, error) {
	doc, err := smithy.Load(schemaURL)
	if err != nil {
		return nil, nil, NoChange, fmt.Errorf("fetch %s: %w", schemaURL, err)
	}

	rawSpec, err := json.Marshal(doc)
	if err != nil {
		return nil, nil, NoChange, fmt.Errorf("marshal fetched spec: %w", err)
	}

	newSchemas, err := buildSmithySchemasForMode(doc, wireName, mode, namespaceOverride)
	if err != nil {
		return nil, nil, NoChange, err
	}

	member := &MemberSnapshot{
		SchemaSource:        SchemaSourceSmithy,
		Mode:                mode,
		Auth:                execCfg.Auth,
		BaseURL:             execCfg.BaseURL,
		Retry:               execCfg.Retry,
		Timeouts:            execCfg.Timeouts,
		Resources:           execCfg.Resources,
		WireName:            wireName,
		TargetPrefix:        targetPrefix,
		DataSourceNamespace: namespaceOverride,
		RawSpec:             rawSpec,
	}

	var oldSchemas map[string]*tfprotov6.Schema
	if prevMember != nil {
		oldSchemas, err = LoadSmithyMemberSchemas(name, prevMember)
		if err != nil {
			return nil, nil, NoChange, fmt.Errorf("reconstruct prior member %q's own real schema for diffing: %w", name, err)
		}
	}
	level := memberChangeLevel(prevMember, oldSchemas, newSchemas)

	return member, newSchemas, level, nil
}

func buildSmithySchemasForMode(doc *smithy.Model, wireName string, mode Mode, namespaceOverride string) (map[string]*tfprotov6.Schema, error) {
	switch mode {
	case ModeResource:
		built, _, err := smithy.Build(doc, wireName, smithy.DefaultKnownNames())
		if err != nil {
			return nil, fmt.Errorf("build resource schemas: %w", err)
		}
		schemas := make(map[string]*tfprotov6.Schema, len(built))
		for typeName, rt := range built {
			schemas[typeName] = rt.Schema
		}
		return schemas, nil
	case ModeDataSource:
		svc, err := smithy.FindService(doc)
		if err != nil {
			return nil, fmt.Errorf("find smithy service for data-source discovery: %w", err)
		}
		built, _, err := smithy.BuildDataSources(doc, wireName, svc, namespaceOverride)
		if err != nil {
			return nil, fmt.Errorf("build data source schemas: %w", err)
		}
		schemas := make(map[string]*tfprotov6.Schema, len(built))
		for typeName, ds := range built {
			schemas[typeName] = ds.Schema
		}
		return schemas, nil
	default:
		return nil, fmt.Errorf("%w: smithy member requested mode %q", ErrUnsupportedMode, mode)
	}
}

// LoadSmithyMember is GenerateSmithyMember's own real, network-free
// counterpart. Reads WireName/DataSourceNamespace from member itself
// (falls back to name when WireName is empty, matching
// config.Provider.WireName's own "defaults to name" convention).
func LoadSmithyMember(name string, member *MemberSnapshot) (resources map[string]*smithy.BuiltResource, dataSources map[string]*smithy.BuiltDataSource, err error) {
	if member.SchemaSource != SchemaSourceSmithy {
		return nil, nil, fmt.Errorf("member %q's own schema_source %q is not smithy", name, member.SchemaSource)
	}
	wireName := member.WireName
	if wireName == "" {
		wireName = name
	}
	var doc smithy.Model
	if err := json.Unmarshal(member.RawSpec, &doc); err != nil {
		return nil, nil, fmt.Errorf("parse member %q's own raw_spec: %w", name, err)
	}
	switch member.Mode {
	case ModeResource:
		resources, _, err = smithy.Build(&doc, wireName, smithy.DefaultKnownNames())
		if err != nil {
			return nil, nil, fmt.Errorf("rebuild member %q's own resource schemas: %w", name, err)
		}
	case ModeDataSource:
		var svc *smithy.Service
		svc, err = smithy.FindService(&doc)
		if err != nil {
			return nil, nil, fmt.Errorf("find smithy service for member %q's own data-source discovery: %w", name, err)
		}
		dataSources, _, err = smithy.BuildDataSources(&doc, wireName, svc, member.DataSourceNamespace)
		if err != nil {
			return nil, nil, fmt.Errorf("rebuild member %q's own data source schemas: %w", name, err)
		}
	default:
		return nil, nil, fmt.Errorf("%w: smithy member %q has mode %q", ErrUnsupportedMode, name, member.Mode)
	}
	return resources, dataSources, nil
}

// LoadSmithyMemberSchemas mirrors LoadOpenAPIMemberSchemas -- the
// converged map[string]*tfprotov6.Schema AssembleGroup's own diffing
// needs, regardless of mode.
func LoadSmithyMemberSchemas(name string, member *MemberSnapshot) (map[string]*tfprotov6.Schema, error) {
	resources, dataSources, err := LoadSmithyMember(name, member)
	if err != nil {
		return nil, err
	}
	if member.Mode == ModeDataSource {
		schemas := make(map[string]*tfprotov6.Schema, len(dataSources))
		for typeName, ds := range dataSources {
			schemas[typeName] = ds.Schema
		}
		return schemas, nil
	}
	schemas := make(map[string]*tfprotov6.Schema, len(resources))
	for typeName, rt := range resources {
		schemas[typeName] = rt.Schema
	}
	return schemas, nil
}

// ---------------------------------------------------------------------
// discovery_docs
// ---------------------------------------------------------------------

// GenerateDiscoveryDocMember mirrors GenerateOpenAPIMember for GCP's real
// Discovery Documents. versionQualifier is stored onto the returned
// MemberSnapshot -- necessary once a pinned entry is the only config a
// caller has. ModeDataSource uses discoverydoc.BuildDataSources, the
// identical pipeline run()'s own live-fetch branch already uses.
//
// wireName is deliberately used ONLY for ModeDataSource, not
// ModeResource -- mirrors run()'s own real, documented asymmetry
// exactly: every existing real [dynamic_providers.google_<api>]
// RESOURCE entry's own table key already IS its real, correct provider
// identity (no separate data-source-mode sibling existed historically to
// need a distinct key from), but a DATA-SOURCE-mode entry needs a TOML
// key distinct from its own resource-mode sibling (TOML tables require
// unique keys), so wireName is what recovers the real, shared identity
// for that case specifically -- using bare name for data sources too
// would reproduce the exact real bug this session's own
// dataSourceWireType doc comment already found and fixed on the
// live-fetch path (a distinct table key leaking into the wire type's
// own second token, corrupting the derived service/local split).
func GenerateDiscoveryDocMember(name, wireName, schemaURL, versionQualifier string, mode Mode, execCfg config.Provider, prevMember *MemberSnapshot) (*MemberSnapshot, map[string]*tfprotov6.Schema, ChangeLevel, error) {
	doc, err := discoverydoc.Load(schemaURL)
	if err != nil {
		return nil, nil, NoChange, fmt.Errorf("fetch %s: %w", schemaURL, err)
	}

	rawSpec, err := json.Marshal(doc)
	if err != nil {
		return nil, nil, NoChange, fmt.Errorf("marshal fetched spec: %w", err)
	}

	newSchemas, err := buildDiscoveryDocSchemasForMode(doc, name, wireName, mode, versionQualifier)
	if err != nil {
		return nil, nil, NoChange, err
	}

	member := &MemberSnapshot{
		SchemaSource:     SchemaSourceDiscoveryDoc,
		Mode:             mode,
		Auth:             execCfg.Auth,
		BaseURL:          execCfg.BaseURL,
		Retry:            execCfg.Retry,
		Timeouts:         execCfg.Timeouts,
		Resources:        execCfg.Resources,
		WireName:         wireName,
		VersionQualifier: versionQualifier,
		RawSpec:          rawSpec,
	}

	var oldSchemas map[string]*tfprotov6.Schema
	if prevMember != nil {
		oldSchemas, err = LoadDiscoveryDocMemberSchemas(name, prevMember)
		if err != nil {
			return nil, nil, NoChange, fmt.Errorf("reconstruct prior member %q's own real schema for diffing: %w", name, err)
		}
	}
	level := memberChangeLevel(prevMember, oldSchemas, newSchemas)

	return member, newSchemas, level, nil
}

func buildDiscoveryDocSchemasForMode(doc *discoverydoc.Document, name, wireName string, mode Mode, versionQualifier string) (map[string]*tfprotov6.Schema, error) {
	switch mode {
	case ModeResource:
		built, _, err := discoverydoc.Build(doc, name, versionQualifier)
		if err != nil {
			return nil, fmt.Errorf("build resource schemas: %w", err)
		}
		schemas := make(map[string]*tfprotov6.Schema, len(built))
		for typeName, rt := range built {
			schemas[typeName] = rt.Schema
		}
		return schemas, nil
	case ModeDataSource:
		built, _, err := discoverydoc.BuildDataSources(doc, wireName, versionQualifier)
		if err != nil {
			return nil, fmt.Errorf("build data source schemas: %w", err)
		}
		schemas := make(map[string]*tfprotov6.Schema, len(built))
		for typeName, ds := range built {
			schemas[typeName] = ds.Schema
		}
		return schemas, nil
	default:
		return nil, fmt.Errorf("%w: discovery_docs member %q requested mode %q", ErrUnsupportedMode, name, mode)
	}
}

// LoadDiscoveryDocMember is GenerateDiscoveryDocMember's own real,
// network-free counterpart. Reads VersionQualifier/WireName from member
// itself -- WireName falls back to name when empty (a real, pre-fix
// member, or a resource-mode member, which never sets it), matching
// GenerateDiscoveryDocMember's own identical resource/data-source
// asymmetry.
func LoadDiscoveryDocMember(name string, member *MemberSnapshot) (resources map[string]*discoverydoc.BuiltResource, dataSources map[string]*discoverydoc.BuiltDataSource, err error) {
	if member.SchemaSource != SchemaSourceDiscoveryDoc {
		return nil, nil, fmt.Errorf("member %q's own schema_source %q is not discovery_docs", name, member.SchemaSource)
	}
	wireName := member.WireName
	if wireName == "" {
		wireName = name
	}
	var doc discoverydoc.Document
	if err := json.Unmarshal(member.RawSpec, &doc); err != nil {
		return nil, nil, fmt.Errorf("parse member %q's own raw_spec: %w", name, err)
	}
	switch member.Mode {
	case ModeResource:
		resources, _, err = discoverydoc.Build(&doc, name, member.VersionQualifier)
		if err != nil {
			return nil, nil, fmt.Errorf("rebuild member %q's own resource schemas: %w", name, err)
		}
	case ModeDataSource:
		dataSources, _, err = discoverydoc.BuildDataSources(&doc, wireName, member.VersionQualifier)
		if err != nil {
			return nil, nil, fmt.Errorf("rebuild member %q's own data source schemas: %w", name, err)
		}
	default:
		return nil, nil, fmt.Errorf("%w: discovery_docs member %q has mode %q", ErrUnsupportedMode, name, member.Mode)
	}
	return resources, dataSources, nil
}

// LoadDiscoveryDocMemberSchemas mirrors LoadOpenAPIMemberSchemas.
func LoadDiscoveryDocMemberSchemas(name string, member *MemberSnapshot) (map[string]*tfprotov6.Schema, error) {
	resources, dataSources, err := LoadDiscoveryDocMember(name, member)
	if err != nil {
		return nil, err
	}
	if member.Mode == ModeDataSource {
		schemas := make(map[string]*tfprotov6.Schema, len(dataSources))
		for typeName, ds := range dataSources {
			schemas[typeName] = ds.Schema
		}
		return schemas, nil
	}
	schemas := make(map[string]*tfprotov6.Schema, len(resources))
	for typeName, rt := range resources {
		schemas[typeName] = rt.Schema
	}
	return schemas, nil
}
