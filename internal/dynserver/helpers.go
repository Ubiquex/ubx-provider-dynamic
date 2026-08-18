package dynserver

import (
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/ubiquex/ubx-provider-dynamic/internal/wire"
)

// valueFromResponse decodes a real REST API response body (already JSON-
// decoded by restexec.Client.Do) into the resource's own object type. A
// nil body (a 204, or any real empty success response) is treated as an
// error here specifically -- Create/Read/Update all need the API's own
// object representation back to build NewState; a provider whose create
// endpoint returns 201 with no body is real but not one either GitHub's or
// Datadog's own resources this ticket validates against do.
func valueFromResponse(body any, objType tftypes.Object) (tftypes.Value, error) {
	if body == nil {
		return tftypes.Value{}, fmt.Errorf("API response had no body -- cannot build resource state without it")
	}
	return wire.FromJSON(body, objType)
}

// requestBody builds a REST request's own JSON body out of a planned
// object value: every attribute except those in exclude (this operation's
// own path parameters, which belong in the URL, never the body) and any
// attribute that is Unknown (Computed, server-filled -- there is no value
// to send) or explicitly Null (Optional and left unset by the user --
// omitted rather than sent as a literal null, the conservative choice for
// both POST create and PATCH partial-update semantics).
func requestBody(v tftypes.Value, exclude []string) (map[string]any, error) {
	var m map[string]tftypes.Value
	if err := v.As(&m); err != nil {
		return nil, fmt.Errorf("value is not an object: %w", err)
	}

	excludeSet := make(map[string]bool, len(exclude))
	for _, e := range exclude {
		excludeSet[e] = true
	}

	out := map[string]any{}
	for name, attr := range m {
		if excludeSet[name] || !attr.IsKnown() || attr.IsNull() {
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

// splitImportID parses ImportResourceState's own "owner/repo"-shaped ID
// convention -- see Server.ImportResourceState's own doc comment for why.
func splitImportID(id string, params []string) (map[string]string, error) {
	if len(params) == 0 {
		return map[string]string{}, nil
	}
	parts := strings.Split(id, "/")
	if len(parts) != len(params) {
		return nil, fmt.Errorf("expected an import ID shaped as %q (%d parts separated by \"/\"), got %q (%d parts)",
			strings.Join(params, "/"), len(params), id, len(parts))
	}
	out := make(map[string]string, len(params))
	for i, p := range params {
		out[p] = parts[i]
	}
	return out, nil
}
