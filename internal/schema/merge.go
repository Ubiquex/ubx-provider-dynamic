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
//   - both sides, read genuinely readOnly (Computed, not Optional, not
//     Required): final Computed only, regardless of what create's own
//     translation of the same field name independently produced. A real
//     readOnly marker is the vendor's own statement that the field is
//     server-set -- create's own view of the same field name never
//     overrides that, whether create is silent about it (the asymmetric
//     case: Kubernetes's own ObjectMeta, where name/namespace are
//     genuinely settable on create and uid/resourceVersion/
//     creationTimestamp are not, and only the read-response schema says
//     so) or create ALSO carries the identical marker because create and
//     read share the literal same schema object (the symmetric case:
//     Google Compute's own Instance, where insert.request and get.response
//     both $ref the same schema, and selfLink/creationTimestamp/id/status
//     all carry a real, structural readOnly: true there). Create's own
//     agreement in the symmetric case is not independent confirmation
//     that the field is settable -- it is the same marker read twice off
//     one shared schema, so it cannot be evidence against itself.
//   - both sides, neither side says readOnly: final Optional+Computed --
//     the user may set it, or leave it for the server to default. This is
//     the only case this package still has no basis to override.
//   - create only: kept exactly as create's own translation produced it
//     (Required/Optional, WriteOnly if the request schema itself said so) --
//     a field the API accepts but never echoes back.
//   - read only: forced Computed, regardless of what the read schema's own
//     translation decided (even a field the response happens to mark
//     writeOnly -- unusual, but by definition unsettable here since create
//     never mentions it at all).
//
// UBI-248: three real bugs here, not one, found in two passes once a real
// create/read pair with a nested, asymmetrically-readOnly shared object
// was checked end to end (Kubernetes's own ObjectMeta, described above),
// and again once the fix for those two was checked against providers
// whose create/read pair shares one literal schema object.
//
// First: a field present on both sides used to keep create's own
// NestedType wholesale (a shallow struct copy, `merged := *c`), so a
// nested child's own read-side readOnly signal was never consulted at
// all -- nothing about merging the PARENT field ever revisited its
// children against read's own version of them, at any depth, for any
// provider whose create and read schemas both define the same nested
// object.
//
// Second, only visible once the first fix let read's own child attribute
// reach this function at all: "both sides, create not Required" was
// unconditionally forced to Optional+Computed even when read's own
// schema had already marked the field genuinely readOnly and create's
// own view of the same field name had not.
//
// Third: the fix for the second bug only covered the asymmetric case,
// gated on create NOT already independently agreeing the field was
// Computed-only -- reasoned at the time to be load-bearing for this
// package's own existing test (now TestBuild_TranslatesRealShape_
// ReadOnlyMergesToComputedOnly_EnumCarried, internal/discoverydoc),
// which models Pub/Sub's own Topic.state. Checking that test's own real
// spec directly found state DOES carry a real readOnly: true marker
// (Pub/Sub's create and get both $ref the identical Topic schema, same
// shape as Compute's Instance) -- the test was never a case with no
// marker, and asserting Optional+Computed for a field the vendor marks
// readOnly was itself the same class of bug as the first two, just
// gated on symmetric schema reuse instead of a missing nested-field
// check. Measured before removing this gate: 110 of 1,106 Azure resource
// pages and 71 of 1,562 Google resource pages showed a real Output
// properties section (Azure and Google reuse one schema across create
// and read on 1,675 of 1,753 real resource pairs checked, the dominant
// shape for both, not an edge case) -- Kubernetes and Cloudflare/GitHub/
// DigitalOcean's own create/read pairs are already mostly asymmetric, so
// they were already covered by the second bug's own fix and are
// unaffected by removing this gate.
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
			if c.NestedType != nil && r.NestedType != nil {
				merged.NestedType = &tfprotov6.SchemaObject{
					Attributes: MergeResourceAttributes(c.NestedType.Attributes, r.NestedType.Attributes),
					Nesting:    c.NestedType.Nesting,
				}
			}
			readSaysReadOnly := r.Computed && !r.Optional && !r.Required
			switch {
			case readSaysReadOnly:
				merged.Required, merged.Optional, merged.WriteOnly = false, false, false
				merged.Computed = true
			case !c.Required:
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
