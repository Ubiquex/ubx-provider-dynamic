package auth

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"
)

func TestBuildSigV4Stub_ValidatesAndRefusesToApply(t *testing.T) {
	a, err := Build("aws_sigv4", map[string]any{"region": "us-east-1", "service": "execute-api", "credential_source": "env"})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	req, _ := http.NewRequest(http.MethodGet, "https://example.invalid", nil)
	if err := a.Apply(req); err == nil {
		t.Fatal("expected aws_sigv4's own Apply to refuse (not yet implemented), got nil error")
	}
}

func TestBuildSigV4Stub_RequiresRegionAndService(t *testing.T) {
	if _, err := Build("aws_sigv4", map[string]any{"service": "execute-api"}); err == nil {
		t.Fatal("expected error for missing region")
	}
	if _, err := Build("aws_sigv4", map[string]any{"region": "us-east-1"}); err == nil {
		t.Fatal("expected error for missing service")
	}
}

func TestBuildSigV4Stub_RejectsUnknownCredentialSource(t *testing.T) {
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
