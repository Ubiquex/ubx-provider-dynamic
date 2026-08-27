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

// fixedStatusServer always returns status with a small JSON error body --
// real net/http, real status codes, nothing mocked.
func fixedStatusServer(status int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"message":"deliberate test failure"}`))
	}))
}

func buildTestServerWithClient(t *testing.T, client *restexec.Client) (*Server, *ResourceType) {
	t.Helper()
	resources, notes, err := Build(buildTestDoc(), "test", config.Provider{})
	if err != nil {
		t.Fatalf("Build: %v (notes: %v)", err, notes)
	}
	rt := resources["test_repository"]
	rt.Client = client
	return &Server{ProviderName: "test", Resources: resources}, rt
}

// noRetryClient is a real restexec.Client with retries disabled -- these
// tests are about the terminal/ambiguous SPLIT itself, not about
// restexec's own retry behavior (already covered in internal/restexec's
// own tests), so no-retry keeps them fast and focused.
func noRetryClient(baseURL string) *restexec.Client {
	c := restexec.NewClient(baseURL, nil)
	c.Retry = restexec.RetryPolicy{MaxAttempts: 1}
	return c
}

func TestApplyResourceChange_Create_TerminalStatus_ReturnsDiagnosticsNoError(t *testing.T) {
	ts := fixedStatusServer(http.StatusBadRequest) // 400 -- terminal
	defer ts.Close()

	srv, rt := buildTestServerWithClient(t, noRetryClient(ts.URL))
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
		t.Fatalf("expected a clean RPC return (err=nil) for a terminal failure, got RPC-level error: %v", err)
	}
	if resp == nil || len(resp.Diagnostics) == 0 {
		t.Fatalf("expected a Diagnostic for a real 400, got %+v", resp)
	}
}

func TestApplyResourceChange_Create_AmbiguousStatus_ReturnsErrorNoDiagnostics(t *testing.T) {
	ts := fixedStatusServer(http.StatusServiceUnavailable) // 503 -- ambiguous once retries exhaust
	defer ts.Close()

	srv, rt := buildTestServerWithClient(t, noRetryClient(ts.URL))
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
		t.Fatal("expected a real RPC-level error (ambiguous) for a 503, not a clean response")
	}
	if resp != nil {
		t.Fatalf("expected a nil response alongside the ambiguous error, got %+v", resp)
	}
}

func TestApplyResourceChange_Update_TerminalVsAmbiguous(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		wantErr  bool
		wantDiag bool
	}{
		{"terminal 409", http.StatusConflict, false, true},
		{"ambiguous 502", http.StatusBadGateway, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := fixedStatusServer(tc.status)
			defer ts.Close()

			srv, rt := buildTestServerWithClient(t, noRetryClient(ts.URL))
			state := objValue(rt, map[string]tftypes.Value{
				"owner": tftypes.NewValue(tftypes.String, "acme"),
				"repo":  tftypes.NewValue(tftypes.String, "widgets"),
				"name":  tftypes.NewValue(tftypes.String, "widgets"),
			})

			resp, err := srv.ApplyResourceChange(t.Context(), &tfprotov6.ApplyResourceChangeRequest{
				TypeName:     "test_repository",
				PriorState:   mustDV(t, rt, state),
				PlannedState: mustDV(t, rt, state),
			})
			if tc.wantErr && err == nil {
				t.Fatal("expected an ambiguous RPC-level error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected a clean RPC return, got error: %v", err)
			}
			if tc.wantDiag && (resp == nil || len(resp.Diagnostics) == 0) {
				t.Fatalf("expected a Diagnostic, got %+v", resp)
			}
		})
	}
}

func TestApplyResourceChange_Destroy_TerminalVsAmbiguous(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		wantErr  bool
		wantDiag bool
	}{
		{"terminal 403", http.StatusForbidden, false, true},
		{"ambiguous 500", http.StatusInternalServerError, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := fixedStatusServer(tc.status)
			defer ts.Close()

			srv, rt := buildTestServerWithClient(t, noRetryClient(ts.URL))
			state := objValue(rt, map[string]tftypes.Value{
				"owner": tftypes.NewValue(tftypes.String, "acme"),
				"repo":  tftypes.NewValue(tftypes.String, "widgets"),
				"name":  tftypes.NewValue(tftypes.String, "widgets"),
			})

			planResp, err := srv.PlanResourceChange(t.Context(), &tfprotov6.PlanResourceChangeRequest{
				TypeName:         "test_repository",
				PriorState:       mustDV(t, rt, state),
				ProposedNewState: mustDV(t, rt, tftypes.NewValue(rt.ObjectType, nil)),
			})
			if err != nil {
				t.Fatal(err)
			}

			resp, err := srv.ApplyResourceChange(t.Context(), &tfprotov6.ApplyResourceChangeRequest{
				TypeName:       "test_repository",
				PriorState:     mustDV(t, rt, state),
				PlannedState:   mustDV(t, rt, tftypes.NewValue(rt.ObjectType, nil)),
				PlannedPrivate: planResp.PlannedPrivate,
			})
			if tc.wantErr && err == nil {
				t.Fatal("expected an ambiguous RPC-level error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected a clean RPC return, got error: %v", err)
			}
			if tc.wantDiag && (resp == nil || len(resp.Diagnostics) == 0) {
				t.Fatalf("expected a Diagnostic, got %+v", resp)
			}
		})
	}
}

func TestReadResource_AmbiguousStatus_ReturnsErrorNoResponse(t *testing.T) {
	ts := fixedStatusServer(http.StatusGatewayTimeout) // 504 -- ambiguous
	defer ts.Close()

	srv, rt := buildTestServerWithClient(t, noRetryClient(ts.URL))
	current := objValue(rt, map[string]tftypes.Value{
		"owner": tftypes.NewValue(tftypes.String, "acme"),
		"repo":  tftypes.NewValue(tftypes.String, "widgets"),
	})

	resp, err := srv.ReadResource(t.Context(), &tfprotov6.ReadResourceRequest{
		TypeName:     "test_repository",
		CurrentState: mustDV(t, rt, current),
	})
	if err == nil {
		t.Fatal("expected a real RPC-level error for an ambiguous read failure")
	}
	if resp != nil {
		t.Fatalf("expected nil response, got %+v", resp)
	}
}
