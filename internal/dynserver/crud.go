package dynserver

import (
	"fmt"
	"math/big"

	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// extractStringAttrs reads templateParams (real URL template parameter
// names, ResourceType.PathParams/CreatePathParams' own entries) out of an
// object Value as strings -- what restexec.BuildPath needs for its own
// params map, keyed by the LITERAL "{name}" segment BuildPath will search
// for, never by the schema attribute name when the two differ. attrFor
// resolves which schema attribute actually holds each template parameter's
// value -- nil, or a template param absent from it, means "same name"
// (the overwhelming common case); a present entry means
// ensurePathParamsPresent had to rename the real schema attribute to avoid
// a genuine collision with a differently-typed response attribute of the
// same name (build.go's own doc comment on ResourceType.PathParamAttr).
// Every resolved attribute name is guaranteed present in the schema
// (ensurePathParamsPresent added any that weren't already there), but a
// real API response can still legitimately leave one null before the
// first successful create -- callers surface that as a real error, not a
// zero-value placeholder that would silently build a wrong URL.
func extractStringAttrs(v tftypes.Value, templateParams []string, attrFor map[string]string) (map[string]string, error) {
	var m map[string]tftypes.Value
	if err := v.As(&m); err != nil {
		return nil, fmt.Errorf("dynserver: value is not an object: %w", err)
	}

	out := make(map[string]string, len(templateParams))
	for _, tp := range templateParams {
		attrName := tp
		if renamed, ok := attrFor[tp]; ok {
			attrName = renamed
		}
		attr, ok := m[attrName]
		if !ok {
			return nil, fmt.Errorf("dynserver: object has no attribute %q", attrName)
		}
		s, err := attrToString(attr)
		if err != nil {
			return nil, fmt.Errorf("dynserver: attribute %q: %w", attrName, err)
		}
		out[tp] = s
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

// resolveAttrNames converts templateParams (real URL template parameter
// names) into the real schema attribute names requestBody's own exclude
// list and carryForwardFields both need -- identical resolution to
// extractStringAttrs' own attrFor parameter, kept as a separate, smaller
// helper since these two callers only need the resolved NAMES, never the
// state VALUES extractStringAttrs also extracts.
func resolveAttrNames(templateParams []string, attrFor map[string]string) []string {
	out := make([]string, len(templateParams))
	for i, tp := range templateParams {
		if renamed, ok := attrFor[tp]; ok {
			out[i] = renamed
		} else {
			out[i] = tp
		}
	}
	return out
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
