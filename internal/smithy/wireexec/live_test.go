package wireexec

import (
	"context"
	"os"
	"testing"

	"github.com/ubiquex/ubx-provider-dynamic/internal/auth"
	"github.com/ubiquex/ubx-provider-dynamic/internal/restexec"
	"github.com/ubiquex/ubx-provider-dynamic/internal/smithy"
)

func requireLive(t *testing.T) {
	t.Helper()
	if os.Getenv("UBX_LIVE_VALIDATION") == "" {
		t.Skip("set UBX_LIVE_VALIDATION=1 to run live validation against real external services")
	}
	if _, err := os.Stat(os.ExpandEnv("$HOME/.aws/credentials")); err != nil {
		t.Skip("no local ~/.aws/credentials file -- skipping real AWS verification")
	}
}

// TestDoJSONRPC_RealListQueuesAgainstLiveAWS is Checkpoint 2's own real,
// required end-to-end proof: wireexec.Client (this package), a real SigV4
// Authenticator (internal/auth), and restexec.Client's real retry/HTTP
// layer, composed exactly as main.go composes them, performing a real,
// read-only ListQueues against live AWS -- not a mock, not a local server.
// Real, deliberate scope: ListQueues rather than GetQueueAttributes,
// because it needs no pre-existing queue to already exist in the account
// (this session's own account has none, confirmed by the SigV4-only
// verification in internal/auth/sigv4_live_test.go) -- a wrong signature or
// a wrong request shape (target prefix, member names) fails exactly as
// loudly here as it would on any other real operation.
func TestDoJSONRPC_RealListQueuesAgainstLiveAWS(t *testing.T) {
	requireLive(t)

	m := loadFixture(t, "../testdata/sqs.json")
	svc, err := smithy.FindService(m)
	if err != nil {
		t.Fatal(err)
	}

	a, err := auth.Build("aws_sigv4", map[string]any{
		"region":            "us-east-1",
		"service":           "sqs",
		"credential_source": "profile",
	})
	if err != nil {
		t.Fatalf("build aws_sigv4: %v", err)
	}

	c := &Client{
		Rest:         restexec.NewClient("https://sqs.us-east-1.amazonaws.com", a),
		Model:        m,
		Service:      svc,
		TargetPrefix: "AmazonSQS",
	}

	status, decoded, _, err := c.Do(context.Background(), "com.amazonaws.sqs#ListQueues", map[string]any{})
	if err != nil {
		t.Fatalf("real ListQueues against live AWS failed: %v", err)
	}
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	t.Logf("real ListQueues response (re-keyed to snake_case): %#v", decoded)
}
