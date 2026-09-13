// Package wire converts between tftypes.Value (tfplugin's own wire shape)
// and plain Go values as encoding/json decodes/encodes them -- the real
// bridge a Dynamic Provider needs that a real HashiCorp provider never
// does, since a real provider's own SDK (AWS/GCP/Azure Go SDKs) speaks Go
// structs, not raw JSON, on its own side of the wire. REST APIs speak
// plain JSON with no side-band type information (no distinction between
// "this map is actually an object with a fixed schema" and "this map is
// a real, open-ended map"), so this conversion is guided entirely by the
// target tftypes.Type -- known ahead of time from the resource's own
// translated schema -- not by anything in the JSON itself, except inside
// a DynamicPseudoType attribute (schema.go's own documented, deliberately
// opaque fallback for OpenAPI shapes with no coherent static type), where
// there is no target type to be guided by and JSON's own shape is
// genuinely all there is to go on.
package wire

import (
	"encoding/json"
	"fmt"
	"math/big"
	"sort"

	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// ToJSON converts a known tftypes.Value into a plain Go value
// (string/float64/bool/map[string]any/[]any/nil) suitable for
// encoding/json.Marshal -- the shape a real REST API request body needs.
func ToJSON(v tftypes.Value) (any, error) {
	if !v.IsKnown() {
		return nil, fmt.Errorf("wire: cannot serialize an unknown value to JSON")
	}
	if v.IsNull() {
		return nil, nil
	}

	t := v.Type()
	switch {
	case t.Is(tftypes.DynamicPseudoType):
		// A DynamicPseudoType Value's own Type() always reports
		// DynamicPseudoType itself, never the concrete shape it was
		// actually built with (see tftypes' valueFromDynamicPseudoType --
		// it infers a real Object/Tuple/primitive type internally, then
		// overwrites the Value's own reported type to the pseudo-type),
		// so there's no concrete type here to type-switch on below; probe
		// Value.As against each real representation in turn instead.
		return dynamicToJSON(v)
	case t.Is(tftypes.String):
		var s string
		if err := v.As(&s); err != nil {
			return nil, err
		}
		return s, nil
	case t.Is(tftypes.Number):
		var f big.Float
		if err := v.As(&f); err != nil {
			return nil, err
		}
		out, _ := f.Float64()
		return out, nil
	case t.Is(tftypes.Bool):
		var b bool
		if err := v.As(&b); err != nil {
			return nil, err
		}
		return b, nil
	}

	switch t.(type) {
	case tftypes.List, tftypes.Set, tftypes.Tuple:
		var elems []tftypes.Value
		if err := v.As(&elems); err != nil {
			return nil, err
		}
		out := make([]any, len(elems))
		for i, e := range elems {
			ev, err := ToJSON(e)
			if err != nil {
				return nil, fmt.Errorf("wire: element %d: %w", i, err)
			}
			out[i] = ev
		}
		return out, nil
	case tftypes.Map, tftypes.Object:
		var m map[string]tftypes.Value
		if err := v.As(&m); err != nil {
			return nil, err
		}
		out := make(map[string]any, len(m))
		for k, e := range m {
			ev, err := ToJSON(e)
			if err != nil {
				return nil, fmt.Errorf("wire: field %q: %w", k, err)
			}
			out[k] = ev
		}
		return out, nil
	}

	return nil, fmt.Errorf("wire: no JSON conversion for tftypes.Type %s", t)
}

// dynamicToJSON is ToJSON's own DynamicPseudoType counterpart -- see its
// call site's comment for why a concrete type-switch doesn't work here.
func dynamicToJSON(v tftypes.Value) (any, error) {
	var s string
	if err := v.As(&s); err == nil {
		return s, nil
	}
	var f big.Float
	if err := v.As(&f); err == nil {
		out, _ := f.Float64()
		return out, nil
	}
	var b bool
	if err := v.As(&b); err == nil {
		return b, nil
	}
	var m map[string]tftypes.Value
	if err := v.As(&m); err == nil {
		out := make(map[string]any, len(m))
		for k, e := range m {
			ev, err := ToJSON(e)
			if err != nil {
				return nil, fmt.Errorf("wire: dynamic field %q: %w", k, err)
			}
			out[k] = ev
		}
		return out, nil
	}
	var elems []tftypes.Value
	if err := v.As(&elems); err == nil {
		out := make([]any, len(elems))
		for i, e := range elems {
			ev, err := ToJSON(e)
			if err != nil {
				return nil, fmt.Errorf("wire: dynamic element %d: %w", i, err)
			}
			out[i] = ev
		}
		return out, nil
	}
	return nil, fmt.Errorf("wire: cannot convert dynamic value of type %s to JSON", v.Type())
}

// FromJSON converts a plain Go value (as produced by encoding/json.Unmarshal
// into an `any`) into a tftypes.Value of type ty -- the shape a real REST
// API's JSON response needs to become before it can populate a
// ReadResource/ApplyResourceChange NewState.
func FromJSON(raw any, ty tftypes.Type) (tftypes.Value, error) {
	if raw == nil {
		return tftypes.NewValue(ty, nil), nil
	}

	switch {
	case ty.Is(tftypes.String):
		s, ok := raw.(string)
		if !ok {
			return tftypes.Value{}, fmt.Errorf("wire: expected JSON string for %s, got %T", ty, raw)
		}
		return tftypes.NewValue(ty, s), nil
	case ty.Is(tftypes.Number):
		switch n := raw.(type) {
		case json.Number:
			f, _, err := big.ParseFloat(n.String(), 10, 0, big.ToNearestEven)
			if err != nil {
				return tftypes.Value{}, fmt.Errorf("wire: parse number %q: %w", n.String(), err)
			}
			return tftypes.NewValue(ty, f), nil
		case float64:
			return tftypes.NewValue(ty, n), nil
		default:
			return tftypes.Value{}, fmt.Errorf("wire: expected JSON number for %s, got %T", ty, raw)
		}
	case ty.Is(tftypes.Bool):
		b, ok := raw.(bool)
		if !ok {
			return tftypes.Value{}, fmt.Errorf("wire: expected JSON bool for %s, got %T", ty, raw)
		}
		return tftypes.NewValue(ty, b), nil
	case ty.Is(tftypes.DynamicPseudoType):
		return dynamicFromJSON(raw)
	}

	switch tt := ty.(type) {
	case tftypes.List:
		arr, ok := raw.([]any)
		if !ok {
			return tftypes.Value{}, fmt.Errorf("wire: expected JSON array for %s, got %T", ty, raw)
		}
		elems := make([]tftypes.Value, len(arr))
		for i, e := range arr {
			ev, err := FromJSON(e, tt.ElementType)
			if err != nil {
				return tftypes.Value{}, fmt.Errorf("wire: element %d: %w", i, err)
			}
			elems[i] = ev
		}
		return tftypes.NewValue(ty, elems), nil
	case tftypes.Map:
		m, ok := raw.(map[string]any)
		if !ok {
			return tftypes.Value{}, fmt.Errorf("wire: expected JSON object for %s, got %T", ty, raw)
		}
		vals := make(map[string]tftypes.Value, len(m))
		for k, e := range m {
			ev, err := FromJSON(e, tt.ElementType)
			if err != nil {
				return tftypes.Value{}, fmt.Errorf("wire: field %q: %w", k, err)
			}
			vals[k] = ev
		}
		return tftypes.NewValue(ty, vals), nil
	case tftypes.Object:
		m, _ := raw.(map[string]any)
		vals := make(map[string]tftypes.Value, len(tt.AttributeTypes))
		for name, attrType := range tt.AttributeTypes {
			if present, ok := m[name]; ok {
				ev, err := FromJSON(present, attrType)
				if err != nil {
					return tftypes.Value{}, fmt.Errorf("wire: field %q: %w", name, err)
				}
				vals[name] = ev
				continue
			}
			vals[name] = tftypes.NewValue(attrType, nil)
		}
		return tftypes.NewValue(ty, vals), nil
	}

	return tftypes.Value{}, fmt.Errorf("wire: no tftypes.Value conversion for type %s", ty)
}

// dynamicFromJSON builds the value for a DynamicPseudoType attribute from
// a raw JSON value with no target type to guide it -- the honest, necessary
// counterpart to schema.go's own DynamicPseudoType fallback for
// genuinely-unknown-shape OpenAPI fields: the wire shape is inferred
// entirely from what the real JSON response actually contains, this one
// time, per value, rather than from any schema.
//
// The inferred type is CONCRETE (String, Number, Bool, Tuple, Object) and
// never DynamicPseudoType itself, even though the attribute holding it is
// declared dynamic. That is not a detail, it is the whole contract msgpack
// encoding rests on: a dynamic attribute is written on the wire as a
// two-element pair of the value's own real type as JSON, then the value
// encoded against that real type, so the other side can recover a shape the
// schema never carried. tftypes only writes that pair when the value's own
// Type() is something concrete (marshalMsgPack's own
// `typ.Is(DynamicPseudoType) && !val.Type().Is(DynamicPseudoType)` guard).
//
// Building these with tftypes.NewValue(tftypes.DynamicPseudoType, ...) --
// which reads naturally, and is what ToJSON's own counterpart comment
// describes receiving -- is therefore silently unencodable. tftypes accepts
// it (valueFromDynamicPseudoType infers the real type, then overwrites the
// Value's reported type back to the pseudo-type), the value behaves
// correctly in memory and in every in-process test, and then the guard
// above declines it, the type switch beneath has no dynamic case, and the
// encode fails with "unknown type tftypes.DynamicPseudoType" -- at the far
// end of a real resource creation, after the remote API has already
// created the resource. It failed that way for every JSON shape, scalars
// included, not just containers.
func dynamicFromJSON(raw any) (tftypes.Value, error) {
	switch v := raw.(type) {
	case string:
		return tftypes.NewValue(tftypes.String, v), nil
	case bool:
		return tftypes.NewValue(tftypes.Bool, v), nil
	case float64:
		return tftypes.NewValue(tftypes.Number, v), nil
	case json.Number:
		f, _, err := big.ParseFloat(v.String(), 10, 0, big.ToNearestEven)
		if err != nil {
			return tftypes.Value{}, fmt.Errorf("wire: parse dynamic number %q: %w", v.String(), err)
		}
		return tftypes.NewValue(tftypes.Number, f), nil
	case []any:
		// A Tuple, not a List: a JSON array carries no promise that its
		// elements share a type, and inside a dynamic attribute there is no
		// schema to say they do. Tuple is the only list-shaped tftypes.Type
		// that can hold a heterogeneous array honestly.
		elems := make([]tftypes.Value, len(v))
		types := make([]tftypes.Type, len(v))
		for i, e := range v {
			ev, err := dynamicFromJSON(e)
			if err != nil {
				return tftypes.Value{}, fmt.Errorf("wire: dynamic element %d: %w", i, err)
			}
			elems[i], types[i] = ev, ev.Type()
		}
		return tftypes.NewValue(tftypes.Tuple{ElementTypes: types}, elems), nil
	case map[string]any:
		// An Object rather than a Map, for the same reason: JSON object
		// fields are independently typed.
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		vals := make(map[string]tftypes.Value, len(v))
		types := make(map[string]tftypes.Type, len(v))
		for _, k := range keys {
			ev, err := dynamicFromJSON(v[k])
			if err != nil {
				return tftypes.Value{}, fmt.Errorf("wire: dynamic field %q: %w", k, err)
			}
			vals[k], types[k] = ev, ev.Type()
		}
		return tftypes.NewValue(tftypes.Object{AttributeTypes: types}, vals), nil
	case nil:
		// The one case with no concrete type to infer, since JSON null says
		// nothing about what it is null OF. DynamicPseudoType is correct
		// here rather than a lapse: marshalMsgPack checks IsNull before it
		// reaches the type switch, so a null encodes as a bare nil whatever
		// its type claims to be.
		return tftypes.NewValue(tftypes.DynamicPseudoType, nil), nil
	default:
		return tftypes.Value{}, fmt.Errorf("wire: no dynamic conversion for JSON value of type %T", raw)
	}
}
