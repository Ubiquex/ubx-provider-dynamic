package ccapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ubiquex/ubx-provider-dynamic/internal/restexec"
)

// TestCreateResource_RealJSONRPCShape proves the real awsJson1_0 request
// shape against a real, local httptest server (only the SERVER side is
// local -- the actual restexec.Client/ccapi.Client code under test
// performs a real HTTP round trip against it, mirroring wireexec's own
// identical real test discipline): POST "/", X-Amz-Target:
// "CloudApiService.CreateResource", DesiredState carried as a JSON
// string (not a nested object).
func TestCreateResource_RealJSONRPCShape(t *testing.T) {
	var gotTarget, gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTarget = r.Header.Get("X-Amz-Target")
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/x-amz-json-1.0")
		w.Write([]byte(`{"ProgressEvent":{"TypeName":"AWS::SQS::Queue","RequestToken":"tok-1","OperationStatus":"IN_PROGRESS"}}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, nil)
	pe, err := c.CreateResource(context.Background(), "AWS::SQS::Queue", `{"QueueName":"my-queue"}`)
	if err != nil {
		t.Fatalf("CreateResource: %v", err)
	}

	if gotMethod != http.MethodPost || gotPath != "/" {
		t.Fatalf("method/path = %s %s, want POST /", gotMethod, gotPath)
	}
	if gotTarget != "CloudApiService.CreateResource" {
		t.Fatalf("X-Amz-Target = %q, want CloudApiService.CreateResource", gotTarget)
	}
	if gotBody["TypeName"] != "AWS::SQS::Queue" {
		t.Fatalf("TypeName = %v, want AWS::SQS::Queue", gotBody["TypeName"])
	}
	if _, ok := gotBody["DesiredState"].(string); !ok {
		t.Fatalf("DesiredState = %T, want a real JSON string, not a nested object", gotBody["DesiredState"])
	}
	if pe.RequestToken != "tok-1" || pe.OperationStatus != "IN_PROGRESS" {
		t.Fatalf("ProgressEvent = %+v, want RequestToken=tok-1 OperationStatus=IN_PROGRESS", pe)
	}
}

// TestAwaitTerminal_PollsUntilSuccess proves the real, distinct
// GetResourceRequestStatus poll loop -- IN_PROGRESS then SUCCESS, never a
// REST GET to a resource path (this package's own doc comment explains
// why that would be wrong for CCAPI specifically).
func TestAwaitTerminal_PollsUntilSuccess(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		target := r.Header.Get("X-Amz-Target")
		if target != "CloudApiService.GetResourceRequestStatus" {
			t.Errorf("unexpected target %q", target)
		}
		status := "IN_PROGRESS"
		if calls >= 3 {
			status = "SUCCESS"
		}
		w.Header().Set("Content-Type", "application/x-amz-json-1.0")
		w.Write([]byte(`{"ProgressEvent":{"RequestToken":"tok-1","Identifier":"https://sqs.example/q","OperationStatus":"` + status + `"}}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, nil)
	pe, err := c.AwaitTerminal(context.Background(), "tok-1", 10*time.Millisecond, time.Second)
	if err != nil {
		t.Fatalf("AwaitTerminal: %v", err)
	}
	if pe.OperationStatus != "SUCCESS" {
		t.Fatalf("OperationStatus = %q, want SUCCESS", pe.OperationStatus)
	}
	if calls < 3 {
		t.Fatalf("calls = %d, want at least 3 real polls before terminal", calls)
	}
}

// TestAwaitTerminal_TimesOutOnPerpetualInProgress proves the real,
// bounded poll timeout -- an operation that never reaches a terminal
// status is reported as a real error, never silently treated as success.
func TestAwaitTerminal_TimesOutOnPerpetualInProgress(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-amz-json-1.0")
		w.Write([]byte(`{"ProgressEvent":{"RequestToken":"tok-1","OperationStatus":"IN_PROGRESS"}}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, nil)
	_, err := c.AwaitTerminal(context.Background(), "tok-1", 5*time.Millisecond, 20*time.Millisecond)
	if err == nil {
		t.Fatal("expected a real timeout error, got nil")
	}
}

// TestGetResource_DecodesProperties proves a real GetResource round trip,
// including the SigV4-authenticator seam (a fake, non-signing
// Authenticator here -- the real one is internal/auth's own aws_sigv4,
// already covered by that package's own real, live tests).
func TestGetResource_DecodesProperties(t *testing.T) {
	var gotAuthApplied bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Fake-Auth") == "applied" {
			gotAuthApplied = true
		}
		w.Header().Set("Content-Type", "application/x-amz-json-1.0")
		w.Write([]byte(`{"TypeName":"AWS::SQS::Queue","ResourceDescription":{"Identifier":"https://sqs.example/q","Properties":"{\"QueueName\":\"my-queue\"}"}}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, fakeAuth{})
	props, err := c.GetResource(context.Background(), "AWS::SQS::Queue", "https://sqs.example/q")
	if err != nil {
		t.Fatalf("GetResource: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(props, &decoded); err != nil {
		t.Fatalf("decode properties: %v", err)
	}
	if decoded["QueueName"] != "my-queue" {
		t.Fatalf("QueueName = %v, want my-queue", decoded["QueueName"])
	}
	if !gotAuthApplied {
		t.Fatal("expected the real restexec.Authenticator.Apply seam to have run")
	}
}

type fakeAuth struct{}

func (fakeAuth) Apply(req *http.Request) error {
	req.Header.Set("X-Fake-Auth", "applied")
	return nil
}

var _ restexec.Authenticator = fakeAuth{}
