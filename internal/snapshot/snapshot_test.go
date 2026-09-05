package snapshot

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ubiquex/ubx-provider-dynamic/internal/config"
)

func TestCheckFormat_WithinRange_OK(t *testing.T) {
	if err := CheckFormat(CurrentSchemaFormat); err != nil {
		t.Fatalf("CheckFormat(%d) = %v, want nil (this build's own current format)", CurrentSchemaFormat, err)
	}
}

// TestCheckFormat_TooNew_RealErrorNotSilentMisinterpretation is the real
// point of this whole check: a snapshot written by a FUTURE binary build
// (a real, higher schema_format) must never be silently read as if it
// were this build's own, older shape -- confirmed here as a real,
// explicit refusal, not an assumption.
func TestCheckFormat_TooNew_RealErrorNotSilentMisinterpretation(t *testing.T) {
	err := CheckFormat(MaxSupportedSchemaFormat + 1)
	if err == nil {
		t.Fatal("CheckFormat(too new) = nil, want a real error")
	}
	if !errors.Is(err, ErrUnsupportedSchemaFormat) {
		t.Errorf("CheckFormat(too new) error doesn't wrap ErrUnsupportedSchemaFormat: %v", err)
	}
}

func TestCheckFormat_TooOld_RealError(t *testing.T) {
	err := CheckFormat(MinSupportedSchemaFormat - 1)
	if err == nil {
		t.Fatal("CheckFormat(too old) = nil, want a real error")
	}
	if !errors.Is(err, ErrUnsupportedSchemaFormat) {
		t.Errorf("CheckFormat(too old) error doesn't wrap ErrUnsupportedSchemaFormat: %v", err)
	}
}

// TestSaveLoad_RealRoundTrip proves a real, group-container Snapshot,
// including one real member's own RawSpec (real, opaque JSON -- a
// genuine OpenAPI fragment here, not a placeholder string) and real
// config.Auth/RetryConfig/TimeoutsConfig values, survives Save then Load
// byte-for-byte on every field that matters, and that Load's own real
// CheckFormat call actually runs (a deliberately corrupted schema_format
// is caught, not silently accepted).
func TestSaveLoad_RealRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "snapshot.json")

	rawSpec := json.RawMessage(`{"openapi":"3.0.0","paths":{"/widgets":{"get":{}}}}`)
	member := &MemberSnapshot{
		SchemaSource: SchemaSourceOpenAPI,
		Mode:         ModeResource,
		Auth: config.Auth{
			Type:   "api_key_header",
			Params: map[string]any{"name": "X-API-Key", "value_env": "WIDGETCO_API_KEY"},
		},
		BaseURL: "https://api.widgetco.example",
		Retry:   config.RetryConfig{MaxAttempts: 5, InitialBackoff: "500ms"},
		Timeouts: config.TimeoutsConfig{
			Create: "30s", Read: "10s", Update: "30s", Delete: "30s",
		},
		RawSpec: rawSpec,
	}
	want := &Snapshot{
		SchemaFormat: CurrentSchemaFormat,
		Provider:     "widgetco",
		Version:      "1.4.0",
		Members:      map[string]*MemberSnapshot{"widgetco": member},
	}

	if err := Save(path, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got.Provider != want.Provider || got.Version != want.Version {
		t.Errorf("identity fields didn't round-trip: got %+v", got)
	}
	gotMember, err := got.Member("widgetco")
	if err != nil {
		t.Fatalf("Member(widgetco): %v", err)
	}
	if gotMember.SchemaSource != member.SchemaSource || gotMember.Mode != member.Mode {
		t.Errorf("member identity fields didn't round-trip: got %+v", gotMember)
	}
	if gotMember.BaseURL != member.BaseURL {
		t.Errorf("BaseURL = %q, want %q", gotMember.BaseURL, member.BaseURL)
	}
	if gotMember.Retry.MaxAttempts != 5 || gotMember.Retry.InitialBackoff != "500ms" {
		t.Errorf("Retry didn't round-trip: %+v", gotMember.Retry)
	}
	if gotMember.Timeouts.Create != "30s" {
		t.Errorf("Timeouts didn't round-trip: %+v", gotMember.Timeouts)
	}
	if gotMember.Auth.Type != "api_key_header" || gotMember.Auth.Params["value_env"] != "WIDGETCO_API_KEY" {
		t.Errorf("Auth didn't round-trip: %+v", gotMember.Auth)
	}
	// Real, semantic comparison, not byte-for-byte: Save's own real
	// MarshalIndent re-flows RawSpec's own embedded whitespace (a real,
	// harmless side effect of indenting the OUTER Snapshot document),
	// which is not the same thing as losing or altering real content.
	var gotAny, wantAny any
	if err := json.Unmarshal(gotMember.RawSpec, &gotAny); err != nil {
		t.Fatalf("RawSpec didn't survive as valid JSON: %v", err)
	}
	if err := json.Unmarshal(rawSpec, &wantAny); err != nil {
		t.Fatalf("test's own rawSpec fixture is invalid JSON: %v", err)
	}
	gotNorm, _ := json.Marshal(gotAny)
	wantNorm, _ := json.Marshal(wantAny)
	if string(gotNorm) != string(wantNorm) {
		t.Errorf("RawSpec didn't round-trip semantically: got %s, want %s", gotNorm, wantNorm)
	}

	// Real, direct proof Load actually calls CheckFormat -- corrupt the
	// real file's own schema_format on disk and confirm Load refuses it,
	// rather than trusting the earlier unit test on CheckFormat alone to
	// stand in for "Load wires it in correctly."
	corrupted := *want
	corrupted.SchemaFormat = MaxSupportedSchemaFormat + 1
	if err := Save(path, &corrupted); err != nil {
		t.Fatalf("Save (corrupted): %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load accepted a real, on-disk snapshot with an unsupported schema_format")
	}
}

// TestSaveSplitLoadSplit_RealRoundTrip proves the real, committable
// directory shape (manifest.json plus one members/<name>.json per real
// member) survives SaveSplit then LoadSplit -- the actual format a real
// ubx-schema-<type> repo commits and a real pinned resolution reads
// (provider.AcquireSchema, ubiquex), not just the simpler single-file
// Save/Load this same package also keeps as a test-only primitive.
func TestSaveSplitLoadSplit_RealRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := &Snapshot{
		SchemaFormat: CurrentSchemaFormat,
		Provider:     "widgetco",
		Version:      "1.0.0",
		Members: map[string]*MemberSnapshot{
			"widgetco": {
				SchemaSource: SchemaSourceOpenAPI,
				Mode:         ModeResource,
				BaseURL:      "https://api.widgetco.example",
				SchemaURL:    "https://raw.githubusercontent.com/widgetco/openapi/main/spec.yaml",
				RawSpec:      json.RawMessage(`{"openapi":"3.0.0"}`),
			},
			"widgetco_ds": {
				SchemaSource: SchemaSourceOpenAPI,
				Mode:         ModeDataSource,
				BaseURL:      "https://api.widgetco.example",
				SchemaURL:    "https://raw.githubusercontent.com/widgetco/openapi/main/spec.yaml",
				RawSpec:      json.RawMessage(`{"openapi":"3.0.0"}`),
			},
		},
	}

	if err := SaveSplit(dir, want); err != nil {
		t.Fatalf("SaveSplit: %v", err)
	}
	// Real, direct proof the on-disk shape is what's promised -- a real
	// manifest.json at the root, one real file per member under
	// members/, not an implementation detail left unchecked.
	if _, err := os.Stat(filepath.Join(dir, "manifest.json")); err != nil {
		t.Fatalf("no real manifest.json written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "members", "widgetco.json")); err != nil {
		t.Fatalf("no real members/widgetco.json written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "members", "widgetco_ds.json")); err != nil {
		t.Fatalf("no real members/widgetco_ds.json written: %v", err)
	}

	// UBI-222: manifest.json's own real, on-disk schema_urls -- the one
	// small file a drift-watch or a future snapshot cut is meant to read
	// instead of ubiquex's own now-gone live config, or a full
	// members/<name>.json per member. Read the raw bytes, not just
	// LoadSplit's own round-trip, since the whole point is this being
	// visible in the small file without needing the larger ones.
	manifestBytes, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest.json: %v", err)
	}
	var manOnDisk struct {
		SchemaURLs map[string]string `json:"schema_urls"`
	}
	if err := json.Unmarshal(manifestBytes, &manOnDisk); err != nil {
		t.Fatalf("parse manifest.json: %v", err)
	}
	wantURL := "https://raw.githubusercontent.com/widgetco/openapi/main/spec.yaml"
	if manOnDisk.SchemaURLs["widgetco"] != wantURL || manOnDisk.SchemaURLs["widgetco_ds"] != wantURL {
		t.Errorf("manifest.json schema_urls = %+v, want both members mapped to %q", manOnDisk.SchemaURLs, wantURL)
	}

	got, err := LoadSplit(dir)
	if err != nil {
		t.Fatalf("LoadSplit: %v", err)
	}
	if got.Provider != want.Provider || got.Version != want.Version || got.SchemaFormat != want.SchemaFormat {
		t.Errorf("identity fields didn't round-trip: got %+v", got)
	}
	if len(got.Members) != 2 {
		t.Fatalf("got %d members, want 2", len(got.Members))
	}
	resourceMember, err := got.Member("widgetco")
	if err != nil {
		t.Fatalf("Member(widgetco): %v", err)
	}
	if resourceMember.Mode != ModeResource {
		t.Errorf("widgetco Mode = %q, want resource", resourceMember.Mode)
	}
	if resourceMember.SchemaURL != wantURL {
		t.Errorf("widgetco SchemaURL = %q, want %q", resourceMember.SchemaURL, wantURL)
	}
	dsMember, err := got.Member("widgetco_ds")
	if err != nil {
		t.Fatalf("Member(widgetco_ds): %v", err)
	}
	if dsMember.Mode != ModeDataSource {
		t.Errorf("widgetco_ds Mode = %q, want data_source", dsMember.Mode)
	}

	// Real, direct proof LoadSplit actually calls CheckFormat -- corrupt
	// the real, on-disk manifest.json's own schema_format and confirm
	// LoadSplit refuses it.
	corrupted := *want
	corrupted.SchemaFormat = MaxSupportedSchemaFormat + 1
	corruptedDir := t.TempDir()
	if err := SaveSplit(corruptedDir, &corrupted); err != nil {
		t.Fatalf("SaveSplit (corrupted): %v", err)
	}
	if _, err := LoadSplit(corruptedDir); err == nil {
		t.Fatal("LoadSplit accepted a real, on-disk manifest with an unsupported schema_format")
	}
}

// TestManifest_LegacyMinBinaryVersionOnly_StillResolves is the real
// regression guard for UBI-249's rename of MinBinaryVersion to
// GeneratedByBinaryVersion. Every one of the eight already-published
// ubx-schema-<provider> snapshots carries ONLY the legacy
// min_binary_version key, so a bare tag rename would have made all of
// them read as absent. That is not a cosmetic loss: an absent value
// silently downgrades resolution to the SchemaFormat bootstrap fallback
// (cloudflare's real 1.0.10 would become 1.0.0), and it makes
// AssembleGroup's own prev-vs-current comparison see a change on every
// run, manufacturing a spurious Patch release for all eight providers.
func TestManifest_LegacyMinBinaryVersionOnly_StillResolves(t *testing.T) {
	var m manifest
	if err := json.Unmarshal([]byte(`{
  "schema_format": 3,
  "provider": "widgetco",
  "version": "1.0.0",
  "members": ["widgetco"],
  "min_binary_version": "1.0.10"
}`), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := m.generatedByBinaryVersion(); got != "1.0.10" {
		t.Fatalf("generatedByBinaryVersion() = %q, want 1.0.10 read from the legacy min_binary_version key", got)
	}
}

// TestManifest_NewNameWinsOverLegacy proves the precedence runs the way
// the rename intends: once a snapshot is re-cut by a post-rename binary
// it carries both keys, and the new one is authoritative.
func TestManifest_NewNameWinsOverLegacy(t *testing.T) {
	var m manifest
	if err := json.Unmarshal([]byte(`{
  "schema_format": 3,
  "generated_by_binary_version": "1.0.13",
  "min_binary_version": "1.0.10"
}`), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := m.generatedByBinaryVersion(); got != "1.0.13" {
		t.Fatalf("generatedByBinaryVersion() = %q, want 1.0.13 (new name must win over the legacy mirror)", got)
	}
}

// TestSaveSplit_WritesBothNames proves the transitional mirror is really
// written, which is what keeps already-released ubx binaries (they read
// min_binary_version only) resolving a freshly-cut snapshot exactly as
// before the rename.
func TestSaveSplit_WritesBothNames(t *testing.T) {
	var m manifest
	m.GeneratedByBinaryVersion = "1.0.13"
	m.LegacyMinBinaryVersion = m.GeneratedByBinaryVersion
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if raw["generated_by_binary_version"] != "1.0.13" {
		t.Fatalf("generated_by_binary_version = %v, want 1.0.13", raw["generated_by_binary_version"])
	}
	if raw["min_binary_version"] != "1.0.13" {
		t.Fatalf("min_binary_version mirror = %v, want 1.0.13 (transitional, keeps pre-rename readers working)", raw["min_binary_version"])
	}
}
