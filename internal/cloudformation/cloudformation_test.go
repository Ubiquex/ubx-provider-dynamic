package cloudformation

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
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

// TestBuild_OptionalPropertiesAreServerDefaultable is UBI-268.
//
// A CloudFormation property that is neither readOnly nor required must be
// declared Optional AND Computed: a user may set it, and AWS supplies a
// value when they do not. This source declared every one of them
// Optional-alone, which says nothing supplies it, across all 15,967
// attributes in the real registry.
//
// The consequence was destructive rather than cosmetic. A consumer that
// branches on Computed to decide whether an omitted attribute should be
// preserved will read Optional-alone as "the user removed this", and a
// modify that never mentioned an AWS-defaulted setting planned to strip it
// from a live queue.
//
// The three flag combinations are pinned together on purpose, because
// getting the rule right means getting all three right at once: the
// readOnly case must NOT gain Optional, and the required case must not
// gain Computed.
func TestBuild_OptionalPropertiesAreServerDefaultable(t *testing.T) {
	built, _, err := Build(
		map[string]*ResourceSchema{"AWS::SQS::Queue": realQueueFixture()},
		smithy.KnownNames{"aws_sqs_queue": true},
	)
	if err != nil {
		t.Fatal(err)
	}
	rt := built["aws_sqs_queue"]

	byName := map[string]*tfprotov6.SchemaAttribute{}
	for _, a := range rt.Schema.Block.Attributes {
		byName[a.Name] = a
	}

	// Neither readOnly nor required: the case that was wrong.
	for _, name := range []string{"delay_seconds", "redrive_policy", "tags"} {
		a, ok := byName[name]
		if !ok {
			t.Fatalf("%s missing from the built schema", name)
		}
		if !a.Optional || !a.Computed {
			t.Fatalf("%s: optional=%v computed=%v, want both -- a user may set it and AWS supplies it otherwise",
				name, a.Optional, a.Computed)
		}
	}

	// readOnly stays Computed and NOT Optional. This is the stronger,
	// accurate statement that a user cannot set it at all, and the
	// distinction is load-bearing on the consuming side: "the provider
	// owns this outright" and "the provider may supply this" answer
	// different questions.
	for _, name := range []string{"queue_url", "arn"} {
		a, ok := byName[name]
		if !ok {
			t.Fatalf("%s missing from the built schema", name)
		}
		if !a.Computed || a.Optional {
			t.Fatalf("%s: optional=%v computed=%v, want computed-only -- a readOnly property is not something a user may set",
				name, a.Optional, a.Computed)
		}
	}

	// required stays required, and must not pick up Computed.
	if a := byName["queue_name"]; a == nil || !a.Required || a.Computed || a.Optional {
		t.Fatalf("queue_name: %+v, want required-only", a)
	}
}

// TestBuild_NestedOptionalPropertiesAreServerDefaultable covers the
// recursion. 3,489 of the registry's 16,017 top-level properties are
// object- or ref-typed, so a rule that stopped at the top level would
// leave most of the schema saying the same untrue thing one level down.
func TestBuild_NestedOptionalPropertiesAreServerDefaultable(t *testing.T) {
	built, _, err := Build(
		map[string]*ResourceSchema{"AWS::SQS::Queue": realQueueFixture()},
		smithy.KnownNames{"aws_sqs_queue": true},
	)
	if err != nil {
		t.Fatal(err)
	}

	var redrive *tfprotov6.SchemaAttribute
	for _, a := range built["aws_sqs_queue"].Schema.Block.Attributes {
		if a.Name == "redrive_policy" {
			redrive = a
		}
	}
	if redrive == nil || redrive.NestedType == nil {
		t.Fatal("redrive_policy: expected a nested object attribute")
	}
	if len(redrive.NestedType.Attributes) == 0 {
		t.Fatal("redrive_policy: nested attributes missing, so this test proves nothing")
	}
	for _, a := range redrive.NestedType.Attributes {
		if !a.Optional || !a.Computed {
			t.Fatalf("redrive_policy.%s: optional=%v computed=%v, want both -- the rule must recurse",
				a.Name, a.Optional, a.Computed)
		}
	}
}
