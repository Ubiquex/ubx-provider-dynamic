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
//     Required) and create does NOT already independently agree (create's
//     own translation of the same field name isn't already Computed-only
//     too): final Computed only. This is the asymmetric case -- read's
//     schema marks the field readOnly and create's own schema for the
//     same field name does not, the real shape a create-request and a
//     read-response that only partially overlap produce (Kubernetes's
//     own ObjectMeta: name/namespace are genuinely settable on create,
//     uid/resourceVersion/creationTimestamp are not, and only the
//     read-response schema says so). Read's marker wins here because
//     create's mere non-required presence of the same field name is not
//     evidence a client may actually set it -- it is at least as likely
//     to mean create's own copy of a shared/reused component schema
//     simply doesn't repeat a marker the response side does carry.
//   - both sides, everything else (create Optional and not Required, and
//     either create ALSO already independently marks the field
//     Computed-only -- the SYMMETRIC case, e.g. a single shared schema
//     referenced identically by both create and read, confirmed real for
//     Google Pub/Sub's own Topic.state -- or neither side treats it as
//     readOnly at all): final Optional+Computed -- the user may set it,
//     or leave it for the server to default. Deliberately NOT narrowed
//     to the asymmetric case above: when both sides already agree, or
//     neither says readOnly, this package has no basis to override that
//     agreement toward a stricter, user-facing-immutable classification.
//   - create only: kept exactly as create's own translation produced it
//     (Required/Optional, WriteOnly if the request schema itself said so) --
//     a field the API accepts but never echoes back.
//   - read only: forced Computed, regardless of what the read schema's own
//     translation decided (even a field the response happens to mark
//     writeOnly -- unusual, but by definition unsettable here since create
//     never mentions it at all).
//
// UBI-248: two real bugs here, not one, both only visible once a real
// create/read pair with a nested, asymmetrically-readOnly shared object
// was checked end to end (Kubernetes's own ObjectMeta, described above).
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
// unconditionally forced to Optional+Computed even in the asymmetric
// case above, where read's own schema had already marked the field
// genuinely readOnly and create's own view of the same field name had
// not. Narrowed to the asymmetric case specifically -- confirmed real
// and load-bearing for the SYMMETRIC case via this package's own
// existing test (TestBuild_TranslatesRealShape_ReadOnlyMergesToOptionalComputed_EnumCarried,
// internal/discoverydoc): Pub/Sub's real live discovery document uses
// the identical Topic schema for both the create request and the read
// response, so state's own readOnly marker already reaches BOTH create's
// and read's independent translations identically, and that test's own
// real, considered reasoning for keeping Optional+Computed there was not
// disturbed.
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
			createAlreadyAgrees := c.Computed && !c.Optional && !c.Required
			switch {
			case readSaysReadOnly && !createAlreadyAgrees:
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
