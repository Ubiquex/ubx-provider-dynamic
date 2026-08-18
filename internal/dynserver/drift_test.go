package dynserver

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/ubiquex/ubx-provider-dynamic/internal/config"
	"github.com/ubiquex/ubx-provider-dynamic/internal/restexec"
)

func buildDriftTestServer(t *testing.T, baseURL string, drift config.DriftConfig) (*Server, *ResourceType) {
	t.Helper()
	cfg := config.Provider{
		Resources: map[string]config.ResourceConfig{
			"test_repository": {Drift: drift},
		},
	}
	resources, notes, err := Build(buildTestDoc(), "test", cfg)
	if err != nil {
		t.Fatalf("Build: %v (notes: %v)", err, notes)
	}
	rt := resources["test_repository"]
	client := restexec.NewClient(baseURL, nil)
	client.Retry = restexec.RetryPolicy{MaxAttempts: 1}
	return &Server{ProviderName: "test", Resources: resources, Client: client}, rt
}

// TestReadResource_DriftIgnore_NeverReportsConfiguredFields proves an
// ignored field's value in NewState always tracks whatever the caller's
// own CurrentState already said, never the API's fresh response -- even
// though the fake API here returns a genuinely different value each read,
// simulating a real server-stamped field (e.g. updated_at) that changes on
// every read regardless of any real drift.
func TestReadResource_DriftIgnore_NeverReportsConfiguredFields(t *testing.T) {
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		// "private" changes every single call -- a real ignore-worthy
		// field would do exactly this without representing real drift.
		priv := calls%2 == 0
		_, _ = w.Write([]byte(`{"id":1,"name":"widgets","private":` + boolStr(priv) + `}`))
	}))
	defer ts.Close()

	srv, rt := buildDriftTestServer(t, ts.URL, config.DriftConfig{Ignore: []string{"private"}})

	current := objValue(rt, map[string]tftypes.Value{
		"owner":   tftypes.NewValue(tftypes.String, "acme"),
		"repo":    tftypes.NewValue(tftypes.String, "widgets"),
		"name":    tftypes.NewValue(tftypes.String, "widgets"),
		"private": tftypes.NewValue(tftypes.Bool, true),
	})

	resp, err := srv.ReadResource(t.Context(), &tfprotov6.ReadResourceRequest{
		TypeName:     "test_repository",
		CurrentState: mustDV(t, rt, current),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Diagnostics) > 0 {
		t.Fatalf("diagnostics: %+v", resp.Diagnostics)
	}
	got := decodeObj(t, rt, resp.NewState)
	var private bool
	if err := got["private"].As(&private); err != nil {
		t.Fatal(err)
	}
	if private != true {
		t.Fatalf("expected the ignored field to keep CurrentState's own value (true) regardless of the API's fresh (and differing) response, got %v", private)
	}
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// TestApplyResourceChange_Create_DriftNormalize_LowercasesBeforeRecording
// proves a normalized field is transformed BEFORE it's ever written into
// state -- the create response here returns an uppercase value, and the
// recorded NewState must already carry the canonical (lowercase) form.
func TestApplyResourceChange_Create_DriftNormalize_LowercasesBeforeRecording(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":1,"name":"WIDGETS-Example","private":false}`))
	}))
	defer ts.Close()

	srv, rt := buildDriftTestServer(t, ts.URL, config.DriftConfig{Normalize: map[string]string{"name": "lowercase"}})

	planned := objValue(rt, map[string]tftypes.Value{
		"org":   tftypes.NewValue(tftypes.String, "acme"),
		"owner": tftypes.NewValue(tftypes.String, "acme"),
		"repo":  tftypes.NewValue(tftypes.String, "widgets"),
		"name":  tftypes.NewValue(tftypes.String, "WIDGETS-Example"),
	})

	resp, err := srv.ApplyResourceChange(t.Context(), &tfprotov6.ApplyResourceChangeRequest{
		TypeName:     "test_repository",
		PriorState:   mustDV(t, rt, tftypes.NewValue(rt.ObjectType, nil)),
		PlannedState: mustDV(t, rt, planned),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Diagnostics) > 0 {
		t.Fatalf("diagnostics: %+v", resp.Diagnostics)
	}
	got := decodeObj(t, rt, resp.NewState)
	var name string
	if err := got["name"].As(&name); err != nil {
		t.Fatal(err)
	}
	if name != "widgets-example" {
		t.Fatalf("expected the normalized (lowercase) value to already be recorded, got %q", name)
	}
}

func TestResolveDriftPolicy_RejectsUnknownNormalizer(t *testing.T) {
	if _, err := resolveDriftPolicy(config.DriftConfig{Normalize: map[string]string{"x": "not-a-real-normalizer"}}); err == nil {
		t.Fatal("expected an error for an unrecognized normalizer name")
	}
}
