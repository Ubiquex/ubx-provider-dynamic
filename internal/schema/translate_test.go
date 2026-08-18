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
		if got := tfName(in); got != want {
			t.Errorf("tfName(%q) = %q, want %q", in, got, want)
		}
	}
}
