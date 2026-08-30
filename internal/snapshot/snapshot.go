// Package snapshot is the real fix for a real, named problem: Dynamic
// Provider fetches a schema from a live URL at every launch today
// (schema_url in [dynamic_providers.<name>]), which makes it a rolling
// release with no pinning, no reproducibility, and no way to reconstruct
// the schema a past build used -- and directly contradicts the
// offline-after-generation property the SDK docs already claim for every
// OTHER real provider path (docs/sdk.md, ubiquex's own repo).
//
// A Snapshot is a real, frozen file: fetch a provider's real spec once,
// verify it needs no further network access to re-parse (real, checked,
// not assumed -- see GenerateOpenAPIMember's own doc comment), and write
// everything the binary needs to serve it again with zero network calls.
//
// UBI-182, group container (SchemaFormat 3): a Snapshot is no longer one
// flat spec. A provider's real published identity (repo_name in
// ubiquex's own [dynamic_provider_groups.<x>], e.g. "kubernetes",
// "datadog", "aws") is almost never one [dynamic_providers.<name>] table
// -- it's a GROUP of them (Kubernetes alone is two: the resource-mode
// "kubernetes" table and the data-source-mode "kubernetes_ds" table,
// both fetched from the identical schema_url but built through genuinely
// different pipelines). AWS's own real group mixes TWO schema sources
// entirely (one CloudFormation resource entry, 429 Smithy data-source
// entries) -- a single flat RawSpec/SchemaSource pair, this package's
// entire shape through SchemaFormat 2, cannot represent that at all,
// confirmed directly against the struct before this change, not assumed.
//
// Snapshot is now the real, whole-group container: one Provider (the
// group's own repo_name), one mechanically-derived Version for the whole
// group, and a Members map -- one MemberSnapshot per real
// [dynamic_providers.<name>] table the group actually bundles, each
// keeping its own SchemaSource/RawSpec/Mode, since members genuinely
// differ in source and mode even within one group. A pinned
// [providers.<name>] entry for ANY one member resolves the SAME real
// group release (provider.AcquireSchema's own cache-by-source+version
// already collapses redundant downloads for free); the launched process
// picks its own member back out of Members by the same
// UBX_DYNAMIC_PROVIDER_NAME it already receives.
//
// Real, deliberate, accepted break: SchemaFormat 2 (a flat, single-member
// snapshot) is not readable by this build at all -- CheckFormat refuses
// it the same way it refuses anything outside [MinSupportedSchemaFormat,
// MaxSupportedSchemaFormat], both now 3. Only one real snapshot was ever
// published under format 2 (ubx-schema-kubernetes v1.0.0, this same
// session), and nothing but this session's own verification tests
// depended on it -- carrying a compatibility shim for one immediately-
// superseded artifact would be real, permanent cost with no real
// beneficiary. v1.0.0 is superseded by a real v2.0.0 (a genuinely
// breaking format change, not a routine content bump) once this ships.
//
// Two real, separately-versioned numbers, deliberately not one:
//
//   - SchemaFormat is THIS PACKAGE's own capability version -- which
//     shape of snapshot file a given binary build knows how to read.
//     Changes only when the binary itself gains a real new capability. A
//     schema_format outside a binary's own declared range is refused
//     loudly (CheckFormat) -- never silently misinterpreted.
//   - Version is the real GROUP's own real API-surface version -- a real
//     semver string, mechanically derived (internal/snapshot/diff.go,
//     AssembleGroup) from the highest real change level found across
//     every member's own translated schema between one snapshot and the
//     next (a whole member disappearing counts as Major, unconditionally
//     -- see AssembleGroup's own doc comment), never hand-picked.
//
// Mode (ModeResource / ModeDataSource) is UBI-182's other real addition:
// resource-mode and data-source-mode entries go through genuinely
// different discovery/translation pipelines (dynserver.Build vs
// resourcemap.BuildDataSources, and the smithy/discoverydoc equivalents)
// -- a snapshot with no way to record which one a member needs used to
// mean a pinned data-source entry would silently be served as
// resource-shaped output instead (or, for the two sources with no
// generation support at all before this, simply fail to generate). Every
// Generate<Source>Member/Load<Source>Member pair now checks Mode against
// what that specific source actually supports and fails loud
// (ErrUnsupportedMode) on any mismatch, immediately -- never falls
// through to the wrong shape. CloudFormation has no real data-source
// concept at all (confirmed directly: zero BuildDataSources/DataSource
// references anywhere in internal/cloudformation) -- ModeDataSource is
// simply not a supported Mode for that source, ever.
package snapshot

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/ubiquex/ubx-provider-dynamic/internal/config"
)

// CurrentSchemaFormat is what THIS BUILD of the binary writes when it
// generates a new snapshot -- always the max of its own declared
// supported range.
const CurrentSchemaFormat = 3

// MinSupportedSchemaFormat/MaxSupportedSchemaFormat is THIS BUILD's own
// real, declared compatibility range. Both moved to 3 together for
// UBI-182's group-container change -- a real, deliberate break, not a
// backward-compatible extension the way 1->2 was (see the package doc
// comment for why format 2 is not worth carrying forward).
const (
	MinSupportedSchemaFormat = 3
	MaxSupportedSchemaFormat = 3
)

// SchemaSource names which real Build/BuildDataSources pipeline a
// MemberSnapshot's own RawSpec needs.
type SchemaSource string

const (
	SchemaSourceOpenAPI        SchemaSource = "openapi"
	SchemaSourceCloudFormation SchemaSource = "cloudformation"
	SchemaSourceSmithy         SchemaSource = "smithy"
	SchemaSourceDiscoveryDoc   SchemaSource = "discovery_docs"
)

// Mode names which real discovery/translation pipeline a member's own
// RawSpec goes through -- the same real distinction
// config.Provider.DataSources (a plain bool) already drives on the live-
// fetch path, made an explicit, checked, first-class part of the frozen
// artifact instead of an implicit runtime flag a snapshot had no way to
// carry at all.
type Mode string

const (
	// ModeResource is the default, CRUD-shaped discovery every source
	// supports: dynserver.Build (openapi), cloudformation.Build,
	// smithy.Build, discoverydoc.Build.
	ModeResource Mode = "resource"
	// ModeDataSource is UBI-186's own schema-only discovery:
	// resourcemap.BuildDataSources (openapi), smithy.BuildDataSources,
	// discoverydoc.BuildDataSources. Not supported by
	// SchemaSourceCloudFormation at all -- see the package doc comment.
	ModeDataSource Mode = "data_source"
)

// ExpandMemberModes decides, for ONE real [dynamic_providers.<name>]
// config entry, which real Mode(s) and member name(s) generation should
// produce from it -- UBI-182's own real collapse of the resource/
// data-source split (see config.Provider.DataSources' own doc comment
// for the full real reasoning this rests on). Kept as a small, pure,
// independently-tested function rather than inline in main.go's own
// generation loop, since this decision is the crux of the whole
// collapse -- get it wrong and either a real data-source-only entry
// (AWS's own 429 real Smithy services) starts attempting resource
// discovery it was never meant to, or CloudFormation attempts a
// data-source build it structurally cannot do.
//
// cfg.DataSources = true keeps its exact prior meaning: this entry is
// restricted to data sources only, one member, keyed by its own name --
// unchanged, so AWS's real data-source-only entries need no migration.
// schema_source = cloudformation always produces resources only, one
// member -- validate() already refuses data_sources = true on it, so
// this case is unconditional, not a fallback for a config mistake.
// Every other case (the new default) produces TWO real members from
// this one entry: resource-mode under the entry's own name, unchanged,
// plus a data-source-mode member synthesized under "<name>_ds" --
// deliberately reusing the exact naming convention the old, separate
// sibling TOML table used to carry, so an already-published manifest's
// own member list needs no format change, only its driving config
// collapsing from two tables to one.
func ExpandMemberModes(memberName string, cfg config.Provider) (modes []Mode, memberNames []string) {
	switch {
	case cfg.DataSources:
		return []Mode{ModeDataSource}, []string{memberName}
	case cfg.SchemaSource == config.SchemaSourceCloudFormation:
		return []Mode{ModeResource}, []string{memberName}
	default:
		return []Mode{ModeResource, ModeDataSource}, []string{memberName, memberName + "_ds"}
	}
}

// ErrUnsupportedMode is every Generate<Source>Member/Load<Source>Member
// pair's own real, fail-loud sentinel for a Mode that source doesn't (or
// doesn't yet) support -- wrapped, not returned bare, so a caller can
// errors.Is against it regardless of which source/direction produced it.
// The whole real point of adding Mode: a mismatch here must be a real,
// immediate, unmistakable error, never a silent fall-through to the
// wrong shape (resource-shaped output served under a data-source label,
// or the reverse).
var ErrUnsupportedMode = fmt.Errorf("schema source does not support this mode")

// MemberSnapshot is one real [dynamic_providers.<name>] table's own
// frozen content -- everything Generate<Source>Member captured for ONE
// member of a real group, in whichever Mode that member actually is.
type MemberSnapshot struct {
	// SchemaSource names which real Build/BuildDataSources pipeline
	// RawSpec needs.
	SchemaSource SchemaSource `json:"schema_source"`

	// Mode names which real pipeline within that source -- see the Mode
	// type's own doc comment.
	Mode Mode `json:"mode"`

	// Auth is the real, unchanged [dynamic_providers.<name>.auth] table
	// this member needs at real execution time -- carries auth TYPE and
	// PARAM NAMES, never a real secret value itself.
	Auth config.Auth `json:"auth"`

	// BaseURL/Retry/Timeouts/Resources are the real, unchanged execution
	// config every dynamic provider already declares in
	// [dynamic_providers.<name>] today -- carried through unchanged so a
	// pinned member is a real, complete replacement for that table.
	BaseURL   string                           `json:"base_url"`
	Retry     config.RetryConfig               `json:"retry"`
	Timeouts  config.TimeoutsConfig            `json:"timeouts"`
	Resources map[string]config.ResourceConfig `json:"resources,omitempty"`

	// WireName/VersionQualifier/TargetPrefix/DataSourceNamespace are the
	// per-source generation/re-translation inputs that were
	// config.Provider fields with no home on a frozen artifact before
	// UBI-182 -- necessary once [providers.<name>] is a member's only
	// config: Load<Source>Member has no live [dynamic_providers.<name>]
	// table left to read these from otherwise. Empty for whichever
	// source/mode combination doesn't need a given one -- omitempty
	// keeps a member's own real JSON free of fields it has no use for.
	WireName            string `json:"wire_name,omitempty"`
	VersionQualifier    string `json:"version_qualifier,omitempty"`
	TargetPrefix        string `json:"target_prefix,omitempty"`
	DataSourceNamespace string `json:"data_source_namespace,omitempty"`

	// NamespaceFromTags carries config.Provider.NamespaceFromTags
	// through to the frozen artifact -- see that field's own doc
	// comment (UBI-222). namespacesForSource (mergegroup.go) reads this
	// per member, not a live config table, since a pinned resolution has
	// no live [dynamic_providers.<name>] table left to consult.
	NamespaceFromTags bool `json:"namespace_from_tags,omitempty"`

	// RawSpec is the real, verbatim, already-fetched provider spec this
	// member was generated from (SchemaSource says which real format).
	// See the package doc comment (prior, single-member version) for why
	// this is the RAW input, not this binary's own already-translated
	// output: re-deriving the translated schema from RawSpec at load
	// time, every time, using whatever translation logic THIS build
	// ships, is the whole point of the SchemaFormat/Version split.
	RawSpec json.RawMessage `json:"raw_spec"`
}

// Snapshot is one real, frozen file: everything the binary needs to
// serve a provider's real, WHOLE GROUP schema with zero network calls at
// resolution time. See the package doc comment for the full real account
// of why this is a container of members, not one flat spec.
type Snapshot struct {
	// SchemaFormat is THIS FILE's own real shape version -- see the
	// package doc comment. Always checked (CheckFormat) before any other
	// field is trusted.
	SchemaFormat int `json:"schema_format"`

	// Provider is the real group identity this snapshot describes --
	// ubiquex's own [dynamic_provider_groups.<x>]'s own repo_name (e.g.
	// "kubernetes", "aws"), matching the real published
	// github.com/ubiquex/ubx-schema-<Provider> repo this snapshot IS.
	Provider string `json:"provider"`

	// Version is the whole GROUP's own real, mechanically-derived semver
	// (AssembleGroup) -- one number for every member together, not one
	// per member. See the package doc comment for why.
	Version string `json:"version"`

	// Members is one MemberSnapshot per real [dynamic_providers.<name>]
	// table this group bundles, keyed by that table's own real name
	// (e.g. "kubernetes", "kubernetes_ds") -- the exact name a launched
	// process's own UBX_DYNAMIC_PROVIDER_NAME identifies, used to pick
	// its own member back out of this map at load time.
	Members map[string]*MemberSnapshot `json:"members"`

	// Exclude records which real member's own copy of a colliding wire
	// type name loses, keyed by member name -- the SAME real shape and
	// the SAME real judgment ubiquex's own [dynamic_provider_groups.<x>
	// .exclude] table already records for codegen (Datadog's own real
	// v1/v2 collisions: v1's richer version wins both times), now a real
	// property of the PUBLISHED ARTIFACT itself rather than only the
	// consuming config -- the pinned-resolution path (mergegroup.go)
	// reads this to resolve a real collision the same way codegen
	// already does, instead of inventing a second, parallel precedence
	// mechanism. Empty/absent means the group has no known real
	// collisions to resolve -- see mergegroup.go's own doc comment for
	// what happens when a real collision has no matching entry here
	// (fails loud, always -- an unresolved collision is never silently
	// defaulted, the exact real failure mode the wire_name bug produced
	// before this existed).
	Exclude map[string][]string `json:"exclude,omitempty"`

	// MinBinaryVersion is UBI-194's own real answer to "which
	// ubx-provider-dynamic binary can correctly SERVE this snapshot" --
	// stamped by AssembleGroup at generation time from BinaryVersion
	// (this exact build's own real, embedded release version), not
	// inferred from SchemaFormat. Confirmed live, not assumed, why
	// SchemaFormat alone can't answer this: AWS's own real mixed-source
	// group needed internal/mixedserver (a real SERVING-time capability)
	// before it could be served at all, but SchemaFormat never moved for
	// that fix -- a snapshot generated the day before vs. the day after
	// that fix both declare the identical SchemaFormat, yet only one can
	// actually be served. The generating binary's own real version is
	// exact by construction (it definitely can serve what it just
	// wrote) where SchemaFormat is coarse and where a hand-maintained
	// compatibility table would need updating in lockstep with every
	// real serving-capability fix, forever, with no way to fix already-
	// published snapshots retroactively either way -- this field
	// doesn't need a table at all, and self-heals on every real
	// regeneration (hash-watch.yml always builds ubx-provider-dynamic
	// fresh), not just future ones.
	//
	// Empty for every snapshot generated before this field existed (all
	// six real, already-published provider repos as of UBI-194) --
	// resolution treats an empty value as a real, explicit, LOGGED
	// bootstrap case, not a silent, permanent second meaning (see
	// provider.AcquireDynamicProviderBinary's own doc comment in
	// ubiquex).
	MinBinaryVersion string `json:"min_binary_version,omitempty"`
}

// BinaryVersion is THIS BUILD's own real, released version -- set via
// -ldflags "-X .../internal/snapshot.BinaryVersion=<version>" by the
// real publish workflow that cuts a tagged ubx-provider-dynamic release
// (UBI-194), left at its own real, honest "dev" default for any build
// that didn't set it (a local `go build`, a worktree checkout used for
// this arc's own live-proof verification, etc.) -- AssembleGroup stamps
// whatever's here into every new snapshot's own real MinBinaryVersion,
// unconditionally; "dev" is a real, deliberately-not-a-semver value a
// caller resolving MinBinaryVersion must already treat as absent/
// untrusted the same way it treats a genuinely empty string, so a local
// build can never be mistaken for a real, fetchable release.
var BinaryVersion = "dev"

// CheckFormat refuses loudly, real and immediate, if the snapshot's own
// SchemaFormat falls outside this build's declared
// [MinSupportedSchemaFormat, MaxSupportedSchemaFormat] range -- never
// silently misinterpreted as a shape this build doesn't actually
// understand.
func CheckFormat(schemaFormat int) error {
	if schemaFormat < MinSupportedSchemaFormat {
		return fmt.Errorf("%w: snapshot schema_format %d is older than this binary still supports (minimum %d) -- regenerate the snapshot with a real, current ubx-provider-dynamic build",
			ErrUnsupportedSchemaFormat, schemaFormat, MinSupportedSchemaFormat)
	}
	if schemaFormat > MaxSupportedSchemaFormat {
		return fmt.Errorf("%w: snapshot schema_format %d is newer than this binary understands (maximum %d) -- upgrade ubx-provider-dynamic before using this snapshot",
			ErrUnsupportedSchemaFormat, schemaFormat, MaxSupportedSchemaFormat)
	}
	return nil
}

// ErrUnsupportedSchemaFormat is CheckFormat's own real sentinel, wrapped
// (not returned bare) so a caller can errors.Is against it regardless of
// which real direction (too old/too new) produced the message.
var ErrUnsupportedSchemaFormat = fmt.Errorf("unsupported schema_format")

// Member looks up name in snap's own real Members map, returning a real,
// named, immediate error (not a nil-map panic or a silent zero value) if
// this group doesn't bundle that member -- the real, single real
// chokepoint every caller (runServeSnapshot, --dump-signals,
// --dump-namespaces) uses to resolve its own UBX_DYNAMIC_PROVIDER_NAME
// against a loaded group container.
func (s *Snapshot) Member(name string) (*MemberSnapshot, error) {
	m, ok := s.Members[name]
	if !ok {
		return nil, fmt.Errorf("snapshot for group %q has no member %q (real members: %v)", s.Provider, name, memberNames(s.Members))
	}
	return m, nil
}

func memberNames(members map[string]*MemberSnapshot) []string {
	names := make([]string, 0, len(members))
	for n := range members {
		names = append(names, n)
	}
	return names
}

// Save writes snap to path as ONE real, indented, human-diffable JSON
// file -- kept as this package's own simple, single-file primitive
// (hermetic tests use it directly, and --prev-snapshot's own internal
// diffing has no real need for split files) even though UBI-182's own
// real distribution format (SaveSplit) is what an actual ubx-schema-<name>
// repo commits and a real pinned resolution reads. Not used by any real
// CLI path once SaveSplit exists -- deliberately kept as a plain,
// reusable primitive rather than deleted.
func Save(path string, snap *Snapshot) error {
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal snapshot: %w", err)
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

// manifest is SaveSplit/LoadSplit's own real, committed manifest.json
// shape -- everything about the GROUP except its members' own real
// content (schema_format/provider/version, plus which real member names
// this group bundles). Deliberately does not embed *MemberSnapshot
// values themselves -- those live in their own real, separately
// diffable members/<name>.json files, the whole real reason this split
// format exists (a version bump's own real git diff shows exactly which
// members changed, not one undifferentiated blob -- UBI-182's own
// explicit design decision, made after a real, measured file-size
// problem at AWS's 430-member scale: one flat file would exceed
// GitHub's own real 100MB commit limit by more than 2x).
type manifest struct {
	SchemaFormat     int                 `json:"schema_format"`
	Provider         string              `json:"provider"`
	Version          string              `json:"version"`
	Members          []string            `json:"members"`
	Exclude          map[string][]string `json:"exclude,omitempty"`
	MinBinaryVersion string              `json:"min_binary_version,omitempty"`
}

// SaveSplit writes snap as a real, committable directory tree:
// <dir>/manifest.json plus one <dir>/members/<name>.json per real
// member, each independently indented and human-diffable. This is the
// real, on-disk shape a ubx-schema-<type> repo commits, and the real
// shape a release's own snapshot.tar.gz archive bundles (provider.Acquire
// Schema, ubiquex) -- SaveSplit's own output IS what gets tar'd, not a
// separately-derived representation.
func SaveSplit(dir string, snap *Snapshot) error {
	membersDir := filepath.Join(dir, "members")
	if err := os.MkdirAll(membersDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", membersDir, err)
	}

	names := memberNames(snap.Members)
	sort.Strings(names)
	man := manifest{
		SchemaFormat:     snap.SchemaFormat,
		Provider:         snap.Provider,
		Version:          snap.Version,
		Members:          names,
		Exclude:          snap.Exclude,
		MinBinaryVersion: snap.MinBinaryVersion,
	}
	manData, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	manData = append(manData, '\n')
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), manData, 0o644); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}

	for _, name := range names {
		memberData, err := json.MarshalIndent(snap.Members[name], "", "  ")
		if err != nil {
			return fmt.Errorf("marshal member %q: %w", name, err)
		}
		memberData = append(memberData, '\n')
		if err := os.WriteFile(filepath.Join(membersDir, name+".json"), memberData, 0o644); err != nil {
			return fmt.Errorf("write member %q: %w", name, err)
		}
	}
	return nil
}

// LoadSplit is SaveSplit's own real, network-free counterpart -- reads a
// real, already-extracted (or hand-populated mirror/checked-out repo)
// directory tree back into one in-memory Snapshot. CheckFormat runs
// here, always, exactly like Load: every real caller gets a real,
// already-format-checked Snapshot or a real, honest error.
func LoadSplit(dir string) (*Snapshot, error) {
	manData, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil, fmt.Errorf("read manifest in %s: %w", dir, err)
	}
	var man manifest
	if err := json.Unmarshal(manData, &man); err != nil {
		return nil, fmt.Errorf("parse manifest in %s: %w", dir, err)
	}
	if err := CheckFormat(man.SchemaFormat); err != nil {
		return nil, fmt.Errorf("snapshot %s: %w", dir, err)
	}

	members := make(map[string]*MemberSnapshot, len(man.Members))
	for _, name := range man.Members {
		memberPath := filepath.Join(dir, "members", name+".json")
		memberData, err := os.ReadFile(memberPath)
		if err != nil {
			return nil, fmt.Errorf("read member %q in %s: %w", name, dir, err)
		}
		var member MemberSnapshot
		if err := json.Unmarshal(memberData, &member); err != nil {
			return nil, fmt.Errorf("parse member %q in %s: %w", name, dir, err)
		}
		members[name] = &member
	}

	return &Snapshot{
		SchemaFormat:     man.SchemaFormat,
		Provider:         man.Provider,
		Version:          man.Version,
		Members:          members,
		Exclude:          man.Exclude,
		MinBinaryVersion: man.MinBinaryVersion,
	}, nil
}

// Load reads and real-validates path's own snapshot -- CheckFormat runs
// here, always, before Load returns: every real caller gets a
// real, already-format-checked Snapshot or a real, honest error, never a
// Snapshot a caller might forget to check itself.
func Load(path string) (*Snapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read snapshot %s: %w", path, err)
	}
	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, fmt.Errorf("parse snapshot %s: %w", path, err)
	}
	if err := CheckFormat(snap.SchemaFormat); err != nil {
		return nil, fmt.Errorf("snapshot %s: %w", path, err)
	}
	return &snap, nil
}
