package dynserver

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// requestBody has always skipped Unknown attributes correctly. Nothing
// proved it.
//
// Before this file, no test anywhere in this repository constructed an
// unknown value at all: `grep -rn "UnknownValue" --include="*_test.go"`
// returned nothing across all three servers. This one and smithy's
// stateAsMap were correct by construction, and internal/cloudformation's
// equivalent was not, which is exactly the shape a silent regression
// takes. A create against a real cloud is the first thing that would
// notice, and by then it has failed in front of a user.
//
// Computed attributes are Unknown on every real create: ubx marks any
// Computed attribute absent from config as unknown in the planned state,
// and wire.ToJSON refuses to serialize an unknown. Skipping them per
// attribute, before ToJSON is reached, is the whole discipline.
func TestRequestBody_SkipsUnknownAndNull(t *testing.T) {
	objType := tftypes.Object{AttributeTypes: map[string]tftypes.Type{
		"name":       tftypes.String,
		"id":         tftypes.String,
		"created_at": tftypes.String,
		"size":       tftypes.Number,
		"region":     tftypes.String,
	}}
	v := tftypes.NewValue(objType, map[string]tftypes.Value{
		"name": tftypes.NewValue(tftypes.String, "widget"),
		"size": tftypes.NewValue(tftypes.Number, 3),
		// Computed and server-filled: unknown until the API answers.
		"id":         tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		"created_at": tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
		// Optional and left unset.
		"region": tftypes.NewValue(tftypes.String, nil),
	})

	got, err := requestBody(v, nil)
	if err != nil {
		t.Fatalf("requestBody refused an unknown attribute: %v", err)
	}
	if got["name"] != "widget" {
		t.Errorf("name = %v, want widget", got["name"])
	}
	if _, ok := got["size"]; !ok {
		t.Errorf("a set attribute was dropped: %v", got)
	}
	for _, k := range []string{"id", "created_at"} {
		if _, present := got[k]; present {
			t.Errorf("unknown attribute %q reached the request body: %v", k, got)
		}
	}
	if _, present := got["region"]; present {
		t.Errorf("null attribute \"region\" was sent as a literal null: %v", got)
	}
}

// An unknown inside the exclude set must still be skipped, and must not
// error on its way past the exclusion check.
func TestRequestBody_UnknownInExcludeSet(t *testing.T) {
	objType := tftypes.Object{AttributeTypes: map[string]tftypes.Type{
		"name": tftypes.String,
		"id":   tftypes.String,
	}}
	v := tftypes.NewValue(objType, map[string]tftypes.Value{
		"name": tftypes.NewValue(tftypes.String, "widget"),
		"id":   tftypes.NewValue(tftypes.String, tftypes.UnknownValue),
	})

	got, err := requestBody(v, []string{"id"})
	if err != nil {
		t.Fatalf("requestBody: %v", err)
	}
	if _, present := got["id"]; present {
		t.Errorf("excluded unknown path parameter reached the body: %v", got)
	}
}
