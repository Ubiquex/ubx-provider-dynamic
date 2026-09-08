package snapshot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Identity, and why it travels in the snapshot at all.
//
// tfplugin6 gives a SchemaAttribute four things to say about itself:
// Required, Optional, Computed, Sensitive. None of them means "this is
// how you find the resource again", so ubx had to guess, and guessed the
// Terraform way: an attribute named "id", or failing that the Required
// ones. A CloudFormation resource has neither, because its
// primaryIdentifier is readOnly and therefore Computed, which ubx's
// derivation deliberately excludes as unstable. Measured across the real
// published AWS snapshot: 10% of resource types could derive no lookup
// key at all and another 53% derived one missing the identifier, which
// left those resources undeletable and, worse, outside drift detection.
//
// The schema is HashiCorp's wire format and not ours to extend. The
// snapshot is ours and already travels with the provider, so identity
// goes there.

const identityQueueSchema = `{
  "typeName": "AWS::SQS::Queue",
  "properties": {
    "QueueName": {"type": "string"},
    "QueueUrl": {"type": "string"},
    "Arn": {"type": "string"}
  },
  "readOnlyProperties": ["/properties/QueueUrl", "/properties/Arn"],
  "primaryIdentifier": ["/properties/QueueUrl"]
}`

// A compound primary identifier, which CCAPI genuinely has: both parts
// must survive, in the resource's own declared order, or the joined
// CCAPI Identifier cannot be reconstructed.
const identityCompoundSchema = `{
  "typeName": "AWS::ApiGateway::Deployment",
  "properties": {
    "RestApiId": {"type": "string"},
    "DeploymentId": {"type": "string"},
    "Description": {"type": "string"}
  },
  "readOnlyProperties": ["/properties/DeploymentId"],
  "primaryIdentifier": ["/properties/RestApiId", "/properties/DeploymentId"]
}`

func cfnMember(t *testing.T, schemas ...string) *MemberSnapshot {
	t.Helper()
	files := map[string]json.RawMessage{}
	for _, s := range schemas {
		var probe struct {
			TypeName string `json:"typeName"`
		}
		if err := json.Unmarshal([]byte(s), &probe); err != nil {
			t.Fatalf("fixture is not valid JSON: %v", err)
		}
		files[probe.TypeName] = json.RawMessage(s)
	}
	raw, err := json.Marshal(files)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	return &MemberSnapshot{SchemaSource: SchemaSourceCloudFormation, Mode: ModeResource, RawSpec: raw}
}

func TestCollectIdentity_CloudFormationPrimaryIdentifier(t *testing.T) {
	got := collectIdentity(map[string]*MemberSnapshot{
		"aws": cfnMember(t, identityQueueSchema, identityCompoundSchema),
	})

	// The exact case that could not be deleted or drift-checked.
	if want := []string{"queue_url"}; !equalStrs(got["aws_sqs_queue"], want) {
		t.Errorf("aws_sqs_queue identity = %v, want %v", got["aws_sqs_queue"], want)
	}
	// Order matters: CCAPI joins a compound identifier with "|" in the
	// resource's own declared order, so a reordered pair is a different
	// identifier, not the same one.
	if want := []string{"rest_api_id", "deployment_id"}; !equalStrs(got["aws_api_gateway_deployment"], want) {
		t.Errorf("compound identity = %v, want %v in declared order", got["aws_api_gateway_deployment"], want)
	}
}

// A resource with no primaryIdentifier reports nothing rather than
// guessing, so ubx falls back to its own derivation instead of being
// handed a key that cannot re-find the resource.
func TestCollectIdentity_NoPrimaryIdentifier_ReportsNothing(t *testing.T) {
	got := collectIdentity(map[string]*MemberSnapshot{
		"aws": cfnMember(t, `{"typeName":"AWS::Fake::Thing","properties":{"Name":{"type":"string"}}}`),
	})
	if len(got) != 0 {
		t.Fatalf("expected no identity reported, got %v", got)
	}
}

// Round trip through the on-disk split format, since that is how ubx
// actually receives this.
func TestSaveLoadSplit_IdentityRoundTrips(t *testing.T) {
	dir := t.TempDir()
	snap := &Snapshot{
		SchemaFormat: CurrentSchemaFormat,
		Provider:     "aws",
		Version:      "9.9.9",
		Members:      map[string]*MemberSnapshot{"aws": cfnMember(t, identityQueueSchema)},
		Identity:     map[string][]string{"aws_sqs_queue": {"queue_url"}},
	}
	if err := SaveSplit(dir, snap); err != nil {
		t.Fatalf("SaveSplit: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "identity.json")); err != nil {
		t.Fatalf("identity.json was not written: %v", err)
	}

	back, err := LoadSplit(dir)
	if err != nil {
		t.Fatalf("LoadSplit: %v", err)
	}
	if !equalStrs(back.Identity["aws_sqs_queue"], []string{"queue_url"}) {
		t.Fatalf("identity did not round trip: %v", back.Identity)
	}
}

// Every snapshot published before this file existed has no identity.json,
// and must keep loading. Absent means "this snapshot cannot say", and ubx
// falls back exactly as before.
func TestLoadSplit_MissingIdentityFile_IsNotAnError(t *testing.T) {
	dir := t.TempDir()
	snap := &Snapshot{
		SchemaFormat: CurrentSchemaFormat,
		Provider:     "aws",
		Version:      "9.9.9",
		Members:      map[string]*MemberSnapshot{"aws": cfnMember(t, identityQueueSchema)},
	}
	if err := SaveSplit(dir, snap); err != nil {
		t.Fatalf("SaveSplit: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "identity.json")); !os.IsNotExist(err) {
		t.Fatalf("expected no identity.json to be written when there is none: %v", err)
	}
	back, err := LoadSplit(dir)
	if err != nil {
		t.Fatalf("a snapshot without identity.json must still load: %v", err)
	}
	if len(back.Identity) != 0 {
		t.Fatalf("expected no identity, got %v", back.Identity)
	}
}

// A corrupt identity.json is an error, deliberately. Silently continuing
// would be indistinguishable from "this provider has no identity to
// report" and would reintroduce the exact silence this mechanism removes.
func TestLoadSplit_CorruptIdentityFile_IsAnError(t *testing.T) {
	dir := t.TempDir()
	snap := &Snapshot{
		SchemaFormat: CurrentSchemaFormat,
		Provider:     "aws",
		Version:      "9.9.9",
		Members:      map[string]*MemberSnapshot{"aws": cfnMember(t, identityQueueSchema)},
		Identity:     map[string][]string{"aws_sqs_queue": {"queue_url"}},
	}
	if err := SaveSplit(dir, snap); err != nil {
		t.Fatalf("SaveSplit: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "identity.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write corrupt identity: %v", err)
	}
	if _, err := LoadSplit(dir); err == nil {
		t.Fatal("a corrupt identity.json must fail loudly, not read as absent")
	}
}

func equalStrs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
