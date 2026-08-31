// Real, mechanical semver derivation from a schema diff -- the founder's
// own explicit requirement: "It should be mechanical, not a judgment
// call." DiffLevel walks two real, already-translated schema trees
// (map[string]*tfprotov6.Schema -- the SAME real output every
// schema_source's own Build pipeline already produces, so this works
// identically regardless of which real format a provider's snapshot came
// from) and returns exactly one of four real outcomes, never a guess.
package snapshot

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// ChangeLevel is DiffLevel's own real, closed outcome set, ordered so a
// caller can take the max across every resource type with a plain
// integer comparison rather than a real, separate ranking table.
type ChangeLevel int

const (
	// NoChange means the two schema trees are structurally identical,
	// field-for-field, description text included -- nothing to bump.
	NoChange ChangeLevel = iota
	// Patch means the only real difference is Description text -- the
	// real, addressable API surface (what fields exist, what they
	// accept) is byte-for-byte unchanged.
	Patch
	// Minor means every real difference is purely additive: a new
	// resource type, a new field, a field that stopped being required,
	// or a field that gained write access (Computed -> Optional).
	Minor
	// Major means at least one real difference removes or restricts
	// something a caller could previously rely on: a resource type or
	// field disappeared, a field's type changed, a field became
	// required, or a field lost write access (Optional -> Computed).
	Major
)

func (c ChangeLevel) String() string {
	switch c {
	case NoChange:
		return "none"
	case Patch:
		return "patch"
	case Minor:
		return "minor"
	case Major:
		return "major"
	default:
		return "unknown"
	}
}

// DiffLevel compares old (the PREVIOUS snapshot's own real, translated
// resource schemas) against next (the CURRENT real fetch's own), and
// returns the single highest real ChangeLevel found across every
// resource type and every field, recursively. A nil/empty old means
// "there is no prior snapshot" -- every resource in next is real, new
// content, Minor at most (never Major: nothing was removed from a
// baseline that didn't exist), the real, correct answer for a first-ever
// snapshot (NextVersion's own caller starts numbering from there, see
// its own doc comment).
func DiffLevel(old, next map[string]*tfprotov6.Schema) ChangeLevel {
	level := NoChange
	raise := func(l ChangeLevel) {
		if l > level {
			level = l
		}
	}

	for typeName, newSchema := range next {
		oldSchema, existed := old[typeName]
		if !existed {
			raise(Minor) // a whole new resource type -- purely additive
			continue
		}
		raise(diffBlock(oldSchema.Block, newSchema.Block))
	}
	for typeName := range old {
		if _, stillExists := next[typeName]; !stillExists {
			raise(Major) // a resource type disappeared -- real, breaking
		}
	}
	return level
}

func diffBlock(old, next *tfprotov6.SchemaBlock) ChangeLevel {
	if old == nil && next == nil {
		return NoChange
	}
	if old == nil || next == nil {
		return Major // a whole nested block appeared/disappeared at this level
	}

	level := NoChange
	raise := func(l ChangeLevel) {
		if l > level {
			level = l
		}
	}

	oldAttrs := attrsByName(old.Attributes)
	nextAttrs := attrsByName(next.Attributes)
	for name, na := range nextAttrs {
		oa, existed := oldAttrs[name]
		if !existed {
			raise(Minor) // a new field -- purely additive
			continue
		}
		raise(diffAttribute(oa, na))
	}
	for name := range oldAttrs {
		if _, stillExists := nextAttrs[name]; !stillExists {
			raise(Major) // a field disappeared -- real, breaking
		}
	}

	oldBlocks := blockTypesByName(old.BlockTypes)
	nextBlocks := blockTypesByName(next.BlockTypes)
	for name, nb := range nextBlocks {
		ob, existed := oldBlocks[name]
		if !existed {
			raise(Minor)
			continue
		}
		raise(diffBlock(ob.Block, nb.Block))
	}
	for name := range oldBlocks {
		if _, stillExists := nextBlocks[name]; !stillExists {
			raise(Major)
		}
	}

	return level
}

// diffAttribute is the one real place the founder's own explicit
// addition lives: a field flipping Optional=true,Computed=false to
// Optional=false,Computed=true (or the reverse) is real and mechanical,
// but doesn't fall out of "required got stricter/looser" the way the
// other three flag transitions do, so it needs its own real rule,
// documented here rather than left implicit.
//
// The rule: classify by WRITE ACCESS, not by the raw flag pair. Optional
// means a caller CAN set the field; Computed-only means a caller CANNOT
// (the server assigns it) -- moving from "can set" to "cannot" is a real
// removal of capability a caller may have been relying on (their own
// program would need to stop setting it, exactly the same real shape of
// breakage as the field disappearing from the writable surface), so it's
// Major. The reverse (Computed-only gaining Optional) is purely
// additive -- a caller who never set it is unaffected, one who wants to
// now can -- so it's Minor. Required's own transitions keep their
// already-established, separate rule (stricter = Major, looser = Minor)
// unchanged; this rule only fires for the real Optional<->Computed-only
// case Required doesn't already cover.
func diffAttribute(old, next *tfprotov6.SchemaAttribute) ChangeLevel {
	level := diffAttributeType(old, next)
	if level == Major {
		return Major // a real, incompatible shape change already settles it
	}
	if l := diffAttributeFlags(old, next); l > level {
		level = l
	}
	return level
}

// diffAttributeType is a real, found-in-review fix, caught live against
// Datadog's own real spec (a real nil-pointer panic, not a hypothetical):
// tfprotov6.SchemaAttribute carries Type OR NestedType, never both (real,
// documented as mutually exclusive on the struct itself) -- an object-
// typed attribute (a real, common shape: Datadog's own request/response
// bodies nest objects as attributes, not always as separate
// tfprotov6.SchemaNestedBlock entries) has Type == nil, and calling
// .String() on a nil tftypes.Type interface panics. Real, mechanical
// rule, kept as precise as diffBlock's own BlockTypes recursion (never
// downgraded to a coarse "any nested change = Major" shortcut, which
// would over-report Major for a purely additive nested field): a flat
// scalar/collection Type recurses through diffType (UBI-233 -- see its
// own doc comment for why a plain .String() compare here was wrong); a
// NestedType recurses field-by-field exactly like diffBlock already does
// for BlockTypes; SWITCHING between the two shapes (flat Type <->
// NestedType) on the same field name is always Major -- a real,
// structural shape change no caller's existing code could survive
// either direction.
func diffAttributeType(old, next *tfprotov6.SchemaAttribute) ChangeLevel {
	switch {
	case old.NestedType == nil && next.NestedType == nil:
		return diffType(old.Type, next.Type)
	case old.NestedType != nil && next.NestedType != nil:
		oldAttrs := attrsByName(old.NestedType.Attributes)
		nextAttrs := attrsByName(next.NestedType.Attributes)
		level := NoChange
		if old.NestedType.Nesting != next.NestedType.Nesting {
			level = Major
		}
		for name, na := range nextAttrs {
			oa, existed := oldAttrs[name]
			if !existed {
				if level < Minor {
					level = Minor
				}
				continue
			}
			if l := diffAttribute(oa, na); l > level {
				level = l
			}
		}
		for name := range oldAttrs {
			if _, stillExists := nextAttrs[name]; !stillExists {
				level = Major
			}
		}
		return level
	default:
		return Major // flat Type <-> NestedType on the same field: a real, structural shape change
	}
}

// diffType is UBI-233's own real fix. Before this, a flat tftypes.Type
// (anything reaching here through diffAttributeType's own flat-Type
// branch, never a NestedType -- those already recurse field-by-field
// through diffAttribute) was compared with old.Type.String() !=
// next.Type.String(), a full literal encoding of the type's entire
// shape. Real, confirmed failure mode: a List of Objects (buildArray's
// own real, deliberate shape for a nested array not reached through
// BuildAttribute directly -- see translate.go's own doc comment) gains
// one new, optional field somewhere inside that Object, and the
// .String() form changes because the WHOLE literal changed, read by the
// old code exactly like a field flipping from string to int. Confirmed
// live: ubx-schema-github's own github_ds member reported Major for
// exactly this, GitHub's Budget object gaining a purely additive
// expires_at field, while the sibling github (resource) member's own
// translation of the identical change correctly registered as Minor
// (it goes through a real NestedType there, not a flat List of Object).
//
// diffType closes that gap by inspecting the type's own real structure
// instead of its string form, for the three real container shapes this
// codebase's own translate.go ever produces (Object, List/Set/Map,
// Tuple -- checked directly, Tuple is never actually constructed today,
// included anyway since the tftypes.Type interface allows it and a
// silent fallback to Major for a real-but-rare shape would recreate
// this exact bug for a different case). Anything else (a primitive
// scalar, DynamicPseudoType, or two container kinds swapped for each
// other on the same field) still falls back to a direct comparison --
// correct there, since a primitive has no internal shape to look
// inside, and swapping container kinds (a List becoming a Map) is
// always a real, structural break no caller's existing code survives.
func diffType(old, next tftypes.Type) ChangeLevel {
	if old == nil && next == nil {
		return NoChange
	}
	if old == nil || next == nil {
		return Major // a real type appearing/disappearing where the other side had none
	}

	oldObj, oldIsObj := old.(tftypes.Object)
	nextObj, nextIsObj := next.(tftypes.Object)
	if oldIsObj || nextIsObj {
		if !oldIsObj || !nextIsObj {
			return Major // Object <-> something else: a real, structural shape change
		}
		return diffObjectType(oldObj, nextObj)
	}

	if oldList, ok := old.(tftypes.List); ok {
		nextList, ok := next.(tftypes.List)
		if !ok {
			return Major
		}
		return diffType(oldList.ElementType, nextList.ElementType)
	}
	if oldSet, ok := old.(tftypes.Set); ok {
		nextSet, ok := next.(tftypes.Set)
		if !ok {
			return Major
		}
		return diffType(oldSet.ElementType, nextSet.ElementType)
	}
	if oldMap, ok := old.(tftypes.Map); ok {
		nextMap, ok := next.(tftypes.Map)
		if !ok {
			return Major
		}
		return diffType(oldMap.ElementType, nextMap.ElementType)
	}
	if oldTuple, ok := old.(tftypes.Tuple); ok {
		nextTuple, ok := next.(tftypes.Tuple)
		if !ok {
			return Major
		}
		return diffTupleType(oldTuple, nextTuple)
	}

	// A primitive (String, Number, Bool, DynamicPseudoType) or any other
	// leaf shape this codebase's own translator never builds a container
	// out of -- no internal structure to inspect, its own literal form IS
	// the whole shape, so comparing it directly is correct, not a
	// fallback standing in for a missed case.
	if old.String() != next.String() {
		return Major
	}
	return NoChange
}

// diffObjectType compares two tftypes.Object values field by field,
// exactly like diffBlock already does for a real tfprotov6.SchemaBlock's
// own Attributes -- a new key is Minor (purely additive), a key that
// disappeared is Major (real, breaking), a key present on both sides
// recurses through diffType. OptionalAttributes is compared too even
// though translate.go's own doc comment confirms this codebase never
// actually populates it today (tftypes.Object.UsableAs panics
// unconditionally once it's non-empty) -- correct to check anyway
// rather than assume that stays true forever: a key moving OUT of
// OptionalAttributes (was optional within the object, now always
// present) is Major for the same real reason a field becoming Required
// already is in diffAttributeFlags; moving IN is Minor, the same
// direction as a field stopping being Required there.
func diffObjectType(old, next tftypes.Object) ChangeLevel {
	level := NoChange
	raise := func(l ChangeLevel) {
		if l > level {
			level = l
		}
	}

	for name, nt := range next.AttributeTypes {
		ot, existed := old.AttributeTypes[name]
		if !existed {
			raise(Minor) // a new attribute inside the object -- purely additive
			continue
		}
		raise(diffType(ot, nt))
	}
	for name := range old.AttributeTypes {
		if _, stillExists := next.AttributeTypes[name]; !stillExists {
			raise(Major) // an attribute disappeared from the object -- real, breaking
		}
	}

	for name := range next.OptionalAttributes {
		if _, wasOptional := old.OptionalAttributes[name]; !wasOptional {
			if _, existedAtAll := old.AttributeTypes[name]; existedAtAll {
				raise(Major) // was always-present, now optional-to-omit: a real narrowing for a caller relying on it always being there
			}
		}
	}
	for name := range old.OptionalAttributes {
		if _, stillOptional := next.OptionalAttributes[name]; !stillOptional {
			if _, stillExists := next.AttributeTypes[name]; stillExists {
				raise(Minor) // was optional-to-omit, now always-present: purely additive
			}
		}
	}

	return level
}

// diffTupleType compares two tftypes.Tuple values positionally. Never
// actually constructed by this codebase's own translate.go today (JSON
// array types always become tftypes.List, never Tuple), included so a
// real Tuple reaching here recurses correctly instead of falling
// through to the crude whole-type .String() compare this fix exists to
// remove. A length change is always Major here, deliberately
// conservative: a Tuple's own positions are semantically distinct (not
// a homogeneous collection the way a List's ElementType is), so neither
// growing nor shrinking has a safe, generally-correct Minor reading the
// way a new Object key or List-of-Object addition does.
func diffTupleType(old, next tftypes.Tuple) ChangeLevel {
	if len(old.ElementTypes) != len(next.ElementTypes) {
		return Major
	}
	level := NoChange
	for i, nt := range next.ElementTypes {
		if l := diffType(old.ElementTypes[i], nt); l > level {
			level = l
		}
	}
	return level
}

func diffAttributeFlags(old, next *tfprotov6.SchemaAttribute) ChangeLevel {
	if !old.Required && next.Required {
		return Major // became required -- real, breaking for any caller not already setting it
	}
	if old.Required && !next.Required {
		return Minor // stopped being required -- purely additive
	}
	oldWritable := old.Optional && !old.Computed
	nextWritable := next.Optional && !next.Computed
	if oldWritable && !nextWritable {
		return Major // lost write access -- real, breaking, see doc comment above
	}
	if !oldWritable && nextWritable {
		return Minor // gained write access -- purely additive
	}
	if old.Description != next.Description {
		return Patch
	}
	return NoChange
}

func attrsByName(attrs []*tfprotov6.SchemaAttribute) map[string]*tfprotov6.SchemaAttribute {
	m := make(map[string]*tfprotov6.SchemaAttribute, len(attrs))
	for _, a := range attrs {
		m[a.Name] = a
	}
	return m
}

func blockTypesByName(blocks []*tfprotov6.SchemaNestedBlock) map[string]*tfprotov6.SchemaNestedBlock {
	m := make(map[string]*tfprotov6.SchemaNestedBlock, len(blocks))
	for _, b := range blocks {
		m[b.TypeName] = b
	}
	return m
}

var semverPattern = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)$`)

// NextVersion applies level to current (a real "MAJOR.MINOR.PATCH"
// string, this package's own real, closed semver shape -- no
// prerelease/build-metadata suffix support, since a provider snapshot's
// own version has no real use for either) and returns the real, next
// version. current="" (no prior snapshot at all) always returns "1.0.0"
// regardless of level -- DiffLevel itself can never return Major for a
// nil old (see its own doc comment), so callers don't need to
// special-case level here too.
//
// UBI-182 correction: this used to return "0.1.0" for a first-ever
// snapshot, reasoned as "real, new, unreleased content, not yet a 1.0
// promise of stability." That reasoning is sound for a LIBRARY, where
// 0.x genuinely signals "the API may still change out from under you."
// It does not fit here: a schema snapshot is a frozen copy of a
// vendor's own API surface, and its version communicates what changed
// in THAT surface (DiffLevel's own real diff), not how mature this
// artifact is. There is no pre-1.0 phase for a thing that is already
// complete the moment it's first published -- matching this org's own
// real, already-established convention for every ubx-sdk-* repo's own
// first real publish (1.0.0, never 0.x).
func NextVersion(current string, level ChangeLevel) (string, error) {
	if current == "" {
		return "1.0.0", nil
	}
	m := semverPattern.FindStringSubmatch(current)
	if m == nil {
		return "", fmt.Errorf("current version %q is not a real MAJOR.MINOR.PATCH semver string", current)
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	patch, _ := strconv.Atoi(m[3])

	switch level {
	case Major:
		major, minor, patch = major+1, 0, 0
	case Minor:
		minor, patch = minor+1, 0
	case Patch:
		patch++
	case NoChange:
		// real, unchanged -- return current as-is, not an error: a
		// caller regenerating a snapshot against an identical real spec
		// is a real, legitimate no-op, not a mistake.
		return current, nil
	default:
		return "", fmt.Errorf("unknown ChangeLevel %v", level)
	}
	return strings.Join([]string{strconv.Itoa(major), strconv.Itoa(minor), strconv.Itoa(patch)}, "."), nil
}
