package snapshot

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func schemaWith(attrs ...*tfprotov6.SchemaAttribute) *tfprotov6.Schema {
	return &tfprotov6.Schema{Block: &tfprotov6.SchemaBlock{Attributes: attrs}}
}

func attr(name string, typ tftypes.Type, required, optional, computed bool) *tfprotov6.SchemaAttribute {
	return &tfprotov6.SchemaAttribute{Name: name, Type: typ, Required: required, Optional: optional, Computed: computed}
}

func TestDiffLevel_NoChange(t *testing.T) {
	old := map[string]*tfprotov6.Schema{
		"widget": schemaWith(attr("id", tftypes.String, false, false, true), attr("name", tftypes.String, true, false, false)),
	}
	next := map[string]*tfprotov6.Schema{
		"widget": schemaWith(attr("id", tftypes.String, false, false, true), attr("name", tftypes.String, true, false, false)),
	}
	if got := DiffLevel(old, next); got != NoChange {
		t.Errorf("DiffLevel = %s, want none", got)
	}
}

func TestDiffLevel_NewResourceType_Minor(t *testing.T) {
	old := map[string]*tfprotov6.Schema{"widget": schemaWith(attr("id", tftypes.String, false, false, true))}
	next := map[string]*tfprotov6.Schema{
		"widget": schemaWith(attr("id", tftypes.String, false, false, true)),
		"gadget": schemaWith(attr("id", tftypes.String, false, false, true)),
	}
	if got := DiffLevel(old, next); got != Minor {
		t.Errorf("DiffLevel(new resource type) = %s, want minor", got)
	}
}

func TestDiffLevel_RemovedResourceType_Major(t *testing.T) {
	old := map[string]*tfprotov6.Schema{
		"widget": schemaWith(attr("id", tftypes.String, false, false, true)),
		"gadget": schemaWith(attr("id", tftypes.String, false, false, true)),
	}
	next := map[string]*tfprotov6.Schema{"widget": schemaWith(attr("id", tftypes.String, false, false, true))}
	if got := DiffLevel(old, next); got != Major {
		t.Errorf("DiffLevel(removed resource type) = %s, want major", got)
	}
}

func TestDiffLevel_NewField_Minor(t *testing.T) {
	old := map[string]*tfprotov6.Schema{"widget": schemaWith(attr("id", tftypes.String, false, false, true))}
	next := map[string]*tfprotov6.Schema{"widget": schemaWith(
		attr("id", tftypes.String, false, false, true),
		attr("color", tftypes.String, false, true, false),
	)}
	if got := DiffLevel(old, next); got != Minor {
		t.Errorf("DiffLevel(new field) = %s, want minor", got)
	}
}

func TestDiffLevel_RemovedField_Major(t *testing.T) {
	old := map[string]*tfprotov6.Schema{"widget": schemaWith(
		attr("id", tftypes.String, false, false, true),
		attr("color", tftypes.String, false, true, false),
	)}
	next := map[string]*tfprotov6.Schema{"widget": schemaWith(attr("id", tftypes.String, false, false, true))}
	if got := DiffLevel(old, next); got != Major {
		t.Errorf("DiffLevel(removed field) = %s, want major", got)
	}
}

func TestDiffLevel_TypeChanged_Major(t *testing.T) {
	old := map[string]*tfprotov6.Schema{"widget": schemaWith(attr("count", tftypes.Number, false, true, false))}
	next := map[string]*tfprotov6.Schema{"widget": schemaWith(attr("count", tftypes.String, false, true, false))}
	if got := DiffLevel(old, next); got != Major {
		t.Errorf("DiffLevel(type changed) = %s, want major", got)
	}
}

func TestDiffLevel_BecameRequired_Major(t *testing.T) {
	old := map[string]*tfprotov6.Schema{"widget": schemaWith(attr("name", tftypes.String, false, true, false))}
	next := map[string]*tfprotov6.Schema{"widget": schemaWith(attr("name", tftypes.String, true, false, false))}
	if got := DiffLevel(old, next); got != Major {
		t.Errorf("DiffLevel(became required) = %s, want major", got)
	}
}

func TestDiffLevel_StoppedBeingRequired_Minor(t *testing.T) {
	old := map[string]*tfprotov6.Schema{"widget": schemaWith(attr("name", tftypes.String, true, false, false))}
	next := map[string]*tfprotov6.Schema{"widget": schemaWith(attr("name", tftypes.String, false, true, false))}
	if got := DiffLevel(old, next); got != Minor {
		t.Errorf("DiffLevel(stopped being required) = %s, want minor", got)
	}
}

// TestDiffLevel_OptionalToComputed_Major is the founder's own explicit
// addition: a field that could be set (Optional, not Computed) becoming
// server-assigned-only (Computed, not Optional) is real, breaking write-
// capability loss -- classified Major, documented in diffAttribute's own
// doc comment.
func TestDiffLevel_OptionalToComputed_Major(t *testing.T) {
	old := map[string]*tfprotov6.Schema{"widget": schemaWith(attr("region", tftypes.String, false, true, false))}
	next := map[string]*tfprotov6.Schema{"widget": schemaWith(attr("region", tftypes.String, false, false, true))}
	if got := DiffLevel(old, next); got != Major {
		t.Errorf("DiffLevel(optional -> computed) = %s, want major (real write-capability loss)", got)
	}
}

// TestDiffLevel_ComputedToOptional_Minor is the real, symmetric reverse:
// gaining write access on a previously server-only field is purely
// additive.
func TestDiffLevel_ComputedToOptional_Minor(t *testing.T) {
	old := map[string]*tfprotov6.Schema{"widget": schemaWith(attr("region", tftypes.String, false, false, true))}
	next := map[string]*tfprotov6.Schema{"widget": schemaWith(attr("region", tftypes.String, false, true, false))}
	if got := DiffLevel(old, next); got != Minor {
		t.Errorf("DiffLevel(computed -> optional) = %s, want minor (real, purely additive write-capability gain)", got)
	}
}

func TestDiffLevel_DescriptionOnly_Patch(t *testing.T) {
	a := attr("id", tftypes.String, false, false, true)
	a.Description = "The real, original description."
	b := attr("id", tftypes.String, false, false, true)
	b.Description = "A real, corrected description."
	old := map[string]*tfprotov6.Schema{"widget": schemaWith(a)}
	next := map[string]*tfprotov6.Schema{"widget": schemaWith(b)}
	if got := DiffLevel(old, next); got != Patch {
		t.Errorf("DiffLevel(description only) = %s, want patch", got)
	}
}

func TestDiffLevel_NilOld_NeverMajor(t *testing.T) {
	next := map[string]*tfprotov6.Schema{
		"widget": schemaWith(attr("id", tftypes.String, false, false, true)),
		"gadget": schemaWith(attr("id", tftypes.String, true, false, false)),
	}
	if got := DiffLevel(nil, next); got != Minor {
		t.Errorf("DiffLevel(nil old) = %s, want minor -- a first-ever snapshot can never be Major, nothing existed to remove from", got)
	}
}

func TestDiffLevel_TakesHighestAcrossMultipleResources(t *testing.T) {
	old := map[string]*tfprotov6.Schema{
		"widget": schemaWith(attr("id", tftypes.String, false, false, true)),
		"gadget": schemaWith(attr("name", tftypes.String, true, false, false)),
	}
	next := map[string]*tfprotov6.Schema{
		// widget: purely additive (new field) -- would be Minor alone.
		"widget": schemaWith(attr("id", tftypes.String, false, false, true), attr("color", tftypes.String, false, true, false)),
		// gadget: field removed -- Major. The real, combined result must be Major, not Minor.
	}
	if got := DiffLevel(old, next); got != Major {
		t.Errorf("DiffLevel(mixed) = %s, want major (the real, highest level across all resources)", got)
	}
}

func TestNextVersion_NoPriorSnapshot(t *testing.T) {
	got, err := NextVersion("", Major)
	if err != nil {
		t.Fatal(err)
	}
	if got != "1.0.0" {
		t.Errorf("NextVersion(no prior) = %q, want 1.0.0 regardless of level", got)
	}
}

func TestNextVersion_RealBumps(t *testing.T) {
	cases := []struct {
		current string
		level   ChangeLevel
		want    string
	}{
		{"1.2.3", Major, "2.0.0"},
		{"1.2.3", Minor, "1.3.0"},
		{"1.2.3", Patch, "1.2.4"},
		{"1.2.3", NoChange, "1.2.3"},
	}
	for _, c := range cases {
		got, err := NextVersion(c.current, c.level)
		if err != nil {
			t.Fatalf("NextVersion(%q, %s): %v", c.current, c.level, err)
		}
		if got != c.want {
			t.Errorf("NextVersion(%q, %s) = %q, want %q", c.current, c.level, got, c.want)
		}
	}
}

func TestNextVersion_RealInvalidCurrent(t *testing.T) {
	if _, err := NextVersion("not-a-version", Patch); err == nil {
		t.Error("NextVersion accepted a non-semver current version")
	}
}

// TestDiffLevel_NestedTypeAttribute_RealRegressionGuard is the real,
// found-live bug this session caught against Datadog's own real spec:
// tfprotov6.SchemaAttribute.Type is nil for an object-typed attribute
// (NestedType is set instead, the two are mutually exclusive on the real
// struct) -- calling .String() on a nil Type panicked. These four cases
// prove NestedType attributes are diffed with the same real, granular
// precision as everything else, never by crashing and never by a coarse
// "any nested change is Major" shortcut.
func TestDiffLevel_NestedTypeAttribute_RealRegressionGuard(t *testing.T) {
	nested := func(inner ...*tfprotov6.SchemaAttribute) *tfprotov6.SchemaAttribute {
		return &tfprotov6.SchemaAttribute{
			Name:     "config",
			Optional: true,
			NestedType: &tfprotov6.SchemaObject{
				Attributes: inner,
				Nesting:    tfprotov6.SchemaObjectNestingModeSingle,
			},
		}
	}

	t.Run("identical nested attributes: no panic, no change", func(t *testing.T) {
		old := map[string]*tfprotov6.Schema{"widget": schemaWith(nested(attr("host", tftypes.String, true, false, false)))}
		next := map[string]*tfprotov6.Schema{"widget": schemaWith(nested(attr("host", tftypes.String, true, false, false)))}
		if got := DiffLevel(old, next); got != NoChange {
			t.Errorf("DiffLevel(identical nested) = %s, want none", got)
		}
	})

	t.Run("new field inside nested object: minor, not major", func(t *testing.T) {
		old := map[string]*tfprotov6.Schema{"widget": schemaWith(nested(attr("host", tftypes.String, true, false, false)))}
		next := map[string]*tfprotov6.Schema{"widget": schemaWith(nested(
			attr("host", tftypes.String, true, false, false),
			attr("port", tftypes.Number, false, true, false),
		))}
		if got := DiffLevel(old, next); got != Minor {
			t.Errorf("DiffLevel(new nested field) = %s, want minor (real, purely additive, not a coarse major)", got)
		}
	})

	t.Run("removed field inside nested object: major", func(t *testing.T) {
		old := map[string]*tfprotov6.Schema{"widget": schemaWith(nested(
			attr("host", tftypes.String, true, false, false),
			attr("port", tftypes.Number, false, true, false),
		))}
		next := map[string]*tfprotov6.Schema{"widget": schemaWith(nested(attr("host", tftypes.String, true, false, false)))}
		if got := DiffLevel(old, next); got != Major {
			t.Errorf("DiffLevel(removed nested field) = %s, want major", got)
		}
	})

	t.Run("flat type becomes nested type on the same field: major", func(t *testing.T) {
		old := map[string]*tfprotov6.Schema{"widget": schemaWith(attr("config", tftypes.String, false, true, false))}
		next := map[string]*tfprotov6.Schema{"widget": schemaWith(nested(attr("host", tftypes.String, true, false, false)))}
		if got := DiffLevel(old, next); got != Major {
			t.Errorf("DiffLevel(flat -> nested shape change) = %s, want major", got)
		}
	})
}

// TestDiffLevel_FlatObjectType_RealRegressionGuard is UBI-233's own
// regression guard, found live against ubx-schema-github's real spec:
// a flat tftypes.List of tftypes.Object (buildArray's own real shape
// for a nested array not reached through BuildAttribute directly, e.g.
// GitHub's own Budget list) used to be compared by the WHOLE type's own
// .String() form, so a purely additive field added anywhere inside that
// Object changed the string and read as Major -- confirmed live:
// github_ds's own budgets attribute reported Major for GitHub's Budget
// object gaining one new, optional expires_at field, while the sibling
// github (resource) member's own NestedType translation of the
// identical change correctly read Minor. These cases prove a flat
// List-of-Object (and a bare flat Object) are diffed with the same
// real, granular precision NestedType already had, never by a coarse
// "the type's own string changed" shortcut.
func TestDiffLevel_FlatObjectType_RealRegressionGuard(t *testing.T) {
	budgetObject := func(fields map[string]tftypes.Type) tftypes.Type {
		return tftypes.List{ElementType: tftypes.Object{AttributeTypes: fields}}
	}

	t.Run("identical list of object: no change", func(t *testing.T) {
		old := map[string]*tfprotov6.Schema{"widget": schemaWith(attr("budgets", budgetObject(map[string]tftypes.Type{
			"id": tftypes.String, "amount": tftypes.Number,
		}), false, false, true))}
		next := map[string]*tfprotov6.Schema{"widget": schemaWith(attr("budgets", budgetObject(map[string]tftypes.Type{
			"id": tftypes.String, "amount": tftypes.Number,
		}), false, false, true))}
		if got := DiffLevel(old, next); got != NoChange {
			t.Errorf("DiffLevel(identical list of object) = %s, want none", got)
		}
	})

	t.Run("new field inside object inside list: minor, not major (the real UBI-233 case)", func(t *testing.T) {
		old := map[string]*tfprotov6.Schema{"widget": schemaWith(attr("budgets", budgetObject(map[string]tftypes.Type{
			"id": tftypes.String, "amount": tftypes.Number,
		}), false, false, true))}
		next := map[string]*tfprotov6.Schema{"widget": schemaWith(attr("budgets", budgetObject(map[string]tftypes.Type{
			"id": tftypes.String, "amount": tftypes.Number, "expires_at": tftypes.String,
		}), false, false, true))}
		if got := DiffLevel(old, next); got != Minor {
			t.Errorf("DiffLevel(new field inside object inside list) = %s, want minor (real, purely additive, this is the exact live bug)", got)
		}
	})

	t.Run("removed field inside object inside list: major", func(t *testing.T) {
		old := map[string]*tfprotov6.Schema{"widget": schemaWith(attr("budgets", budgetObject(map[string]tftypes.Type{
			"id": tftypes.String, "amount": tftypes.Number,
		}), false, false, true))}
		next := map[string]*tfprotov6.Schema{"widget": schemaWith(attr("budgets", budgetObject(map[string]tftypes.Type{
			"id": tftypes.String,
		}), false, false, true))}
		if got := DiffLevel(old, next); got != Major {
			t.Errorf("DiffLevel(removed field inside object inside list) = %s, want major (real, breaking, must not be missed by the fix)", got)
		}
	})

	t.Run("field type changed inside object inside list: major", func(t *testing.T) {
		old := map[string]*tfprotov6.Schema{"widget": schemaWith(attr("budgets", budgetObject(map[string]tftypes.Type{
			"id": tftypes.String, "amount": tftypes.Number,
		}), false, false, true))}
		next := map[string]*tfprotov6.Schema{"widget": schemaWith(attr("budgets", budgetObject(map[string]tftypes.Type{
			"id": tftypes.String, "amount": tftypes.String,
		}), false, false, true))}
		if got := DiffLevel(old, next); got != Major {
			t.Errorf("DiffLevel(field type changed inside object inside list) = %s, want major (a real, incompatible shape change one level down must still be caught)", got)
		}
	})

	t.Run("list becomes map on the same field: major", func(t *testing.T) {
		old := map[string]*tfprotov6.Schema{"widget": schemaWith(attr("budgets", tftypes.List{ElementType: tftypes.Object{AttributeTypes: map[string]tftypes.Type{"id": tftypes.String}}}, false, false, true))}
		next := map[string]*tfprotov6.Schema{"widget": schemaWith(attr("budgets", tftypes.Map{ElementType: tftypes.Object{AttributeTypes: map[string]tftypes.Type{"id": tftypes.String}}}, false, false, true))}
		if got := DiffLevel(old, next); got != Major {
			t.Errorf("DiffLevel(list -> map on same field) = %s, want major (swapping container kinds is a real, structural break)", got)
		}
	})

	t.Run("new field inside a bare flat object (not wrapped in a list): minor", func(t *testing.T) {
		old := map[string]*tfprotov6.Schema{"widget": schemaWith(attr("meta", tftypes.Object{AttributeTypes: map[string]tftypes.Type{"created_at": tftypes.String}}, false, false, true))}
		next := map[string]*tfprotov6.Schema{"widget": schemaWith(attr("meta", tftypes.Object{AttributeTypes: map[string]tftypes.Type{"created_at": tftypes.String, "updated_at": tftypes.String}}, false, false, true))}
		if got := DiffLevel(old, next); got != Minor {
			t.Errorf("DiffLevel(new field inside bare flat object) = %s, want minor", got)
		}
	})
}
