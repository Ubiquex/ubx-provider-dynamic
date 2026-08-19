package resourcemap

import (
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
		ref, wantService, wantNoun string
	}{
		{"io.k8s.api.apps.v1.Deployment", "apps", "Deployment"},
		{"io.k8s.api.core.v1.Pod", "core", "Pod"},
		{"io.k8s.api.admissionregistration.v1alpha1.MutatingAdmissionPolicy", "admissionregistration", "MutatingAdmissionPolicy"},
		{"io.k8s.apiextensions-apiserver.pkg.apis.apiextensions.v1.CustomResourceDefinition", "apiextensions", "CustomResourceDefinition"},
	}
	for _, c := range cases {
		service, noun, ok := splitQualifiedRefName(c.ref)
		if !ok {
			t.Errorf("splitQualifiedRefName(%q): ok = false, want true", c.ref)
			continue
		}
		if service != c.wantService || noun != c.wantNoun {
			t.Errorf("splitQualifiedRefName(%q) = (%q, %q), want (%q, %q)", c.ref, service, noun, c.wantService, c.wantNoun)
		}
	}
}

// TestSplitQualifiedRefName_NonQualifiedNamesUnaffected proves GitHub's/
// Datadog's own real, flatter, single-concept ref names (no version-like
// segment anywhere) correctly report ok=false -- deriveNoun's own existing,
// unchanged single-noun fallback applies, not a false-positive split.
func TestSplitQualifiedRefName_NonQualifiedNamesUnaffected(t *testing.T) {
	for _, ref := range []string{"full-repository", "gist-simple", "Widget", "a.b"} {
		if _, _, ok := splitQualifiedRefName(ref); ok {
			t.Errorf("splitQualifiedRefName(%q): ok = true, want false (no real version-shaped segment present)", ref)
		}
	}
}
