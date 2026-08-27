package dynserver

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/ubiquex/ubx-provider-dynamic/internal/auth"
	"github.com/ubiquex/ubx-provider-dynamic/internal/config"
	"github.com/ubiquex/ubx-provider-dynamic/internal/restexec"
)

// requireHeaderMiddleware stands in for a real REST API's own auth check --
// real net/http, real 401s, nothing mocked.
func requireHeaderMiddleware(next http.Handler, headerName, wantValue string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(headerName) != wantValue {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// TestServer_AuthenticatedReadResource_RealAuthFlow proves Phase 2's auth
// package and Phase 1's dynserver/restexec actually compose: a real
// restexec.Authenticator built via auth.Build, wired into a real
// restexec.Client, driving a real ReadResource RPC against a real HTTP
// server that genuinely rejects unauthenticated requests -- not each
// layer tested in isolation.
func TestServer_AuthenticatedReadResource_RealAuthFlow(t *testing.T) {
	api := newFakeAPI()
	api.repos["widgets"] = map[string]any{"id": float64(1), "name": "widgets", "private": true}
	ts := httptest.NewServer(requireHeaderMiddleware(api.handler(), "Authorization", "Bearer real-secret-token"))
	defer ts.Close()

	t.Setenv("UBX_TEST_INTEGRATION_TOKEN", "real-secret-token")
	authenticator, err := auth.Build("api_key_header", map[string]any{
		"headers": []map[string]any{{"name": "Authorization", "value_env": "UBX_TEST_INTEGRATION_TOKEN", "value_prefix": "Bearer "}},
	})
	if err != nil {
		t.Fatal(err)
	}

	resources, notes, err := Build(buildTestDoc(), "test", config.Provider{})
	if err != nil {
		t.Fatalf("Build: %v (notes: %v)", err, notes)
	}
	rt := resources["test_repository"]
	rt.Client = restexec.NewClient(ts.URL, authenticator)
	srv := &Server{ProviderName: "test", Resources: resources}

	current := objValue(rt, map[string]tftypes.Value{
		"owner": tftypes.NewValue(tftypes.String, "acme"),
		"repo":  tftypes.NewValue(tftypes.String, "widgets"),
	})
	resp, err := srv.ReadResource(t.Context(), &tfprotov6.ReadResourceRequest{
		TypeName:     "test_repository",
		CurrentState: mustDV(t, rt, current),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Diagnostics) > 0 {
		t.Fatalf("authenticated read failed: %+v", resp.Diagnostics)
	}
	if resp.NewState == nil {
		t.Fatal("expected a real NewState from the authenticated read")
	}
}

func TestServer_UnauthenticatedReadResource_RealRejection(t *testing.T) {
	api := newFakeAPI()
	api.repos["widgets"] = map[string]any{"id": float64(1), "name": "widgets", "private": true}
	ts := httptest.NewServer(requireHeaderMiddleware(api.handler(), "Authorization", "Bearer real-secret-token"))
	defer ts.Close()

	resources, notes, err := Build(buildTestDoc(), "test", config.Provider{})
	if err != nil {
		t.Fatalf("Build: %v (notes: %v)", err, notes)
	}
	rt := resources["test_repository"]
	rt.Client = restexec.NewClient(ts.URL, nil) // no auth configured
	srv := &Server{ProviderName: "test", Resources: resources}

	current := objValue(rt, map[string]tftypes.Value{
		"owner": tftypes.NewValue(tftypes.String, "acme"),
		"repo":  tftypes.NewValue(tftypes.String, "widgets"),
	})
	resp, err := srv.ReadResource(t.Context(), &tfprotov6.ReadResourceRequest{
		TypeName:     "test_repository",
		CurrentState: mustDV(t, rt, current),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Diagnostics) == 0 {
		t.Fatal("expected a diagnostic for an unauthenticated request against a real server that requires auth")
	}
}
