package smithy

import (
	"encoding/json"
	"os"
	"testing"
)

func loadFixture(t *testing.T, path string) *Model {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m Model
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return &m
}

// TestFindService_RealSQSFixture uses a real, previously-fetched SQS
// Smithy model snapshot (testdata/sqs.json, fetched live from
// aws/api-models-aws during this session's own research) to prove the
// real shape decoding matches what the real file actually contains --
// service traits, protocol, and operation/input/output wiring -- not a
// synthetic fixture invented to fit the code.
func TestFindService_RealSQSFixture(t *testing.T) {
	m := loadFixture(t, "testdata/sqs.json")
	svc, err := FindService(m)
	if err != nil {
		t.Fatal(err)
	}
	if svc.ShapeID != "com.amazonaws.sqs#AmazonSQS" {
		t.Fatalf("ShapeID = %q", svc.ShapeID)
	}
	if svc.Traits.EndpointPrefix != "sqs" || svc.Traits.ArnNamespace != "sqs" {
		t.Fatalf("traits = %+v", svc.Traits)
	}
	if svc.Protocol != ProtocolAWSJSON10 {
		t.Fatalf("protocol = %q, want awsJson1_0 (confirmed live against the real service)", svc.Protocol)
	}

	createQueue, ok := m.Shapes["com.amazonaws.sqs#CreateQueue"]
	if !ok || createQueue.Type != "operation" {
		t.Fatalf("expected CreateQueue operation shape, got %+v", createQueue)
	}
	if createQueue.Input == nil || createQueue.Input.Target != "com.amazonaws.sqs#CreateQueueRequest" {
		t.Fatalf("CreateQueue.Input = %+v", createQueue.Input)
	}
	if _, present, err := createQueue.HTTPTrait(); err != nil || present {
		t.Fatalf("expected no smithy.api#http trait on an awsJson1_0 operation, present=%v err=%v", present, err)
	}

	req := m.Shapes["com.amazonaws.sqs#CreateQueueRequest"]
	nameMember, ok := req.Members["QueueName"]
	if !ok {
		t.Fatal("expected QueueName member on CreateQueueRequest")
	}
	if !nameMember.HasTrait("smithy.api#required") {
		t.Fatal("expected QueueName to carry smithy.api#required")
	}
}

func TestFindService_MultipleServiceShapes(t *testing.T) {
	m := &Model{Shapes: map[string]Shape{
		"a#A": {Type: "service"},
		"b#B": {Type: "service"},
	}}
	if _, err := FindService(m); err == nil {
		t.Fatal("expected an error for more than one service shape")
	}
}

func TestFindService_NoServiceShape(t *testing.T) {
	m := &Model{Shapes: map[string]Shape{"a#A": {Type: "structure"}}}
	if _, err := FindService(m); err == nil {
		t.Fatal("expected an error for zero service shapes")
	}
}

// TestLive_FetchRealSQSModel fetches the real, live model directly from
// aws/api-models-aws -- gated behind UBX_LIVE_VALIDATION like every other
// real external call in this repo.
func TestLive_FetchRealSQSModel(t *testing.T) {
	requireLiveSmithy(t)
	m, err := Load("https://raw.githubusercontent.com/aws/api-models-aws/main/models/sqs/service/2012-11-05/sqs-2012-11-05.json")
	if err != nil {
		t.Fatal(err)
	}
	svc, err := FindService(m)
	if err != nil {
		t.Fatal(err)
	}
	if svc.Traits.EndpointPrefix != "sqs" {
		t.Fatalf("unexpected live endpointPrefix: %q", svc.Traits.EndpointPrefix)
	}
	t.Logf("live SQS model: %d shapes, protocol %s", len(m.Shapes), svc.Protocol)
}

func requireLiveSmithy(t *testing.T) {
	t.Helper()
	if os.Getenv("UBX_LIVE_VALIDATION") == "" {
		t.Skip("set UBX_LIVE_VALIDATION=1 to run live validation against real external services")
	}
}
