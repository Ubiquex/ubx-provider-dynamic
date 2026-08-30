package snapshot

import (
	"errors"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"

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

	resources, dataSources, _, err := MergeOpenAPIGroup(group)
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

// TestMergeOpenAPIGroup_ResourceMemberOf_MatchesRealDivergentOrigins is
// UBI-193's own real, direct proof: resourceMemberOf correctly
// attributes each real resource type to its own real originating
// member, even when two real members genuinely disagree on BaseURL --
// exactly Azure's and Google's own real, live shape (Snapshot.
// ExecConfig's own former, removed strict-equality check would have
// refused this group outright before any RPC could even be served).
func TestMergeOpenAPIGroup_ResourceMemberOf_MatchesRealDivergentOrigins(t *testing.T) {
	memberA, _, _, err := GenerateOpenAPIMember("widgetco_compute", "widgetco_compute", serveSpec(t, widgetSpecV1), ModeResource, config.Provider{BaseURL: "https://compute.widgetco.example"}, nil)
	if err != nil {
		t.Fatalf("generate member A: %v", err)
	}
	memberB, _, _, err := GenerateOpenAPIMember("widgetco_storage", "widgetco_storage", serveSpec(t, widgetSpecV1), ModeResource, config.Provider{BaseURL: "https://storage.widgetco.example"}, nil)
	if err != nil {
		t.Fatalf("generate member B: %v", err)
	}
	group, err := AssembleGroup("widgetco", nil, map[string]*MemberSnapshot{"widgetco_compute": memberA, "widgetco_storage": memberB}, map[string]ChangeLevel{"widgetco_compute": Minor, "widgetco_storage": Minor}, nil)
	if err != nil {
		t.Fatalf("AssembleGroup: %v", err)
	}

	resources, _, resourceMemberOf, err := MergeOpenAPIGroup(group)
	if err != nil {
		t.Fatalf("MergeOpenAPIGroup on a group with genuinely different real member BaseURLs: %v", err)
	}
	if len(resources) == 0 {
		t.Fatal("merged group has zero resources")
	}

	checked := 0
	for typeName := range resources {
		memberName, ok := resourceMemberOf[typeName]
		if !ok {
			t.Errorf("resourceMemberOf has no entry for real type %q", typeName)
			continue
		}
		// Both real members share the identical real spec but distinct
		// real wireNames, so every real type name's own prefix directly
		// names which member it came from -- a real, independent check
		// that resourceMemberOf's own attribution is correct, not just
		// present.
		var wantPrefix string
		switch {
		case strings.HasPrefix(typeName, "widgetco_compute_"):
			wantPrefix = "widgetco_compute"
		case strings.HasPrefix(typeName, "widgetco_storage_"):
			wantPrefix = "widgetco_storage"
		default:
			t.Errorf("real type %q has neither expected real prefix", typeName)
			continue
		}
		if memberName != wantPrefix {
			t.Errorf("type %q: resourceMemberOf says member %q, but its own real wire prefix says %q", typeName, memberName, wantPrefix)
		}
		if got := group.Members[memberName].BaseURL; got == "" {
			t.Errorf("member %q has no real BaseURL", memberName)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no real resource types were actually checked")
	}
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

	_, _, _, err := MergeOpenAPIGroup(group)
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

	resources, _, _, err := MergeOpenAPIGroup(group)
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

	_, _, _, err := MergeOpenAPIGroup(group)
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

	if _, _, _, err := MergeOpenAPIGroup(group); !errors.Is(err, ErrMixedSchemaSourceGroup) {
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

// TestSummarize_RealTwoMemberGroup_MatchesMergeOpenAPIGroup proves
// Summarize's own counts come from the SAME real merge every pinned
// resolution already goes through, not a separate, possibly-drifting
// count -- and that it returns real numbers, never a member name.
func TestSummarize_RealTwoMemberGroup_MatchesMergeOpenAPIGroup(t *testing.T) {
	group := realKubernetesLikeGroup(t)
	wantResources, wantDataSources, _, err := MergeOpenAPIGroup(group)
	if err != nil {
		t.Fatalf("MergeOpenAPIGroup: %v", err)
	}

	resources, dataSources, err := Summarize(group)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if resources != len(wantResources) {
		t.Errorf("Summarize resources = %d, want %d (MergeOpenAPIGroup's own real count)", resources, len(wantResources))
	}
	if dataSources != len(wantDataSources) {
		t.Errorf("Summarize data sources = %d, want %d (MergeOpenAPIGroup's own real count)", dataSources, len(wantDataSources))
	}
}

// TestSummarize_RealCollisionResolvedByExclude proves Summarize counts
// AFTER exclude resolution, not before -- the real, collision-losing
// copy must not be double-counted.
func TestSummarize_RealCollisionResolvedByExclude(t *testing.T) {
	group := realDatadogLikeCollidingGroup(t, map[string][]string{
		"widgetco_v2": {"widgetco_widget"},
	})
	resources, _, err := Summarize(group)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	want, _, _, err := MergeOpenAPIGroup(group)
	if err != nil {
		t.Fatalf("MergeOpenAPIGroup: %v", err)
	}
	if resources != len(want) {
		t.Errorf("Summarize resources = %d, want %d", resources, len(want))
	}
}

// realAWSLikeMixedGroup builds a group shaped exactly like AWS's own
// real group (repo_name "aws"): one CloudFormation resource-mode
// member, one Smithy data-source-mode member -- the only real mixed-
// source shape among this org's six real providers, confirmed by
// checking every real group's own member composition directly before
// building UBI-193's own dispatch layer.
func realAWSLikeMixedGroup(t *testing.T) *Snapshot {
	t.Helper()
	return &Snapshot{
		Provider: "aws",
		Version:  "1.0.0",
		Members: map[string]*MemberSnapshot{
			"aws":        {SchemaSource: SchemaSourceCloudFormation, Mode: ModeResource},
			"aws_data_x": {SchemaSource: SchemaSourceSmithy, Mode: ModeDataSource},
			"aws_data_y": {SchemaSource: SchemaSourceSmithy, Mode: ModeDataSource},
		},
	}
}

// TestDistinctSources_RealMixedGroup_ReturnsBothSourcesSorted proves
// DistinctSources reports every real source present, sorted
// deterministically -- the dispatch layer's own real entry point for
// deciding how many per-source sub-servers a real mixed group needs.
func TestDistinctSources_RealMixedGroup_ReturnsBothSourcesSorted(t *testing.T) {
	group := realAWSLikeMixedGroup(t)
	sources := group.DistinctSources()
	want := []SchemaSource{SchemaSourceCloudFormation, SchemaSourceSmithy}
	if len(sources) != len(want) {
		t.Fatalf("DistinctSources = %v, want %v", sources, want)
	}
	for i := range want {
		if sources[i] != want[i] {
			t.Errorf("DistinctSources[%d] = %q, want %q", i, sources[i], want[i])
		}
	}
}

// TestDistinctSources_RealSingleSourceGroup_ReturnsOne proves a group
// that ISN'T mixed reports exactly one source, matching
// GroupSchemaSource's own real success case.
func TestDistinctSources_RealSingleSourceGroup_ReturnsOne(t *testing.T) {
	group := realKubernetesLikeGroup(t)
	sources := group.DistinctSources()
	if len(sources) != 1 || sources[0] != SchemaSourceOpenAPI {
		t.Errorf("DistinctSources = %v, want [openapi]", sources)
	}
}

// TestSubsetBySource_RealMixedGroup_SplitsCleanly proves SubsetBySource
// partitions a real mixed group into real single-source subsets, each
// one accepted by GroupSchemaSource without error -- the exact real
// precondition every Merge<Source>Group function still requires,
// unchanged, per UBI-193's own dispatch-layer design (SubsetBySource is
// called BEFORE handing off to a Merge<Source>Group, never inside one).
func TestSubsetBySource_RealMixedGroup_SplitsCleanly(t *testing.T) {
	group := realAWSLikeMixedGroup(t)

	cfnSubset := group.SubsetBySource(SchemaSourceCloudFormation)
	if len(cfnSubset.Members) != 1 {
		t.Fatalf("cfn subset has %d members, want 1", len(cfnSubset.Members))
	}
	if src, err := cfnSubset.GroupSchemaSource(); err != nil || src != SchemaSourceCloudFormation {
		t.Errorf("cfn subset GroupSchemaSource = %q, %v, want cloudformation, nil", src, err)
	}

	smithySubset := group.SubsetBySource(SchemaSourceSmithy)
	if len(smithySubset.Members) != 2 {
		t.Fatalf("smithy subset has %d members, want 2", len(smithySubset.Members))
	}
	if src, err := smithySubset.GroupSchemaSource(); err != nil || src != SchemaSourceSmithy {
		t.Errorf("smithy subset GroupSchemaSource = %q, %v, want smithy, nil", src, err)
	}

	if cfnSubset.Provider != group.Provider || smithySubset.Provider != group.Provider {
		t.Error("SubsetBySource must preserve the real group's own Provider identity")
	}
}

// TestMergeMixedSourceSchemas_RealTwoSourceGroup_MergesCleanly proves
// two real sources' own contributions merge into one real schema map
// when their type names don't collide -- the real, common AWS-shaped
// case (CloudFormation's "aws_*" resource types never share a wire name
// with Smithy's "aws_data_*" data-source types).
func TestMergeMixedSourceSchemas_RealTwoSourceGroup_MergesCleanly(t *testing.T) {
	dest := map[string]*tfprotov6.Schema{}
	placedBy := map[string]string{}

	cfnContribution := map[string]*tfprotov6.Schema{"aws_s3_bucket": {}}
	if err := MergeMixedSourceSchemas(dest, placedBy, cfnContribution, "cloudformation", nil, "resource"); err != nil {
		t.Fatalf("merge cloudformation contribution: %v", err)
	}

	smithyContribution := map[string]*tfprotov6.Schema{"aws_data_ec2_instance": {}}
	if err := MergeMixedSourceSchemas(dest, placedBy, smithyContribution, "smithy", nil, "resource"); err != nil {
		t.Fatalf("merge smithy contribution: %v", err)
	}

	if len(dest) != 2 {
		t.Fatalf("merged dest has %d entries, want 2", len(dest))
	}
	if placedBy["aws_s3_bucket"] != "cloudformation" {
		t.Errorf("aws_s3_bucket placedBy = %q, want cloudformation", placedBy["aws_s3_bucket"])
	}
	if placedBy["aws_data_ec2_instance"] != "smithy" {
		t.Errorf("aws_data_ec2_instance placedBy = %q, want smithy", placedBy["aws_data_ec2_instance"])
	}
}

// realAWSLikeGeneratedMixedGroup builds a group shaped exactly like
// AWS's own real group, using REAL generated members (not the plain
// struct literals realAWSLikeMixedGroup uses, which have empty
// Resources maps unsuited to Summarize's own real
// realTypeNamesForSource calls): one real CloudFormation resource
// member (the same real widget fixture GenerateCloudFormationMember's
// own tests use) and one real Smithy data-source member (the same
// real, already-committed SQS fixture GenerateSmithyMember's own tests
// use).
func realAWSLikeGeneratedMixedGroup(t *testing.T) *Snapshot {
	t.Helper()
	cfnURL := serveCFNZip(t, map[string]string{"aws-widget-thing.json": widgetCFNSchemaV1})
	cfnMember, _, cfnLevel, err := GenerateCloudFormationMember("aws", cfnURL, ModeResource, config.Provider{BaseURL: "https://cloudcontrolapi.us-east-1.amazonaws.com"}, nil)
	if err != nil {
		t.Fatalf("GenerateCloudFormationMember: %v", err)
	}
	smithyURL := serveBytes(t, realSQSSmithyModel(t))
	smithyMember, _, smithyLevel, err := GenerateSmithyMember("aws_data_sqs", "aws", smithyURL, "AmazonSQS", ModeDataSource, "", config.Provider{BaseURL: "https://sqs.us-east-1.amazonaws.com"}, nil)
	if err != nil {
		t.Fatalf("GenerateSmithyMember: %v", err)
	}
	group, err := AssembleGroup("aws", nil,
		map[string]*MemberSnapshot{"aws": cfnMember, "aws_data_sqs": smithyMember},
		map[string]ChangeLevel{"aws": cfnLevel, "aws_data_sqs": smithyLevel},
		nil)
	if err != nil {
		t.Fatalf("AssembleGroup: %v", err)
	}
	return group
}

// TestSummarize_RealMixedGroup_CountsAcrossBothSources proves
// Summarize's own mixed-source path merges real type names across
// CloudFormation and Smithy the same way buildMixedSourceServer does,
// returning real, nonzero, collision-checked counts spanning both real
// sources -- not refusing (ErrMixedSchemaSourceGroup), and not just
// counting the first source it happens to see.
func TestSummarize_RealMixedGroup_CountsAcrossBothSources(t *testing.T) {
	group := realAWSLikeGeneratedMixedGroup(t)

	cfnResources, err := MergeCloudFormationGroup(group.SubsetBySource(SchemaSourceCloudFormation))
	if err != nil {
		t.Fatalf("MergeCloudFormationGroup: %v", err)
	}
	_, smithyDataSources, _, err := MergeSmithyGroup(group.SubsetBySource(SchemaSourceSmithy))
	if err != nil {
		t.Fatalf("MergeSmithyGroup: %v", err)
	}

	resources, dataSources, err := Summarize(group)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if resources != len(cfnResources) {
		t.Errorf("Summarize resources = %d, want %d (CloudFormation's own real count)", resources, len(cfnResources))
	}
	if dataSources != len(smithyDataSources) {
		t.Errorf("Summarize data sources = %d, want %d (Smithy's own real count)", dataSources, len(smithyDataSources))
	}
	if resources == 0 || dataSources == 0 {
		t.Fatalf("expected real, nonzero counts on both sides, got resources=%d dataSources=%d", resources, dataSources)
	}
}

// TestMergeMixedSourceSchemas_RealCollision_FailsLoud proves a type
// name owned by two real sources fails loud (ErrDuplicateWireType),
// identically to a same-source collision -- UBI-193's own explicit
// instruction: "a type owned by two sources should fail loud, same as
// today."
// TestNamespaces_SingleSource_UsesFastPath proves Namespaces' own fast
// path (GroupSchemaSource succeeds, no mixed-source fallback needed)
// still returns real, non-empty namespaces for an ordinary single-
// source group -- the shape every real provider except AWS has.
func TestNamespaces_SingleSource_UsesFastPath(t *testing.T) {
	group := realKubernetesLikeGroup(t)
	out, err := Namespaces(group)
	if err != nil {
		t.Fatalf("Namespaces: %v", err)
	}
	// realKubernetesLikeGroup's own members never set
	// NamespaceFromTags, so openapi's real namespaces stay a real,
	// honest empty map here (UBI-222's own namespacesFromTags,
	// unopted-in) -- the real assertion is that the single-source path
	// runs at all and returns successfully, not ErrMixedSchemaSourceGroup.
	if out == nil {
		t.Fatal("expected a real, non-nil map from the single-source path")
	}
	if len(out) != 0 {
		t.Errorf("expected an empty map (NamespaceFromTags unset on every member), got %v", out)
	}
}

// TestNamespacesForSource_OpenAPI_TagsOptIn is UBI-222's own real, direct
// proof: a member with NamespaceFromTags set derives a real namespace
// from its own operations' first real OpenAPI Tag -- for both a
// resource (ReadOperation.Tags) and a data source (Operation.Tags) --
// where before this existed namespacesForSource returned a real, honest
// empty map for every openapi-sourced member unconditionally.
func TestNamespacesForSource_OpenAPI_TagsOptIn(t *testing.T) {
	execCfg := config.Provider{BaseURL: "https://api.widgetco.example", NamespaceFromTags: true}
	resourceMember, _, _, err := GenerateOpenAPIMember("widgetco", "widgetco", serveSpec(t, widgetSpecWithTags), ModeResource, execCfg, nil)
	if err != nil {
		t.Fatalf("generate resource member: %v", err)
	}
	dsMember, _, _, err := GenerateOpenAPIMember("widgetco_ds", "widgetco", serveSpec(t, widgetSpecWithTags), ModeDataSource, execCfg, nil)
	if err != nil {
		t.Fatalf("generate data-source member: %v", err)
	}
	group, err := AssembleGroup("widgetco", nil, map[string]*MemberSnapshot{"widgetco": resourceMember, "widgetco_ds": dsMember}, map[string]ChangeLevel{"widgetco": Minor, "widgetco_ds": Minor}, nil)
	if err != nil {
		t.Fatalf("AssembleGroup: %v", err)
	}

	out, err := Namespaces(group)
	if err != nil {
		t.Fatalf("Namespaces: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected exactly 2 real namespace entries (one resource, one data source), got %d: %v", len(out), out)
	}
	values := map[string]bool{}
	for _, ns := range out {
		values[ns] = true
	}
	if !values["widgetstorage"] {
		t.Errorf(`expected a real entry with namespace "widgetstorage" (from the resource's own "Widget Storage" tag), got %v`, out)
	}
	if !values["reports"] {
		t.Errorf(`expected a real entry with namespace "reports" (from the data source's own "Reports" tag), got %v`, out)
	}
}

// TestNamespacesForSource_OpenAPI_BareNameCollision_ResourceWinsDeterministically
// is UBI-222's own real regression test for a real, live bug: the first
// version of namespacesFromTags assigned both loops' own contributions
// via unsorted Go map iteration, so a real bare-name collision between a
// resource and an unrelated data source (DigitalOcean's own real
// digitalocean_droplet: a real Droplet resource AND a real, different
// Droplet Autoscale Pools data source both derive that identical bare
// type name) resolved to whichever one Go's own randomized map order
// happened to visit last -- a real violation of this project's own
// determinism rule (CLAUDE.md), caught only by running the fix against
// DigitalOcean's own real spec, not by any test at the time. Runs the
// real fix repeatedly to prove the sorted, first-wins resolution is
// actually deterministic, not merely correct on one lucky run.
func TestNamespacesForSource_OpenAPI_BareNameCollision_ResourceWinsDeterministically(t *testing.T) {
	execCfg := config.Provider{BaseURL: "https://api.widgetco.example", NamespaceFromTags: true}
	resourceMember, _, _, err := GenerateOpenAPIMember("widgetco", "widgetco", serveSpec(t, widgetSpecWithTagCollision), ModeResource, execCfg, nil)
	if err != nil {
		t.Fatalf("generate resource member: %v", err)
	}
	dsMember, _, _, err := GenerateOpenAPIMember("widgetco_ds", "widgetco", serveSpec(t, widgetSpecWithTagCollision), ModeDataSource, execCfg, nil)
	if err != nil {
		t.Fatalf("generate data-source member: %v", err)
	}
	group, err := AssembleGroup("widgetco", nil, map[string]*MemberSnapshot{"widgetco": resourceMember, "widgetco_ds": dsMember}, map[string]ChangeLevel{"widgetco": Minor, "widgetco_ds": Minor}, nil)
	if err != nil {
		t.Fatalf("AssembleGroup: %v", err)
	}

	for i := 0; i < 20; i++ {
		out, err := Namespaces(group)
		if err != nil {
			t.Fatalf("Namespaces (run %d): %v", i, err)
		}
		if got, want := out["widgetco_widget"], "widgetstorage"; got != want {
			t.Fatalf("run %d: widgetco_widget namespace = %q, want %q (the resource's own tag -- the colliding data source's own \"Widget Aliases\" tag must never win)", i, got, want)
		}
	}
}

// TestTagToNamespace proves the real normalization tagToNamespace
// applies to a raw OpenAPI Tag name -- lowercased, separator-free --
// against real, representative DigitalOcean tag names (UBI-222).
func TestTagToNamespace(t *testing.T) {
	cases := map[string]string{
		"BYOIP Prefixes":       "byoipprefixes",
		"1-Click Applications": "ns1clickapplications",
		"Block Storage":        "blockstorage",
		"Container Registry":   "containerregistry",
	}
	for tag, want := range cases {
		if got := tagToNamespace(tag); got != want {
			t.Errorf("tagToNamespace(%q) = %q, want %q", tag, got, want)
		}
	}
}

// TestNamespaces_RealMixedGroup_ComputesBothSources is UBI-199's own
// real regression test: before this fix, calling Namespaces (formerly
// inline in cmd/ubx-provider-dynamic's own runDumpNamespacesFromSnapshot)
// against a real mixed group failed outright with
// ErrMixedSchemaSourceGroup -- confirmed live against the real,
// published AWS snapshot before writing this fix, not assumed. Proves
// both the CloudFormation resource and the Smithy data source get a
// real, non-empty, correctly-attributed namespace, the same real shape
// buildMixedSourceServer and Summarize already prove for schema
// content and counts respectively.
func TestNamespaces_RealMixedGroup_ComputesBothSources(t *testing.T) {
	group := realAWSLikeGeneratedMixedGroup(t)

	out, err := Namespaces(group)
	if err != nil {
		t.Fatalf("Namespaces: %v", err)
	}

	cfnResources, err := MergeCloudFormationGroup(group.SubsetBySource(SchemaSourceCloudFormation))
	if err != nil {
		t.Fatalf("MergeCloudFormationGroup: %v", err)
	}
	for typeName := range cfnResources {
		ns, ok := out[typeName]
		if !ok || ns == "" {
			t.Errorf("CloudFormation type %q has no real namespace in Namespaces' own output: %q", typeName, ns)
		}
	}

	_, smithyDataSources, _, err := MergeSmithyGroup(group.SubsetBySource(SchemaSourceSmithy))
	if err != nil {
		t.Fatalf("MergeSmithyGroup: %v", err)
	}
	for typeName := range smithyDataSources {
		ns, ok := out[typeName]
		if !ok || ns == "" {
			t.Errorf("Smithy data source %q has no real namespace in Namespaces' own output: %q", typeName, ns)
		}
	}

	if len(cfnResources) == 0 || len(smithyDataSources) == 0 {
		t.Fatalf("expected real, nonzero types on both sides, got cfn=%d smithy=%d", len(cfnResources), len(smithyDataSources))
	}
}

func TestMergeMixedSourceSchemas_RealCollision_FailsLoud(t *testing.T) {
	dest := map[string]*tfprotov6.Schema{}
	placedBy := map[string]string{}

	first := map[string]*tfprotov6.Schema{"aws_widget": {}}
	if err := MergeMixedSourceSchemas(dest, placedBy, first, "cloudformation", nil, "resource"); err != nil {
		t.Fatalf("merge first contribution: %v", err)
	}

	second := map[string]*tfprotov6.Schema{"aws_widget": {}}
	err := MergeMixedSourceSchemas(dest, placedBy, second, "smithy", nil, "resource")
	if err == nil {
		t.Fatal("expected a real error for a type name owned by two real sources with no Exclude entry")
	}
	if !errors.Is(err, ErrDuplicateWireType) {
		t.Errorf("error doesn't wrap ErrDuplicateWireType: %v", err)
	}
}
