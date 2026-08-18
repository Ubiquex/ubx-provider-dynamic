package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/ubiquex/ubx-provider-dynamic/internal/restexec"
	"github.com/ubiquex/ubx-provider-dynamic/internal/smithy"
	"github.com/ubiquex/ubx-provider-dynamic/internal/smithy/wireexec"
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

// objectValue builds a tftypes.Value of ty (an Object type) from fields
// (string-valued only, sufficient for this test's own real purposes),
// every other attribute left explicitly null -- a real ReadResource
// request's own CurrentState always carries every schema attribute, known
// or null, never a partial object.
func objectValue(ty tftypes.Object, fields map[string]string) tftypes.Value {
	vals := make(map[string]tftypes.Value, len(ty.AttributeTypes))
	for name, attrType := range ty.AttributeTypes {
		if s, ok := fields[name]; ok {
			vals[name] = tftypes.NewValue(attrType, s)
		} else {
			vals[name] = tftypes.NewValue(attrType, nil)
		}
	}
	return tftypes.NewValue(ty, vals)
}

// TestServer_RealSQSReadResource is Checkpoint 2's own end-to-end proof,
// short of live AWS itself (that's live_test.go's job): the real, compiled
// pipeline -- Build's own real discovery+translation (Checkpoint 1,
// unchanged), this server's own real ReadResource RPC, wireexec's real
// awsJson1_0 encoding, and wire.go's real JSON<->tftypes conversion -- all
// composed exactly as main.go composes them, driven against a real local
// HTTP server standing in only for AWS's own network endpoint (not for any
// of this repo's own code, matching "real tests, no transport mocking").
func TestServer_RealSQSReadResource(t *testing.T) {
	var gotTarget string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTarget = r.Header.Get("X-Amz-Target")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/x-amz-json-1.0")
		w.Write([]byte(`{"Attributes":{"QueueArn":"arn:aws:sqs:us-east-1:123456789012:my-queue"}}`))
	}))
	defer srv.Close()

	m := loadFixture(t, "../testdata/sqs.json")
	built, notes, err := smithy.Build(m, "aws", smithy.DefaultKnownNames())
	if err != nil {
		t.Fatalf("Build: %v (notes: %v)", err, notes)
	}
	queue, ok := built["aws_sqs_queue"]
	if !ok {
		t.Fatalf("expected aws_sqs_queue in built resources")
	}

	svc, err := smithy.FindService(m)
	if err != nil {
		t.Fatal(err)
	}
	wc := &wireexec.Client{
		Rest:         restexec.NewClient(srv.URL, nil),
		Model:        m,
		Service:      svc,
		TargetPrefix: "AmazonSQS",
	}
	s := &Server{ProviderName: "aws", Resources: built, Model: m, Wire: wc}

	current := objectValue(queue.ObjectType, map[string]string{
		"queue_url": "https://sqs.us-east-1.amazonaws.com/123456789012/my-queue",
	})
	currentState, err := tfprotov6.NewDynamicValue(queue.ObjectType, current)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := s.ReadResource(context.Background(), &tfprotov6.ReadResourceRequest{
		TypeName:     "aws_sqs_queue",
		CurrentState: &currentState,
	})
	if err != nil {
		t.Fatalf("ReadResource: %v", err)
	}
	if len(resp.Diagnostics) > 0 {
		t.Fatalf("ReadResource diagnostics: %+v", resp.Diagnostics)
	}
	if resp.NewState == nil {
		t.Fatal("expected a NewState, got nil (resource treated as not-found?)")
	}

	if gotTarget != "AmazonSQS.GetQueueAttributes" {
		t.Fatalf("X-Amz-Target = %q, want AmazonSQS.GetQueueAttributes", gotTarget)
	}
	if gotBody["QueueUrl"] == nil {
		t.Fatalf("expected the real request to carry QueueUrl (carried from current state via the read op's own input members), got %+v", gotBody)
	}

	newVal, err := resp.NewState.Unmarshal(queue.ObjectType)
	if err != nil {
		t.Fatal(err)
	}
	var asMap map[string]tftypes.Value
	if err := newVal.As(&asMap); err != nil {
		t.Fatal(err)
	}
	if asMap["queue_url"].IsNull() {
		t.Fatal("expected queue_url to be carried forward from current state (GetQueueAttributes' own real response never echoes it back), got null")
	}
	var queueURL string
	if err := asMap["queue_url"].As(&queueURL); err != nil {
		t.Fatal(err)
	}
	if queueURL != "https://sqs.us-east-1.amazonaws.com/123456789012/my-queue" {
		t.Fatalf("queue_url = %q, want the carried-forward original value", queueURL)
	}
}

// TestServer_UnknownResourceType proves the real "unknown type" refusal
// every RPC method shares.
func TestServer_UnknownResourceType(t *testing.T) {
	s := &Server{Resources: map[string]*smithy.BuiltResource{}}
	resp, err := s.ValidateResourceConfig(context.Background(), &tfprotov6.ValidateResourceConfigRequest{TypeName: "aws_does_not_exist"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Diagnostics) == 0 {
		t.Fatal("expected a diagnostic for an unknown resource type")
	}
}
