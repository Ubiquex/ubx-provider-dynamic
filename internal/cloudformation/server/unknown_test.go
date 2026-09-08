package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// Computed attributes are Unknown on a real create, and this package used
// to refuse them.
//
// ubx marks every Computed attribute absent from config as Unknown in the
// planned state (its own ctyvalue.go, standard planning semantics), so on
// a create the primary identifier is always unknown: AWS has not assigned
// it yet. desiredStateJSON converted the whole planned object through
// wire.ToJSON in one call and filtered Computed attributes afterwards, one
// step too late. ToJSON recurses into an object and refuses the first
// unknown field it meets, so `ubx ship` could not create any AWS resource
// carrying a computed attribute, which is 96% of them:
//
//	create resource: encode planned state: wire: field "queue_url":
//	wire: cannot serialize an unknown value to JSON
//
// The pre-existing tests in this package all set queue_url and arn to
// null rather than unknown, which is the one shape a real create can
// never produce, so the whole computed path went unexercised. These tests
// exist so it cannot go unexercised again.

func TestDesiredStateJSON_UnknownComputed_OmittedNotRefused(t *testing.T) {
	rt := testResource(t)

	planned := tftypes.NewValue(rt.ObjectType, map[string]tftypes.Value{
		"queue_name": tftypes.NewValue(tftypes.String, "my-queue"),
		// Unknown, not null. AWS assigns these; they cannot be known yet.
		"queue_url":      tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"arn":            tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"delay_seconds":  tftypes.NewValue(tftypes.Number, nil),
		"redrive_policy": tftypes.NewValue(objAttrType(rt, "redrive_policy"), nil),
		"tags":           tftypes.NewValue(objAttrType(rt, "tags"), nil),
	})

	got, err := desiredStateJSON(planned, rt)
	if err != nil {
		t.Fatalf("desiredStateJSON refused an unknown computed attribute: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatalf("DesiredState is not valid JSON: %v (%s)", err, got)
	}
	if m["QueueName"] != "my-queue" {
		t.Errorf("QueueName = %v, want my-queue -- the configured attribute must survive", m["QueueName"])
	}
	for _, k := range []string{"QueueUrl", "Arn"} {
		if _, present := m[k]; present {
			t.Errorf("%s was sent to CCAPI, but it is unknown and AWS is the one that assigns it: %s", k, got)
		}
	}
}

// The update path used the identical wholesale conversion (two calls, on
// prior and planned), so it carried the same defect. Confirmed by
// reverting the fix: this test fails with `encode planned state: wire:
// field "arn": wire: cannot serialize an unknown value to JSON`.
func TestBuildPatch_UnknownComputed_OmittedNotRefused(t *testing.T) {
	rt := testResource(t)

	prior := tftypes.NewValue(rt.ObjectType, map[string]tftypes.Value{
		"queue_name":     tftypes.NewValue(tftypes.String, "my-queue"),
		"queue_url":      tftypes.NewValue(tftypes.String, "https://sqs.example.com/q"),
		"arn":            tftypes.NewValue(tftypes.String, "arn:aws:sqs:::my-queue"),
		"delay_seconds":  tftypes.NewValue(tftypes.Number, 10),
		"redrive_policy": tftypes.NewValue(objAttrType(rt, "redrive_policy"), nil),
		"tags":           tftypes.NewValue(objAttrType(rt, "tags"), nil),
	})
	// A real update: delay_seconds changes, and a computed attribute the
	// user never wrote is unknown again because it is absent from config.
	planned := tftypes.NewValue(rt.ObjectType, map[string]tftypes.Value{
		"queue_name":     tftypes.NewValue(tftypes.String, "my-queue"),
		"queue_url":      tftypes.NewValue(tftypes.String, "https://sqs.example.com/q"),
		"arn":            tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"delay_seconds":  tftypes.NewValue(tftypes.Number, 30),
		"redrive_policy": tftypes.NewValue(objAttrType(rt, "redrive_policy"), nil),
		"tags":           tftypes.NewValue(objAttrType(rt, "tags"), nil),
	})

	got, err := buildPatch(prior, planned, rt)
	if err != nil {
		t.Fatalf("buildPatch refused an unknown computed attribute: %v", err)
	}
	if !strings.Contains(got, "DelaySeconds") {
		t.Errorf("the real change was dropped from the patch: %s", got)
	}
	if strings.Contains(got, "Arn") {
		t.Errorf("an unknown attribute reached the patch document: %s", got)
	}
}

// The null case, kept separate from the unknown case on purpose.
//
// buildPatch detects "unset this attribute" as a planned null against a
// non-null prior, and it can only see that because the key is present in
// the map with a nil value. Skipping nulls alongside unknowns in
// objectAsMap, which is what smithy and dynserver correctly do for
// request bodies, would silently stop emitting every remove operation and
// turn unsetting an attribute into a no-op. Nothing else covers this, and
// it is a quieter failure than the one being fixed.
func TestBuildPatch_PlannedNull_StillEmitsRemove(t *testing.T) {
	rt := testResource(t)

	prior := tftypes.NewValue(rt.ObjectType, map[string]tftypes.Value{
		"queue_name":     tftypes.NewValue(tftypes.String, "my-queue"),
		"queue_url":      tftypes.NewValue(tftypes.String, "https://sqs.example.com/q"),
		"arn":            tftypes.NewValue(tftypes.String, "arn:aws:sqs:::my-queue"),
		"delay_seconds":  tftypes.NewValue(tftypes.Number, 30),
		"redrive_policy": tftypes.NewValue(objAttrType(rt, "redrive_policy"), nil),
		"tags":           tftypes.NewValue(objAttrType(rt, "tags"), nil),
	})
	planned := tftypes.NewValue(rt.ObjectType, map[string]tftypes.Value{
		"queue_name": tftypes.NewValue(tftypes.String, "my-queue"),
		"queue_url":  tftypes.NewValue(tftypes.String, "https://sqs.example.com/q"),
		"arn":        tftypes.NewValue(tftypes.String, "arn:aws:sqs:::my-queue"),
		// Explicitly unset.
		"delay_seconds":  tftypes.NewValue(tftypes.Number, nil),
		"redrive_policy": tftypes.NewValue(objAttrType(rt, "redrive_policy"), nil),
		"tags":           tftypes.NewValue(objAttrType(rt, "tags"), nil),
	})

	got, err := buildPatch(prior, planned, rt)
	if err != nil {
		t.Fatalf("buildPatch: %v", err)
	}
	if !strings.Contains(got, `"remove"`) || !strings.Contains(got, "DelaySeconds") {
		t.Fatalf("unsetting an attribute must still emit a remove operation, got: %s", got)
	}
}
