package auth

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestBuildSigV4_RequiresExplicitEnvCredentialsWhenSourceIsEnv(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	if _, err := Build("aws_sigv4", map[string]any{"region": "us-east-1", "service": "execute-api", "credential_source": "env"}); err == nil {
		t.Fatal("expected an error when AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY are unset")
	}
}

func TestBuildSigV4_SignsRealRequestWithEnvCredentials(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIAEXAMPLE00000000")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "0123456789abcdef0123456789abcdef01234567")
	t.Setenv("AWS_SESSION_TOKEN", "")
	a, err := Build("aws_sigv4", map[string]any{"region": "us-east-1", "service": "execute-api", "credential_source": "env"})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://example.invalid/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Host", "example.invalid")
	if err := a.Apply(req); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	auth := req.Header.Get("Authorization")
	if auth == "" {
		t.Fatal("expected a real Authorization header after signing, got none")
	}
	if !strings.Contains(auth, "AWS4-HMAC-SHA256") || !strings.Contains(auth, "Credential=AKIAEXAMPLE00000000/") || !strings.Contains(auth, "us-east-1/execute-api/aws4_request") {
		t.Fatalf("Authorization header does not look like a real SigV4 signature: %q", auth)
	}
	if req.Header.Get("X-Amz-Date") == "" {
		t.Fatal("expected X-Amz-Date to be set by the real signer")
	}
	if req.Header.Get("X-Amz-Content-Sha256") == "" {
		t.Fatal("expected X-Amz-Content-Sha256 to be set")
	}
}

func TestBuildSigV4_RequiresRegionAndService(t *testing.T) {
	if _, err := Build("aws_sigv4", map[string]any{"service": "execute-api"}); err == nil {
		t.Fatal("expected error for missing region")
	}
	if _, err := Build("aws_sigv4", map[string]any{"region": "us-east-1"}); err == nil {
		t.Fatal("expected error for missing service")
	}
}

func TestBuildSigV4_RejectsUnknownCredentialSource(t *testing.T) {
	_, err := Build("aws_sigv4", map[string]any{"region": "us-east-1", "service": "execute-api", "credential_source": "magic"})
	if err == nil {
		t.Fatal("expected error for unrecognized credential_source")
	}
}

// TestGetBodyIsPopulatedForBytesReaderBody verifies, directly against real
// net/http (not cited from docs), the exact claim sigv4.go's own doc
// comment makes: a request built from a *bytes.Reader body (restexec.Do's
// own real shape) gets a working req.GetBody, so a future real SigV4
// Apply can read the body to hash it and then restore it without
// restexec.go itself needing to change at all.
func TestGetBodyIsPopulatedForBytesReaderBody(t *testing.T) {
	payload := []byte(`{"hello":"world"}`)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://example.invalid", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	if req.GetBody == nil {
		t.Fatal("expected GetBody to be populated for a *bytes.Reader body, got nil")
	}

	// Read the body once (simulating a SigV4 signer hashing it)...
	first, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, payload) {
		t.Fatalf("first read: got %q, want %q", first, payload)
	}

	// ...then restore it via GetBody, exactly as a real Apply would.
	fresh, err := req.GetBody()
	if err != nil {
		t.Fatal(err)
	}
	req.Body = fresh
	second, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(second, payload) {
		t.Fatalf("second read after GetBody restore: got %q, want %q", second, payload)
	}
}
