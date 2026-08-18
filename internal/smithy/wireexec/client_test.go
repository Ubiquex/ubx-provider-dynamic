package wireexec

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/ubiquex/ubx-provider-dynamic/internal/restexec"
	"github.com/ubiquex/ubx-provider-dynamic/internal/smithy"
)

func loadFixture(t *testing.T, path string) *smithy.Model {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	var m smithy.Model
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parse fixture %s: %v", path, err)
	}
	return &m
}

// TestDoJSONRPC_RealSQSCreateQueue proves the real awsJson1_0 request shape
// against SQS's own real model: POST "/", X-Amz-Target header built from
// the real, explicit TargetPrefix config, and a flat JSON body keyed by the
// real Smithy member names (never the caller's own snake_case attribute
// names) -- a real, local httptest server, not a mocked restexec.Client
// (CLAUDE.md's own "real tests, no transport mocking" rule: only the
// SERVER side is local here, the actual restexec.Client/wireexec.Client
// code under test performs a real HTTP round trip against it).
func TestDoJSONRPC_RealSQSCreateQueue(t *testing.T) {
	var gotTarget, gotContentType, gotMethod, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTarget = r.Header.Get("X-Amz-Target")
		gotContentType = r.Header.Get("Content-Type")
		gotMethod = r.Method
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/x-amz-json-1.0")
		w.Write([]byte(`{"QueueUrl":"https://sqs.us-east-1.amazonaws.com/123456789012/my-queue"}`))
	}))
	defer srv.Close()

	m := loadFixture(t, "../testdata/sqs.json")
	svc, err := smithy.FindService(m)
	if err != nil {
		t.Fatal(err)
	}

	c := &Client{
		Rest:         restexec.NewClient(srv.URL, nil),
		Model:        m,
		Service:      svc,
		TargetPrefix: "AmazonSQS",
	}

	status, decoded, _, err := c.Do(context.Background(), "com.amazonaws.sqs#CreateQueue", map[string]any{
		"queue_name": "my-queue",
	})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if status != 200 {
		t.Fatalf("status = %d", status)
	}
	if gotMethod != "POST" || gotPath != "/" {
		t.Fatalf("expected POST /, got %s %s", gotMethod, gotPath)
	}
	if gotTarget != "AmazonSQS.CreateQueue" {
		t.Fatalf("X-Amz-Target = %q, want AmazonSQS.CreateQueue", gotTarget)
	}
	if gotContentType != "application/x-amz-json-1.0" {
		t.Fatalf("Content-Type = %q", gotContentType)
	}
	if gotBody["QueueName"] != "my-queue" {
		t.Fatalf("expected real Smithy member name QueueName in request body, got %+v", gotBody)
	}
	m2, ok := decoded.(map[string]any)
	if !ok {
		t.Fatalf("expected decoded map, got %T", decoded)
	}
	if m2["queue_url"] == nil {
		t.Fatalf("expected response re-keyed to snake_case queue_url, got %+v", m2)
	}
}

// TestDoJSONRPC_RequiresTargetPrefix proves the real, deliberate refusal
// when target_prefix is missing -- the real, confirmed finding that AWS's
// own Smithy model carries no such field, so a misconfigured provider fails
// loudly rather than sending a request AWS would reject as
// UnknownOperationException anyway.
func TestDoJSONRPC_RequiresTargetPrefix(t *testing.T) {
	m := loadFixture(t, "../testdata/sqs.json")
	svc, err := smithy.FindService(m)
	if err != nil {
		t.Fatal(err)
	}
	c := &Client{Rest: restexec.NewClient("https://example.invalid", nil), Model: m, Service: svc}
	if _, _, _, err := c.Do(context.Background(), "com.amazonaws.sqs#CreateQueue", map[string]any{"queue_name": "x"}); err == nil {
		t.Fatal("expected an error when TargetPrefix is unset")
	}
}

// TestDoRestJSON_RealLambdaGetFunction proves the restJson1 path -- method
// and URI templating (httpLabel substitution) from the real Lambda model's
// own smithy.api#http trait, against a real local server.
func TestDoRestJSON_RealLambdaGetFunction(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"Configuration":{"FunctionName":"my-func","FunctionArn":"arn:aws:lambda:us-east-1:123456789012:function:my-func"}}`))
	}))
	defer srv.Close()

	m := loadFixture(t, "../testdata/lambda.json")
	svc, err := smithy.FindService(m)
	if err != nil {
		t.Fatal(err)
	}
	if svc.Protocol != smithy.ProtocolRestJSON1 {
		t.Fatalf("expected Lambda to be restJson1, got %q", svc.Protocol)
	}

	c := &Client{Rest: restexec.NewClient(srv.URL, nil), Model: m, Service: svc}
	status, decoded, _, err := c.Do(context.Background(), "com.amazonaws.lambda#GetFunction", map[string]any{
		"function_name": "my-func",
	})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if status != 200 {
		t.Fatalf("status = %d", status)
	}
	if gotMethod != "GET" {
		t.Fatalf("method = %q, want GET", gotMethod)
	}
	if !strings.Contains(gotPath, "my-func") {
		t.Fatalf("expected the real httpLabel-substituted path to contain the function name, got %q", gotPath)
	}
	m2, ok := decoded.(map[string]any)
	if !ok || m2["configuration"] == nil {
		t.Fatalf("expected response re-keyed to snake_case configuration, got %+v (%T)", decoded, decoded)
	}
}

// TestDoQuery_RealSNSListTopics proves the awsQuery path against SNS's own
// real model -- Action/Version form-encoded body, real
// "<OperationName>Result" XML unwrapping, confirmed against the exact real
// shape aws-cli's own --debug trace showed this session
// (Action=ListTopics&Version=2010-03-31).
func TestDoQuery_RealSNSListTopics(t *testing.T) {
	var gotContentType, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		w.Header().Set("Content-Type", "text/xml")
		w.Write([]byte(`<ListTopicsResponse><ListTopicsResult><Topics><member><TopicArn>arn:aws:sns:us-east-1:123456789012:my-topic</TopicArn></member></Topics></ListTopicsResult></ListTopicsResponse>`))
	}))
	defer srv.Close()

	m := loadFixture(t, "../testdata/sns.json")
	svc, err := smithy.FindService(m)
	if err != nil {
		t.Fatal(err)
	}
	if svc.Protocol != smithy.ProtocolAWSQuery {
		t.Fatalf("expected SNS to be awsQuery, got %q", svc.Protocol)
	}

	c := &Client{Rest: restexec.NewClient(srv.URL, nil), Model: m, Service: svc}
	status, decoded, _, err := c.Do(context.Background(), "com.amazonaws.sns#ListTopics", map[string]any{})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if status != 200 {
		t.Fatalf("status = %d", status)
	}
	if gotContentType != "application/x-www-form-urlencoded" {
		t.Fatalf("Content-Type = %q", gotContentType)
	}
	if !strings.Contains(gotBody, "Action=ListTopics") || !strings.Contains(gotBody, "Version=") {
		t.Fatalf("expected real Action/Version form fields, got %q", gotBody)
	}
	m2, ok := decoded.(map[string]any)
	if !ok {
		t.Fatalf("expected decoded map, got %T", decoded)
	}
	if m2["topics"] == nil {
		t.Fatalf("expected the real ListTopicsResult unwrapped and re-keyed to snake_case, got %+v", m2)
	}
}
