package auth

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// requireLive gates a test on UBX_LIVE_VALIDATION, the identical
// convention internal/dynserver's own validate_live_test.go establishes
// (hermetic `go test ./...` by default; real external network calls are
// opt-in).
func requireLive(t *testing.T) {
	t.Helper()
	if os.Getenv("UBX_LIVE_VALIDATION") == "" {
		t.Skip("set UBX_LIVE_VALIDATION=1 to run live validation against real external services")
	}
}

func TestAPIKeyHeader_Apply_SingleHeader(t *testing.T) {
	t.Setenv("UBX_TEST_TOKEN", "s3cr3t")
	a, err := Build("api_key_header", map[string]any{
		"headers": []map[string]any{
			{"name": "Authorization", "value_env": "UBX_TEST_TOKEN", "value_prefix": "Bearer "},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, "https://example.invalid", nil)
	if err := a.Apply(req); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer s3cr3t" {
		t.Fatalf("Authorization header = %q", got)
	}
}

func TestAPIKeyHeader_Apply_MultipleHeaders(t *testing.T) {
	t.Setenv("UBX_TEST_API_KEY", "apikey123")
	t.Setenv("UBX_TEST_APP_KEY", "appkey456")
	a, err := Build("api_key_header", map[string]any{
		"headers": []map[string]any{
			{"name": "DD-API-KEY", "value_env": "UBX_TEST_API_KEY"},
			{"name": "DD-APPLICATION-KEY", "value_env": "UBX_TEST_APP_KEY"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, "https://example.invalid", nil)
	if err := a.Apply(req); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("DD-API-KEY"); got != "apikey123" {
		t.Fatalf("DD-API-KEY = %q", got)
	}
	if got := req.Header.Get("DD-APPLICATION-KEY"); got != "appkey456" {
		t.Fatalf("DD-APPLICATION-KEY = %q", got)
	}
}

func TestAPIKeyHeader_Apply_MissingEnvVar(t *testing.T) {
	os.Unsetenv("UBX_TEST_MISSING_TOKEN")
	a, err := Build("api_key_header", map[string]any{
		"headers": []map[string]any{{"name": "Authorization", "value_env": "UBX_TEST_MISSING_TOKEN"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, "https://example.invalid", nil)
	if err := a.Apply(req); err == nil {
		t.Fatal("expected an error when the env var is unset")
	}
}

func TestAPIKeyHeader_Build_RejectsMissingFields(t *testing.T) {
	if _, err := Build("api_key_header", map[string]any{"headers": []map[string]any{}}); err == nil {
		t.Fatal("expected error for zero headers")
	}
	if _, err := Build("api_key_header", map[string]any{"headers": []map[string]any{{"value_env": "X"}}}); err == nil {
		t.Fatal("expected error for a header with no name")
	}
	if _, err := Build("api_key_header", map[string]any{"headers": []map[string]any{{"name": "X-Key"}}}); err == nil {
		t.Fatal("expected error for a header with no value_env (a literal value must never be accepted)")
	}
}

// TestAPIKeyHeader_RealHTTPServer proves the header actually lands on a
// real wire request (net/http/httptest, real TCP, not a mocked
// RoundTripper) -- hermetic, always runs.
func TestAPIKeyHeader_RealHTTPServer(t *testing.T) {
	t.Setenv("UBX_TEST_TOKEN", "wire-check")
	var seen string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	a, err := Build("api_key_header", map[string]any{
		"headers": []map[string]any{{"name": "Authorization", "value_env": "UBX_TEST_TOKEN", "value_prefix": "Bearer "}},
	})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, ts.URL, nil)
	if err := a.Apply(req); err != nil {
		t.Fatal(err)
	}
	if _, err := http.DefaultClient.Do(req); err != nil {
		t.Fatal(err)
	}
	if seen != "Bearer wire-check" {
		t.Fatalf("server observed Authorization = %q", seen)
	}
}

// TestLive_GitHub_APIKeyHeader verifies api_key_header against the real,
// live GitHub API -- a genuine 200 response with real user data, not a
// mock. Requires GITHUB_TOKEN (a real token; this session's own
// verification used `gh auth token`'s real output).
func TestLive_GitHub_APIKeyHeader(t *testing.T) {
	requireLive(t)
	if os.Getenv("GITHUB_TOKEN") == "" {
		t.Skip("GITHUB_TOKEN not set")
	}
	a, err := Build("api_key_header", map[string]any{
		"headers": []map[string]any{{"name": "Authorization", "value_env": "GITHUB_TOKEN", "value_prefix": "Bearer "}},
	})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, "https://api.github.com/user", nil)
	if err := a.Apply(req); err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /user: status %d: %s", resp.StatusCode, body)
	}
	var out struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if out.Login == "" {
		t.Fatal("expected a real, non-empty login in GitHub's own response")
	}
	t.Logf("authenticated live against GitHub as %q", out.Login)
}

// TestLive_Datadog_APIKeyHeader_WireFormat verifies api_key_header's real
// two-header shape against Datadog's own real, live API -- honestly
// scoped: no real Datadog account credentials were available in this
// environment, so this cannot prove a successful authentication. What it
// does prove, against the real service, is that both headers are
// genuinely received and processed as Datadog's own docs describe: a
// deliberately invalid key gets Datadog's own real, structured
// {"errors":["Forbidden"]} 403 response -- confirmed live via a plain curl
// before writing this test -- rather than a 400 complaining about a
// missing/malformed header, which is what a wire-format bug here would
// produce instead.
func TestLive_Datadog_APIKeyHeader_WireFormat(t *testing.T) {
	requireLive(t)
	t.Setenv("UBX_TEST_DD_API_KEY", "invalid-key-no-real-credentials-available")
	t.Setenv("UBX_TEST_DD_APP_KEY", "invalid-app-key-no-real-credentials-available")

	a, err := Build("api_key_header", map[string]any{
		"headers": []map[string]any{
			{"name": "DD-API-KEY", "value_env": "UBX_TEST_DD_API_KEY"},
			{"name": "DD-APPLICATION-KEY", "value_env": "UBX_TEST_DD_APP_KEY"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, "https://api.datadoghq.com/api/v1/validate", nil)
	if err := a.Apply(req); err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected Datadog's own real 403 for an invalid key (proving the headers were received), got status %d: %s", resp.StatusCode, body)
	}
	var out struct {
		Errors []string `json:"errors"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("expected Datadog's own real structured error body, got: %s", body)
	}
	if len(out.Errors) == 0 {
		t.Fatalf("expected a non-empty errors list, got: %s", body)
	}
	t.Logf("Datadog's real API responded to the two-header request with: %v (proves wire format, not authentication -- no real credentials available)", out.Errors)
}
