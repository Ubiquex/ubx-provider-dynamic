package server

import (
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/ubiquex/ubx-provider-dynamic/internal/wire"
)

// stateAsMap converts a known object Value into the snake_case-keyed
// map[string]any wireexec.Client.Do expects as input -- every attribute
// wire.ToJSON can represent, Unknown (Computed, not yet known) attributes
// omitted entirely (there is no real value to send yet), matching
// dynserver's own identical real requestBody discipline for the same
// reason. Unlike dynserver's requestBody, no exclude list is needed here:
// each real per-protocol binder in wireexec already reads out only the
// specific fields that operation's own real Smithy input shape declares,
// ignoring everything else in this map -- see wireexec's own doc comment.
func stateAsMap(v tftypes.Value) (map[string]any, error) {
	var m map[string]tftypes.Value
	if err := v.As(&m); err != nil {
		return nil, fmt.Errorf("value is not an object: %w", err)
	}
	out := map[string]any{}
	for name, attr := range m {
		if !attr.IsKnown() || attr.IsNull() {
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

// mergeCarryForward mirrors dynserver's own identical real mechanism: a
// fresh API response never mentions fields that only ever came from the
// request that produced it (e.g. SQS's own real GetQueueAttributes
// response has no QueueUrl field at all -- it IS the lookup key, not part
// of what it returns), so those fields must be carried forward from
// prior/planned state on every real write, not left to go null.
func mergeCarryForward(fresh, prior tftypes.Value, carryForward []string) (tftypes.Value, error) {
	if len(carryForward) == 0 {
		return fresh, nil
	}
	var freshMap map[string]tftypes.Value
	if err := fresh.As(&freshMap); err != nil {
		return tftypes.Value{}, fmt.Errorf("merge carry-forward: fresh value is not an object: %w", err)
	}
	var priorMap map[string]tftypes.Value
	if err := prior.As(&priorMap); err != nil {
		return tftypes.Value{}, fmt.Errorf("merge carry-forward: prior value is not an object: %w", err)
	}
	for _, name := range carryForward {
		if v, ok := priorMap[name]; ok {
			if fv, ok := freshMap[name]; !ok || fv.IsNull() {
				freshMap[name] = v
			}
		}
	}
	return tftypes.NewValue(fresh.Type(), freshMap), nil
}

// splitImportID parses an import ID as "/"-joined values for idFields, in
// order -- mirrors dynserver's own identical real convention (see its own
// doc comment on Server.ImportResourceState), producing map[string]any
// (rather than map[string]string) to match wireexec.Client.Do's own input
// shape directly.
func splitImportID(id string, idFields []string) (map[string]any, error) {
	if len(idFields) == 0 {
		return map[string]any{}, nil
	}
	parts := strings.Split(id, "/")
	if len(parts) != len(idFields) {
		return nil, fmt.Errorf("expected an import ID shaped as %q (%d parts separated by \"/\"), got %q (%d parts)",
			strings.Join(idFields, "/"), len(idFields), id, len(parts))
	}
	out := make(map[string]any, len(idFields))
	for i, f := range idFields {
		out[f] = parts[i]
	}
	return out, nil
}
