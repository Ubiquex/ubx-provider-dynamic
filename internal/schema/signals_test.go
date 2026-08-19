package schema

import (
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
)

func TestCollectSignals_LeafEnumAndConstraints(t *testing.T) {
	s := openapi3.NewObjectSchema().
		WithProperty("status", openapi3.NewStringSchema().WithEnum("standard", "fifo")).
		WithProperty("retention", openapi3.NewIntegerSchema().WithMin(60).WithMax(1209600)).
		WithProperty("name", openapi3.NewStringSchema().WithPattern("^[a-z]+$").WithMinLength(1).WithMaxLength(64))

	got := CollectSignals(s)
	if got == nil {
		t.Fatal("CollectSignals returned nil")
	}

	status := got["status"]
	if status == nil || len(status.Enum) != 2 || status.Enum[0] != "standard" || status.Enum[1] != "fifo" {
		t.Fatalf("status signal = %+v", status)
	}

	retention := got["retention"]
	if retention == nil || retention.Minimum == nil || *retention.Minimum != 60 || retention.Maximum == nil || *retention.Maximum != 1209600 {
		t.Fatalf("retention signal = %+v", retention)
	}

	name := got["name"]
	if name == nil || name.Pattern != "^[a-z]+$" || name.MinLength == nil || *name.MinLength != 1 || name.MaxLength == nil || *name.MaxLength != 64 {
		t.Fatalf("name signal = %+v", name)
	}
}

func TestCollectSignals_NoSignal_OmittedNotEmpty(t *testing.T) {
	s := openapi3.NewObjectSchema().
		WithProperty("id", openapi3.NewStringSchema()) // no enum/min/max/pattern at all

	got := CollectSignals(s)
	if _, ok := got["id"]; ok {
		t.Fatalf("a field with no real constraint/enum signal should not appear at all, got %+v", got["id"])
	}
}

func TestCollectSignals_NestedObject_KeyedBySnakeCase(t *testing.T) {
	inner := openapi3.NewObjectSchema().
		WithProperty("Max-Retries", openapi3.NewIntegerSchema().WithMin(0).WithMax(10))
	s := openapi3.NewObjectSchema().WithProperty("retry_policy", inner)

	got := CollectSignals(s)
	rp := got["retry_policy"]
	if rp == nil || rp.Nested == nil {
		t.Fatalf("retry_policy nested signal missing: %+v", got)
	}
	mr := rp.Nested["max_retries"]
	if mr == nil || mr.Minimum == nil || *mr.Minimum != 0 || mr.Maximum == nil || *mr.Maximum != 10 {
		t.Fatalf("nested max_retries signal = %+v (keys: %v)", mr, rp.Nested)
	}
}

func TestCollectSignals_ArrayOfObjects_NestedFromItems(t *testing.T) {
	item := openapi3.NewObjectSchema().
		WithProperty("level", openapi3.NewStringSchema().WithEnum("low", "medium", "high"))
	s := openapi3.NewObjectSchema().
		WithProperty("tiers", openapi3.NewArraySchema().WithItems(item))

	got := CollectSignals(s)
	tiers := got["tiers"]
	if tiers == nil || tiers.Nested == nil {
		t.Fatalf("tiers array-of-object signal missing nested: %+v", got)
	}
	level := tiers.Nested["level"]
	if level == nil || len(level.Enum) != 3 {
		t.Fatalf("tiers[].level signal = %+v", level)
	}
}

func TestCollectSignals_SelfReferential_DoesNotHang(t *testing.T) {
	s := &openapi3.Schema{Properties: openapi3.Schemas{}}
	s.Properties["child"] = openapi3.NewSchemaRef("", s) // real cycle

	done := make(chan map[string]*FieldSignal, 1)
	go func() { done <- CollectSignals(s) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("CollectSignals hung on a self-referential schema")
	}
}

func TestMergeFieldSignal_UnionsEnumAndPrefersSetConstraints(t *testing.T) {
	a := &FieldSignal{Enum: []string{"a", "b"}, Pattern: "^x"}
	b := &FieldSignal{Enum: []string{"b", "c"}, Minimum: floatPtr(5)}

	merged := MergeFieldSignal(a, b)
	if len(merged.Enum) != 3 {
		t.Fatalf("merged enum = %v, want 3 deduplicated values", merged.Enum)
	}
	if merged.Pattern != "^x" {
		t.Fatalf("merged pattern = %q, want %q carried over from a", merged.Pattern, "^x")
	}
	if merged.Minimum == nil || *merged.Minimum != 5 {
		t.Fatalf("merged minimum = %v, want 5 carried over from b", merged.Minimum)
	}
}

func TestMergeSignalMaps_CreateAndReadCombineByKey(t *testing.T) {
	create := map[string]*FieldSignal{"status": {Enum: []string{"pending"}}}
	read := map[string]*FieldSignal{
		"status": {Enum: []string{"pending", "active"}},
		"quota":  {Maximum: floatPtr(100)},
	}

	merged := MergeSignalMaps(create, read)
	if len(merged["status"].Enum) != 2 {
		t.Fatalf("merged status enum = %v", merged["status"].Enum)
	}
	if merged["quota"] == nil || merged["quota"].Maximum == nil || *merged["quota"].Maximum != 100 {
		t.Fatalf("merged quota = %+v, want the read-only field carried through untouched", merged["quota"])
	}
}

func floatPtr(f float64) *float64 { return &f }
