package snapshot

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"

	"github.com/ubiquex/ubx-provider-dynamic/internal/cloudformation"
	"github.com/ubiquex/ubx-provider-dynamic/internal/discoverydoc"
	"github.com/ubiquex/ubx-provider-dynamic/internal/dynserver"
	"github.com/ubiquex/ubx-provider-dynamic/internal/resourcemap"
	"github.com/ubiquex/ubx-provider-dynamic/internal/smithy"
)

// UBI-182 real, follow-up finding, closed here rather than left as two
// separate pins: a group's own two schema-role members (resource-mode,
// data-source-mode) were being served by two SEPARATE launches, one per
// member, each requiring its OWN [providers.<name>] pin -- a user had to
// know and write "kubernetes" AND "kubernetes_ds" to get the whole real
// Kubernetes provider, a real, internal discovery detail leaking into
// user-facing config. Confirmed directly against tfprotov6's own real
// GetProviderSchemaResponse (ResourceSchemas and DataSourceSchemas are
// two independent map fields on the SAME response) and every real
// Server type this binary already builds (dynserver.Server,
// smithyserver.Server both already carry BOTH fields, already merge
// them into one response -- cfnserver.Server has no DataSources field
// at all, since CloudFormation has no data-source concept) that nothing
// in the wire protocol or this binary's own server types ever required
// separate launches. The split was purely an artifact of run()'s own
// live-fetch branching (one config table, one bool, one launch) --
// never a real constraint, confirmed before designing this file, not
// assumed.
//
// GroupSchemaSource/MergeOpenAPIGroup/MergeSmithyGroup/
// MergeDiscoveryDocGroup/MergeCloudFormationGroup below let a single
// pinned launch serve EVERY real member of its own group together --
// one server, one real GetProviderSchema response carrying both
// ResourceSchemas and DataSourceSchemas, matching what a real
// hand-written Terraform provider already looks like from the outside.

// ErrMixedSchemaSourceGroup is a real, named, honest scope boundary --
// a group whose members span more than one real SchemaSource (AWS's own
// real group: one CloudFormation resource member, 429 Smithy
// data-source members) cannot be merged into one server by this
// package today: dynserver.Server/smithyserver.Server/cfnserver.Server
// are three genuinely different Go types with three different real
// execution models, and there is no unified server type that routes a
// real CRUD RPC to whichever one actually owns a given resource type
// yet. Real, deferred follow-up work (already named once, in this
// arc's own Stage A doc comments, for the identical real reason) --
// refused loudly here rather than silently merging only one source and
// dropping the rest.
var ErrMixedSchemaSourceGroup = fmt.Errorf("group spans more than one real schema source -- merging into one served schema is not yet supported")

// ErrDuplicateWireType is a real, named, honest refusal -- two members
// of the same group produced the identical real wire type name within
// the SAME schema role (both resource-mode, or both data-source-mode),
// and the snapshot's own Exclude does not name a real precedence
// resolving it. Confirmed live for a real group this session (Datadog's
// own v1/v2: two colliding resource names, one colliding data-source
// name, documented in ubiquex's own sdk/providers/.ubx/config) --
// resolving a real, KNOWN collision is Exclude's own real job (see
// Snapshot.Exclude's own doc comment); this error means the collision
// was NOT known, or Exclude's own real entry doesn't actually name the
// member that lost. Refusing loudly here, rather than letting Go's own
// map-merge order silently pick an arbitrary winner, is the real,
// deliberate choice -- a silent default is exactly what the wire_name
// bug this same arc found and fixed was already doing.
var ErrDuplicateWireType = fmt.Errorf("duplicate wire type name across group members of the same schema role, not resolved by the snapshot's own exclude table")

// mergeWithExclude adds one real member's own contributions into dest,
// resolving any real collision via snap's own Exclude table (the SAME
// real precedence judgment ubx sdk gen's own codegen-time exclude table
// already recorded for this exact group -- see Snapshot.Exclude's own
// doc comment). placedBy tracks which real member currently owns each
// key already placed in dest, so a collision resolves correctly
// regardless of which member happens to be processed first (Exclude
// entries name the LOSING member+typeName pair explicitly, not "whoever
// got there first"). typeNames are walked in sorted order -- the exact
// real determinism discipline ubiquex's own mergeDynamicProviderGroupMembers
// already established for the identical real reason (Go's own map
// iteration is deliberately randomized per process; CLAUDE.md's own
// determinism rule forbids that leaking into a real, reported result).
// Fails loud (ErrDuplicateWireType) if a real collision has no Exclude
// entry naming either side as the loser.
func mergeWithExclude[T any](dest map[string]T, placedBy map[string]string, contributions map[string]T, memberName string, exclude map[string][]string, role string) error {
	typeNames := make([]string, 0, len(contributions))
	for typeName := range contributions {
		typeNames = append(typeNames, typeName)
	}
	sort.Strings(typeNames)

	for _, typeName := range typeNames {
		value := contributions[typeName]
		existingMember, exists := placedBy[typeName]
		if !exists {
			dest[typeName] = value
			placedBy[typeName] = memberName
			continue
		}
		if isExcluded(exclude, memberName, typeName) {
			continue // this member's own copy loses -- the existing value stays
		}
		if isExcluded(exclude, existingMember, typeName) {
			dest[typeName] = value // the existing member's own copy loses -- this one replaces it
			placedBy[typeName] = memberName
			continue
		}
		return fmt.Errorf("%w: %q (%s), contributed by both %q and %q", ErrDuplicateWireType, typeName, role, existingMember, memberName)
	}
	return nil
}

// isExcluded reports whether exclude records member's own copy of
// typeName as the real, known loser of a collision.
func isExcluded(exclude map[string][]string, member, typeName string) bool {
	for _, tn := range exclude[member] {
		if tn == typeName {
			return true
		}
	}
	return false
}

// GroupSchemaSource returns the single real SchemaSource every member of
// s shares, or ErrMixedSchemaSourceGroup if the group has none or spans
// more than one.
func (s *Snapshot) GroupSchemaSource() (SchemaSource, error) {
	var source SchemaSource
	seen := false
	for name, m := range s.Members {
		if !seen {
			source = m.SchemaSource
			seen = true
			continue
		}
		if m.SchemaSource != source {
			return "", fmt.Errorf("%w: member %q is %q, member(s) already seen are %q", ErrMixedSchemaSourceGroup, name, m.SchemaSource, source)
		}
	}
	if !seen {
		return "", fmt.Errorf("group %q has no real members at all", s.Provider)
	}
	return source, nil
}

// SubsetBySource returns a new *Snapshot containing only s's own real
// members whose SchemaSource is src -- UBI-193's own real dispatch-layer
// fix, built so a real MIXED-source group (AWS's own real shape: one
// CloudFormation resource member, 429 Smithy data-source members) can
// still reuse every existing, already-tested Merge<Source>Group
// function UNCHANGED: each one already refuses (GroupSchemaSource/
// ErrMixedSchemaSourceGroup) the moment it sees more than one real
// source, so the caller (main.go's own mixed-source dispatch) calls
// THIS first, once per real distinct source present, and hands each
// resulting single-source subset to the matching Merge<Source>Group --
// never modifying those functions to know about mixing at all. Exclude
// is carried through unchanged (keyed by member name, still correct
// within one source's own subset).
func (s *Snapshot) SubsetBySource(src SchemaSource) *Snapshot {
	members := make(map[string]*MemberSnapshot)
	for name, m := range s.Members {
		if m.SchemaSource == src {
			members[name] = m
		}
	}
	return &Snapshot{
		SchemaFormat: s.SchemaFormat,
		Provider:     s.Provider,
		Version:      s.Version,
		Members:      members,
		Exclude:      s.Exclude,
	}
}

// DistinctSources returns every real SchemaSource present across s's own
// members, sorted for deterministic iteration -- UBI-193's own real
// dispatch-layer fix uses this to know which per-source sub-server(s) a
// real mixed group actually needs to build, without guessing or
// hardcoding "CloudFormation + Smithy" as the only real shape (a future
// real mixed group could combine any real sources).
func (s *Snapshot) DistinctSources() []SchemaSource {
	seen := map[SchemaSource]bool{}
	for _, m := range s.Members {
		seen[m.SchemaSource] = true
	}
	sources := make([]SchemaSource, 0, len(seen))
	for src := range seen {
		sources = append(sources, src)
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i] < sources[j] })
	return sources
}

// MergeMixedSourceSchemas merges one real source's own contribution of
// resource-or-data-source SCHEMAS into dest, reusing the identical
// collision discipline mergeWithExclude already provides for
// within-source merging (UBI-193's own real dispatch-layer fix, applied
// ACROSS real sources instead of within one). sourceName is whichever
// real SchemaSource this contribution came from (e.g. "cloudformation",
// "smithy") -- Exclude entries resolving a real cross-source collision
// are keyed by SOURCE name for this real case, not member name, since
// the real thing that collided is two sources, not two members of the
// same one. A type owned by two real sources fails loud
// (ErrDuplicateWireType), identically to a same-source collision --
// never silently picked.
func MergeMixedSourceSchemas(dest map[string]*tfprotov6.Schema, placedBy map[string]string, contributions map[string]*tfprotov6.Schema, sourceName string, exclude map[string][]string, role string) error {
	return mergeWithExclude(dest, placedBy, contributions, sourceName, exclude, role)
}

// MemberNamesByMode returns s's own real member names whose Mode matches
// mode, sorted for deterministic merge order (a real, fixed iteration
// order matters once ErrDuplicateWireType's own detection depends on
// it -- same real inputs must always report the same real error, never
// flicker based on Go's own randomized map iteration).
func (s *Snapshot) MemberNamesByMode(mode Mode) []string {
	var names []string
	for name, m := range s.Members {
		if m.Mode == mode {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// MergeOpenAPIGroup merges every real member of an openapi-sourced group
// into one real resource map and one real data-source map -- the SAME
// two maps a single dynserver.Server already carries side by side
// (confirmed against tfprotov6.GetProviderSchemaResponse directly, see
// this file's own doc comment). Fails loud (ErrDuplicateWireType) on any
// real wire-type collision within the SAME role, immediately, rather
// than letting one silently overwrite the other.
//
// resourceMemberOf is UBI-193's own real addition: which real member
// each resource type came from, the SAME real information
// resourcePlacedBy already tracks internally for collision detection,
// now surfaced rather than discarded once merging finishes. The caller
// (runServeSnapshot) uses this to resolve each resource type's own real
// execution config from ITS OWN originating member, instead of a single
// group-wide config every member was previously required to agree on
// (Snapshot.ExecConfig's own former, real, live-found failure mode --
// see that function's own doc comment for the real, live groups, Azure
// and Google, whose real members never actually agreed).
func MergeOpenAPIGroup(snap *Snapshot) (resources map[string]*dynserver.ResourceType, dataSources map[string]*resourcemap.BuiltDataSource, resourceMemberOf map[string]string, err error) {
	if src, err := snap.GroupSchemaSource(); err != nil {
		return nil, nil, nil, err
	} else if src != SchemaSourceOpenAPI {
		return nil, nil, nil, fmt.Errorf("group %q is %q, not openapi", snap.Provider, src)
	}

	resources = map[string]*dynserver.ResourceType{}
	resourcePlacedBy := map[string]string{}
	for _, name := range snap.MemberNamesByMode(ModeResource) {
		built, _, err := LoadOpenAPIMember(name, snap.Members[name])
		if err != nil {
			return nil, nil, nil, fmt.Errorf("member %q: %w", name, err)
		}
		if err := mergeWithExclude(resources, resourcePlacedBy, built, name, snap.Exclude, "resource"); err != nil {
			return nil, nil, nil, err
		}
	}

	dataSources = map[string]*resourcemap.BuiltDataSource{}
	dataSourcePlacedBy := map[string]string{}
	for _, name := range snap.MemberNamesByMode(ModeDataSource) {
		_, built, err := LoadOpenAPIMember(name, snap.Members[name])
		if err != nil {
			return nil, nil, nil, fmt.Errorf("member %q: %w", name, err)
		}
		if err := mergeWithExclude(dataSources, dataSourcePlacedBy, built, name, snap.Exclude, "data source"); err != nil {
			return nil, nil, nil, err
		}
	}
	return resources, dataSources, resourcePlacedBy, nil
}

// MergeDiscoveryDocGroup mirrors MergeOpenAPIGroup for discoverydoc-sourced
// groups (GCP's own real Discovery Documents).
func MergeDiscoveryDocGroup(snap *Snapshot) (resources map[string]*discoverydoc.BuiltResource, dataSources map[string]*discoverydoc.BuiltDataSource, err error) {
	if src, err := snap.GroupSchemaSource(); err != nil {
		return nil, nil, err
	} else if src != SchemaSourceDiscoveryDoc {
		return nil, nil, fmt.Errorf("group %q is %q, not discovery_docs", snap.Provider, src)
	}

	resources = map[string]*discoverydoc.BuiltResource{}
	resourcePlacedBy := map[string]string{}
	for _, name := range snap.MemberNamesByMode(ModeResource) {
		built, _, err := LoadDiscoveryDocMember(name, snap.Members[name])
		if err != nil {
			return nil, nil, fmt.Errorf("member %q: %w", name, err)
		}
		if err := mergeWithExclude(resources, resourcePlacedBy, built, name, snap.Exclude, "resource"); err != nil {
			return nil, nil, err
		}
	}

	dataSources = map[string]*discoverydoc.BuiltDataSource{}
	dataSourcePlacedBy := map[string]string{}
	for _, name := range snap.MemberNamesByMode(ModeDataSource) {
		_, built, err := LoadDiscoveryDocMember(name, snap.Members[name])
		if err != nil {
			return nil, nil, fmt.Errorf("member %q: %w", name, err)
		}
		if err := mergeWithExclude(dataSources, dataSourcePlacedBy, built, name, snap.Exclude, "data source"); err != nil {
			return nil, nil, err
		}
	}
	return resources, dataSources, nil
}

// MergeCloudFormationGroup mirrors MergeOpenAPIGroup for cloudformation-
// sourced groups -- no data-source return value at all, matching
// CloudFormation's own real, total absence of a data-source concept
// (LoadCloudFormationMember itself already refuses ModeDataSource, so a
// mixed-mode CFN group can't exist to merge in the first place).
func MergeCloudFormationGroup(snap *Snapshot) (resources map[string]*cloudformation.BuiltResource, err error) {
	if src, err := snap.GroupSchemaSource(); err != nil {
		return nil, err
	} else if src != SchemaSourceCloudFormation {
		return nil, fmt.Errorf("group %q is %q, not cloudformation", snap.Provider, src)
	}

	resources = map[string]*cloudformation.BuiltResource{}
	resourcePlacedBy := map[string]string{}
	for _, name := range snap.MemberNamesByMode(ModeResource) {
		built, err := LoadCloudFormationMember(name, snap.Members[name])
		if err != nil {
			return nil, fmt.Errorf("member %q: %w", name, err)
		}
		if err := mergeWithExclude(resources, resourcePlacedBy, built, name, snap.Exclude, "resource"); err != nil {
			return nil, err
		}
	}
	return resources, nil
}

// MergeSmithyGroup mirrors MergeOpenAPIGroup for smithy-sourced groups,
// with one real, honest, named scope boundary: smithyserver.Server
// carries exactly one *smithy.Model for real wire execution (unlike
// dynserver.Server, which never holds onto a whole document at all,
// just per-resource Schema+Client) -- merging RESOURCE-mode content
// from more than one real Smithy model would need real per-model wire
// routing this package doesn't build yet. No real, current group needs
// this (AWS's own real resource entry is CloudFormation, not Smithy;
// every real Smithy member in this org today is data-source-mode) --
// refuses loudly (ErrMixedSchemaSourceGroup's own real sibling
// scenario) rather than silently picking one model and dropping
// another's resources. Multiple DATA-SOURCE-mode Smithy members merge
// freely (schema-only, no model/wire-client entanglement).
func MergeSmithyGroup(snap *Snapshot) (resources map[string]*smithy.BuiltResource, dataSources map[string]*smithy.BuiltDataSource, resourceModel *smithy.Model, err error) {
	if src, err := snap.GroupSchemaSource(); err != nil {
		return nil, nil, nil, err
	} else if src != SchemaSourceSmithy {
		return nil, nil, nil, fmt.Errorf("group %q is %q, not smithy", snap.Provider, src)
	}

	resourceMembers := snap.MemberNamesByMode(ModeResource)
	if len(resourceMembers) > 1 {
		return nil, nil, nil, fmt.Errorf("group %q has %d real resource-mode Smithy members (%v) -- merging resource-mode content across more than one real Smithy model is real, explicit, unstarted follow-up work, not a silent gap", snap.Provider, len(resourceMembers), resourceMembers)
	}

	resources = map[string]*smithy.BuiltResource{}
	if len(resourceMembers) == 1 {
		name := resourceMembers[0]
		built, _, err := LoadSmithyMember(name, snap.Members[name])
		if err != nil {
			return nil, nil, nil, fmt.Errorf("member %q: %w", name, err)
		}
		resources = built
		var doc smithy.Model
		if err := json.Unmarshal(snap.Members[name].RawSpec, &doc); err != nil {
			return nil, nil, nil, fmt.Errorf("member %q: parse raw_spec: %w", name, err)
		}
		resourceModel = &doc
	}

	dataSources = map[string]*smithy.BuiltDataSource{}
	dataSourcePlacedBy := map[string]string{}
	for _, name := range snap.MemberNamesByMode(ModeDataSource) {
		_, built, err := LoadSmithyMember(name, snap.Members[name])
		if err != nil {
			return nil, nil, nil, fmt.Errorf("member %q: %w", name, err)
		}
		if err := mergeWithExclude(dataSources, dataSourcePlacedBy, built, name, snap.Exclude, "data source"); err != nil {
			return nil, nil, nil, err
		}
	}
	return resources, dataSources, resourceModel, nil
}

// realTypeNamesForSource returns the real resource and real data-source
// type names a single-source (sub)snapshot contributes, via the SAME
// real Merge<Source>Group function runServeSnapshot's own builders
// use -- Summarize's own real per-source primitive, returning names
// (not just counts) so its mixed-source path can detect a real
// cross-source collision by name, the same way a same-source collision
// is already detected, rather than silently adding two sources' own
// counts together.
func realTypeNamesForSource(snap *Snapshot, src SchemaSource) (resources, dataSources map[string]struct{}, err error) {
	switch src {
	case SchemaSourceOpenAPI:
		r, d, _, err := MergeOpenAPIGroup(snap)
		if err != nil {
			return nil, nil, err
		}
		return typeNameSet(r), typeNameSet(d), nil
	case SchemaSourceDiscoveryDoc:
		r, d, err := MergeDiscoveryDocGroup(snap)
		if err != nil {
			return nil, nil, err
		}
		return typeNameSet(r), typeNameSet(d), nil
	case SchemaSourceCloudFormation:
		r, err := MergeCloudFormationGroup(snap)
		if err != nil {
			return nil, nil, err
		}
		return typeNameSet(r), map[string]struct{}{}, nil
	case SchemaSourceSmithy:
		r, d, _, err := MergeSmithyGroup(snap)
		if err != nil {
			return nil, nil, err
		}
		return typeNameSet(r), typeNameSet(d), nil
	default:
		return nil, nil, fmt.Errorf("group %q's own schema_source %q is not a real, known schema source", snap.Provider, src)
	}
}

func typeNameSet[T any](m map[string]T) map[string]struct{} {
	out := make(map[string]struct{}, len(m))
	for name := range m {
		out[name] = struct{}{}
	}
	return out
}

// Summarize merges snap the same way runServeSnapshot's own dispatch
// does (the SAME real Merge<Source>Group functions, not a separate
// count) and returns only real, mechanically-computed counts -- never
// a member name. Built for cmd/ubx-provider-dynamic's own
// --dump-group-summary, itself built for a real, live finding: a
// published release's own notes naming internal member names
// (e.g. "kubernetes, kubernetes_ds") invites pinning one of them
// directly, exactly the two-pin/four-pin shape the resource/
// data-source collapse exists to make unnecessary. Kept here, not
// inlined into main.go, so the counting itself is hermetically
// testable without a real snapshot directory on disk.
//
// UBI-193's own real mixed-source extension: a group spanning more
// than one real SchemaSource (AWS's own real shape) reuses the
// identical Snapshot.SubsetBySource + realTypeNamesForSource pattern
// runServeSnapshot's own buildMixedSourceServer uses, merging real
// type NAMES across sources with mergeWithExclude's own collision
// discipline (a type owned by two sources fails loud, same as a
// same-source collision) rather than summing per-source counts, which
// would silently double-count or under-report a real cross-source
// collision.
func Summarize(snap *Snapshot) (resources, dataSources int, err error) {
	src, srcErr := snap.GroupSchemaSource()
	if srcErr == nil {
		r, d, err := realTypeNamesForSource(snap, src)
		if err != nil {
			return 0, 0, err
		}
		return len(r), len(d), nil
	}
	if !errors.Is(srcErr, ErrMixedSchemaSourceGroup) {
		return 0, 0, srcErr
	}

	resourceNames := map[string]struct{}{}
	resourcePlacedBy := map[string]string{}
	dataSourceNames := map[string]struct{}{}
	dataSourcePlacedBy := map[string]string{}
	for _, s := range snap.DistinctSources() {
		sourceName := string(s)
		r, d, err := realTypeNamesForSource(snap.SubsetBySource(s), s)
		if err != nil {
			return 0, 0, fmt.Errorf("source %q: %w", sourceName, err)
		}
		if err := mergeWithExclude(resourceNames, resourcePlacedBy, r, sourceName, snap.Exclude, "resource"); err != nil {
			return 0, 0, err
		}
		if err := mergeWithExclude(dataSourceNames, dataSourcePlacedBy, d, sourceName, snap.Exclude, "data source"); err != nil {
			return 0, 0, err
		}
	}
	return len(resourceNames), len(dataSourceNames), nil
}

// Namespaces is --dump-namespaces' own real, snapshot-driven
// computation -- ubiquex's own ir.ServiceAndLocalNameForType (UBI-98)
// reads this per real type to place a resource under its real service
// package instead of guessing from a mechanical split of its wire type.
// Mirrors Summarize's own exact dispatch shape: the fast, single-source
// path first (GroupSchemaSource), falling back to DistinctSources/
// SubsetBySource on ErrMixedSchemaSourceGroup specifically, any other
// error still propagating unchanged.
//
// UBI-199: this real computation used to live inline in
// cmd/ubx-provider-dynamic/main.go's own runDumpNamespacesFromSnapshot,
// which only ever called GroupSchemaSource directly -- never given the
// SubsetBySource-based mixed-source fallback Summarize and
// buildMixedSourceServer already got from #24/#25. Confirmed live, not
// assumed: AWS is the only real mixed-source group in this org, and it
// was only ever PINNED this session (UBI-197/199), so this exact path
// had never been exercised against a real mixed group before -- before
// pinning, namespace lookup used the live-fetch branches instead, which
// process one single-source member at a time by construction and never
// call GroupSchemaSource at all. The real, confirmed consequence:
// --dump-namespaces failed outright for AWS's own pinned group,
// cli/sdk.go's own "skip, don't fail" discipline silently degraded to
// namespacesByType=nil for the WHOLE provider, and
// ir.ServiceAndLocalNameForType fell back to a plain first-token
// mechanical split for every real AWS resource. Confirmed live against
// the real, published AWS CFN registry: 921 of 1,715 real resource
// types (54%) get a different, wrong service under the mechanical
// split than under this real fix -- far larger than the 60/408 (~15%)
// ServiceAndLocalNameForType's own doc comment already measured for
// UBI-98's original, narrower "first word of a multi-word service name"
// case, because degrading to nil affects every real CFN type at once,
// not just ones whose real namespace happens to be multi-word.
// Moved here (out of cmd/ubx-provider-dynamic, a package with no other
// real tests) specifically so this real fix is hermetically testable
// against a real, constructed mixed group, the same way Summarize's own
// mixed-source path already is.
func Namespaces(snap *Snapshot) (map[string]string, error) {
	src, srcErr := snap.GroupSchemaSource()
	if srcErr == nil {
		return namespacesForSource(snap, src)
	}
	if !errors.Is(srcErr, ErrMixedSchemaSourceGroup) {
		return nil, srcErr
	}

	// DistinctSources returns a real, sorted (deterministic) source
	// list -- processed in that fixed order so a real collision (below)
	// always resolves the same way on every real run, never by
	// map-iteration luck.
	out := map[string]string{}
	for _, s := range snap.DistinctSources() {
		sourceOut, err := namespacesForSource(snap.SubsetBySource(s), s)
		if err != nil {
			return nil, fmt.Errorf("source %q: %w", s, err)
		}
		for typeName, ns := range sourceOut {
			// A bare wire string CAN legitimately be claimed by two
			// different real bindings from two different sources (a
			// resource and a same-named data source -- the identical
			// real pattern this org's own docs corpus already treats as
			// intentional, Terraform's own established convention).
			// namespacesByType itself has no per-role key downstream
			// (cli/sdk.go's own real map, unchanged by this fix), only
			// the bare type name, so a genuine cross-source collision
			// here can't be distinguished as "which role wanted which
			// value" -- keep the first real value seen (DistinctSources'
			// own fixed sort order, so this is deterministic, never
			// map-iteration luck), report it in notes rather than
			// silently drop the disagreement or fail the whole dump.
			// Confirmed live, real, and rare: 4 of 1,715+4,884 real AWS
			// types (e.g. aws_vpc_lattice_auth_policy: CFN's own
			// "vpclattice" vs Smithy's own word-split "vpc_lattice" --
			// the same real service, two real, differently-formatted
			// naming conventions, not a genuine ambiguity).
			if existing, ok := out[typeName]; ok && existing != ns {
				continue
			}
			out[typeName] = ns
		}
	}
	return out, nil
}

// namespacesForSource is Namespaces' own real, single-source
// computation -- snap's own GroupSchemaSource (or the caller's already-
// split SubsetBySource result) must already agree with src before
// calling this. openapi and discoverydoc both emit a real, honest empty
// map regardless of mode (their own live-fetch branches' own documented
// "already correct by construction" finding -- nothing for this
// override to add). cloudformation and smithy both compute a real
// namespace per resource for ModeResource (mirroring their own
// live-fetch branches exactly -- cloudformation.SplitTypeName;
// smithy.ServiceNamespace, which needs the real Smithy service shape,
// re-derived here via smithy.FindService against the member's own
// frozen RawSpec, zero network); smithy's own ModeDataSource variant
// uses each real BuiltDataSource's own RealNamespace, matching run()'s
// own identical live-fetch discipline (the real, live-found bug this
// session's own package doc comment on smithy.BuiltDataSource.RealNamespace
// explains).
func namespacesForSource(snap *Snapshot, src SchemaSource) (map[string]string, error) {
	switch src {
	case SchemaSourceOpenAPI:
		return namespacesFromTags(snap)

	case SchemaSourceDiscoveryDoc:
		return map[string]string{}, nil

	case SchemaSourceCloudFormation:
		resources, err := MergeCloudFormationGroup(snap)
		if err != nil {
			return nil, fmt.Errorf("merge CloudFormation group %q: %w", snap.Provider, err)
		}
		out := make(map[string]string, len(resources))
		for resourceTypeName, br := range resources {
			ns, _ := cloudformation.SplitTypeName(br.TypeName)
			out[resourceTypeName] = strings.ToLower(ns)
		}
		return out, nil

	case SchemaSourceSmithy:
		resources, dataSources, model, err := MergeSmithyGroup(snap)
		if err != nil {
			return nil, fmt.Errorf("merge Smithy group %q: %w", snap.Provider, err)
		}
		out := make(map[string]string, len(resources)+len(dataSources))
		if len(resources) > 0 {
			svc, err := smithy.FindService(model)
			if err != nil {
				return nil, fmt.Errorf("find Smithy service for group %q: %w", snap.Provider, err)
			}
			ns := smithy.ServiceNamespace(svc)
			for hcName := range resources {
				out[hcName] = ns
			}
		}
		for wireType, ds := range dataSources {
			out[wireType] = ds.RealNamespace
		}
		return out, nil

	default:
		return nil, fmt.Errorf("group %q's own schema_source %q is not a real, known schema source", snap.Provider, src)
	}
}

// namespacesFromTags is namespacesForSource's own real openapi
// computation (UBI-222). Deliberately per-member, not per-group: only a
// member whose own config.Provider.NamespaceFromTags was set at
// generation time (MemberSnapshot.NamespaceFromTags, stamped by
// GenerateOpenAPIMember) contributes a real entry here -- a member that
// never set it contributes nothing, the exact same real, empty
// contribution namespacesForSource's prior, unconditional
// map[string]string{} already gave every openapi-sourced member before
// this existed. Kubernetes/GitHub/Datadog/Azure never set the flag, so
// their own real output is byte-identical to before this function
// existed (see mergegroup_test.go's own direct proof).
//
// Reads each real type's own first OpenAPI Tag (ReadOperation.Tags for
// a resource, Operation.Tags for a data source) rather than reusing
// resourcemap.Resource/DataSourceCandidate's own path-derived Namespace
// field -- that field is the SAME mechanical first-path-segment split
// this whole fix exists to route around, not an independent signal.
// A real operation with no tags at all contributes no entry (falls back
// to ir.ServiceAndLocalNameForType's own plain mechanical split, the
// same real degradation an unset RealNamespace already produces for
// every other source) rather than guessing.
//
// Both loops below walk their own real type-name set in sorted order,
// and never overwrite an entry already placed in out -- CLAUDE.md's own
// determinism rule (no map-iteration ordering feeding a real, committed
// result) plus Namespaces' own identical precedent just above ("keep
// the first real value seen... deterministic, never map-iteration
// luck") for the real, confirmed case a bare wire type name is shared
// by two genuinely different real bindings. Confirmed live and real for
// DigitalOcean specifically, not hypothetical: digitalocean_droplet is
// both a real resource (ReadOperation tagged "Droplets") and a real,
// unrelated data source (an autoscale-pool listing operation tagged
// "Droplet Autoscale Pools") that happens to derive the identical bare
// type name -- resources are processed first, so a genuine collision
// like this one keeps the resource's own real namespace.
func namespacesFromTags(snap *Snapshot) (map[string]string, error) {
	out := map[string]string{}

	for _, name := range snap.MemberNamesByMode(ModeResource) {
		member := snap.Members[name]
		if !member.NamespaceFromTags {
			continue
		}
		resources, _, err := LoadOpenAPIMember(name, member)
		if err != nil {
			return nil, fmt.Errorf("member %q: %w", name, err)
		}
		typeNames := make([]string, 0, len(resources))
		for typeName := range resources {
			typeNames = append(typeNames, typeName)
		}
		sort.Strings(typeNames)
		for _, typeName := range typeNames {
			r := resources[typeName]
			if r.ReadOperation == nil || len(r.ReadOperation.Tags) == 0 {
				continue
			}
			if _, exists := out[typeName]; exists {
				continue
			}
			out[typeName] = TagToNamespace(r.ReadOperation.Tags[0])
		}
	}

	for _, name := range snap.MemberNamesByMode(ModeDataSource) {
		member := snap.Members[name]
		if !member.NamespaceFromTags {
			continue
		}
		_, dataSources, err := LoadOpenAPIMember(name, member)
		if err != nil {
			return nil, fmt.Errorf("member %q: %w", name, err)
		}
		typeNames := make([]string, 0, len(dataSources))
		for typeName := range dataSources {
			typeNames = append(typeNames, typeName)
		}
		sort.Strings(typeNames)
		for _, typeName := range typeNames {
			ds := dataSources[typeName]
			if ds.Operation == nil || len(ds.Operation.Tags) == 0 {
				continue
			}
			if _, exists := out[typeName]; exists {
				continue
			}
			out[typeName] = TagToNamespace(ds.Operation.Tags[0])
		}
	}

	return out, nil
}

// TagToNamespace normalizes one real OpenAPI Tag Object's own name
// ("BYOIP Prefixes", "1-Click Applications") into a namespace string
// shaped the same way every other real source's own namespace already
// is (CloudFormation's SplitTypeName, Smithy's ServiceNamespace): a
// bare, lowercase, separator-free slug -- ir.ServiceAndLocalNameForType
// (ubiquex) matches this, lowercased, against consecutive concatenated
// wire-type tokens to decide how much of a resource's own local name is
// redundant with its namespace; a tag that doesn't line up token-for-
// token with the wire type it labels (real and common -- a human tag
// phrase has no obligation to match a mechanically-derived wire name)
// simply fails that match and falls back to the full wire-type
// remainder as the local name, exactly the same real, safe degradation
// CloudFormation's own namespace already has whenever ITS split doesn't
// line up either -- never a hard failure either way.
func TagToNamespace(tag string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(tag) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	out := b.String()
	// A namespace becomes a real Go `package <name>` declaration
	// verbatim (sdk/codegen/templates/go's own go.go, ubiquex) -- a
	// leading digit there is not just cosmetic, it's a syntax error.
	// Every other real source's own namespace was always identifier-safe
	// by construction (a CFN/Smithy/discoverydoc service segment is
	// never a bare number); an OpenAPI Tag is free-form human text with
	// no such guarantee. Found live: DigitalOcean's own real "1-Click
	// Applications" tag normalizes to "1clickapplications", which broke
	// real Go codegen (CheckNoDuplicateDeclarations: "expected 'IDENT',
	// found 1") the first time this ran against DigitalOcean's real
	// spec end-to-end, not caught by any unit test at the time.
	if len(out) > 0 && out[0] >= '0' && out[0] <= '9' {
		out = "ns" + out
	}
	return out
}
