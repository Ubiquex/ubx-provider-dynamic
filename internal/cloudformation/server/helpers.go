package server

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/ubiquex/ubx-provider-dynamic/internal/cloudformation"
	"github.com/ubiquex/ubx-provider-dynamic/internal/wire"
)

// extractIdentifier builds CCAPI's own real, single Identifier string
// from v's own primary-identifier attribute(s) -- a plain value for a
// single real primary identifier, or the real, documented "|"-joined
// form (in the resource's own declared order) for a compound one (see
// GetResourceInput.Identifier's own real botocore documentation, quoted
// in ccapi's own doc comment).
func extractIdentifier(v tftypes.Value, rt *cloudformation.BuiltResource) (string, error) {
	if len(rt.PrimaryIdentifier) == 0 {
		return "", fmt.Errorf("resource %s has no known primary identifier", rt.TypeName)
	}
	var m map[string]tftypes.Value
	if err := v.As(&m); err != nil {
		return "", fmt.Errorf("value is not an object: %w", err)
	}
	parts := make([]string, 0, len(rt.PrimaryIdentifier))
	for _, attr := range rt.PrimaryIdentifier {
		av, ok := m[attr]
		if !ok || av.IsNull() || !av.IsKnown() {
			return "", fmt.Errorf("primary identifier attribute %q is not set", attr)
		}
		s, err := attrToString(av)
		if err != nil {
			return "", fmt.Errorf("primary identifier attribute %q: %w", attr, err)
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, "|"), nil
}

func attrToString(v tftypes.Value) (string, error) {
	if v.Type().Is(tftypes.String) {
		var s string
		if err := v.As(&s); err != nil {
			return "", err
		}
		return s, nil
	}
	j, err := wire.ToJSON(v)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(j)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// objectAsMap converts an object Value into a snake_case-keyed
// map[string]any, skipping Unknown (Computed, not yet known) attributes
// entirely and preserving null ones as a nil entry.
//
// The per-attribute skip has to happen BEFORE wire.ToJSON is reached, and
// that ordering is the whole point of this function. ToJSON recurses into
// an object and refuses the first unknown field it meets, so converting a
// whole planned object in one call cannot skip anything: on a create,
// every Computed attribute absent from config is unknown by design (ubx's
// own ctyvalue.go marks it so, standard planning semantics), and the
// primary identifier is Computed on essentially every CCAPI resource.
// desiredStateJSON did convert wholesale and filtered Computed attributes
// afterwards, one step too late, so `ubx ship` could not create any AWS
// resource carrying a computed attribute -- 96% of them -- failing with
// `create resource: encode planned state: wire: field "queue_url": wire:
// cannot serialize an unknown value to JSON`.
//
// Unknown only, and NOT null, which is the one place this deliberately
// differs from smithy/server's own stateAsMap and dynserver's own
// requestBody. Those two build a request body, where a null attribute
// means "do not send this field" and dropping it is right. buildPatch
// builds an RFC 6902 patch, where a planned null against a non-null prior
// means "remove this field" and is only detectable because the key is
// present with a nil value -- dropping nulls here would silently stop
// emitting every remove operation, turning "unset this attribute" into a
// no-op. wire.ToJSON already renders a null as nil, so preserving them
// costs nothing beyond not skipping them. desiredStateJSON does want
// nulls gone and drops them itself, one line later, exactly as before.
func objectAsMap(v tftypes.Value) (map[string]any, error) {
	var m map[string]tftypes.Value
	if err := v.As(&m); err != nil {
		return nil, fmt.Errorf("value is not an object: %w", err)
	}
	out := make(map[string]any, len(m))
	for name, attr := range m {
		if !attr.IsKnown() {
			continue
		}
		jv, err := wire.ToJSON(attr)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", name, err)
		}
		out[name] = jv
	}
	return out, nil
}

// desiredStateJSON builds CCAPI's own real, JSON-string-encoded
// "DesiredState" from planned -- every non-null, non-Computed top-level
// attribute, real-cased via rt.WireNames.
func desiredStateJSON(planned tftypes.Value, rt *cloudformation.BuiltResource) (string, error) {
	m, err := objectAsMap(planned)
	if err != nil {
		return "", fmt.Errorf("encode planned state: %w", err)
	}
	computed := computedAttrNames(rt.Schema.Block.Attributes)

	real := map[string]any{}
	for snake, val := range m {
		if val == nil || computed[snake] {
			continue
		}
		wn, ok := rt.WireNames[snake]
		if !ok {
			continue
		}
		real[wn.Real] = rekeyToReal(val, wn.Children)
	}
	b, err := json.Marshal(real)
	if err != nil {
		return "", fmt.Errorf("marshal desired state: %w", err)
	}
	return string(b), nil
}

func computedAttrNames(attrs []*tfprotov6.SchemaAttribute) map[string]bool {
	out := make(map[string]bool, len(attrs))
	for _, a := range attrs {
		if a.Computed && !a.Optional && !a.Required {
			out[a.Name] = true
		}
	}
	return out
}

// buildPatch builds a real, top-level-only RFC 6902 JSON Patch document
// from prior -> planned (see this package's own doc comment for the
// real, deliberate top-level-only scope). Create-only attributes are
// skipped: CCAPI's own real handlers reject a patch touching one of
// them, and Terraform's own plan/diff already forces a replace (new
// resource) rather than an update whenever a create-only-shaped
// attribute genuinely changes -- ubx core's own plan step, not this
// provider's job to re-derive here.
func buildPatch(prior, planned tftypes.Value, rt *cloudformation.BuiltResource) (string, error) {
	// Same per-attribute skip as desiredStateJSON, for the same reason: an
	// update's own planned state carries unknowns too, for any Computed
	// attribute absent from config, so converting either side wholesale
	// fails identically. A skipped attribute simply produces no patch
	// operation, which is correct: an unknown is a value CCAPI is about to
	// decide, never one to send it.
	priorMap, err := objectAsMap(prior)
	if err != nil {
		return "", fmt.Errorf("encode prior state: %w", err)
	}
	plannedMap, err := objectAsMap(planned)
	if err != nil {
		return "", fmt.Errorf("encode planned state: %w", err)
	}
	computed := computedAttrNames(rt.Schema.Block.Attributes)

	type op struct {
		Op    string `json:"op"`
		Path  string `json:"path"`
		Value any    `json:"value,omitempty"`
	}
	var ops []op

	for snake, newVal := range plannedMap {
		if computed[snake] || rt.CreateOnlyProperties[snake] {
			continue
		}
		wn, ok := rt.WireNames[snake]
		if !ok {
			continue
		}
		oldVal, existed := priorMap[snake]
		if newVal == nil {
			if existed && oldVal != nil {
				ops = append(ops, op{Op: "remove", Path: "/" + wn.Real})
			}
			continue
		}
		newB, _ := json.Marshal(rekeyToReal(newVal, wn.Children))
		oldB, _ := json.Marshal(rekeyToReal(oldVal, wn.Children))
		if string(newB) == string(oldB) {
			continue
		}
		action := "replace"
		if !existed || oldVal == nil {
			action = "add"
		}
		ops = append(ops, op{Op: action, Path: "/" + wn.Real, Value: rekeyToReal(newVal, wn.Children)})
	}

	b, err := json.Marshal(ops)
	if err != nil {
		return "", fmt.Errorf("marshal patch document: %w", err)
	}
	return string(b), nil
}

// rekeyToReal recursively remaps a decoded/JSON-shaped value's own map
// keys from ubx's own snake_case to the resource's real CFN-cased
// property names, using names (this level's own real WireNames -- see
// that type's own doc comment for why this exists instead of an
// algorithmic case reversal).
func rekeyToReal(v any, names cloudformation.WireNames) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			wn, ok := names[k]
			if !ok {
				out[k] = val
				continue
			}
			out[wn.Real] = rekeyToReal(val, wn.Children)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = rekeyToReal(e, names)
		}
		return out
	default:
		return v
	}
}

// rekeyToSnake recursively remaps a decoded CCAPI response's own real
// CFN-cased map keys back to ubx's own snake_case attribute names, the
// reverse of rekeyToReal -- wire.FromJSON needs keys matching the
// resource's own real ObjectType attribute names (always snake_case).
func rekeyToSnake(v any, names cloudformation.WireNames) any {
	reverse := make(map[string]string, len(names))
	children := make(map[string]cloudformation.WireNames, len(names))
	for snake, wn := range names {
		reverse[wn.Real] = snake
		children[wn.Real] = wn.Children
	}
	return rekeyToSnakeWith(v, reverse, children)
}

func rekeyToSnakeWith(v any, reverse map[string]string, children map[string]cloudformation.WireNames) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			snake, ok := reverse[k]
			if !ok {
				continue
			}
			childNames := children[k]
			childReverse := make(map[string]string, len(childNames))
			childChildren := make(map[string]cloudformation.WireNames, len(childNames))
			for s, wn := range childNames {
				childReverse[wn.Real] = s
				childChildren[wn.Real] = wn.Children
			}
			out[snake] = rekeyToSnakeWith(val, childReverse, childChildren)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = rekeyToSnakeWith(e, reverse, children)
		}
		return out
	default:
		return v
	}
}
