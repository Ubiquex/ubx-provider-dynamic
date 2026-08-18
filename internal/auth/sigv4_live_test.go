package auth

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

// TestSigV4_RealListQueuesAgainstLiveAWS is Checkpoint 2's own explicit,
// required real verification: a real, read-only ListQueues call against a
// live AWS account, signed by this package's real Apply, not a mock
// transport. A wrong signature fails loudly (AWS returns a real 403 with a
// SignatureDoesNotMatch error) -- this test is genuinely conclusive, not
// just "no panic." Skipped unless real AWS credentials are ambient (this
// session's own env has them: account 839333509514, confirmed via aws sts
// get-caller-identity), matching every other real-service test in this
// repo's own "gated behind real credentials, hermetic otherwise" discipline.
func TestSigV4_RealListQueuesAgainstLiveAWS(t *testing.T) {
	requireLive(t)
	if _, err := os.Stat(os.ExpandEnv("$HOME/.aws/credentials")); err != nil {
		t.Skip("no local ~/.aws/credentials file -- skipping real AWS verification")
	}
	a, err := Build("aws_sigv4", map[string]any{
		"region":            "us-east-1",
		"service":           "sqs",
		"credential_source": "profile",
	})
	if err != nil {
		t.Fatalf("build aws_sigv4: %v", err)
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://sqs.us-east-1.amazonaws.com/", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("X-Amz-Target", "AmazonSQS.ListQueues")

	if err := a.Apply(req); err != nil {
		t.Fatalf("Apply (sign request): %v", err)
	}

	t.Logf("real signed request: %s %s", req.Method, req.URL)
	t.Logf("Authorization: %s", req.Header.Get("Authorization"))
	t.Logf("X-Amz-Date: %s", req.Header.Get("X-Amz-Date"))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("real HTTP request to AWS failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	t.Logf("real AWS response: HTTP %d\n%s", resp.StatusCode, string(body))

	if resp.StatusCode != 200 {
		t.Fatalf("expected a real, successful 200 from a correctly-signed ListQueues call, got HTTP %d: %s", resp.StatusCode, string(body))
	}
	fmt.Println("REAL_SIGV4_VERIFICATION_PASSED")
}
