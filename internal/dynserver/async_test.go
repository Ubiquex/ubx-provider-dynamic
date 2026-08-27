package dynserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/ubiquex/ubx-provider-dynamic/internal/config"
	"github.com/ubiquex/ubx-provider-dynamic/internal/restexec"
)

// asyncFakeAPI is a real net/http server modeling a genuinely common async
// REST convention: create returns 202 + a job id in the body, a separate
// /jobs/{id} endpoint reports "pending" for a few real polls before
// "succeeded," and only then does the resource's own canonical GET return
// full data (widgets/{id}) -- the exact shape awaitAsyncOperation/
// finalizeAfterWrite are built to handle generically, no AWS/GitHub/
// Datadog-specific code involved.
type asyncFakeAPI struct {
	pollsBeforeSuccess int32
	polls              int32
}

func (a *asyncFakeAPI) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/orgs/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{"operation_id": "job-1", "name": "widgets"})
	})
	mux.HandleFunc("/jobs/job-1", func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&a.polls, 1)
		status := "pending"
		if n > a.pollsBeforeSuccess {
			status = "succeeded"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": status})
	})
	mux.HandleFunc("/repos/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": float64(1), "name": "widgets", "private": true})
	})
	return mux
}

func buildAsyncTestServer(t *testing.T, baseURL string) (*Server, *ResourceType) {
	t.Helper()
	cfg := config.Provider{
		Resources: map[string]config.ResourceConfig{
			"test_repository": {
				Async: config.AsyncConfig{
					Enabled:               true,
					OperationIDField:      "operation_id",
					PollPathTemplate:      "/jobs/{operation_id}",
					StatusField:           "status",
					TerminalSuccessValues: []string{"succeeded"},
					TerminalFailureValues: []string{"failed"},
					PollInterval:          "10ms",
					PollTimeout:           "2s",
				},
			},
		},
	}
	resources, notes, err := Build(buildTestDoc(), "test", cfg)
	if err != nil {
		t.Fatalf("Build: %v (notes: %v)", err, notes)
	}
	rt := resources["test_repository"]
	client := restexec.NewClient(baseURL, nil)
	client.Retry = restexec.RetryPolicy{MaxAttempts: 1}
	rt.Client = client
	return &Server{ProviderName: "test", Resources: resources}, rt
}

func TestApplyResourceChange_Create_AsyncPollsUntilSuccessThenReads(t *testing.T) {
	api := &asyncFakeAPI{pollsBeforeSuccess: 2}
	ts := httptest.NewServer(api.handler())
	defer ts.Close()

	srv, rt := buildAsyncTestServer(t, ts.URL)
	planned := objValue(rt, map[string]tftypes.Value{
		"org":   tftypes.NewValue(tftypes.String, "acme"),
		"owner": tftypes.NewValue(tftypes.String, "acme"),
		"repo":  tftypes.NewValue(tftypes.String, "widgets"),
		"name":  tftypes.NewValue(tftypes.String, "widgets"),
	})

	resp, err := srv.ApplyResourceChange(t.Context(), &tfprotov6.ApplyResourceChangeRequest{
		TypeName:     "test_repository",
		PriorState:   mustDV(t, rt, tftypes.NewValue(rt.ObjectType, nil)),
		PlannedState: mustDV(t, rt, planned),
	})
	if err != nil {
		t.Fatalf("expected a clean success after polling, got RPC error: %v", err)
	}
	if len(resp.Diagnostics) > 0 {
		t.Fatalf("diagnostics: %+v", resp.Diagnostics)
	}
	if resp.NewState == nil {
		t.Fatal("expected a real NewState after async completion")
	}
	final := decodeObj(t, rt, resp.NewState)
	var name string
	if err := final["name"].As(&name); err != nil || name != "widgets" {
		t.Fatalf("expected the FINAL read's own data (name=widgets), got %v (%v)", name, err)
	}
	if got := atomic.LoadInt32(&api.polls); got < 3 {
		t.Fatalf("expected at least 3 real polls (2 pending + 1 succeeded), got %d", got)
	}
}

func TestApplyResourceChange_Create_AsyncReachesFailureStatus_ReturnsDiagnostic(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/orgs/acme/repos":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{"operation_id": "job-2"})
		case r.URL.Path == "/jobs/job-2":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "failed"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	srv, rt := buildAsyncTestServer(t, ts.URL)
	planned := objValue(rt, map[string]tftypes.Value{
		"org":   tftypes.NewValue(tftypes.String, "acme"),
		"owner": tftypes.NewValue(tftypes.String, "acme"),
		"repo":  tftypes.NewValue(tftypes.String, "widgets"),
		"name":  tftypes.NewValue(tftypes.String, "widgets"),
	})

	resp, err := srv.ApplyResourceChange(t.Context(), &tfprotov6.ApplyResourceChangeRequest{
		TypeName:     "test_repository",
		PriorState:   mustDV(t, rt, tftypes.NewValue(rt.ObjectType, nil)),
		PlannedState: mustDV(t, rt, planned),
	})
	if err != nil {
		t.Fatalf("expected a clean RPC return with a Diagnostic, got error: %v", err)
	}
	if resp == nil || len(resp.Diagnostics) == 0 {
		t.Fatalf("expected a Diagnostic for a real async failure status, got %+v", resp)
	}
}

func TestApplyResourceChange_Create_AsyncPollTimeout_ReturnsAmbiguousError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/orgs/acme/repos":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{"operation_id": "job-3"})
		case r.URL.Path == "/jobs/job-3":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "pending"}) // never terminal
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	cfg := config.Provider{
		Resources: map[string]config.ResourceConfig{
			"test_repository": {
				Async: config.AsyncConfig{
					Enabled:               true,
					OperationIDField:      "operation_id",
					PollPathTemplate:      "/jobs/{operation_id}",
					StatusField:           "status",
					TerminalSuccessValues: []string{"succeeded"},
					TerminalFailureValues: []string{"failed"},
					PollInterval:          "5ms",
					PollTimeout:           "30ms", // deliberately tiny -- real, fast test
				},
			},
		},
	}
	resources, notes, err := Build(buildTestDoc(), "test", cfg)
	if err != nil {
		t.Fatalf("Build: %v (notes: %v)", err, notes)
	}
	rt := resources["test_repository"]
	client := restexec.NewClient(ts.URL, nil)
	client.Retry = restexec.RetryPolicy{MaxAttempts: 1}
	rt.Client = client
	srv := &Server{ProviderName: "test", Resources: resources}

	planned := objValue(rt, map[string]tftypes.Value{
		"org":   tftypes.NewValue(tftypes.String, "acme"),
		"owner": tftypes.NewValue(tftypes.String, "acme"),
		"repo":  tftypes.NewValue(tftypes.String, "widgets"),
		"name":  tftypes.NewValue(tftypes.String, "widgets"),
	})

	resp, err := srv.ApplyResourceChange(t.Context(), &tfprotov6.ApplyResourceChangeRequest{
		TypeName:     "test_repository",
		PriorState:   mustDV(t, rt, tftypes.NewValue(rt.ObjectType, nil)),
		PlannedState: mustDV(t, rt, planned),
	})
	if err == nil {
		t.Fatalf("expected an ambiguous RPC-level error for a poll timeout, got clean response: %+v", resp)
	}
	if resp != nil {
		t.Fatalf("expected a nil response alongside the ambiguous error, got %+v", resp)
	}
}
