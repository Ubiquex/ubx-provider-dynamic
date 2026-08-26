package snapshot

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"

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

// ErrInconsistentExecConfig is a real, named refusal -- a group whose
// real members disagree on BaseURL or Auth cannot pick one to serve real
// CRUD/wire execution with any real justification for preferring it
// over another; every real group surveyed while building this (Kubernetes,
// Datadog) has byte-identical BaseURL/Auth across every one of its own
// real members, so this is a real, deliberate safety check against a
// real misconfiguration, not a case any current real config actually
// hits.
var ErrInconsistentExecConfig = fmt.Errorf("group members disagree on real execution config (base_url or auth)")

// ExecConfig returns the real BaseURL/Auth/Retry every member of a
// well-formed group shares, verified, not assumed -- fails loud
// (ErrInconsistentExecConfig) if any two real members actually disagree,
// rather than silently picking one member's own config arbitrarily.
// Deterministic (sorted member names) so which member's own Retry/
// Timeouts values get used, on the rare case those two legitimately
// differ, is at least stable across runs.
func (s *Snapshot) ExecConfig() (*MemberSnapshot, error) {
	var names []string
	for name := range s.Members {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return nil, fmt.Errorf("group %q has no real members at all", s.Provider)
	}

	first := s.Members[names[0]]
	for _, name := range names[1:] {
		m := s.Members[name]
		if m.BaseURL != first.BaseURL {
			return nil, fmt.Errorf("%w: %q is %q, %q is %q", ErrInconsistentExecConfig, names[0], first.BaseURL, name, m.BaseURL)
		}
		if !reflect.DeepEqual(m.Auth, first.Auth) {
			return nil, fmt.Errorf("%w: %q and %q have different auth config", ErrInconsistentExecConfig, names[0], name)
		}
	}
	return first, nil
}

// MergeOpenAPIGroup merges every real member of an openapi-sourced group
// into one real resource map and one real data-source map -- the SAME
// two maps a single dynserver.Server already carries side by side
// (confirmed against tfprotov6.GetProviderSchemaResponse directly, see
// this file's own doc comment). Fails loud (ErrDuplicateWireType) on any
// real wire-type collision within the SAME role, immediately, rather
// than letting one silently overwrite the other.
func MergeOpenAPIGroup(snap *Snapshot) (resources map[string]*dynserver.ResourceType, dataSources map[string]*resourcemap.BuiltDataSource, err error) {
	if src, err := snap.GroupSchemaSource(); err != nil {
		return nil, nil, err
	} else if src != SchemaSourceOpenAPI {
		return nil, nil, fmt.Errorf("group %q is %q, not openapi", snap.Provider, src)
	}

	resources = map[string]*dynserver.ResourceType{}
	resourcePlacedBy := map[string]string{}
	for _, name := range snap.MemberNamesByMode(ModeResource) {
		built, _, err := LoadOpenAPIMember(name, snap.Members[name])
		if err != nil {
			return nil, nil, fmt.Errorf("member %q: %w", name, err)
		}
		if err := mergeWithExclude(resources, resourcePlacedBy, built, name, snap.Exclude, "resource"); err != nil {
			return nil, nil, err
		}
	}

	dataSources = map[string]*resourcemap.BuiltDataSource{}
	dataSourcePlacedBy := map[string]string{}
	for _, name := range snap.MemberNamesByMode(ModeDataSource) {
		_, built, err := LoadOpenAPIMember(name, snap.Members[name])
		if err != nil {
			return nil, nil, fmt.Errorf("member %q: %w", name, err)
		}
		if err := mergeWithExclude(dataSources, dataSourcePlacedBy, built, name, snap.Exclude, "data source"); err != nil {
			return nil, nil, err
		}
	}
	return resources, dataSources, nil
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
