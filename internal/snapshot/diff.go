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
// scalar/collection Type is compared by its own real .String() form
// (unchanged from before this fix); a NestedType recurses field-by-field
// exactly like diffBlock already does for BlockTypes; SWITCHING between
// the two shapes (flat Type <-> NestedType) on the same field name is
// always Major -- a real, structural shape change no caller's existing
// code could survive either direction.
func diffAttributeType(old, next *tfprotov6.SchemaAttribute) ChangeLevel {
	switch {
	case old.NestedType == nil && next.NestedType == nil:
		if old.Type.String() != next.Type.String() {
			return Major
		}
		return NoChange
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
