package schema

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
)

func attr(name string, opts func(*tfprotov6.SchemaAttribute)) *tfprotov6.SchemaAttribute {
	a := &tfprotov6.SchemaAttribute{Name: name}
	if opts != nil {
		opts(a)
	}
	return a
}

func nested(attrs ...*tfprotov6.SchemaAttribute) *tfprotov6.SchemaObject {
	return &tfprotov6.SchemaObject{Attributes: attrs, Nesting: tfprotov6.SchemaObjectNestingModeSingle}
}

func findAttr(attrs []*tfprotov6.SchemaAttribute, name string) *tfprotov6.SchemaAttribute {
	for _, a := range attrs {
		if a.Name == name {
			return a
		}
	}
	return nil
}

// UBI-248: a nested object present on both sides of a create/read pair
// (Kubernetes's own real shape -- metadata is settable on create via
// name/namespace/labels, but its uid/resource_version/creation_timestamp
// children are readOnly only in the read-response schema) used to keep
// create's own NestedType wholesale on merge, so a child field's own
// read-only signal from the read side was never consulted at all -- it
// stayed whatever create's own translation of that same child produced
// (Optional, not Computed, since create's own schema for it carries no
// readOnly marker). Confirmed against the real bug shape, not a
// hypothetical one.
func TestMergeResourceAttributes_NestedReadOnlyChild(t *testing.T) {
	// create side: metadata accepts name and uid, neither marked readOnly
	// in create's own schema (a real create request body can define the
	// same field names as the response without repeating readOnly).
	createMeta := attr("metadata", func(a *tfprotov6.SchemaAttribute) {
		a.Optional = true
		a.NestedType = nested(
			attr("name", func(a *tfprotov6.SchemaAttribute) { a.Optional = true }),
			attr("uid", func(a *tfprotov6.SchemaAttribute) { a.Optional = true }),
		)
	})
	// read side: metadata itself is an ordinary optional object in the
	// response too (not readOnly as a whole -- real callers can specify
	// name/namespace on create), its own uid child is genuinely readOnly.
	readMeta := attr("metadata", func(a *tfprotov6.SchemaAttribute) {
		a.Optional = true
		a.NestedType = nested(
			attr("name", func(a *tfprotov6.SchemaAttribute) { a.Optional = true }),
			attr("uid", func(a *tfprotov6.SchemaAttribute) { a.Computed = true }),
		)
	})

	merged := MergeResourceAttributes(
		[]*tfprotov6.SchemaAttribute{createMeta},
		[]*tfprotov6.SchemaAttribute{readMeta},
	)

	metadata := findAttr(merged, "metadata")
	if metadata == nil {
		t.Fatalf("metadata missing from merged result")
	}
	if !metadata.Optional || !metadata.Computed {
		t.Fatalf("metadata itself: got Optional=%v Computed=%v, want both true", metadata.Optional, metadata.Computed)
	}
	if metadata.NestedType == nil {
		t.Fatalf("metadata.NestedType is nil, nested children lost entirely")
	}

	uid := findAttr(metadata.NestedType.Attributes, "uid")
	if uid == nil {
		t.Fatalf("metadata.uid missing from merged nested attributes")
	}
	if !uid.Computed || uid.Optional || uid.Required {
		t.Fatalf("metadata.uid: got Computed=%v Optional=%v Required=%v, want Computed=true, Optional=false, Required=false",
			uid.Computed, uid.Optional, uid.Required)
	}

	// name is present on both sides, neither marks it readOnly -- the
	// existing, unchanged "both sides, not required" rule applies: the
	// user may set it, or leave it for the server to default. Same
	// pattern the already-working top-level `status` field has.
	name := findAttr(metadata.NestedType.Attributes, "name")
	if name == nil {
		t.Fatalf("metadata.name missing from merged nested attributes")
	}
	if !name.Optional || !name.Computed {
		t.Fatalf("metadata.name: got Optional=%v Computed=%v, want both true",
			name.Optional, name.Computed)
	}
}

// A field whose NestedType exists only on one side (an inconsistent or
// unusual schema shape) must not attempt to recurse -- falls back to the
// existing, pre-fix behavior of keeping create's own version wholesale,
// rather than guessing at how to reconcile a shape mismatch.
func TestMergeResourceAttributes_AsymmetricNestedType_NoRecursion(t *testing.T) {
	createField := attr("thing", func(a *tfprotov6.SchemaAttribute) {
		a.Optional = true
		a.NestedType = nested(attr("inner", func(a *tfprotov6.SchemaAttribute) { a.Optional = true }))
	})
	readField := attr("thing", func(a *tfprotov6.SchemaAttribute) {
		a.Computed = true
		// no NestedType on the read side -- shape mismatch
	})

	merged := MergeResourceAttributes(
		[]*tfprotov6.SchemaAttribute{createField},
		[]*tfprotov6.SchemaAttribute{readField},
	)

	thing := findAttr(merged, "thing")
	if thing == nil {
		t.Fatalf("thing missing from merged result")
	}
	if thing.NestedType == nil {
		t.Fatalf("thing.NestedType lost entirely -- should have kept create's own version unchanged")
	}
	if findAttr(thing.NestedType.Attributes, "inner") == nil {
		t.Fatalf("thing.NestedType.Attributes lost create's own inner field")
	}
}
