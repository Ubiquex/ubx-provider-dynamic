package wire

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func TestRoundTrip_Object(t *testing.T) {
	ty := tftypes.Object{
		AttributeTypes: map[string]tftypes.Type{
			"name":  tftypes.String,
			"count": tftypes.Number,
			"tags":  tftypes.Map{ElementType: tftypes.String},
			"items": tftypes.List{ElementType: tftypes.String},
		},
		OptionalAttributes: map[string]struct{}{"count": {}, "tags": {}, "items": {}},
	}

	raw := []byte(`{"name":"repo1","count":3,"tags":{"env":"prod"},"items":["a","b"]}`)
	var decoded map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&decoded); err != nil {
		t.Fatal(err)
	}

	v, err := FromJSON(decoded, ty)
	if err != nil {
		t.Fatal(err)
	}

	back, err := ToJSON(v)
	if err != nil {
		t.Fatal(err)
	}
	m, ok := back.(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %T", back)
	}
	if m["name"] != "repo1" {
		t.Fatalf("name: %+v", m)
	}
	if m["count"] != 3.0 {
		t.Fatalf("count: %+v", m["count"])
	}
	tags, ok := m["tags"].(map[string]any)
	if !ok || tags["env"] != "prod" {
		t.Fatalf("tags: %+v", m["tags"])
	}
	items, ok := m["items"].([]any)
	if !ok || len(items) != 2 || items[0] != "a" {
		t.Fatalf("items: %+v", m["items"])
	}
}

func TestFromJSON_MissingOptionalField(t *testing.T) {
	ty := tftypes.Object{
		AttributeTypes:     map[string]tftypes.Type{"name": tftypes.String, "id": tftypes.String},
		OptionalAttributes: map[string]struct{}{"id": {}},
	}
	v, err := FromJSON(map[string]any{"name": "x"}, ty)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]tftypes.Value
	if err := v.As(&m); err != nil {
		t.Fatal(err)
	}
	if !m["id"].IsNull() {
		t.Fatalf("expected id null, got %v", m["id"])
	}
}

func TestDynamic_RoundTrip(t *testing.T) {
	raw := map[string]any{"a": "x", "b": []any{1.0, 2.0}}
	v, err := FromJSON(raw, tftypes.DynamicPseudoType)
	if err != nil {
		t.Fatal(err)
	}
	back, err := ToJSON(v)
	if err != nil {
		t.Fatal(err)
	}
	m, ok := back.(map[string]any)
	if !ok || m["a"] != "x" {
		t.Fatalf("dynamic round trip: %+v", back)
	}
}

func TestToJSON_Null(t *testing.T) {
	v := tftypes.NewValue(tftypes.String, nil)
	out, err := ToJSON(v)
	if err != nil {
		t.Fatal(err)
	}
	if out != nil {
		t.Fatalf("expected nil, got %v", out)
	}
}
