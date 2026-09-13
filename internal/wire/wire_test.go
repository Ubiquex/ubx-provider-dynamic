package wire

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
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

// TestDynamic_RoundTripsThroughMsgpack is TestDynamic_RoundTrip's own
// reason for not being enough. That test converts a JSON value to a
// tftypes.Value and straight back, entirely in memory, and has passed for
// as long as it has existed -- while every value it was proving correct
// was unencodable, so no dynamic attribute could ever reach a real
// Terraform or ubx on the other end. The missing step is the only one that
// exercises the guard that actually rejected them: encoding the value
// against the schema, the way the servers do when returning new state.
//
// So this asserts the full path a real response takes -- JSON, to value,
// to msgpack against a schema declaring the attribute dynamic, back to a
// value, back to JSON -- rather than the in-process half of it.
func TestDynamic_RoundTripsThroughMsgpack(t *testing.T) {
	// One case per JSON shape, since the failure was per-shape and hit
	// every one of them, scalars included.
	for name, raw := range map[string]any{
		"string": "x",
		"number": 3.0,
		"bool":   true,
		"null":   nil,
		"array":  []any{"a", 1.0},
		"object": map[string]any{"Version": "2012-10-17", "Statement": []any{
			map[string]any{"Effect": "Allow", "Principal": map[string]any{"AWS": "*"}},
		}},
	} {
		t.Run(name, func(t *testing.T) {
			v, err := FromJSON(raw, tftypes.DynamicPseudoType)
			if err != nil {
				t.Fatalf("FromJSON: %v", err)
			}

			schema := tftypes.Object{AttributeTypes: map[string]tftypes.Type{
				"policy": tftypes.DynamicPseudoType,
			}}
			state := tftypes.NewValue(schema, map[string]tftypes.Value{"policy": v})

			dv, err := tfprotov6.NewDynamicValue(schema, state)
			if err != nil {
				t.Fatalf("encode against a dynamic attribute: %v", err)
			}

			decoded, err := dv.Unmarshal(schema)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			var attrs map[string]tftypes.Value
			if err := decoded.As(&attrs); err != nil {
				t.Fatal(err)
			}
			back, err := ToJSON(attrs["policy"])
			if err != nil {
				t.Fatalf("ToJSON: %v", err)
			}

			// Compared as JSON text: the point is that what the other side
			// recovers is the same document, and the concrete tftypes.Type
			// this now infers along the way is an implementation detail of
			// getting it there.
			want, _ := json.Marshal(raw)
			got, _ := json.Marshal(back)
			if !bytes.Equal(want, got) {
				t.Fatalf("round trip changed the document:\n want %s\n  got %s", want, got)
			}
		})
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
