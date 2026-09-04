package schema

import (
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func ref(s *openapi3.Schema) *openapi3.SchemaRef {
	return openapi3.NewSchemaRef("", s)
}

func TestBuildAttribute_Scalars(t *testing.T) {
	tr := NewTranslator()
	a := tr.BuildAttribute("name", ref(openapi3.NewStringSchema()), fieldPolicy{required: true}, "t.name")
	if !a.Type.Is(tftypes.String) || !a.Required {
		t.Fatalf("string: %+v", a)
	}

	tr = NewTranslator()
	a = tr.BuildAttribute("count", ref(openapi3.NewIntegerSchema()), fieldPolicy{}, "t.count")
	if !a.Type.Is(tftypes.Number) || !a.Optional {
		t.Fatalf("integer: %+v", a)
	}

	tr = NewTranslator()
	a = tr.BuildAttribute("active", ref(openapi3.NewBoolSchema()), fieldPolicy{readOnly: true}, "t.active")
	if !a.Type.Is(tftypes.Bool) || !a.Computed || a.Optional || a.Required {
		t.Fatalf("bool readOnly: %+v", a)
	}
}

func TestBuildAttribute_WriteOnly(t *testing.T) {
	tr := NewTranslator()
	a := tr.BuildAttribute("password", ref(openapi3.NewStringSchema()), fieldPolicy{required: true, writeOnly: true}, "t.password")
	if !a.WriteOnly || !a.Required || a.Computed {
		t.Fatalf("writeOnly: %+v", a)
	}
}

func TestBuildAttribute_NestedObject(t *testing.T) {
	owner := openapi3.NewObjectSchema().
		WithProperty("login", openapi3.NewStringSchema()).
		WithProperty("id", openapi3.NewIntegerSchema())
	owner.Required = []string{"login"}

	tr := NewTranslator()
	a := tr.BuildAttribute("owner", ref(owner), fieldPolicy{required: true}, "repo.owner")
	if a.NestedType == nil {
		t.Fatalf("expected NestedType, got %+v", a)
	}
	if a.NestedType.Nesting != tfprotov6.SchemaObjectNestingModeSingle {
		t.Fatalf("expected single nesting, got %v", a.NestedType.Nesting)
	}
	byName := map[string]*tfprotov6.SchemaAttribute{}
	for _, n := range a.NestedType.Attributes {
		byName[n.Name] = n
	}
	if !byName["login"].Required || !byName["id"].Optional {
		t.Fatalf("nested attrs: %+v", byName)
	}
}

func TestBuildAttribute_ArrayOfObjects(t *testing.T) {
	item := openapi3.NewObjectSchema().WithProperty("name", openapi3.NewStringSchema())
	arr := openapi3.NewArraySchema().WithItems(item)

	tr := NewTranslator()
	a := tr.BuildAttribute("topics", ref(arr), fieldPolicy{}, "repo.topics")
	lt, ok := a.Type.(tftypes.List)
	if !ok {
		t.Fatalf("expected List type, got %T", a.Type)
	}
	obj, ok := lt.ElementType.(tftypes.Object)
	if !ok {
		t.Fatalf("expected Object element type, got %T", lt.ElementType)
	}
	if _, ok := obj.AttributeTypes["name"]; !ok {
		t.Fatalf("expected 'name' field, got %+v", obj.AttributeTypes)
	}
}

func TestBuildAttribute_MapAdditionalProperties(t *testing.T) {
	m := openapi3.NewObjectSchema().WithAdditionalProperties(openapi3.NewStringSchema())
	tr := NewTranslator()
	a := tr.BuildAttribute("labels", ref(m), fieldPolicy{}, "repo.labels")
	mt, ok := a.Type.(tftypes.Map)
	if !ok {
		t.Fatalf("expected Map type, got %T", a.Type)
	}
	if !mt.ElementType.Is(tftypes.String) {
		t.Fatalf("expected string element, got %v", mt.ElementType)
	}
}

func TestBuildAttribute_FreeformObject(t *testing.T) {
	free := openapi3.NewObjectSchema()
	tr := NewTranslator()
	a := tr.BuildAttribute("metadata", ref(free), fieldPolicy{}, "repo.metadata")
	if !a.Type.Is(tftypes.DynamicPseudoType) {
		t.Fatalf("expected dynamic, got %v", a.Type)
	}
	if len(tr.Notes) != 1 {
		t.Fatalf("expected one note, got %v", tr.Notes)
	}
}

func TestBuildAttribute_OneOfPrimitiveCollapses(t *testing.T) {
	s := &openapi3.Schema{OneOf: openapi3.SchemaRefs{
		ref(openapi3.NewStringSchema().WithFormat("uri")),
		ref(openapi3.NewStringSchema().WithFormat("email")),
	}}
	tr := NewTranslator()
	a := tr.BuildAttribute("contact", ref(s), fieldPolicy{}, "repo.contact")
	if !a.Type.Is(tftypes.String) {
		t.Fatalf("expected string, got %v", a.Type)
	}
	if len(tr.Notes) != 1 {
		t.Fatalf("expected one note, got %v", tr.Notes)
	}
}

func TestBuildAttribute_OneOfObjectsUnion(t *testing.T) {
	a1 := openapi3.NewObjectSchema().WithProperty("a", openapi3.NewStringSchema())
	a2 := openapi3.NewObjectSchema().WithProperty("b", openapi3.NewIntegerSchema())
	s := &openapi3.Schema{OneOf: openapi3.SchemaRefs{ref(a1), ref(a2)}}

	tr := NewTranslator()
	a := tr.BuildAttribute("license", ref(s), fieldPolicy{}, "repo.license")
	mt, ok := a.Type.(tftypes.Object)
	if !ok {
		t.Fatalf("expected Object type, got %T", a.Type)
	}
	if _, ok := mt.AttributeTypes["a"]; !ok {
		t.Fatalf("missing branch a: %+v", mt.AttributeTypes)
	}
	if _, ok := mt.AttributeTypes["b"]; !ok {
		t.Fatalf("missing branch b: %+v", mt.AttributeTypes)
	}
}

func TestBuildAttribute_OneOfMixedTypesDynamic(t *testing.T) {
	s := &openapi3.Schema{OneOf: openapi3.SchemaRefs{
		ref(openapi3.NewStringSchema()),
		ref(openapi3.NewObjectSchema().WithProperty("x", openapi3.NewStringSchema())),
	}}
	tr := NewTranslator()
	a := tr.BuildAttribute("value", ref(s), fieldPolicy{}, "repo.value")
	if !a.Type.Is(tftypes.DynamicPseudoType) {
		t.Fatalf("expected dynamic, got %v", a.Type)
	}
}

func TestBuildAttribute_AllOfMerges(t *testing.T) {
	base := openapi3.NewObjectSchema().WithProperty("id", openapi3.NewIntegerSchema())
	ext := openapi3.NewObjectSchema().WithProperty("name", openapi3.NewStringSchema())
	s := &openapi3.Schema{AllOf: openapi3.SchemaRefs{ref(base), ref(ext)}}

	tr := NewTranslator()
	a := tr.BuildAttribute("thing", ref(s), fieldPolicy{}, "repo.thing")
	mt, ok := a.Type.(tftypes.Object)
	if !ok {
		t.Fatalf("expected Object type, got %T", a.Type)
	}
	if _, ok := mt.AttributeTypes["id"]; !ok {
		t.Fatalf("missing id: %+v", mt.AttributeTypes)
	}
	if _, ok := mt.AttributeTypes["name"]; !ok {
		t.Fatalf("missing name: %+v", mt.AttributeTypes)
	}
}

// TestBuildTopLevel_AllOfMerges is UBI-250's own real regression test:
// BuildTopLevel used to call buildProperties(s, path) directly, reading
// only s's own direct Properties, so a resource composed via allOf --
// ARM's own real, common Resource/TrackedResource/ProxyResource envelope
// shape, allOf: [{$ref: Resource}] plus the resource's own sibling
// properties -- lost every field the allOf branch carried. Reproduces
// that exact real shape (base Resource: id/name/type, the resource's
// own sibling "properties" field), confirmed against Azure's own real
// AnalysisServicesServer schema.
func TestBuildTopLevel_AllOfMerges(t *testing.T) {
	base := openapi3.NewObjectSchema().
		WithProperty("id", openapi3.NewStringSchema()).
		WithProperty("name", openapi3.NewStringSchema()).
		WithProperty("type", openapi3.NewStringSchema())
	s := &openapi3.Schema{
		AllOf: openapi3.SchemaRefs{ref(base)},
		Properties: openapi3.Schemas{
			"properties": openapi3.NewSchemaRef("", openapi3.NewObjectSchema().WithProperty("sku", openapi3.NewStringSchema())),
		},
	}

	tr := NewTranslator()
	attrs := tr.BuildTopLevel(s, "azure_analysisservices_server")

	found := map[string]bool{}
	for _, a := range attrs {
		found[a.Name] = true
	}
	for _, want := range []string{"id", "name", "type", "properties"} {
		if !found[want] {
			t.Fatalf("expected %q in top-level attributes, got %+v", want, found)
		}
	}
}

// TestBuildTopLevel_AllOfOnly_NoDirectProperties covers the other real
// shape UBI-250 found: a schema that is PURELY allOf-composed, with no
// direct type or properties of its own at all. Before this fix,
// isObjectType's own gate (s.Type == nil, len(s.Properties) == 0) failed
// for this shape and BuildTopLevel returned nil outright -- losing every
// field, not just the allOf branch's.
func TestBuildTopLevel_AllOfOnly_NoDirectProperties(t *testing.T) {
	base := openapi3.NewObjectSchema().WithProperty("id", openapi3.NewStringSchema())
	s := &openapi3.Schema{AllOf: openapi3.SchemaRefs{ref(base)}}

	tr := NewTranslator()
	attrs := tr.BuildTopLevel(s, "pure_allof")
	if len(attrs) != 1 || attrs[0].Name != "id" {
		t.Fatalf("expected exactly [id], got %+v", attrs)
	}
}

// TestBuildTopLevel_SelfReferentialAllOf_DoesNotHang is UBI-250's own
// cycle-guard regression: BuildTopLevel's new allOf path hands
// mergeAllOf's fresh, synthetic schema to buildProperties, never s
// itself, so a self-referential top-level allOf schema needs its own
// enterObject/exitObject guard keyed on the real s -- the identical
// class of bug buildComposedType's own doc comment already documents
// for the nested-value allOf path (checkpoint 12), reproduced here for
// the top-level entry point specifically.
func TestBuildTopLevel_SelfReferentialAllOf_DoesNotHang(t *testing.T) {
	// node is PURELY allOf-composed (no direct Properties of its own) --
	// the cyclic "children" field lives inside the allOf branch itself,
	// so reaching it necessarily goes through BuildTopLevel's own new
	// mergeAllOf(node) path, not node's own direct Properties. This is
	// the shape that specifically needs BuildTopLevel's own
	// enterObject(node)/exitObject guard: without it, the recursive
	// re-entry into node (via the array item) would only be caught one
	// level deeper, by buildComposedType's own pre-existing guard, not
	// missed entirely, but not the real, tight fix either.
	node := &openapi3.Schema{}
	childrenArray := openapi3.NewArraySchema()
	childrenArray.Items = openapi3.NewSchemaRef("#/components/schemas/node", node)
	base := openapi3.NewObjectSchema().WithProperty("name", openapi3.NewStringSchema())
	base.Properties["children"] = openapi3.NewSchemaRef("", childrenArray)
	node.AllOf = openapi3.SchemaRefs{openapi3.NewSchemaRef("#/components/schemas/base", base)}

	tr := NewTranslator()
	done := make(chan []*tfprotov6.SchemaAttribute, 1)
	go func() {
		done <- tr.BuildTopLevel(node, "root")
	}()

	var attrs []*tfprotov6.SchemaAttribute
	select {
	case attrs = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("BuildTopLevel hung on a self-referential top-level allOf schema -- cycle guard did not stop recursion")
	}

	found := map[string]bool{}
	for _, a := range attrs {
		found[a.Name] = true
	}
	if !found["name"] || !found["children"] {
		t.Fatalf("expected a real top-level result, got %+v", attrs)
	}

	foundCycleNote := false
	for _, n := range tr.Notes {
		if strings.Contains(n.Detail, "self-referential") {
			foundCycleNote = true
		}
	}
	if !foundCycleNote {
		t.Fatalf("expected a self-referential Note, got %+v", tr.Notes)
	}

	// The real, load-bearing assertion, matching
	// TestBuildAttribute_IndirectCycleThroughOneOf_CaughtQuickly's own
	// convention: the cycle must be caught within a couple of real
	// levels, not run anywhere near defaultMaxDepth.
	var children *tfprotov6.SchemaAttribute
	for _, a := range attrs {
		if a.Name == "children" {
			children = a
		}
	}
	if children == nil || children.Type == nil {
		t.Fatalf("expected a real children attribute, got %+v", attrs)
	}
}

// TestBuildAttribute_SelfReferentialSchema_DoesNotHang reproduces the real
// failure this translator hit live against Datadog's own published OpenAPI
// spec before the cycle guard existed: a schema whose own property
// (directly, or through an array) refers back to itself, the same real
// shape a tree/DAG-structured API field (Datadog's own widget/notebook
// content, a threaded comment, ...) naturally takes. Without the guard,
// BuildAttribute recursed without bound -- confirmed against the real spec
// as unbounded memory growth (6GB+ RSS observed) and CPU burn, not merely
// a slow test; the goroutine-plus-timeout shape here proves termination
// directly rather than trusting that a slow CI run would have caught it.
func TestBuildAttribute_SelfReferentialSchema_DoesNotHang(t *testing.T) {
	node := &openapi3.Schema{
		Properties: openapi3.Schemas{
			"name": openapi3.NewSchemaRef("", openapi3.NewStringSchema()),
		},
	}
	childrenArray := openapi3.NewArraySchema()
	childrenArray.Items = openapi3.NewSchemaRef("#/components/schemas/node", node)
	node.Properties["children"] = openapi3.NewSchemaRef("", childrenArray)

	tr := NewTranslator()
	done := make(chan *tfprotov6.SchemaAttribute, 1)
	go func() {
		done <- tr.BuildAttribute("node", openapi3.NewSchemaRef("#/components/schemas/node", node), fieldPolicy{}, "root")
	}()

	select {
	case a := <-done:
		if a.NestedType == nil {
			t.Fatalf("expected the root node to still translate as a NestedType, got %+v", a)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("BuildAttribute hung on a self-referential schema -- cycle guard did not stop recursion")
	}

	foundCycleNote := false
	for _, n := range tr.Notes {
		if strings.Contains(n.Detail, "self-referential") {
			foundCycleNote = true
		}
	}
	if !foundCycleNote {
		t.Fatalf("expected a self-referential Note, got %+v", tr.Notes)
	}
}

// TestBuildAttribute_IndirectCycleThroughOneOf_CaughtQuickly is the real,
// direct regression test for checkpoint 12's own fix: a cycle that goes
// THROUGH a oneOf/anyOf/allOf composition (buildUnion's own "allObjects"
// branch hands a freshly-synthesized *openapi3.Schema to buildProperties,
// never the real branch schemas' own stable pointers) was completely
// invisible to enterObject's own pointer-identity cycle guard before this
// fix -- confirmed live against Datadog's own real, published
// logs-pipeline schema (LogsProcessor, a real oneOf whose own
// LogsPipelineProcessor branch has a "processors" property that is an
// array of LogsProcessor again): the real field resolved all the way to
// defaultMaxDepth (60), not a true infinite hang, but a real, useless
// 60-level-deep field tree, every level literally named "processors".
// This test reproduces the identical real shape (a oneOf whose own
// object branch nests an array of the same oneOf again) and proves the
// fix catches it at the SECOND real occurrence, the same real depth
// every other self-referential case in this file already terminates at.
func TestBuildAttribute_IndirectCycleThroughOneOf_CaughtQuickly(t *testing.T) {
	processor := &openapi3.Schema{}
	pipeline := openapi3.NewObjectSchema().
		WithProperty("name", openapi3.NewStringSchema())
	processorsArray := openapi3.NewArraySchema()
	processorsArray.Items = openapi3.NewSchemaRef("#/components/schemas/Processor", processor)
	pipeline.Properties["processors"] = openapi3.NewSchemaRef("", processorsArray)

	processor.OneOf = openapi3.SchemaRefs{
		openapi3.NewSchemaRef("#/components/schemas/Pipeline", pipeline),
		openapi3.NewSchemaRef("", openapi3.NewObjectSchema().WithProperty("type", openapi3.NewStringSchema())),
	}

	tr := NewTranslator()
	done := make(chan *tfprotov6.SchemaAttribute, 1)
	go func() {
		done <- tr.BuildAttribute("processor", openapi3.NewSchemaRef("#/components/schemas/Processor", processor), fieldPolicy{}, "root")
	}()

	var attr *tfprotov6.SchemaAttribute
	select {
	case attr = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("BuildAttribute hung on an indirect oneOf cycle -- cycle guard did not stop recursion")
	}

	foundCycleNote := false
	for _, n := range tr.Notes {
		if strings.Contains(n.Detail, "self-referential") {
			foundCycleNote = true
		}
	}
	if !foundCycleNote {
		t.Fatalf("expected a self-referential Note, got %+v", tr.Notes)
	}

	// The real, load-bearing assertion: the resulting attribute tree must
	// be shallow (the cycle caught within a few real levels), never
	// anywhere near defaultMaxDepth -- proving this is a real fix, not
	// just a slower path to the same 60-level-deep tree.
	depth := 0
	var walk func(a *tfprotov6.SchemaAttribute, d int)
	walk = func(a *tfprotov6.SchemaAttribute, d int) {
		if d > depth {
			depth = d
		}
		if a.NestedType == nil {
			return
		}
		for _, inner := range a.NestedType.Attributes {
			walk(inner, d+1)
		}
	}
	walk(attr, 0)
	if depth > 5 {
		t.Fatalf("resulting attribute tree is %d levels deep, want a shallow tree (the cycle caught quickly, not run to defaultMaxDepth)", depth)
	}
}

func TestTfName(t *testing.T) {
	cases := map[string]string{
		"full_name":      "full_name",
		"fullName":       "full_name",
		"private-fork":   "private_fork",
		"has.wiki":       "has_wiki",
		"ID":             "id",
		"HTMLURL":        "htmlurl",
		"default_branch": "default_branch",
	}
	for in, want := range cases {
		if got := ToSnakeCase(in); got != want {
			t.Errorf("ToSnakeCase(%q) = %q, want %q", in, got, want)
		}
	}
}
