package server

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// stateAsMap has always skipped Unknown attributes correctly. Nothing
// proved it.
//
// Before this file, no test anywhere in this repository constructed an
// unknown value at all: `grep -rn "UnknownValue" --include="*_test.go"`
// returned nothing across all three servers. This one and dynserver's
// requestBody were correct by construction, and internal/cloudformation's
// equivalent was not, which is exactly the shape a silent regression
// takes. A create against a real cloud is the first thing that would
// notice, and by then it has failed in front of a user.
//
// Computed attributes are Unknown on every real create: ubx marks any
// Computed attribute absent from config as unknown in the planned state,
// and wire.ToJSON refuses to serialize an unknown. Skipping them per
// attribute, before ToJSON is reached, is the whole discipline.
func TestStateAsMap_SkipsUnknownAndNull(t *testing.T) {
	objType := tftypes.Object{AttributeTypes: map[string]tftypes.Type{
		"queue_name": tftypes.String,
		"queue_url":  tftypes.String,
		"arn":        tftypes.String,
		"delay":      tftypes.Number,
		"tags":       tftypes.Map{ElementType: tftypes.String},
	}}
	v := tftypes.NewValue(objType, map[string]tftypes.Value{
		"queue_name": tftypes.NewValue(tftypes.String, "my-queue"),
		"delay":      tftypes.NewValue(tftypes.Number, 30),
		// Computed: AWS assigns these, so they cannot be known yet.
		"queue_url": tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"arn":       tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		// Optional and left unset.
		"tags": tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
	})

	got, err := stateAsMap(v)
	if err != nil {
		t.Fatalf("stateAsMap refused an unknown attribute: %v", err)
	}
	if got["queue_name"] != "my-queue" {
		t.Errorf("queue_name = %v, want my-queue", got["queue_name"])
	}
	if _, ok := got["delay"]; !ok {
		t.Errorf("a set attribute was dropped: %v", got)
	}
	for _, k := range []string{"queue_url", "arn"} {
		if _, present := got[k]; present {
			t.Errorf("unknown attribute %q reached the request: %v", k, got)
		}
	}
	if _, present := got["tags"]; present {
		t.Errorf("null attribute \"tags\" was sent as a literal null: %v", got)
	}
}

// A nested object carrying an unknown leaf is the case a per-attribute
// skip does NOT cover: the attribute itself is known, so it is converted,
// and wire.ToJSON then meets the unknown one level down. Recording the
// real behaviour rather than asserting a fix, since no caller produces
// this shape today (ubx marks whole attributes unknown, not leaves inside
// a known object) and inventing a policy for it here would be guessing.
func TestStateAsMap_NestedUnknownLeaf_IsRefusedNotSilent(t *testing.T) {
	inner := tftypes.Object{AttributeTypes: map[string]tftypes.Type{
		"target_arn": tftypes.String,
		"max_count":  tftypes.Number,
	}}
	objType := tftypes.Object{AttributeTypes: map[string]tftypes.Type{
		"queue_name":     tftypes.String,
		"redrive_policy": inner,
	}}
	v := tftypes.NewValue(objType, map[string]tftypes.Value{
		"queue_name": tftypes.NewValue(tftypes.String, "my-queue"),
		"redrive_policy": tftypes.NewValue(inner, map[string]tftypes.Value{
			"target_arn": tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
			"max_count":  tftypes.NewValue(tftypes.Number, 5),
		}),
	})

	_, err := stateAsMap(v)
	if err == nil {
		t.Fatal("a nested unknown leaf was silently accepted; if that becomes intended, this test should assert the new behaviour rather than be deleted")
	}
}
