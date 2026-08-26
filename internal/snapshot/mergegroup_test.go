package snapshot

import (
	"errors"
	"testing"

	"github.com/ubiquex/ubx-provider-dynamic/internal/config"
)

func realKubernetesLikeGroup(t *testing.T) *Snapshot {
	t.Helper()
	execCfg := config.Provider{BaseURL: "https://api.widgetco.example"}
	// Both members share the SAME real wireName ("widgetco") despite
	// living under different table keys -- the exact real shape
	// Kubernetes' own kubernetes/kubernetes_ds pair already uses
	// (wire_name = "kubernetes" on the _ds sibling specifically so its
	// own generated type names carry the plain "kubernetes_" prefix, not
	// a version/mode-revealing one) -- proves the merge is a genuine,
	// real no-collision case, not one that happens to avoid collision
	// only because the two members were generated under different,
	// unrealistic identities.
	resourceMember, _, _, err := GenerateOpenAPIMember("widgetco", "widgetco", serveSpec(t, widgetSpecV1), ModeResource, execCfg, nil)
	if err != nil {
		t.Fatalf("generate resource member: %v", err)
	}
	dsMember, _, _, err := GenerateOpenAPIMember("widgetco_ds", "widgetco", serveSpec(t, widgetSpecV1), ModeDataSource, execCfg, nil)
	if err != nil {
		t.Fatalf("generate data-source member: %v", err)
	}
	group, err := AssembleGroup("widgetco", nil, map[string]*MemberSnapshot{"widgetco": resourceMember, "widgetco_ds": dsMember}, map[string]ChangeLevel{"widgetco": Minor, "widgetco_ds": Minor}, nil)
	if err != nil {
		t.Fatalf("AssembleGroup: %v", err)
	}
	return group
}

// TestMergeOpenAPIGroup_RealTwoMemberGroup_MergesCleanly is the real,
// direct proof behind UBI-182's own single-pin fix: a group shaped
// exactly like Kubernetes' own real group (one resource-mode member, one
// data-source-mode member, zero real collisions between them) merges
// into ONE real resource map and ONE real data-source map, no error.
func TestMergeOpenAPIGroup_RealTwoMemberGroup_MergesCleanly(t *testing.T) {
	group := realKubernetesLikeGroup(t)

	resources, dataSources, err := MergeOpenAPIGroup(group)
	if err != nil {
		t.Fatalf("MergeOpenAPIGroup: %v", err)
	}
	if len(resources) == 0 {
		t.Fatal("merged group has zero resources")
	}
	if len(dataSources) == 0 {
		t.Fatal("merged group has zero data sources")
	}
	t.Logf("merged: %d resources, %d data sources", len(resources), len(dataSources))
}

// realDatadogLikeCollidingGroup builds a real, two-member group that
// genuinely collides on one resource wire type name -- the same real
// shape Datadog's own live v1/v2 group has (both members share
// wireName "widgetco", both generated from the identical real spec, so
// both produce the identical real "widgetco_widget" type name).
func realDatadogLikeCollidingGroup(t *testing.T, exclude map[string][]string) *Snapshot {
	t.Helper()
	execCfg := config.Provider{BaseURL: "https://api.widgetco.example"}
	memberA, _, _, err := GenerateOpenAPIMember("widgetco", "widgetco", serveSpec(t, widgetSpecV1), ModeResource, execCfg, nil)
	if err != nil {
		t.Fatalf("generate member A: %v", err)
	}
	memberB, _, _, err := GenerateOpenAPIMember("widgetco_v2", "widgetco", serveSpec(t, widgetSpecV1), ModeResource, execCfg, nil)
	if err != nil {
		t.Fatalf("generate member B: %v", err)
	}
	group, err := AssembleGroup("widgetco", nil, map[string]*MemberSnapshot{"widgetco": memberA, "widgetco_v2": memberB}, map[string]ChangeLevel{"widgetco": Minor, "widgetco_v2": Minor}, exclude)
	if err != nil {
		t.Fatalf("AssembleGroup: %v", err)
	}
	return group
}

// TestMergeOpenAPIGroup_DuplicateResourceWireType_RealFailLoud is the
// real, direct proof of the founder's own explicit requirement: a
// group with a genuine collision (Datadog's own real v1/v2 shape, not
// hypothetical) that the snapshot's own Exclude does NOT resolve must
// refuse loudly, never let one member's own content silently overwrite
// another's -- the exact real failure mode the wire_name bug was
// producing before Exclude existed at all.
func TestMergeOpenAPIGroup_DuplicateResourceWireType_RealFailLoud(t *testing.T) {
	group := realDatadogLikeCollidingGroup(t, nil)

	_, _, err := MergeOpenAPIGroup(group)
	if err == nil {
		t.Fatal("expected a real error for two resource-mode members producing the identical wire type name with no Exclude entry")
	}
	if !errors.Is(err, ErrDuplicateWireType) {
		t.Errorf("error doesn't wrap ErrDuplicateWireType: %v", err)
	}
}

// TestMergeOpenAPIGroup_DuplicateResourceWireType_ResolvedByExclude is
// the founder's own explicit design, made real: the SAME real collision
// as the fail-loud test above, but this time the snapshot's own Exclude
// records the real precedence judgment (widgetco_v2's own copy loses,
// mirroring Datadog's own real "v1 wins" rule) -- the merge must now
// succeed, keeping memberA's own value under the contested type name.
func TestMergeOpenAPIGroup_DuplicateResourceWireType_ResolvedByExclude(t *testing.T) {
	group := realDatadogLikeCollidingGroup(t, map[string][]string{
		"widgetco_v2": {"widgetco_widget"},
	})

	resources, _, err := MergeOpenAPIGroup(group)
	if err != nil {
		t.Fatalf("MergeOpenAPIGroup with a real Exclude entry resolving the collision: %v", err)
	}
	if _, ok := resources["widgetco_widget"]; !ok {
		t.Fatal("expected widgetco_widget to survive the merge (memberA's own copy, per Exclude)")
	}
}

// TestMergeOpenAPIGroup_ExcludeOnWrongMember_StillFailsLoud proves
// Exclude is checked by REAL (member, typeName) pair, not just typeName
// alone -- excluding a DIFFERENT member for the SAME contested type name
// does not accidentally resolve this real collision.
func TestMergeOpenAPIGroup_ExcludeOnWrongMember_StillFailsLoud(t *testing.T) {
	group := realDatadogLikeCollidingGroup(t, map[string][]string{
		"widgetco_v2": {"some_other_type_name_entirely"},
	})

	_, _, err := MergeOpenAPIGroup(group)
	if err == nil {
		t.Fatal("expected a real error -- the real Exclude entry names a different type name, not the one that actually collided")
	}
	if !errors.Is(err, ErrDuplicateWireType) {
		t.Errorf("error doesn't wrap ErrDuplicateWireType: %v", err)
	}
}

// TestGroupSchemaSource_MixedSources_RealFailLoud proves a group
// spanning more than one real SchemaSource (AWS's own real shape: one
// CloudFormation resource member, many Smithy data-source members)
// refuses loudly rather than silently merging only one source.
func TestGroupSchemaSource_MixedSources_RealFailLoud(t *testing.T) {
	group := &Snapshot{
		Provider: "aws",
		Version:  "1.0.0",
		Members: map[string]*MemberSnapshot{
			"aws":        {SchemaSource: SchemaSourceCloudFormation, Mode: ModeResource},
			"aws_data_x": {SchemaSource: SchemaSourceSmithy, Mode: ModeDataSource},
		},
	}
	_, err := group.GroupSchemaSource()
	if err == nil {
		t.Fatal("expected a real error for a group spanning more than one schema source")
	}
	if !errors.Is(err, ErrMixedSchemaSourceGroup) {
		t.Errorf("error doesn't wrap ErrMixedSchemaSourceGroup: %v", err)
	}

	if _, _, err := MergeOpenAPIGroup(group); !errors.Is(err, ErrMixedSchemaSourceGroup) {
		t.Errorf("MergeOpenAPIGroup on a mixed-source group: got %v, want ErrMixedSchemaSourceGroup", err)
	}
}

func TestGroupSchemaSource_SingleSource_RealSuccess(t *testing.T) {
	group := realKubernetesLikeGroup(t)
	src, err := group.GroupSchemaSource()
	if err != nil {
		t.Fatalf("GroupSchemaSource: %v", err)
	}
	if src != SchemaSourceOpenAPI {
		t.Errorf("GroupSchemaSource = %q, want openapi", src)
	}
}
