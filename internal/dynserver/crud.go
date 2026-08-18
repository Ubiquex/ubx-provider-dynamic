package dynserver

import (
	"fmt"
	"math/big"

	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// extractStringAttrs reads a fixed set of attribute names out of an object
// Value as strings -- what BuildPath needs for its own params map. Every
// name here is guaranteed present (ensurePathParamsPresent added any that
// weren't already real schema attributes), but a real API response can
// still legitimately leave one null before the first successful create --
// callers surface that as a real error, not a zero-value placeholder that
// would silently build a wrong URL.
func extractStringAttrs(v tftypes.Value, names []string) (map[string]string, error) {
	var m map[string]tftypes.Value
	if err := v.As(&m); err != nil {
		return nil, fmt.Errorf("dynserver: value is not an object: %w", err)
	}

	out := make(map[string]string, len(names))
	for _, name := range names {
		attr, ok := m[name]
		if !ok {
			return nil, fmt.Errorf("dynserver: object has no attribute %q", name)
		}
		s, err := attrToString(attr)
		if err != nil {
			return nil, fmt.Errorf("dynserver: attribute %q: %w", name, err)
		}
		out[name] = s
	}
	return out, nil
}

func attrToString(v tftypes.Value) (string, error) {
	if v.IsNull() {
		return "", fmt.Errorf("value is null")
	}
	if !v.IsKnown() {
		return "", fmt.Errorf("value is not known")
	}
	if v.Type().Is(tftypes.String) {
		var s string
		if err := v.As(&s); err != nil {
			return "", err
		}
		return s, nil
	}
	if v.Type().Is(tftypes.Number) {
		var f big.Float
		if err := v.As(&f); err != nil {
			return "", err
		}
		return f.Text('f', -1), nil
	}
	return "", fmt.Errorf("attribute type %s cannot be used as a path parameter (only string/number)", v.Type())
}

// mergeCarryForward replaces every name in carryForward on top of fresh's
// own decoded object, taking each such value from prior instead -- the
// real fix for wire.FromJSON's own honest behavior of nulling out any
// object field a JSON response simply doesn't mention (see
// ensurePathParamsPresent's doc comment: path-only attributes like
// GitHub's "owner"/"repo" never appear inside the API's own response body
// at all, so a fresh Read/Create/Update response has nothing to decode
// them FROM -- they only ever come from the request that was already made).
func mergeCarryForward(fresh, prior tftypes.Value, carryForward []string) (tftypes.Value, error) {
	if len(carryForward) == 0 {
		return fresh, nil
	}
	var freshMap map[string]tftypes.Value
	if err := fresh.As(&freshMap); err != nil {
		return tftypes.Value{}, fmt.Errorf("dynserver: merge carry-forward: fresh value is not an object: %w", err)
	}
	var priorMap map[string]tftypes.Value
	if err := prior.As(&priorMap); err != nil {
		return tftypes.Value{}, fmt.Errorf("dynserver: merge carry-forward: prior value is not an object: %w", err)
	}
	for _, name := range carryForward {
		if v, ok := priorMap[name]; ok {
			freshMap[name] = v
		}
	}
	return tftypes.NewValue(fresh.Type(), freshMap), nil
}
