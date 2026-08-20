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
// not assumed -- see GenerateOpenAPI's own doc comment), and write
// everything the binary needs to serve it again with zero network calls.
//
// Two real, separately-versioned numbers, deliberately not one:
//
//   - SchemaFormat is THIS PACKAGE's own capability version -- which
//     shape of snapshot file a given binary build knows how to read.
//     Changes only when the binary itself gains a real new capability
//     (a new field this struct needs, a new SchemaSource this package
//     learns to generate/load). A schema_format outside a binary's own
//     declared [MinSupportedSchemaFormat, MaxSupportedSchemaFormat]
//     range is refused loudly (CheckFormat) -- never silently
//     misinterpreted.
//   - Version is the PROVIDER's own real API-surface version -- a real
//     semver string, mechanically derived (internal/snapshot/diff.go)
//     from what actually changed in the provider's own real spec between
//     one snapshot and the next, never hand-picked.
//
// Scope, real and explicit, not silently assumed to generalize further:
// this package's own real Generate/Load pair is built and proven for
// schema_source = "openapi" only (GenerateOpenAPI/LoadOpenAPI) --
// smithy/cloudformation/discovery_docs each have their own real, separate
// Build pipeline (cmd/ubx-provider-dynamic/main.go's own real per-source
// branches) and need their own real Generate/Load pair before this
// package's own promise ("no network at schema resolution time") is true
// for them too. Named here as real, necessary follow-up work, not
// attempted this session.
package snapshot

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/ubiquex/ubx-provider-dynamic/internal/config"
)

// CurrentSchemaFormat is what THIS BUILD of the binary writes when it
// generates a new snapshot -- always the max of its own declared
// supported range (a binary always writes its own newest, best-understood
// shape; MinSupportedSchemaFormat exists only so a build one or more
// versions old can still read snapshots older builds already wrote).
const CurrentSchemaFormat = 1

// MinSupportedSchemaFormat/MaxSupportedSchemaFormat is THIS BUILD's own
// real, declared compatibility range -- CheckFormat's own real source of
// truth. Both equal CurrentSchemaFormat today (this package's first real
// version); a future binary that changes the snapshot shape bumps
// CurrentSchemaFormat and MaxSupportedSchemaFormat together and may widen
// MinSupportedSchemaFormat downward if it chooses to keep reading the
// prior shape too -- a real, deliberate choice for that future change to
// make, not decided here.
const (
	MinSupportedSchemaFormat = 1
	MaxSupportedSchemaFormat = 1
)

// SchemaSource names which real Build pipeline a Snapshot's own RawSpec
// needs (mirrors config.SchemaSourceType's own real values -- a separate
// type, not a reuse of it, since a Snapshot is real, on-disk, versioned
// data with its own independent compatibility contract, deliberately not
// coupled to config.Provider's own in-memory, TOML-sourced shape even
// though the same real strings apply).
type SchemaSource string

const (
	SchemaSourceOpenAPI SchemaSource = "openapi"
)

// Snapshot is one real, frozen file: everything the binary needs to serve
// a provider's real schema with zero network calls at resolution time.
type Snapshot struct {
	// SchemaFormat is THIS FILE's own real shape version -- see the
	// package doc comment. Always checked (CheckFormat) before any other
	// field is trusted.
	SchemaFormat int `json:"schema_format"`

	// Provider is the real provider name this snapshot describes (e.g.
	// "aws", "datadog") -- the same real identity
	// [dynamic_providers.<name>]'s own table key already carries, kept
	// here too since a snapshot file is meant to be a real, standalone,
	// self-describing artifact, not dependent on whatever local config
	// happens to reference it.
	Provider string `json:"provider"`

	// Version is the provider's own real, mechanically-derived semver
	// (internal/snapshot/diff.go) -- see the package doc comment for why
	// this is a real, separate number from SchemaFormat.
	Version string `json:"version"`

	// SchemaSource names which real Build pipeline RawSpec needs.
	SchemaSource SchemaSource `json:"schema_source"`

	// Auth is the real, unchanged [dynamic_providers.<name>.auth] table
	// this provider needs at real execution time -- carries auth TYPE and
	// PARAM NAMES (e.g. "client_secret_env" -> an env var NAME), never a
	// real secret value itself, identical to how config.Provider.Auth
	// already works today.
	Auth config.Auth `json:"auth"`

	// BaseURL/Retry/Timeouts/Resources are the real, unchanged execution
	// config every dynamic provider already declares in
	// [dynamic_providers.<name>] today -- carried through unchanged so a
	// snapshot is a real, complete replacement for that table, not a
	// partial one still needing a live config file alongside it for
	// anything beyond the schema itself.
	BaseURL   string                           `json:"base_url"`
	Retry     config.RetryConfig               `json:"retry"`
	Timeouts  config.TimeoutsConfig            `json:"timeouts"`
	Resources map[string]config.ResourceConfig `json:"resources,omitempty"`

	// RawSpec is the real, verbatim, already-fetched provider spec this
	// snapshot was generated from (SchemaSource says which real format).
	// Deliberately the RAW spec, not this binary's own already-translated
	// tfprotov6.Schema output: tftypes.Type (a real field on every
	// tfprotov6.SchemaAttribute) has no working JSON UnmarshalJSON,
	// confirmed live before this package was written (marshals fine,
	// fails to round-trip back) -- but the real, deeper reason is that
	// storing the RAW input, not the derived output, is what makes the
	// SchemaFormat/Version split actually correct: Version tracks how
	// THIS raw spec changed (the real provider's own API surface),
	// SchemaFormat tracks how the BINARY'S OWN translation of that raw
	// spec into a servable schema is allowed to change -- re-deriving the
	// tfprotov6.Schema from RawSpec at load time, every time, using
	// whatever translation logic THIS build ships, is the whole point.
	RawSpec json.RawMessage `json:"raw_spec"`
}

// CheckFormat refuses loudly, real and immediate, if the snapshot's own
// SchemaFormat falls outside this build's declared
// [MinSupportedSchemaFormat, MaxSupportedSchemaFormat] range -- never
// silently misinterpreted as a shape this build doesn't actually
// understand. The real, explicit direction matters for the error message:
// a snapshot NEWER than this build understands needs a real binary
// upgrade; a snapshot OLDER than this build still supports needs nothing
// (that's the whole point of Min being allowed to trail Max), so only the
// "too new" and "too old to still support" cases are real errors.
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

// Save writes snap to path as real, indented, human-diffable JSON -- a
// real snapshot file is meant to be reviewable (a real git diff on a
// version bump should show what actually changed), not a packed binary
// blob.
func Save(path string, snap *Snapshot) error {
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal snapshot: %w", err)
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
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
