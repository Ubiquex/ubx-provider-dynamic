package resourcemap

import (
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

// testOp and newTestDoc are this file's own small, local DSL for
// building a minimal *openapi3.T with a handful of GET-only paths --
// resourcemap_test.go's own real openapi3.T/openapi3.NewPaths
// construction, generalized to N paths instead of each test hand-rolling
// its own, since every test in this file needs the identical
// "GET-only path -> a JSON response schema" shape.
type testOp struct {
	opID string
	resp *openapi3.SchemaRef
}

func newTestDoc(paths map[string]*testOp) *openapi3.T {
	doc := &openapi3.T{OpenAPI: "3.0.3", Info: &openapi3.Info{Title: "t", Version: "1"}}
	var opts []openapi3.NewPathsOption
	for path, o := range paths {
		opts = append(opts, openapi3.WithPath(path, &openapi3.PathItem{
			Get: &openapi3.Operation{OperationID: o.opID, Responses: responses200(o.resp)},
		}))
	}
	doc.Paths = openapi3.NewPaths(opts...)
	return doc
}

// TestDiscoverDataSources_UBI181Rules_ExcludesOperationAndWatch is the
// real, live-shaped proof the five UBI-181 rules are wired into
// resourcemap's own DiscoverDataSources: Azure's own real
// "OperationStatus"-response operations-polling GET, and Kubernetes' own
// real Watch-prefixed operationId, are both excluded, while a genuine,
// unrelated GET survives.
func TestDiscoverDataSources_UBI181Rules_ExcludesOperationAndWatch(t *testing.T) {
	targetTypeRef := openapi3.NewSchemaRef("#/components/schemas/TargetType",
		openapi3.NewObjectSchema().WithProperty("id", openapi3.NewStringSchema()))
	opStatusRef := openapi3.NewSchemaRef("#/components/schemas/OperationStatus",
		openapi3.NewObjectSchema().WithProperty("status", openapi3.NewStringSchema()))

	doc := newTestDoc(map[string]*testOp{
		"/targetTypes/{id}":         {opID: "targetTypes_get", resp: targetTypeRef},
		"/operations/{operationId}": {opID: "operations_get", resp: opStatusRef},
		"/pods/watch/{namespace}":   {opID: "watchNamespacedPod", resp: targetTypeRef},
	})

	candidates, notes, err := DiscoverDataSources(doc, "azure")
	if err != nil {
		t.Fatalf("DiscoverDataSources: %v", err)
	}

	byTypeName := map[string]bool{}
	for _, c := range candidates {
		byTypeName[c.TypeName] = true
	}

	if !byTypeName["azure_target_type"] {
		t.Errorf("expected the genuine target-type lookup to survive, got candidates: %v", candidates)
	}
	if byTypeName["azure_operation"] || byTypeName["azure_operation_status"] {
		t.Error("expected the operations-status polling GET to be excluded (rule 2), but it was kept")
	}
	for _, c := range candidates {
		if c.Operation != nil && c.Operation.OperationID == "watchNamespacedPod" {
			t.Error("expected the Watch-prefixed operation to be excluded (rule 1, watch path), but it was kept")
		}
	}

	foundNote := false
	for _, n := range notes {
		if n.Detail == "excluded from data-source candidates: operation-status shape -- async job/operation polling, not stored infrastructure data" {
			foundNote = true
		}
	}
	if !foundNote {
		t.Error("expected a Note recording the operation-status exclusion -- must be auditable, never a silent drop")
	}
}

// TestDiscoverDataSources_CollectionEnvelope_UnwrapsToRealItemNoun is the
// real, live-found deriveNoun fix's own proof: a collection-listing GET
// whose response schema is a real envelope wrapper (Azure's own
// "TargetTypeListResult" convention -- a "value" array of $ref'd items
// plus a "nextLink" pagination field, confirmed live against the real
// Azure chaos studio spec) must be named from the real item noun,
// never from the wrapper's own literal name
// ("azure_target_type_list_result").
//
// UBI-241 amended what it then expects. The unwrap is unchanged and
// still the point of this test; what changed is that the collection now
// carries a _list suffix rather than taking the item's own bare name.
// Naming a collection after its item meant a list GET and an item GET
// derived the same type name, and only one survived: on the real
// kubernetes artifact that took single-item data sources from 35 to 6,
// with the list winning every collision on lexical path order alone.
//
// So this asserts both halves now: the wrapper name is still gone, AND
// the collection is distinguishable from the item.
func TestDiscoverDataSources_CollectionEnvelope_UnwrapsToRealItemNoun(t *testing.T) {
	itemRef := openapi3.NewSchemaRef("#/components/schemas/TargetType",
		openapi3.NewObjectSchema().WithProperty("id", openapi3.NewStringSchema()))

	envelope := openapi3.NewObjectSchema().
		WithProperty("nextLink", openapi3.NewStringSchema())
	arraySchema := openapi3.NewArraySchema()
	arraySchema.Items = itemRef
	envelope.WithPropertyRef("value", openapi3.NewSchemaRef("", arraySchema))
	envelopeRef := openapi3.NewSchemaRef("#/components/schemas/TargetTypeListResult", envelope)

	doc := newTestDoc(map[string]*testOp{
		"/targetTypes": {opID: "targetTypes_list", resp: envelopeRef},
	})

	candidates, _, err := DiscoverDataSources(doc, "azure")
	if err != nil {
		t.Fatalf("DiscoverDataSources: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected exactly 1 candidate, got %d: %v", len(candidates), candidates)
	}
	got := candidates[0].TypeName
	if got != "azure_target_type_list" {
		t.Errorf("expected the collection to be named from the item noun with a _list suffix, \"azure_target_type_list\", got %q", got)
	}
	// The original point of this test, kept explicit rather than implied
	// by the assertion above: the wrapper's own name must never surface.
	if strings.Contains(got, "list_result") {
		t.Errorf("the envelope wrapper's own name leaked into the type name: %q", got)
	}
}

// TestDiscoverDataSources_ItemAndCollection_BothSurvive is UBI-241's own
// regression: a list GET and an item GET over the same type are two data
// sources, not a name clash. Before the fix they derived the identical
// name and the one sorting later by path was dropped with a note.
func TestDiscoverDataSources_ItemAndCollection_BothSurvive(t *testing.T) {
	item := openapi3.NewObjectSchema().WithProperty("id", openapi3.NewStringSchema())
	itemRef := openapi3.NewSchemaRef("#/components/schemas/Widget", item)

	envelope := openapi3.NewObjectSchema().WithProperty("nextLink", openapi3.NewStringSchema())
	arr := openapi3.NewArraySchema()
	arr.Items = itemRef
	envelope.WithPropertyRef("value", openapi3.NewSchemaRef("", arr))
	envelopeRef := openapi3.NewSchemaRef("#/components/schemas/WidgetListResult", envelope)

	// The collection path sorts FIRST, which is what used to let it claim
	// the shared name and drop the item read.
	doc := newTestDoc(map[string]*testOp{
		"/widgets":              {opID: "widgets_list", resp: envelopeRef},
		"/widgets/{widgetName}": {opID: "widgets_get", resp: itemRef},
	})

	candidates, notes, err := DiscoverDataSources(doc, "azure")
	if err != nil {
		t.Fatalf("DiscoverDataSources: %v", err)
	}
	names := map[string]bool{}
	for _, c := range candidates {
		names[c.TypeName] = true
	}
	if !names["azure_widget"] {
		t.Errorf("the single-item read should survive as azure_widget, got %v", names)
	}
	if !names["azure_widget_list"] {
		t.Errorf("the collection should survive as azure_widget_list, got %v", names)
	}
	for _, n := range notes {
		if strings.Contains(n.Detail, "already claimed") {
			t.Errorf("neither should be dropped as a name clash, but one was: %s", n.Detail)
		}
	}
}

// TestDiscoverDataSources_FlatArrayResponse_NotMistakenForEnvelope is the
// negative-path proof collectionItemRefName's own shape test isn't just
// broad enough to strip any name ending in a list-shaped suffix: a real,
// unrelated domain type that merely happens to end in "List" (a flat
// array of strings, no $ref to another named schema -- a genuine
// allow-list, not a wrapper) must NOT be unwrapped.
func TestDiscoverDataSources_FlatArrayResponse_NotMistakenForEnvelope(t *testing.T) {
	flatArraySchema := openapi3.NewArraySchema()
	flatArraySchema.Items = openapi3.NewSchemaRef("", openapi3.NewStringSchema())
	flatArray := openapi3.NewObjectSchema().
		WithPropertyRef("entries", openapi3.NewSchemaRef("", flatArraySchema))
	flatArrayRef := openapi3.NewSchemaRef("#/components/schemas/AllowList", flatArray)

	doc := newTestDoc(map[string]*testOp{
		"/allowList": {opID: "allowList_get", resp: flatArrayRef},
	})

	candidates, _, err := DiscoverDataSources(doc, "azure")
	if err != nil {
		t.Fatalf("DiscoverDataSources: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected exactly 1 candidate, got %d: %v", len(candidates), candidates)
	}
	if got := candidates[0].TypeName; got != "azure_allow_list" {
		t.Errorf("expected the flat-array AllowList to keep its own real name \"azure_allow_list\" (not unwrapped), got %q", got)
	}
}

// TestDiscoverDataSources_PathFallbackNoun_UsesRealLastSegment is the
// real, live-found deriveNoun fallback fix's own proof (UBI-222,
// Cloudflare): a collection-listing GET with NO response-schema
// component name at all (a genuinely inline schema, forcing deriveNoun's
// own path-based fallback) and NO trailing {param} in its own read path
// must derive its noun from the path's real LAST segment, not the
// segment before it.
//
// Confirmed live against Cloudflare's own real spec: deriveNoun's
// fallback loop started at len(segs)-2, correct only when the last
// segment is always a {param} -- true for a single-item resource read
// path, never checked for a collection-shaped data source path (the
// wider candidate surface DiscoverDataSources' own doc comment
// describes). Real, confirmed failures this produced: "ai" instead of
// "finetune" for /accounts/{account_id}/ai/finetunes, "d1" instead of
// "database" for /accounts/{account_id}/d1/database, "kv" instead of
// "namespace" for /accounts/{account_id}/storage/kv/namespaces, and one
// genuine crash: a path ending in a literal "-" segment
// (/accounts/{account_id}/cloudforce-one/events/dataset/-/groups)
// singularized "-" down to an empty string, producing a bare
// "cloudflare_" TypeName that failed sdk/codegen/ir's own
// ServiceAndLocalName outright ("wire type must be at least
// <provider>_<service>") the moment a real `ubx sdk gen --dump-ir` run
// reached it.
func TestDiscoverDataSources_PathFallbackNoun_UsesRealLastSegment(t *testing.T) {
	// No $ref at all -- an inline response schema, the one real case
	// deriveNoun's own path-based fallback exists for.
	inlineArray := openapi3.NewArraySchema()
	inlineArray.Items = openapi3.NewSchemaRef("", openapi3.NewObjectSchema().WithProperty("name", openapi3.NewStringSchema()))
	inlineResp := openapi3.NewSchemaRef("", inlineArray)

	doc := newTestDoc(map[string]*testOp{
		"/accounts/{account_id}/d1/database": {opID: "d1-list-databases", resp: inlineResp},
	})

	candidates, _, err := DiscoverDataSources(doc, "cloudflare")
	if err != nil {
		t.Fatalf("DiscoverDataSources: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected exactly 1 candidate, got %d: %v", len(candidates), candidates)
	}
	if got := candidates[0].TypeName; got != "cloudflare_database" {
		t.Errorf("expected the path's own real last segment \"database\" (not \"d1\", the segment before it), got %q", got)
	}
}
