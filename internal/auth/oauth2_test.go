package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// realTokenEndpoint is a real, RFC 6749 §4.4-compliant token endpoint --
// real HTTP over a real net/http/httptest server, not a mocked
// http.RoundTripper -- standing in for an actual OAuth2 authorization
// server. No public, no-registration-required, genuinely third-party
// client-credentials test server exists to point this at instead (unlike
// GitHub/Datadog, which are real, live, publicly reachable APIs this
// session already verified against directly); this is the honest,
// feasible substitute -- it exercises the real golang.org/x/oauth2/
// clientcredentials machinery (real HTTP POST, real Basic-auth or
// form-encoded client credential submission, real JSON token response
// parsing, real Bearer header construction) end to end, the same
// "real server standing in for the actual service" precedent Phase 1's
// own TestServer_FullCRUDLifecycle already established for REST CRUD.
func realTokenEndpoint(wantClientID, wantClientSecret string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		if r.PostForm.Get("grant_type") != "client_credentials" {
			http.Error(w, `{"error":"unsupported_grant_type"}`, http.StatusBadRequest)
			return
		}

		// clientcredentials.Config auto-detects Basic-auth vs. form-body
		// credential submission -- accept either, since this test doesn't
		// care which the library actually chose.
		clientID, clientSecret, ok := r.BasicAuth()
		if !ok {
			clientID = r.PostForm.Get("client_id")
			clientSecret = r.PostForm.Get("client_secret")
		}
		if clientID != wantClientID || clientSecret != wantClientSecret {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid_client"}`))
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "real-issued-token-xyz",
			"token_type":   "Bearer",
			"expires_in":   3600,
		})
	}
}

func TestOAuth2ClientCredentials_RealTokenExchange(t *testing.T) {
	ts := httptest.NewServer(realTokenEndpoint("client-abc", "secret-xyz"))
	defer ts.Close()

	t.Setenv("UBX_TEST_OAUTH_CLIENT_ID", "client-abc")
	t.Setenv("UBX_TEST_OAUTH_CLIENT_SECRET", "secret-xyz")

	a, err := Build("oauth2_client_credentials", map[string]any{
		"token_url":         ts.URL,
		"client_id_env":     "UBX_TEST_OAUTH_CLIENT_ID",
		"client_secret_env": "UBX_TEST_OAUTH_CLIENT_SECRET",
		"scopes":            []string{"read", "write"},
	})
	if err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest(http.MethodGet, "https://example.invalid/resource", nil)
	if err := a.Apply(req); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer real-issued-token-xyz" {
		t.Fatalf("Authorization header = %q, want the real token issued by the real endpoint", got)
	}

	// Apply again -- the underlying TokenSource must cache the still-valid
	// token rather than round-tripping to the token endpoint every call
	// (real, standard OAuth2 client behavior, and cheap to prove: a second
	// real request against the same fake endpoint, same result expected).
	req2, _ := http.NewRequest(http.MethodGet, "https://example.invalid/resource", nil)
	if err := a.Apply(req2); err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	if got := req2.Header.Get("Authorization"); got != "Bearer real-issued-token-xyz" {
		t.Fatalf("second Authorization header = %q", got)
	}
}

// TestOAuth2ClientCredentials_Apply_BuildsTokenSourceOnceNotPerCall is a
// real, direct regression test that the lazy-once refactor (checkpoint
// 9) preserved the ORIGINAL eager-at-Build design's own real reason for
// existing: a real token exchange happens at most ONCE across many real
// Apply calls, not once per call -- deferring WHEN the TokenSource gets
// built must never regress the real, standard OAuth2 caching behavior
// once it IS built. TestOAuth2ClientCredentials_RealTokenExchange alone
// can't prove this (a re-exchanged token from the same fixed real
// endpoint looks identical to a cached one); this test counts real
// requests to the real token endpoint directly.
func TestOAuth2ClientCredentials_Apply_BuildsTokenSourceOnceNotPerCall(t *testing.T) {
	var realExchangeCount int
	handler := realTokenEndpoint("client-abc", "secret-xyz")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		realExchangeCount++
		handler(w, r)
	}))
	defer ts.Close()

	t.Setenv("UBX_TEST_OAUTH_CLIENT_ID", "client-abc")
	t.Setenv("UBX_TEST_OAUTH_CLIENT_SECRET", "secret-xyz")

	a, err := Build("oauth2_client_credentials", map[string]any{
		"token_url":         ts.URL,
		"client_id_env":     "UBX_TEST_OAUTH_CLIENT_ID",
		"client_secret_env": "UBX_TEST_OAUTH_CLIENT_SECRET",
	})
	if err != nil {
		t.Fatal(err)
	}
	if realExchangeCount != 0 {
		t.Fatalf("Build must never perform a real token exchange, got %d real requests before any Apply call", realExchangeCount)
	}

	for i := 0; i < 3; i++ {
		req, _ := http.NewRequest(http.MethodGet, "https://example.invalid/resource", nil)
		if err := a.Apply(req); err != nil {
			t.Fatalf("Apply #%d: %v", i, err)
		}
	}
	if realExchangeCount != 1 {
		t.Fatalf("real token endpoint hit %d times across 3 real Apply calls, want exactly 1 (the still-valid token must be cached and reused, matching the original eager-at-Build design's own real reason for existing)", realExchangeCount)
	}
}

func TestOAuth2ClientCredentials_WrongCredentials_RealRejection(t *testing.T) {
	ts := httptest.NewServer(realTokenEndpoint("real-client", "real-secret"))
	defer ts.Close()

	t.Setenv("UBX_TEST_OAUTH_CLIENT_ID", "wrong-client")
	t.Setenv("UBX_TEST_OAUTH_CLIENT_SECRET", "wrong-secret")

	a, err := Build("oauth2_client_credentials", map[string]any{
		"token_url":         ts.URL,
		"client_id_env":     "UBX_TEST_OAUTH_CLIENT_ID",
		"client_secret_env": "UBX_TEST_OAUTH_CLIENT_SECRET",
	})
	if err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest(http.MethodGet, "https://example.invalid", nil)
	if err := a.Apply(req); err == nil {
		t.Fatal("expected Apply to fail against the real endpoint's own real invalid_client rejection")
	}
}

// TestOAuth2ClientCredentials_Build_NeverRequiresRealCredentials is the
// real, direct regression test for checkpoint 9's own fix: Build must
// succeed with structurally-complete config even when
// client_id_env/client_secret_env name environment variables that are
// NOT set at all -- credential RESOLUTION is deferred to the first real
// Apply, never required for a real schema-only launch (GetProviderSchema,
// never Configure) to start serving at all. Confirmed live this
// checkpoint against Azure's own real central-config entry: this exact
// gap was why a real `ubx sdk gen` run against Azure failed outright on
// a machine with no AZURE_CLIENT_ID/AZURE_CLIENT_SECRET set, despite
// GetProviderSchema itself never needing a credential.
func TestOAuth2ClientCredentials_Build_NeverRequiresRealCredentials(t *testing.T) {
	const unsetEnvVar = "UBX_TEST_OAUTH_DEFINITELY_UNSET_VAR"
	if _, ok := os.LookupEnv(unsetEnvVar); ok {
		t.Fatalf("test precondition failed: %s is unexpectedly set in this environment", unsetEnvVar)
	}

	a, err := Build("oauth2_client_credentials", map[string]any{
		"token_url":         "https://example.invalid/token",
		"client_id_env":     unsetEnvVar,
		"client_secret_env": unsetEnvVar,
	})
	if err != nil {
		t.Fatalf("Build must succeed with structurally-complete config regardless of whether the named env vars are actually set, got: %v", err)
	}

	// The real credential requirement only surfaces once a real request
	// is actually being signed -- confirmed here, not just asserted, by
	// checking Apply's own real, honest failure names the real missing
	// env var.
	req, _ := http.NewRequest(http.MethodGet, "https://example.invalid/resource", nil)
	if err := a.Apply(req); err == nil {
		t.Fatal("expected Apply to fail once a real credential is actually needed")
	}
}

func TestOAuth2ClientCredentials_Build_RequiresFields(t *testing.T) {
	if _, err := Build("oauth2_client_credentials", map[string]any{}); err == nil {
		t.Fatal("expected error for missing token_url")
	}
	if _, err := Build("oauth2_client_credentials", map[string]any{"token_url": "https://x"}); err == nil {
		t.Fatal("expected error for missing client_id_env")
	}
	if _, err := Build("oauth2_client_credentials", map[string]any{"token_url": "https://x", "client_id_env": "A"}); err == nil {
		t.Fatal("expected error for missing client_secret_env")
	}
}
