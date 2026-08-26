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
	group, err := AssembleGroup("widgetco", nil, map[string]*MemberSnapshot{"widgetco": resourceMember, "widgetco_ds": dsMember}, map[string]ChangeLevel{"widgetco": Minor, "widgetco_ds": Minor})
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

// TestMergeOpenAPIGroup_DuplicateResourceWireType_RealFailLoud is the
// real, direct proof of the founder's own explicit requirement: a
// group with a genuine collision (Datadog's own real v1/v2 shape, not
// hypothetical) must refuse loudly, never let one member's own content
// silently overwrite another's.
func TestMergeOpenAPIGroup_DuplicateResourceWireType_RealFailLoud(t *testing.T) {
	execCfg := config.Provider{BaseURL: "https://api.widgetco.example"}
	memberA, _, _, err := GenerateOpenAPIMember("widgetco", "widgetco", serveSpec(t, widgetSpecV1), ModeResource, execCfg, nil)
	if err != nil {
		t.Fatalf("generate member A: %v", err)
	}
	// memberB is a real, SEPARATE member sharing the SAME real wireName
	// as memberA (mirroring Datadog's own real v1/v2 shape, where BOTH
	// members carry wire_name = "datadog") and generated from the SAME
	// real spec, so it produces the IDENTICAL real wire type name
	// "widgetco_widget" -- a genuine, real collision, not a contrived
	// edge case.
	memberB, _, _, err := GenerateOpenAPIMember("widgetco_v2", "widgetco", serveSpec(t, widgetSpecV1), ModeResource, execCfg, nil)
	if err != nil {
		t.Fatalf("generate member B: %v", err)
	}
	group, err := AssembleGroup("widgetco", nil, map[string]*MemberSnapshot{"widgetco": memberA, "widgetco_v2": memberB}, map[string]ChangeLevel{"widgetco": Minor, "widgetco_v2": Minor})
	if err != nil {
		t.Fatalf("AssembleGroup: %v", err)
	}

	_, _, err = MergeOpenAPIGroup(group)
	if err == nil {
		t.Fatal("expected a real error for two resource-mode members producing the identical wire type name")
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
