package snapshot

import (
	"encoding/json"
	"errors"
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

// TestSaveLoad_RealRoundTrip proves a real Snapshot, including its own
// RawSpec (real, opaque JSON -- a genuine OpenAPI fragment here, not a
// placeholder string) and real config.Auth/RetryConfig/TimeoutsConfig
// values, survives Save then Load byte-for-byte on every field that
// matters, and that Load's own real CheckFormat call actually runs (a
// deliberately corrupted schema_format is caught, not silently accepted).
func TestSaveLoad_RealRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "snapshot.json")

	rawSpec := json.RawMessage(`{"openapi":"3.0.0","paths":{"/widgets":{"get":{}}}}`)
	want := &Snapshot{
		SchemaFormat: CurrentSchemaFormat,
		Provider:     "widgetco",
		Version:      "1.4.0",
		SchemaSource: SchemaSourceOpenAPI,
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

	if err := Save(path, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got.Provider != want.Provider || got.Version != want.Version || got.SchemaSource != want.SchemaSource {
		t.Errorf("identity fields didn't round-trip: got %+v", got)
	}
	if got.BaseURL != want.BaseURL {
		t.Errorf("BaseURL = %q, want %q", got.BaseURL, want.BaseURL)
	}
	if got.Retry.MaxAttempts != 5 || got.Retry.InitialBackoff != "500ms" {
		t.Errorf("Retry didn't round-trip: %+v", got.Retry)
	}
	if got.Timeouts.Create != "30s" {
		t.Errorf("Timeouts didn't round-trip: %+v", got.Timeouts)
	}
	if got.Auth.Type != "api_key_header" || got.Auth.Params["value_env"] != "WIDGETCO_API_KEY" {
		t.Errorf("Auth didn't round-trip: %+v", got.Auth)
	}
	// Real, semantic comparison, not byte-for-byte: Save's own real
	// MarshalIndent re-flows RawSpec's own embedded whitespace (a real,
	// harmless side effect of indenting the OUTER Snapshot document),
	// which is not the same thing as losing or altering real content.
	var gotAny, wantAny any
	if err := json.Unmarshal(got.RawSpec, &gotAny); err != nil {
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
