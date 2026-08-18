package smithy

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	uschema "github.com/ubiquex/ubx-provider-dynamic/internal/schema"
)

// TestConvert_RealSQSCreateQueueRequest proves the adapter against the
// real SQS fixture, then feeds the result straight into Phase 1's own,
// completely unchanged schema.Translator -- proving the reuse claim
// directly, not just asserting the intermediate openapi3.Schema shape.
func TestConvert_RealSQSCreateQueueRequest(t *testing.T) {
	m := loadFixture(t, "testdata/sqs.json")
	c := NewConverter(m)

	s, err := c.Convert("com.amazonaws.sqs#CreateQueueRequest")
	if err != nil {
		t.Fatal(err)
	}
	if !s.Type.Is("object") {
		t.Fatalf("expected object, got %v", s.Type)
	}
	if len(s.Required) != 1 || s.Required[0] != "QueueName" {
		t.Fatalf("expected QueueName required (from the member-level smithy.api#required trait), got %v", s.Required)
	}
	if _, ok := s.Properties["Attributes"]; !ok {
		t.Fatalf("expected an Attributes property, got %v", s.Properties)
	}

	tr := uschema.NewTranslator()
	attrs := tr.BuildTopLevel(s, "sqs_queue.create")
	byName := map[string]*tfprotov6.SchemaAttribute{}
	for _, a := range attrs {
		byName[a.Name] = a
	}
	if byName["queue_name"] == nil || !byName["queue_name"].Required {
		t.Fatalf("expected queue_name to be Required after translation, got %+v", byName["queue_name"])
	}
	if byName["attributes"] == nil {
		t.Fatalf("expected an attributes attribute, got %v", byName)
	}
}

// TestConvert_RealSQSAttributesMap proves the map->additionalProperties
// path against SQS's own real QueueAttributeMap shape, and that it
// reaches Phase 1's translator as a real tftypes.Map.
func TestConvert_RealSQSAttributesMap(t *testing.T) {
	m := loadFixture(t, "testdata/sqs.json")
	c := NewConverter(m)

	s, err := c.Convert("com.amazonaws.sqs#QueueAttributeMap")
	if err != nil {
		t.Fatal(err)
	}
	if !s.Type.Is("object") || s.AdditionalProperties.Schema == nil {
		t.Fatalf("expected an additionalProperties object, got %+v", s)
	}

	wrapper := &openapi3.Schema{
		Type:       &openapi3.Types{"object"},
		Properties: openapi3.Schemas{"attrs": openapi3.NewSchemaRef("", s)},
	}
	tr := uschema.NewTranslator()
	attrs := tr.BuildTopLevel(wrapper, "sqs.wrapper")
	var found *tfprotov6.SchemaAttribute
	for _, a := range attrs {
		if a.Name == "attrs" {
			found = a
		}
	}
	if found == nil {
		t.Fatalf("expected an attrs attribute, got %v", attrs)
	}
	if _, ok := found.Type.(tftypes.Map); !ok {
		t.Fatalf("expected a tftypes.Map, got %T (%+v)", found.Type, found)
	}
}

// TestConvert_SelfReferentialShape_DoesNotHang proves the memoization-
// before-recursion discipline actually terminates on a genuinely
// self-referential Smithy shape, mirroring internal/schema's own
// identical real test for the OpenAPI side (found live against Datadog's
// spec in Phase 1) -- here, a synthetic but structurally real shape
// (a Smithy structure whose own member targets itself), since no small,
// fast-to-fetch real AWS model conveniently exercises this for a unit
// test's own purposes.
func TestConvert_SelfReferentialShape_DoesNotHang(t *testing.T) {
	m := &Model{
		SmithyVersion: "2.0",
		Shapes: map[string]Shape{
			"x#Node": {
				Type: "structure",
				Members: map[string]Member{
					"Name":     {Target: "x#Str"},
					"Children": {Target: "x#NodeList"},
				},
			},
			"x#NodeList": {Type: "list", Member: &Member{Target: "x#Node"}},
			"x#Str":      {Type: "string"},
		},
	}
	c := NewConverter(m)

	done := make(chan error, 1)
	go func() {
		_, err := c.Convert("x#Node")
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Convert hung on a self-referential Smithy shape")
	}
}

func TestConvert_UnresolvedShapeReference(t *testing.T) {
	m := &Model{Shapes: map[string]Shape{
		"x#A": {Type: "structure", Members: map[string]Member{"B": {Target: "x#Missing"}}},
	}}
	c := NewConverter(m)
	if _, err := c.Convert("x#A"); err == nil {
		t.Fatal("expected an error for an unresolved shape reference")
	}
}

func TestConvert_Enum(t *testing.T) {
	m := &Model{Shapes: map[string]Shape{
		"x#State": {
			Type: "enum",
			Members: map[string]Member{
				"Active":   {Target: "smithy.api#Unit", Traits: map[string]json.RawMessage{"smithy.api#enumValue": json.RawMessage(`"Active"`)}},
				"Inactive": {Target: "smithy.api#Unit", Traits: map[string]json.RawMessage{"smithy.api#enumValue": json.RawMessage(`"Inactive"`)}},
			},
		},
	}}
	c := NewConverter(m)
	s, err := c.Convert("x#State")
	if err != nil {
		t.Fatal(err)
	}
	if !s.Type.Is("string") {
		t.Fatalf("expected string, got %v", s.Type)
	}
	if len(s.Enum) != 2 {
		t.Fatalf("expected 2 enum values, got %v", s.Enum)
	}
}
