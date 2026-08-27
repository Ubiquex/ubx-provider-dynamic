package dynserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/ubiquex/ubx-provider-dynamic/internal/config"
	"github.com/ubiquex/ubx-provider-dynamic/internal/restexec"
)

// buildTestDoc is the same real structural shape resourcemap's own test
// uses (create path differs from read path), reused here to drive a full
// Server end to end against a real net/http server -- proving layers 1
// (tfplugin schema/RPC shape), 2-3 (schema/resource mapping already proven
// by resourcemap/schema's own tests), and 4/6 (real HTTP CRUD execution,
// wire encoding) work together, not just in isolation.
func buildTestDoc() *openapi3.T {
	repoSchema := openapi3.NewObjectSchema().
		WithProperty("id", openapi3.NewIntegerSchema()).
		WithProperty("name", openapi3.NewStringSchema()).
		WithProperty("private", openapi3.NewBoolSchema())
	repoSchema.Required = []string{"name"}
	repoSchema.Properties["id"].Value.ReadOnly = true
	repoRef := openapi3.NewSchemaRef("#/components/schemas/repository", repoSchema)

	createBody := openapi3.NewObjectSchema().
		WithProperty("name", openapi3.NewStringSchema()).
		WithProperty("private", openapi3.NewBoolSchema())
	createBody.Required = []string{"name"}

	desc := "ok"
	okResp := func() *openapi3.Responses {
		r := openapi3.NewResponses()
		r.Set("200", &openapi3.ResponseRef{Value: &openapi3.Response{
			Description: &desc,
			Content:     openapi3.Content{"application/json": openapi3.NewMediaType().WithSchemaRef(repoRef)},
		}})
		return r
	}
	createdResp := func() *openapi3.Responses {
		r := openapi3.NewResponses()
		r.Set("201", &openapi3.ResponseRef{Value: &openapi3.Response{
			Description: &desc,
			Content:     openapi3.Content{"application/json": openapi3.NewMediaType().WithSchemaRef(repoRef)},
		}})
		return r
	}

	doc := &openapi3.T{OpenAPI: "3.0.3", Info: &openapi3.Info{Title: "t", Version: "1"}}
	doc.Paths = openapi3.NewPaths(
		openapi3.WithPath("/repos/{owner}/{repo}", &openapi3.PathItem{
			Get:    &openapi3.Operation{OperationID: "repos/get", Responses: okResp()},
			Patch:  &openapi3.Operation{OperationID: "repos/update", Responses: okResp(), RequestBody: &openapi3.RequestBodyRef{Value: &openapi3.RequestBody{Content: openapi3.Content{"application/json": openapi3.NewMediaType().WithSchema(createBody)}}}},
			Delete: &openapi3.Operation{OperationID: "repos/delete", Responses: openapi3.NewResponses()},
		}),
		openapi3.WithPath("/orgs/{org}/repos", &openapi3.PathItem{
			Post: &openapi3.Operation{
				OperationID: "repos/create",
				Responses:   createdResp(),
				RequestBody: &openapi3.RequestBodyRef{Value: &openapi3.RequestBody{Content: openapi3.Content{"application/json": openapi3.NewMediaType().WithSchema(createBody)}}},
			},
		}),
	)
	return doc
}

// fakeAPI is a minimal, real net/http server standing in for the actual
// REST service -- one in-memory repository, keyed by name, enough to
// exercise create/read/update/delete for real over the wire (no transport
// mocking: real HTTP requests, real JSON, real net/http.Client on the
// restexec side).
type fakeAPI struct {
	mu     sync.Mutex
	repos  map[string]map[string]any
	nextID int
}

func newFakeAPI() *fakeAPI {
	return &fakeAPI{repos: map[string]map[string]any{}, nextID: 100}
}

func (f *fakeAPI) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/orgs/", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.nextID++
		body["id"] = float64(f.nextID)
		f.repos[body["name"].(string)] = body
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(body)
	})
	mux.HandleFunc("/repos/", func(w http.ResponseWriter, r *http.Request) {
		parts := splitPath(r.URL.Path) // ["repos", owner, repo]
		if len(parts) != 3 {
			http.NotFound(w, r)
			return
		}
		name := parts[2]
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.Method {
		case http.MethodGet:
			repo, ok := f.repos[name]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(repo)
		case http.MethodPatch:
			repo, ok := f.repos[name]
			if !ok {
				http.NotFound(w, r)
				return
			}
			var patch map[string]any
			_ = json.NewDecoder(r.Body).Decode(&patch)
			for k, v := range patch {
				repo[k] = v
			}
			_ = json.NewEncoder(w).Encode(repo)
		case http.MethodDelete:
			delete(f.repos, name)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "unsupported", http.StatusMethodNotAllowed)
		}
	})
	return mux
}

func splitPath(p string) []string {
	var out []string
	cur := ""
	for _, c := range p {
		if c == '/' {
			if cur != "" {
				out = append(out, cur)
			}
			cur = ""
			continue
		}
		cur += string(c)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func buildTestServer(t *testing.T, baseURL string) (*Server, *ResourceType) {
	t.Helper()
	resources, notes, err := Build(buildTestDoc(), "test", config.Provider{})
	if err != nil {
		t.Fatalf("Build: %v (notes: %v)", err, notes)
	}
	rt, ok := resources["test_repository"]
	if !ok {
		t.Fatalf("expected test_repository, got %v", resources)
	}
	rt.Client = restexec.NewClient(baseURL, nil)
	srv := &Server{ProviderName: "test", Resources: resources}
	return srv, rt
}

func objValue(rt *ResourceType, m map[string]tftypes.Value) tftypes.Value {
	full := map[string]tftypes.Value{}
	for name, ty := range rt.ObjectType.AttributeTypes {
		if v, ok := m[name]; ok {
			full[name] = v
		} else {
			full[name] = tftypes.NewValue(ty, nil)
		}
	}
	return tftypes.NewValue(rt.ObjectType, full)
}

func mustDV(t *testing.T, rt *ResourceType, v tftypes.Value) *tfprotov6.DynamicValue {
	t.Helper()
	dv, err := tfprotov6.NewDynamicValue(rt.ObjectType, v)
	if err != nil {
		t.Fatalf("NewDynamicValue: %v", err)
	}
	return &dv
}

func decodeObj(t *testing.T, rt *ResourceType, dv *tfprotov6.DynamicValue) map[string]tftypes.Value {
	t.Helper()
	v, err := dv.Unmarshal(rt.ObjectType)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	var m map[string]tftypes.Value
	if err := v.As(&m); err != nil {
		t.Fatalf("As: %v", err)
	}
	return m
}

func TestServer_FullCRUDLifecycle(t *testing.T) {
	api := newFakeAPI()
	ts := httptest.NewServer(api.handler())
	defer ts.Close()

	ctx := t.Context()
	srv, rt := buildTestServer(t, ts.URL)

	// --- Create ---
	planned := objValue(rt, map[string]tftypes.Value{
		"org":     tftypes.NewValue(tftypes.String, "acme"),
		"owner":   tftypes.NewValue(tftypes.String, "acme"),
		"repo":    tftypes.NewValue(tftypes.String, "widgets"),
		"name":    tftypes.NewValue(tftypes.String, "widgets"),
		"private": tftypes.NewValue(tftypes.Bool, true),
		"id":      tftypes.NewValue(tftypes.Number, nil),
	})
	applyResp, err := srv.ApplyResourceChange(ctx, &tfprotov6.ApplyResourceChangeRequest{
		TypeName:     "test_repository",
		PriorState:   mustDV(t, rt, tftypes.NewValue(rt.ObjectType, nil)),
		PlannedState: mustDV(t, rt, planned),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(applyResp.Diagnostics) > 0 {
		t.Fatalf("create diagnostics: %+v", applyResp.Diagnostics)
	}
	created := decodeObj(t, rt, applyResp.NewState)
	var createdName string
	if err := created["name"].As(&createdName); err != nil || createdName != "widgets" {
		t.Fatalf("created name: %v %v", createdName, err)
	}
	if created["id"].IsNull() {
		t.Fatal("expected server-assigned id, got null")
	}
	if created["org"].IsNull() {
		t.Fatal("expected org carried forward from planned state into create's NewState")
	}

	// --- Read ---
	readResp, err := srv.ReadResource(ctx, &tfprotov6.ReadResourceRequest{
		TypeName:     "test_repository",
		CurrentState: mustDV(t, rt, tftypes.NewValue(rt.ObjectType, created)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(readResp.Diagnostics) > 0 {
		t.Fatalf("read diagnostics: %+v", readResp.Diagnostics)
	}
	readBack := decodeObj(t, rt, readResp.NewState)
	if readBack["org"].IsNull() {
		t.Fatal("expected org carried forward across Read (create-only path param) -- this is the exact bug found and fixed before this test was written")
	}

	// --- Update ---
	updatePlanned := map[string]tftypes.Value{}
	for k, v := range readBack {
		updatePlanned[k] = v
	}
	updatePlanned["private"] = tftypes.NewValue(tftypes.Bool, false)
	applyResp, err = srv.ApplyResourceChange(ctx, &tfprotov6.ApplyResourceChangeRequest{
		TypeName:     "test_repository",
		PriorState:   mustDV(t, rt, tftypes.NewValue(rt.ObjectType, readBack)),
		PlannedState: mustDV(t, rt, tftypes.NewValue(rt.ObjectType, updatePlanned)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(applyResp.Diagnostics) > 0 {
		t.Fatalf("update diagnostics: %+v", applyResp.Diagnostics)
	}
	updated := decodeObj(t, rt, applyResp.NewState)
	var private bool
	if err := updated["private"].As(&private); err != nil || private != false {
		t.Fatalf("expected private=false after update, got %v (%v)", private, err)
	}

	// --- Destroy ---
	planResp, err := srv.PlanResourceChange(ctx, &tfprotov6.PlanResourceChangeRequest{
		TypeName:         "test_repository",
		PriorState:       mustDV(t, rt, tftypes.NewValue(rt.ObjectType, updated)),
		ProposedNewState: mustDV(t, rt, tftypes.NewValue(rt.ObjectType, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	applyResp, err = srv.ApplyResourceChange(ctx, &tfprotov6.ApplyResourceChangeRequest{
		TypeName:       "test_repository",
		PriorState:     mustDV(t, rt, tftypes.NewValue(rt.ObjectType, updated)),
		PlannedState:   mustDV(t, rt, tftypes.NewValue(rt.ObjectType, nil)),
		PlannedPrivate: planResp.PlannedPrivate,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(applyResp.Diagnostics) > 0 {
		t.Fatalf("destroy diagnostics: %+v", applyResp.Diagnostics)
	}

	// --- Read after destroy: genuinely gone ---
	readResp, err = srv.ReadResource(ctx, &tfprotov6.ReadResourceRequest{
		TypeName:     "test_repository",
		CurrentState: mustDV(t, rt, tftypes.NewValue(rt.ObjectType, updated)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if readResp.NewState != nil {
		t.Fatalf("expected no NewState after destroy (resource gone), got %+v", readResp.NewState)
	}
}

func TestServer_DestroyWithoutPlannedPrivate_Refused(t *testing.T) {
	api := newFakeAPI()
	ts := httptest.NewServer(api.handler())
	defer ts.Close()

	ctx := t.Context()
	srv, rt := buildTestServer(t, ts.URL)

	prior := objValue(rt, map[string]tftypes.Value{
		"owner": tftypes.NewValue(tftypes.String, "acme"),
		"repo":  tftypes.NewValue(tftypes.String, "widgets"),
		"name":  tftypes.NewValue(tftypes.String, "widgets"),
	})
	resp, err := srv.ApplyResourceChange(ctx, &tfprotov6.ApplyResourceChangeRequest{
		TypeName:     "test_repository",
		PriorState:   mustDV(t, rt, prior),
		PlannedState: mustDV(t, rt, tftypes.NewValue(rt.ObjectType, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Diagnostics) == 0 {
		t.Fatal("expected a refusal diagnostic for destroy without PlannedPrivate")
	}
}
