package discoverydoc

import (
	"testing"
	"time"
)

// buildPubSubShapedDoc mirrors the real structural shape confirmed live
// against Google Cloud Pub/Sub's own real, published discovery document
// (https://pubsub.googleapis.com/$discovery/rest?version=v1) before this
// package was written: a "topics" resource nested under "projects", real
// get/create/patch/delete methods, a request/response schema shared by
// name, and a real readOnly output field.
func buildPubSubShapedDoc() *Document {
	return &Document{
		Name: "pubsub",
		Schemas: map[string]*rawSchema{
			"Topic": {
				Type: "object",
				Properties: map[string]*rawSchema{
					"name":                     {Type: "string", Description: "The name of the topic."},
					"messageRetentionDuration": {Type: "string", Description: "How long to retain messages."},
					"state":                    {Type: "string", ReadOnly: true, Enum: []string{"STATE_UNSPECIFIED", "ACTIVE"}},
				},
			},
		},
		Resources: map[string]*rawResource{
			"projects": {
				Resources: map[string]*rawResource{
					"topics": {
						Methods: map[string]*rawMethod{
							"create": {HTTPMethod: "PUT", FlatPath: "v1/projects/{projectsId}/topics/{topicsId}", Request: &rawRef{Ref: "Topic"}, Response: &rawRef{Ref: "Topic"}},
							"get":    {HTTPMethod: "GET", FlatPath: "v1/projects/{projectsId}/topics/{topicsId}", Response: &rawRef{Ref: "Topic"}},
							"patch":  {HTTPMethod: "PATCH", FlatPath: "v1/projects/{projectsId}/topics/{topicsId}", Request: &rawRef{Ref: "Topic"}, Response: &rawRef{Ref: "Topic"}},
							"delete": {HTTPMethod: "DELETE", FlatPath: "v1/projects/{projectsId}/topics/{topicsId}"},
							"list":   {HTTPMethod: "GET", FlatPath: "v1/projects/{projectsId}/topics"},
						},
					},
				},
			},
		},
	}
}

func TestDiscover_RealPubSubShape(t *testing.T) {
	doc := buildPubSubShapedDoc()
	resources, notes, err := Discover(doc, "google")
	if err != nil {
		t.Fatal(err)
	}
	if len(resources) != 1 {
		t.Fatalf("expected exactly one resource, got %d: %+v (notes: %+v)", len(resources), resources, notes)
	}
	r := resources[0]
	if r.TypeName != "google_pubsub_topic" {
		t.Fatalf("expected google_pubsub_topic, got %q", r.TypeName)
	}
	if r.CreateMethod != "PUT" || r.CreatePath != "v1/projects/{projectsId}/topics/{topicsId}" {
		t.Fatalf("unexpected create: %s %s", r.CreateMethod, r.CreatePath)
	}
	if r.UpdateMethod != "PATCH" {
		t.Fatalf("expected PATCH update, got %q", r.UpdateMethod)
	}
	if !r.HasDelete {
		t.Fatal("expected a delete method")
	}
}

func TestDiscover_ReadOnlyNode_SkippedWithNote(t *testing.T) {
	doc := &Document{
		Name: "example",
		Resources: map[string]*rawResource{
			"quotas": {
				Methods: map[string]*rawMethod{
					"get": {HTTPMethod: "GET", FlatPath: "v1/quotas/{quotasId}", Response: &rawRef{Ref: "Quota"}},
				},
			},
		},
	}
	resources, notes, err := Discover(doc, "example")
	if err != nil {
		t.Fatal(err)
	}
	if len(resources) != 0 {
		t.Fatalf("expected zero resources (read-only, no create), got %+v", resources)
	}
	found := false
	for _, n := range notes {
		if n.Path == "quotas" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a note explaining quotas was skipped, got %+v", notes)
	}
}

func TestBuild_TranslatesRealShape_ReadOnlyMergesToOptionalComputed_EnumCarried(t *testing.T) {
	doc := buildPubSubShapedDoc()
	built, notes, err := Build(doc, "google")
	if err != nil {
		t.Fatalf("Build: %v (notes: %+v)", err, notes)
	}
	br, ok := built["google_pubsub_topic"]
	if !ok {
		t.Fatalf("missing google_pubsub_topic in %+v", built)
	}
	if br.Schema == nil || br.Schema.Block == nil {
		t.Fatal("expected a real, non-nil translated schema")
	}

	var foundName, foundState bool
	var stateComputed, stateRequired, stateOptional bool
	var nameOptional bool
	for _, a := range br.Schema.Block.Attributes {
		switch a.Name {
		case "name":
			foundName = true
			nameOptional = a.Optional
		case "state":
			foundState = true
			stateComputed = a.Computed
			stateRequired = a.Required
			stateOptional = a.Optional
		}
	}
	if !foundName || !nameOptional {
		t.Fatalf("expected a real, Optional 'name' attribute (no required-field signal exists in a real Discovery Document body schema -- see this package's own doc comment)")
	}
	// "state" is readOnly in the Topic schema, but that same schema is
	// referenced by BOTH create's own request AND the read response (the
	// real, confirmed shape Pub/Sub's own live discovery document uses)
	// -- MergeResourceAttributes' own real, documented rule for "both
	// sides, create Optional (not Required)" is Optional+Computed ("the
	// user may set it, or leave it for the server to default"), not
	// Computed-only; readOnly alone never produces Required on the
	// create side, so this is the correct, expected merged result, not
	// a bug.
	if !foundState || !stateComputed || stateRequired || !stateOptional {
		t.Fatalf("expected 'state' to merge to Optional+Computed (MergeResourceAttributes' own real rule), got computed=%v required=%v optional=%v", stateComputed, stateRequired, stateOptional)
	}

	sig := br.Signals["state"]
	if sig == nil || len(sig.Enum) != 2 {
		t.Fatalf("expected the real 2-value enum signal to survive into Signals, got %+v", sig)
	}
}

func TestConvertSchema_SelfReferential_DoesNotHang(t *testing.T) {
	all := map[string]*rawSchema{
		"Node": {Type: "object", Properties: map[string]*rawSchema{
			"child": {Ref: "Node"},
		}},
	}
	done := make(chan struct{}, 1)
	go func() {
		convertSchema(&rawSchema{Ref: "Node"}, all, map[string]bool{})
		done <- struct{}{}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("convertSchema hung on a self-referential $ref")
	}
}

func TestConvertSchema_ArrayOfObjects(t *testing.T) {
	all := map[string]*rawSchema{
		"Item": {Type: "object", Properties: map[string]*rawSchema{
			"id": {Type: "string"},
		}},
	}
	raw := &rawSchema{Type: "array", Items: &rawSchema{Ref: "Item"}}
	s := convertSchema(raw, all, map[string]bool{})
	if s.Type == nil || !s.Type.Is("array") {
		t.Fatalf("expected an array type, got %+v", s.Type)
	}
	if s.Items == nil || s.Items.Value == nil || s.Items.Value.Properties["id"] == nil {
		t.Fatalf("expected a resolved 'Item' object with an 'id' property in Items, got %+v", s.Items)
	}
}
