package resourcemap

import (
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

// buildGitHubShapedDoc mirrors the real structural quirk this package was
// built to handle: repositories are READ at /repos/{owner}/{repo} but
// CREATED at /orgs/{org}/repos -- two entirely different path prefixes,
// connected only by both returning the same "repository" component schema.
func buildGitHubShapedDoc() *openapi3.T {
	repoSchema := openapi3.NewObjectSchema().
		WithProperty("id", openapi3.NewIntegerSchema()).
		WithProperty("name", openapi3.NewStringSchema())
	repoSchema.Required = []string{"name"}
	repoSchemaRef := openapi3.NewSchemaRef("#/components/schemas/repository", repoSchema)

	readOp := &openapi3.Operation{
		OperationID: "repos/get",
		Responses:   responses200(repoSchemaRef),
	}
	updateOp := &openapi3.Operation{
		OperationID: "repos/update",
		Responses:   responses200(repoSchemaRef),
	}
	deleteOp := &openapi3.Operation{
		OperationID: "repos/delete",
		Responses:   openapi3.NewResponses(),
	}
	createOp := &openapi3.Operation{
		OperationID: "repos/create-in-org",
		Responses:   responses201(repoSchemaRef),
	}

	doc := &openapi3.T{OpenAPI: "3.0.3", Info: &openapi3.Info{Title: "t", Version: "1"}}
	doc.Paths = openapi3.NewPaths(
		openapi3.WithPath("/repos/{owner}/{repo}", &openapi3.PathItem{
			Get: readOp, Patch: updateOp, Delete: deleteOp,
		}),
		openapi3.WithPath("/orgs/{org}/repos", &openapi3.PathItem{
			Post: createOp,
		}),
		// A trailing-param item path (a real read candidate) with no
		// matching create operation anywhere in the document -- must be
		// skipped, not turned into a broken resource.
		openapi3.WithPath("/orgs/{org}", &openapi3.PathItem{
			Get: &openapi3.Operation{
				OperationID: "orgs/get",
				Responses:   responses200(openapi3.NewSchemaRef("#/components/schemas/organization-full", openapi3.NewObjectSchema().WithProperty("login", openapi3.NewStringSchema()))),
			},
		}),
	)
	return doc
}

func responses200(schema *openapi3.SchemaRef) *openapi3.Responses {
	r := openapi3.NewResponses()
	desc := "ok"
	r.Set("200", &openapi3.ResponseRef{Value: &openapi3.Response{
		Description: &desc,
		Content:     openapi3.Content{"application/json": openapi3.NewMediaType().WithSchemaRef(schema)},
	}})
	return r
}

func responses201(schema *openapi3.SchemaRef) *openapi3.Responses {
	r := openapi3.NewResponses()
	desc := "created"
	r.Set("201", &openapi3.ResponseRef{Value: &openapi3.Response{
		Description: &desc,
		Content:     openapi3.Content{"application/json": openapi3.NewMediaType().WithSchemaRef(schema)},
	}})
	return r
}

func TestDiscover_GitHubShapedCreatePathDiffersFromReadPath(t *testing.T) {
	doc := buildGitHubShapedDoc()
	resources, notes, err := Discover(doc, "github")
	if err != nil {
		t.Fatal(err)
	}
	if len(resources) != 1 {
		t.Fatalf("expected exactly one resource, got %d: %+v", len(resources), resources)
	}
	r := resources[0]
	if r.TypeName != "github_repository" {
		t.Fatalf("expected github_repository, got %q", r.TypeName)
	}
	if r.ReadPath != "/repos/{owner}/{repo}" {
		t.Fatalf("unexpected read path: %s", r.ReadPath)
	}
	if r.CreatePath != "/orgs/{org}/repos" || r.CreateMethod != "POST" {
		t.Fatalf("unexpected create: %s %s", r.CreateMethod, r.CreatePath)
	}
	if r.UpdateMethod != "PATCH" {
		t.Fatalf("expected PATCH update, got %q", r.UpdateMethod)
	}
	if r.DeleteOperation == nil {
		t.Fatal("expected a delete operation")
	}
	if got := r.PathParams; len(got) != 2 || got[0] != "owner" || got[1] != "repo" {
		t.Fatalf("unexpected path params: %v", got)
	}

	foundSkipNote := false
	for _, n := range notes {
		if n.Path == "/orgs/{org}" {
			foundSkipNote = true
		}
	}
	if !foundSkipNote {
		t.Fatalf("expected a note explaining /orgs/{org} was skipped (no matching create), got %+v", notes)
	}
}

func TestDiscover_NoPaths(t *testing.T) {
	doc := &openapi3.T{OpenAPI: "3.0.3", Info: &openapi3.Info{Title: "t", Version: "1"}}
	if _, _, err := Discover(doc, "x"); err == nil {
		t.Fatal("expected error for nil Paths")
	}
}

// TestSplitQualifiedRefName_RealKubernetesShapes proves the real, dotted,
// package-qualified ref-name split against the exact real shapes confirmed
// live against Kubernetes' own real, converted-from-Swagger2 spec this
// session (the onboarding-pipeline naming work) -- both the common
// "io.k8s.api.<group>.<version>.<Kind>" family and the real, structurally
// different "io.k8s.<component>.pkg.apis.<group>.<version>.<Kind>" family
// (apiextensions-apiserver's own CustomResourceDefinition), proving the
// "last 3 segments, middle one a real version token" heuristic generalizes
// across both without hardcoding either real prefix depth.
// buildARMShapedDoc mirrors Azure's own real ARM convention, confirmed
// live against the real Microsoft.Compute spec: a resource is created via
// PUT on the EXACT SAME item path as its own GET, not POST-to-a-collection
// the way GitHub's own convention works -- no POST operation anywhere in
// the document at all.
func buildARMShapedDoc() *openapi3.T {
	vmSchema := openapi3.NewObjectSchema().
		WithProperty("id", openapi3.NewStringSchema()).
		WithProperty("name", openapi3.NewStringSchema())
	vmSchemaRef := openapi3.NewSchemaRef("#/components/schemas/VirtualMachine", vmSchema)

	readOp := &openapi3.Operation{OperationID: "vm/get", Responses: responses200(vmSchemaRef)}
	putOp := &openapi3.Operation{OperationID: "vm/createOrUpdate", Responses: responses200(vmSchemaRef)}
	deleteOp := &openapi3.Operation{OperationID: "vm/delete", Responses: openapi3.NewResponses()}

	doc := &openapi3.T{OpenAPI: "3.0.3", Info: &openapi3.Info{Title: "t", Version: "1"}}
	doc.Paths = openapi3.NewPaths(
		openapi3.WithPath("/subscriptions/{subscriptionId}/resourceGroups/{rg}/virtualMachines/{vmName}", &openapi3.PathItem{
			Get: readOp, Put: putOp, Delete: deleteOp,
		}),
	)
	return doc
}

func TestDiscover_ARMShapedCreateIsPUTOnTheSameItemPath(t *testing.T) {
	doc := buildARMShapedDoc()
	resources, _, err := Discover(doc, "azure")
	if err != nil {
		t.Fatal(err)
	}
	if len(resources) != 1 {
		t.Fatalf("expected exactly one resource (a real, generic PUT-only create/update convention -- ARM is not the only API that creates with PUT), got %d: %+v", len(resources), resources)
	}
	r := resources[0]
	const itemPath = "/subscriptions/{subscriptionId}/resourceGroups/{rg}/virtualMachines/{vmName}"
	if r.CreateMethod != "PUT" || r.CreatePath != itemPath {
		t.Fatalf("expected create via PUT on the item path itself, got %s %s", r.CreateMethod, r.CreatePath)
	}
	// The identical real PUT operation ALSO serves as this resource's own
	// update (ARM's real idempotent-upsert semantics: PUT creates if
	// absent, replaces if present) -- both findCreate and findSibling
	// independently arriving at the same real operation is correct, not
	// a bug to reconcile away.
	if r.UpdateMethod != "PUT" {
		t.Fatalf("expected PUT update (the same real upsert operation), got %q", r.UpdateMethod)
	}
	if r.DeleteOperation == nil {
		t.Fatal("expected a delete operation")
	}
}

// TestFindCreate_PrefersPOSTOverPUTWhenBothMatch confirms the existing
// fewest-path-params tiebreak still correctly favors a real
// POST-to-collection create endpoint over an item-path PUT when a
// provider's own real spec offers both for the same resource shape (a
// real, common combination: create via POST, full-replace via PUT) --
// the PUT-as-create widening must never regress a provider that already
// works correctly via POST (GitHub/Kubernetes/Datadog).
func TestFindCreate_PrefersPOSTOverPUTWhenBothMatch(t *testing.T) {
	widgetSchema := openapi3.NewObjectSchema().WithProperty("id", openapi3.NewStringSchema())
	widgetRef := openapi3.NewSchemaRef("#/components/schemas/Widget", widgetSchema)

	readOp := &openapi3.Operation{OperationID: "widget/get", Responses: responses200(widgetRef)}
	postOp := &openapi3.Operation{OperationID: "widget/create", Responses: responses201(widgetRef)}
	putOp := &openapi3.Operation{OperationID: "widget/replace", Responses: responses200(widgetRef)}

	doc := &openapi3.T{OpenAPI: "3.0.3", Info: &openapi3.Info{Title: "t", Version: "1"}}
	doc.Paths = openapi3.NewPaths(
		openapi3.WithPath("/widgets/{id}", &openapi3.PathItem{Get: readOp, Put: putOp}),
		openapi3.WithPath("/widgets", &openapi3.PathItem{Post: postOp}),
	)

	resources, _, err := Discover(doc, "acme")
	if err != nil {
		t.Fatal(err)
	}
	if len(resources) != 1 {
		t.Fatalf("expected exactly one resource, got %d: %+v", len(resources), resources)
	}
	if r := resources[0]; r.CreateMethod != "POST" || r.CreatePath != "/widgets" {
		t.Fatalf("expected the real POST-to-collection endpoint to win over the item-path PUT, got %s %s", r.CreateMethod, r.CreatePath)
	}
}

func TestSplitQualifiedRefName_RealKubernetesShapes(t *testing.T) {
	cases := []struct {
		ref, wantService, wantVersion, wantNoun string
	}{
		{"io.k8s.api.apps.v1.Deployment", "apps", "v1", "Deployment"},
		{"io.k8s.api.core.v1.Pod", "core", "v1", "Pod"},
		{"io.k8s.api.admissionregistration.v1alpha1.MutatingAdmissionPolicy", "admissionregistration", "v1alpha1", "MutatingAdmissionPolicy"},
		{"io.k8s.apiextensions-apiserver.pkg.apis.apiextensions.v1.CustomResourceDefinition", "apiextensions", "v1", "CustomResourceDefinition"},
	}
	for _, c := range cases {
		service, version, noun, ok := splitQualifiedRefName(c.ref)
		if !ok {
			t.Errorf("splitQualifiedRefName(%q): ok = false, want true", c.ref)
			continue
		}
		if service != c.wantService || version != c.wantVersion || noun != c.wantNoun {
			t.Errorf("splitQualifiedRefName(%q) = (%q, %q, %q), want (%q, %q, %q)", c.ref, service, version, noun, c.wantService, c.wantVersion, c.wantNoun)
		}
	}
}

// TestSplitQualifiedRefName_NonQualifiedNamesUnaffected proves GitHub's/
// Datadog's own real, flatter, single-concept ref names (no version-like
// segment anywhere) correctly report ok=false -- deriveNoun's own existing,
// unchanged single-noun fallback applies, not a false-positive split.
func TestSplitQualifiedRefName_NonQualifiedNamesUnaffected(t *testing.T) {
	for _, ref := range []string{"full-repository", "gist-simple", "Widget", "a.b"} {
		if _, _, _, ok := splitQualifiedRefName(ref); ok {
			t.Errorf("splitQualifiedRefName(%q): ok = true, want false (no real version-shaped segment present)", ref)
		}
	}
}

// TestDiscover_VersionCollisionRecoveredNotDropped is UBI-176's own real
// regression test: a stable v1 and a beta v1beta1 sibling of the same real
// Kind (identical response schema shape, e.g. Kubernetes'
// io.k8s.api.apps.v1.Deployment / io.k8s.api.apps.v1beta1.Deployment) used
// to collide on an identical, version-stripped typeName -- seenTypeNames'
// own collision guard then silently kept whichever sorted first by
// ReadPath and dropped the other, no matter which version was actually
// current. Both must now survive: the path-sort winner keeps its own
// plain, unversioned name unchanged (a real, deliberate backward-
// compatibility constraint -- no already-published typeName may move),
// the loser gets a version-qualified name instead of being dropped.
func TestDiscover_VersionCollisionRecoveredNotDropped(t *testing.T) {
	schemaRefFor := func(refName string) *openapi3.SchemaRef {
		return openapi3.NewSchemaRef("#/components/schemas/"+refName, openapi3.NewObjectSchema())
	}

	stableSchemaRef := schemaRefFor("io.k8s.api.apps.v1.Deployment")
	betaSchemaRef := schemaRefFor("io.k8s.api.apps.v1beta1.Deployment")

	stableRead := &openapi3.Operation{OperationID: "apps-v1/read", Responses: responses200(stableSchemaRef)}
	stableCreate := &openapi3.Operation{OperationID: "apps-v1/create", Responses: responses201(stableSchemaRef)}
	betaRead := &openapi3.Operation{OperationID: "apps-v1beta1/read", Responses: responses200(betaSchemaRef)}
	betaCreate := &openapi3.Operation{OperationID: "apps-v1beta1/create", Responses: responses201(betaSchemaRef)}

	doc := &openapi3.T{OpenAPI: "3.0.3", Info: &openapi3.Info{Title: "t", Version: "1"}}
	doc.Paths = openapi3.NewPaths(
		openapi3.WithPath("/apis/apps/v1/deployments/{name}", &openapi3.PathItem{Get: stableRead}),
		openapi3.WithPath("/apis/apps/v1/deployments", &openapi3.PathItem{Post: stableCreate}),
		openapi3.WithPath("/apis/apps/v1beta1/deployments/{name}", &openapi3.PathItem{Get: betaRead}),
		openapi3.WithPath("/apis/apps/v1beta1/deployments", &openapi3.PathItem{Post: betaCreate}),
	)

	resources, notes, err := Discover(doc, "kubernetes")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range notes {
		if strings.Contains(n.Detail, "skipped rather than disambiguated") {
			t.Errorf("expected no drop notes, got: %s: %s", n.Path, n.Detail)
		}
	}
	if len(resources) != 2 {
		t.Fatalf("expected both the v1 and v1beta1 Deployment to survive, got %d: %+v", len(resources), resources)
	}
	byType := map[string]Resource{}
	for _, r := range resources {
		byType[r.TypeName] = r
	}
	if _, ok := byType["kubernetes_apps_deployment"]; !ok {
		t.Errorf("expected the path-sort winner to keep the plain, unversioned name kubernetes_apps_deployment, got types: %v", keysOf(byType))
	}
	if _, ok := byType["kubernetes_apps_v1beta1_deployment"]; !ok {
		t.Errorf("expected the recovered sibling to get the version-qualified name kubernetes_apps_v1beta1_deployment, got types: %v", keysOf(byType))
	}
}

// TestDiscover_HigherMajorVersionWinsThePlainNameOverPathSortOrder is
// UBI-176's own real, live-found refinement: ReadPath's own lexicographic
// sort order is NOT the same thing as real API version priority --
// "v1" < "v2" as strings, so a naive path-sort-wins rule would let an
// OLDER, superseded major version (autoscaling/v1) keep the plain name
// over the real, current, preferred one (autoscaling/v2), even though
// neither is a prerelease. Confirmed live against the real
// autoscaling/v2 vs v1 HorizontalPodAutoscaler case (v2 replaces v1's
// own single CPU-utilization target with a real multi-metric array) and
// the real per-API-group "preferredVersion" signal Kubernetes itself
// publishes (api/discovery/apis__autoscaling.json, same repo, same
// release tag as the OpenAPI spec, confirming v2) -- this package
// computes the identical, real, standard version-priority order
// locally (versionPriority) rather than requiring a second live fetch.
func TestDiscover_HigherMajorVersionWinsThePlainNameOverPathSortOrder(t *testing.T) {
	schemaRefFor := func(refName string) *openapi3.SchemaRef {
		return openapi3.NewSchemaRef("#/components/schemas/"+refName, openapi3.NewObjectSchema())
	}
	v1Ref := schemaRefFor("io.k8s.api.autoscaling.v1.HorizontalPodAutoscaler")
	v2Ref := schemaRefFor("io.k8s.api.autoscaling.v2.HorizontalPodAutoscaler")

	v1Read := &openapi3.Operation{OperationID: "hpa-v1/read", Responses: responses200(v1Ref)}
	v1Create := &openapi3.Operation{OperationID: "hpa-v1/create", Responses: responses201(v1Ref)}
	v2Read := &openapi3.Operation{OperationID: "hpa-v2/read", Responses: responses200(v2Ref)}
	v2Create := &openapi3.Operation{OperationID: "hpa-v2/create", Responses: responses201(v2Ref)}

	doc := &openapi3.T{OpenAPI: "3.0.3", Info: &openapi3.Info{Title: "t", Version: "1"}}
	// v1's own paths are declared FIRST and sort lexicographically ahead
	// of v2's -- exactly the real shape that used to make v1 win purely
	// by path order, the bug this test locks in the fix for.
	doc.Paths = openapi3.NewPaths(
		openapi3.WithPath("/apis/autoscaling/v1/namespaces/{namespace}/horizontalpodautoscalers/{name}", &openapi3.PathItem{Get: v1Read}),
		openapi3.WithPath("/apis/autoscaling/v1/namespaces/{namespace}/horizontalpodautoscalers", &openapi3.PathItem{Post: v1Create}),
		openapi3.WithPath("/apis/autoscaling/v2/namespaces/{namespace}/horizontalpodautoscalers/{name}", &openapi3.PathItem{Get: v2Read}),
		openapi3.WithPath("/apis/autoscaling/v2/namespaces/{namespace}/horizontalpodautoscalers", &openapi3.PathItem{Post: v2Create}),
	)

	resources, _, err := Discover(doc, "kubernetes")
	if err != nil {
		t.Fatal(err)
	}
	byType := map[string]Resource{}
	for _, r := range resources {
		byType[r.TypeName] = r
	}
	if r, ok := byType["kubernetes_autoscaling_horizontal_pod_autoscaler"]; !ok {
		t.Fatalf("expected the plain, unversioned name to exist, got types: %v", keysOf(byType))
	} else if !strings.Contains(r.ReadPath, "/v2/") {
		t.Errorf("expected the plain name to belong to v2 (the real, current, preferred version), got ReadPath %q", r.ReadPath)
	}
	if _, ok := byType["kubernetes_autoscaling_v1_horizontal_pod_autoscaler"]; !ok {
		t.Errorf("expected the recovered older-major sibling to get the version-qualified name kubernetes_autoscaling_v1_horizontal_pod_autoscaler, got types: %v", keysOf(byType))
	}
}

func TestVersionPriority_RealKubernetesOrdering(t *testing.T) {
	// Each row must outrank every row after it -- the real, standard
	// Kubernetes API version-priority order: GA beats any prerelease
	// regardless of number, beta beats alpha, higher number wins within
	// a tier.
	inOrder := []string{"v10", "v2", "v1", "v11beta2", "v10beta3", "v3beta1", "v12alpha1", "v11alpha2", "v1alpha1"}
	for i := 0; i < len(inOrder)-1; i++ {
		hi, lo := versionPriority(inOrder[i]), versionPriority(inOrder[i+1])
		if !hi.higherThan(lo) {
			t.Errorf("expected versionPriority(%q) to outrank versionPriority(%q), got %+v vs %+v", inOrder[i], inOrder[i+1], hi, lo)
		}
	}
}

func keysOf(m map[string]Resource) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestCombineTypeName_TrimsRealOverlap(t *testing.T) {
	// Real, live-found Azure ARM cases (ubiquex-docs UBI: the
	// "azure_hdinsight_cluster_cluster" investigation) -- the response
	// schema for a sub-domain's own primary resource is often named
	// after the sub-domain itself. Full-word overlap collapses the
	// noun entirely; partial (leading-token-only) overlap keeps the
	// non-overlapping remainder.
	cases := []struct {
		provider, service, noun, want string
	}{
		{"azure_hdinsight_cluster", "", "cluster", "azure_hdinsight_cluster"},
		{"azure_synapse_workspace", "", "workspace", "azure_synapse_workspace"},
		{"azure_servicefabric_cluster", "", "cluster", "azure_servicefabric_cluster"},
		{"azure_servicefabric_application", "", "application_resource", "azure_servicefabric_application_resource"},
		{"azure_devtestlabs_dtl", "", "dtl_environment", "azure_devtestlabs_dtl_environment"},
		{"azure_loadtestservice_playwright", "", "playwright_workspace", "azure_loadtestservice_playwright_workspace"},
		// no real overlap -- untouched, matches the old, pre-fix behavior exactly.
		{"azure_datadog", "", "monitor_resource", "azure_datadog_monitor_resource"},
		{"kubernetes", "apps", "deployment", "kubernetes_apps_deployment"},
		// a real service-qualified case whose service ALSO overlaps the
		// provider's own trailing token.
		{"github_repos", "repos", "commit", "github_repos_commit"},
	}
	for _, c := range cases {
		got := combineTypeName(c.provider, c.service, c.noun)
		if got != c.want {
			t.Errorf("combineTypeName(%q, %q, %q) = %q, want %q", c.provider, c.service, c.noun, got, c.want)
		}
	}
}

func TestCombineTypeName_NoFalsePositiveOnSimilarButDifferentWord(t *testing.T) {
	// A noun that merely LOOKS similar to the provider's own trailing
	// token, without an exact token match, must never be trimmed --
	// only a real, exact token-run overlap counts.
	got := combineTypeName("azure_cluster", "", "clusters")
	want := "azure_cluster_clusters"
	if got != want {
		t.Errorf("combineTypeName should not trim a non-exact token match, got %q, want %q", got, want)
	}
}
