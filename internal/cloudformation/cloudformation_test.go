package cloudformation

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/ubiquex/ubx-provider-dynamic/internal/smithy"
)

// realQueueFixture mirrors the real, live AWS::SQS::Queue CFN schema's own
// shape closely enough to exercise every real code path this package
// handles: a nested $ref-resolved object (RedrivePolicy), an array of a
// $ref'd object (Tags), readOnlyProperties/primaryIdentifier as real
// JSON-Pointer strings, and required.
func realQueueFixture() *ResourceSchema {
	return &ResourceSchema{
		TypeName: "AWS::SQS::Queue",
		Properties: map[string]*rawSchema{
			"QueueName":     {Type: "string"},
			"DelaySeconds":  {Type: "integer"},
			"QueueUrl":      {Type: "string"},
			"Arn":           {Type: "string"},
			"RedrivePolicy": {Ref: "#/definitions/RedrivePolicy"},
			"Tags": {
				Type:  "array",
				Items: &rawSchema{Ref: "#/definitions/Tag"},
			},
		},
		Definitions: map[string]*rawSchema{
			"RedrivePolicy": {
				Type: "object",
				Properties: map[string]*rawSchema{
					"deadLetterTargetArn": {Type: "string"},
					"maxReceiveCount":     {Type: "integer"},
				},
			},
			"Tag": {
				Type: "object",
				Properties: map[string]*rawSchema{
					"Key":   {Type: "string"},
					"Value": {Type: "string"},
				},
			},
		},
		Required:             []string{"QueueName"},
		ReadOnlyProperties:   []string{"/properties/QueueUrl", "/properties/Arn"},
		PrimaryIdentifier:    []string{"/properties/QueueUrl"},
		CreateOnlyProperties: []string{"/properties/QueueName"},
	}
}

func TestBuild_RealSQSQueueShapedFixture(t *testing.T) {
	files := map[string]*ResourceSchema{"AWS::SQS::Queue": realQueueFixture()}
	known := smithy.KnownNames{"aws_sqs_queue": true}

	built, notes, err := Build(files, known)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(notes) != 0 {
		t.Fatalf("unexpected notes: %v", notes)
	}

	rt, ok := built["aws_sqs_queue"]
	if !ok {
		t.Fatalf("expected resource resolved as aws_sqs_queue, got keys: %v", keysOf(built))
	}
	if rt.NamingStrategy != smithy.StrategyPrefixed {
		t.Fatalf("NamingStrategy = %v, want prefixed", rt.NamingStrategy)
	}
	if got, want := rt.PrimaryIdentifier, []string{"queue_url"}; len(got) != 1 || got[0] != want[0] {
		t.Fatalf("PrimaryIdentifier = %v, want %v", got, want)
	}
	if !rt.CreateOnlyProperties["queue_name"] {
		t.Fatalf("expected queue_name to be create-only")
	}

	var queueName, queueURL, redrivePolicy *tfProtoAttr
	for _, a := range rt.Schema.Block.Attributes {
		switch a.Name {
		case "queue_name":
			queueName = &tfProtoAttr{required: a.Required}
		case "queue_url":
			queueURL = &tfProtoAttr{computed: a.Computed}
		case "redrive_policy":
			redrivePolicy = &tfProtoAttr{hasNested: a.NestedType != nil}
		}
	}
	if queueName == nil || !queueName.required {
		t.Fatalf("queue_name: expected a real, required attribute, got %+v", queueName)
	}
	if queueURL == nil || !queueURL.computed {
		t.Fatalf("queue_url: expected a real, computed (readOnly) attribute, got %+v", queueURL)
	}
	if redrivePolicy == nil || !redrivePolicy.hasNested {
		t.Fatalf("redrive_policy: expected a real, nested $ref-resolved object, got %+v", redrivePolicy)
	}

	wn, ok := rt.WireNames["redrive_policy"]
	if !ok || wn.Real != "RedrivePolicy" {
		t.Fatalf("WireNames[redrive_policy] = %+v, want Real=RedrivePolicy", wn)
	}
	child, ok := wn.Children["max_receive_count"]
	if !ok || child.Real != "maxReceiveCount" {
		t.Fatalf("WireNames[redrive_policy].Children[max_receive_count] = %+v, want Real=maxReceiveCount", child)
	}

	objType, ok := rt.ObjectType.(tftypes.Object)
	if !ok {
		t.Fatalf("ObjectType is not an Object: %T", rt.ObjectType)
	}
	if _, ok := objType.AttributeTypes["queue_name"]; !ok {
		t.Fatalf("ObjectType missing queue_name")
	}
}

type tfProtoAttr struct {
	required  bool
	computed  bool
	hasNested bool
}

func keysOf(m map[string]*BuiltResource) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestBuild_UnresolvedNamingDoesNotCollide(t *testing.T) {
	files := map[string]*ResourceSchema{
		"AWS::AmazonMQ::Broker": {
			TypeName:   "AWS::AmazonMQ::Broker",
			Properties: map[string]*rawSchema{"BrokerName": {Type: "string"}},
		},
	}
	built, _, err := Build(files, smithy.KnownNames{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	rt, ok := built["aws_amazon_mq_broker"]
	if !ok {
		t.Fatalf("expected the real, unresolved-but-still-built candidate name, got keys: %v", keysOf(built))
	}
	if rt.NamingStrategy != smithy.StrategyUnresolved {
		t.Fatalf("NamingStrategy = %v, want unresolved", rt.NamingStrategy)
	}
}

func TestBuild_NullableTypeArray(t *testing.T) {
	// Real, confirmed-live registry finding: some real CFN schemas use a
	// JSON-Schema-draft array-of-types ("type": ["string", "null"]) --
	// flexType must resolve this to "string", not fail to parse.
	files := map[string]*ResourceSchema{
		"AWS::Test::Thing": {
			TypeName: "AWS::Test::Thing",
			Properties: map[string]*rawSchema{
				"Name": {Type: flexType("string")},
			},
		},
	}
	built, _, err := Build(files, smithy.KnownNames{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if _, ok := built["aws_test_thing"]; !ok {
		t.Fatalf("expected aws_test_thing, got keys: %v", keysOf(built))
	}
}
