package schema

import (
	"sort"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
)

// MergeResourceAttributes combines a resource's independently-translated
// create-request and read-response attribute sets into the single,
// consistent attribute list a tfplugin resource schema needs -- the
// resource-level optionality decision Translator itself deliberately
// leaves undecided (see fieldPolicy's own doc comment): a schema alone
// can't tell "settable, and the server may also default it" from "settable,
// full stop" or "server-only" -- that classification only exists once both
// sides of a real create/read pair are known.
//
// Rules, by where a field name appears:
//   - both sides, create Required: final Required (create's own type wins;
//     a mismatch against read's own type for the same name is real but rare
//     enough in practice not to need a distinct third outcome here).
//   - both sides, create Optional (not Required): final Optional+Computed --
//     the user may set it, or leave it for the server to default.
//   - create only: kept exactly as create's own translation produced it
//     (Required/Optional, WriteOnly if the request schema itself said so) --
//     a field the API accepts but never echoes back.
//   - read only: forced Computed, regardless of what the read schema's own
//     translation decided (even a field the response happens to mark
//     writeOnly -- unusual, but by definition unsettable here since create
//     never mentions it at all).
func MergeResourceAttributes(create, read []*tfprotov6.SchemaAttribute) []*tfprotov6.SchemaAttribute {
	createByName := map[string]*tfprotov6.SchemaAttribute{}
	for _, a := range create {
		createByName[a.Name] = a
	}
	readByName := map[string]*tfprotov6.SchemaAttribute{}
	for _, a := range read {
		readByName[a.Name] = a
	}

	names := map[string]struct{}{}
	for _, a := range create {
		names[a.Name] = struct{}{}
	}
	for _, a := range read {
		names[a.Name] = struct{}{}
	}
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)

	out := make([]*tfprotov6.SchemaAttribute, 0, len(sorted))
	for _, name := range sorted {
		c, cok := createByName[name]
		r, rok := readByName[name]
		switch {
		case cok && rok:
			merged := *c
			if !c.Required {
				merged.Optional = true
				merged.Computed = true
			}
			out = append(out, &merged)
		case cok:
			out = append(out, c)
		default:
			merged := *r
			merged.Required, merged.Optional, merged.WriteOnly = false, false, false
			merged.Computed = true
			out = append(out, &merged)
		}
	}
	return out
}
