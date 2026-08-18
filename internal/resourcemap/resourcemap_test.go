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
