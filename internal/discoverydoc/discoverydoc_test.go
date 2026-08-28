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
	resources, notes, err := Discover(doc, "google", "")
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

// TestDiscover_VersionQualifier_ThreadsIntoTypeName_ZeroChangeWhenEmpty
// locks in the real fix for a real, live bug: Google keeps a Discovery
// Document's own top-level "name" field IDENTICAL across release
// channels (compute's v1 and beta documents both report name="compute"),
// so a stable and a beta/alpha entry configured for the same API produce
// byte-identical typeNames for every resource the two channels share.
// versionQualifier, threaded through from config.Provider.VersionQualifier,
// is the fix -- empty (every one of the 162 already-configured GCP
// entries) produces the exact same typeName as before this parameter
// existed; a real value (a new secondary-channel entry) inserts it
// between the API name and the noun, so the two channels' otherwise-
// identical typeNames no longer collide.
func TestDiscover_VersionQualifier_ThreadsIntoTypeName_ZeroChangeWhenEmpty(t *testing.T) {
	doc := buildPubSubShapedDoc()

	stable, _, err := Discover(doc, "google", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(stable) != 1 || stable[0].TypeName != "google_pubsub_topic" {
		t.Fatalf("empty versionQualifier must produce the exact pre-existing typeName, got %+v", stable)
	}

	beta, _, err := Discover(doc, "google", "beta")
	if err != nil {
		t.Fatal(err)
	}
	if len(beta) != 1 || beta[0].TypeName != "google_pubsub_beta_topic" {
		t.Fatalf("non-empty versionQualifier must thread between the API name and the noun, got %+v", beta)
	}

	if stable[0].TypeName == beta[0].TypeName {
		t.Fatal("stable and beta typeNames must not collide -- this is the exact real bug this parameter fixes")
	}
}

// TestDiscover_PrefixedCreateMethod_RealIamV2PoliciesShape mirrors the
// real structural shape confirmed live against Google Cloud IAM's own
// real, published v2 discovery document
// (https://iam.googleapis.com/$discovery/rest?version=v2): the real
// "policies" collection is fully CRUD-capable but names its own create
// method "createPolicy" rather than the bare "create" firstMethod alone
// requires -- found while investigating why the founder's own "129 real
// GCP gap" figure included 6 resources this collection alone already
// covers. Before this fix, Discover silently treated this as a read-only
// node; this test locks in the corrected behavior.
func TestDiscover_PrefixedCreateMethod_RealIamV2PoliciesShape(t *testing.T) {
	doc := &Document{
		Name: "iampolicies",
		Schemas: map[string]*rawSchema{
			"GoogleIamV2Policy": {
				Type: "object",
				Properties: map[string]*rawSchema{
					"name": {Type: "string", Description: "The resource name of the policy."},
				},
			},
		},
		Resources: map[string]*rawResource{
			"policies": {
				Methods: map[string]*rawMethod{
					"createPolicy": {HTTPMethod: "POST", FlatPath: "v2/{policyParent}", Request: &rawRef{Ref: "GoogleIamV2Policy"}, Response: &rawRef{Ref: "GoogleIamV2Policy"}},
					"get":          {HTTPMethod: "GET", FlatPath: "v2/{policyName}", Response: &rawRef{Ref: "GoogleIamV2Policy"}},
					"update":       {HTTPMethod: "PUT", FlatPath: "v2/{policyName}", Request: &rawRef{Ref: "GoogleIamV2Policy"}, Response: &rawRef{Ref: "GoogleIamV2Policy"}},
					"delete":       {HTTPMethod: "DELETE", FlatPath: "v2/{policyName}"},
					"listPolicies": {HTTPMethod: "GET", FlatPath: "v2/{policyParent}"},
				},
			},
		},
	}
	resources, _, err := Discover(doc, "google", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(resources) != 1 {
		t.Fatalf("expected exactly one resource (createPolicy should now match as a create fallback), got %d", len(resources))
	}
	r := resources[0]
	if r.TypeName != "google_iampolicies_policy" {
		t.Fatalf("expected google_iampolicies_policy, got %q", r.TypeName)
	}
	if r.CreateMethod != "POST" || r.CreatePath != "v2/{policyParent}" {
		t.Fatalf("unexpected create: %s %s", r.CreateMethod, r.CreatePath)
	}
	if r.UpdateMethod != "PUT" {
		t.Fatalf("expected PUT update, got %q", r.UpdateMethod)
	}
}

// TestDiscover_UnrelatedVerb_NotMatchedAsCreate confirms the fix stays
// narrow: a real, differently-named create-equivalent verb ("register",
// domains:v1's own real "registrations" resource) must NOT be picked up
// by firstPrefixedMethod's own prefix match -- that's a genuinely
// different action, not the same "createXxx"/"insertXxx" naming
// convention, and this package correctly still reports it read-only/
// skipped rather than guessing.
func TestDiscover_UnrelatedVerb_NotMatchedAsCreate(t *testing.T) {
	doc := &Document{
		Name: "domains",
		Resources: map[string]*rawResource{
			"registrations": {
				Methods: map[string]*rawMethod{
					"register": {HTTPMethod: "POST", FlatPath: "v1/{parent}/registrations:register", Response: &rawRef{Ref: "Registration"}},
					"get":      {HTTPMethod: "GET", FlatPath: "v1/{name}", Response: &rawRef{Ref: "Registration"}},
					"patch":    {HTTPMethod: "PATCH", FlatPath: "v1/{name}", Response: &rawRef{Ref: "Registration"}},
					"delete":   {HTTPMethod: "DELETE", FlatPath: "v1/{name}"},
				},
			},
		},
	}
	resources, notes, err := Discover(doc, "google", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(resources) != 0 {
		t.Fatalf("expected zero resources (\"register\" must not be treated as a create fallback), got %+v", resources)
	}
	found := false
	for _, n := range notes {
		if n.Path == "registrations" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a note explaining registrations was skipped, got %+v", notes)
	}
}

// TestDiscover_UBI181AllowlistedVerb_RealBackupAndDRShape mirrors the
// ticket's own real, named example: Google Backup and DR's "backups"
// resource has get/list/patch/delete/restore/initiateBackup, no method
// literally named "create"/"insert" or "create"/"insert"-prefixed --
// UBI-181's own narrow create-verb allowlist (dsfilter.MatchesCreateVerb
// matching "restore" here) is what promotes it to a real resource
// instead of a permanent skip Note.
func TestDiscover_UBI181AllowlistedVerb_RealBackupAndDRShape(t *testing.T) {
	doc := &Document{
		Name: "backupdr",
		Resources: map[string]*rawResource{
			"backups": {
				Methods: map[string]*rawMethod{
					"get":            {HTTPMethod: "GET", FlatPath: "v1/{name}", Response: &rawRef{Ref: "Backup"}},
					"list":           {HTTPMethod: "GET", FlatPath: "v1/{parent}/backups", Response: &rawRef{Ref: "ListBackupsResponse"}},
					"patch":          {HTTPMethod: "PATCH", FlatPath: "v1/{name}", Response: &rawRef{Ref: "Operation"}},
					"delete":         {HTTPMethod: "DELETE", FlatPath: "v1/{name}", Response: &rawRef{Ref: "Operation"}},
					"restore":        {HTTPMethod: "POST", FlatPath: "v1/{name}:restore", Response: &rawRef{Ref: "Operation"}},
					"initiateBackup": {HTTPMethod: "POST", FlatPath: "v1/{name}:initiateBackup", Response: &rawRef{Ref: "Operation"}},
				},
			},
		},
	}
	resources, notes, err := Discover(doc, "google", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(resources) != 1 {
		t.Fatalf("expected the allowlisted-verb backup to be admitted as one real resource, got %d: %+v (notes: %+v)", len(resources), resources, notes)
	}
	if resources[0].CreateMethod != "POST" {
		t.Fatalf("expected create via the allowlisted restore/initiateBackup method, got %+v", resources[0])
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
	resources, notes, err := Discover(doc, "example", "")
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
	built, notes, err := Build(doc, "google", "")
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

// TestDiscover_CamelCaseResourceKey_SnakeCased is a real regression test
// for a real bug caught only by actually generating Go code against the
// live, configured GCP Compute discovery document (95 real resources):
// a Discovery Document's own resource-tree keys are camelCase
// ("backendBuckets", "targetHttpProxies"), unlike OpenAPI's own already-
// snake_case ref names -- a raw camelCase noun reached every per-language
// template's own real "only lowercase ascii + digits + underscore"
// wire-name guard and failed generation outright.
func TestDiscover_CamelCaseResourceKey_SnakeCased(t *testing.T) {
	doc := &Document{
		Name: "compute",
		Schemas: map[string]*rawSchema{
			"BackendBucket": {Type: "object", Properties: map[string]*rawSchema{
				"name": {Type: "string"},
			}},
		},
		Resources: map[string]*rawResource{
			"backendBuckets": {
				Methods: map[string]*rawMethod{
					"get":    {HTTPMethod: "GET", FlatPath: "v1/projects/{project}/global/backendBuckets/{backendBucket}", Response: &rawRef{Ref: "BackendBucket"}},
					"insert": {HTTPMethod: "POST", FlatPath: "v1/projects/{project}/global/backendBuckets", Request: &rawRef{Ref: "BackendBucket"}},
				},
			},
		},
	}
	resources, _, err := Discover(doc, "google", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(resources) != 1 {
		t.Fatalf("expected exactly one resource, got %d: %+v", len(resources), resources)
	}
	if got := resources[0].TypeName; got != "google_compute_backend_bucket" {
		t.Fatalf("TypeName = %q, want the real, wire-name-safe snake_case form %q", got, "google_compute_backend_bucket")
	}
}

// TestDiscover_CamelCaseDocName_SnakeCased is UBI-180's own real
// regression test: a Discovery Document's own top-level "name" field
// (doc.Name, Google's own live, API-owner-chosen string) went straight
// into the typeName unnormalized, unlike the resource-tree key just
// above -- confirmed live against the real, configured
// google_siteverification family, whose own real doc.Name is
// "siteVerification" (mixed case), producing the raw typeName
// "google_siteverification_siteVerification_web_resource" and failing
// every per-language template's own real wire-name guard outright, the
// identical failure mode TestDiscover_CamelCaseResourceKey_SnakeCased
// already covers for the resource-tree key, just never exercised for
// doc.Name until a real provider's own live discovery document actually
// carried a mixed-case one.
func TestDiscover_CamelCaseDocName_SnakeCased(t *testing.T) {
	doc := &Document{
		Name: "siteVerification",
		Schemas: map[string]*rawSchema{
			"SiteVerificationWebResourceResource": {Type: "object", Properties: map[string]*rawSchema{
				"id": {Type: "string"},
			}},
		},
		Resources: map[string]*rawResource{
			"webResource": {
				Methods: map[string]*rawMethod{
					"get":    {HTTPMethod: "GET", FlatPath: "v1/webResource/{id}", Response: &rawRef{Ref: "SiteVerificationWebResourceResource"}},
					"insert": {HTTPMethod: "POST", FlatPath: "v1/webResource", Request: &rawRef{Ref: "SiteVerificationWebResourceResource"}},
				},
			},
		},
	}
	resources, _, err := Discover(doc, "google_siteverification", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(resources) != 1 {
		t.Fatalf("expected exactly one resource, got %d: %+v", len(resources), resources)
	}
	if got := resources[0].TypeName; got != "google_siteverification_site_verification_web_resource" {
		t.Fatalf("TypeName = %q, want the real, wire-name-safe snake_case form %q", got, "google_siteverification_site_verification_web_resource")
	}
}

// TestSingularize_RealEsPlurals is the real, found-in-review fix,
// TWICE over. Round 1: the original `strings.TrimSuffix(s, "s")`
// mishandled every real "-es" English plural, confirmed live against
// the real, current compute/v1 Discovery Document --
// "addresses"/"policies"/"proxies" all produced a genuinely misspelled
// singular ("addresse", "policie", "proxie"), 22 real resources
// affected. Round 2: that first fix over-corrected -- checking the
// suffix BEFORE stripping "es" wrongly matched real words that
// already end in "-se" in their own singular form, shipped and
// caught only by re-reading the real generated page list before
// reporting done: "licenses" -> "licens" (real, live, briefly
// shipped). "license"/"house"/"purse" are this test's own real
// regression guards for that second bug specifically -- never delete
// them as "redundant" with the -es cases above them.
func TestSingularize_RealEsPlurals(t *testing.T) {
	cases := map[string]string{
		"addresses":      "address",
		"policies":       "policy",
		"proxies":        "proxy",
		"instances":      "instance",
		"backendBuckets": "backendBucket",
		"boxes":          "box",
		"branches":       "branch",
		"dishes":         "dish",
		"licenses":       "license",
		"houses":         "house",
		"purses":         "purse",
		"dns":            "dn", // real, known limitation: a genuine non-plural ending in "s" -- see doc comment
	}
	for in, want := range cases {
		if got := singularize(in); got != want {
			t.Errorf("singularize(%q) = %q, want %q", in, got, want)
		}
	}
}
