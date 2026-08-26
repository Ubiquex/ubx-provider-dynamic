package snapshot

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/ubiquex/ubx-provider-dynamic/internal/config"
)

// ---------------------------------------------------------------------
// cloudformation
// ---------------------------------------------------------------------

// widgetCFNSchemaV1/V2AddsField are real, minimal, self-contained CFN
// resource schema JSON documents (the real "typeName"/"properties"/
// "required"/"readOnlyProperties"/"primaryIdentifier" wire shape
// internal/cloudformation.ResourceSchema's own JSON tags expect) -- V2
// adds a purely additive field on top of V1, matching the same real
// minor-bump shape internal/snapshot's own openapi tests already prove.
const widgetCFNSchemaV1 = `{
  "typeName": "AWS::Widget::Thing",
  "properties": {
    "id": {"type": "string"},
    "name": {"type": "string"}
  },
  "required": ["name"],
  "readOnlyProperties": ["/properties/id"],
  "primaryIdentifier": ["/properties/id"]
}`

const widgetCFNSchemaV2AddsField = `{
  "typeName": "AWS::Widget::Thing",
  "properties": {
    "id": {"type": "string"},
    "name": {"type": "string"},
    "color": {"type": "string"}
  },
  "required": ["name"],
  "readOnlyProperties": ["/properties/id"],
  "primaryIdentifier": ["/properties/id"]
}`

// serveCFNZip builds a real, in-memory zip (the exact real shape
// internal/cloudformation.Fetch/parseRegistryZip already parses -- one
// "aws-*.json" entry per resource type) and serves it over httptest,
// mirroring the real, live CloudformationSchema.zip registry shape.
func serveCFNZip(t *testing.T, entries map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip.Create(%s): %v", name, err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip.Close: %v", err)
	}
	zipBytes := buf.Bytes()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		w.Write(zipBytes)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestGenerateCloudFormation_FirstEverSnapshot_Real100(t *testing.T) {
	url := serveCFNZip(t, map[string]string{"aws-widget-thing.json": widgetCFNSchemaV1})
	snap, err := GenerateCloudFormation("aws", url, config.Provider{BaseURL: "https://cloudcontrolapi.us-east-1.amazonaws.com"}, nil)
	if err != nil {
		t.Fatalf("GenerateCloudFormation: %v", err)
	}
	if snap.Version != "1.0.0" {
		t.Errorf("first-ever snapshot version = %q, want 1.0.0", snap.Version)
	}
	if snap.SchemaSource != SchemaSourceCloudFormation {
		t.Errorf("SchemaSource = %q, want %q", snap.SchemaSource, SchemaSourceCloudFormation)
	}
	var probe map[string]any
	if err := json.Unmarshal(snap.RawSpec, &probe); err != nil {
		t.Fatalf("RawSpec is not valid JSON: %v", err)
	}

	built, err := LoadCloudFormation(snap)
	if err != nil {
		t.Fatalf("LoadCloudFormation: %v", err)
	}
	if len(built) != 1 {
		t.Fatalf("LoadCloudFormation reconstructed %d resources, want 1", len(built))
	}
}

func TestGenerateCloudFormation_AdditiveChange_RealMinorBump(t *testing.T) {
	execCfg := config.Provider{BaseURL: "https://cloudcontrolapi.us-east-1.amazonaws.com"}
	prev, err := GenerateCloudFormation("aws", serveCFNZip(t, map[string]string{"aws-widget-thing.json": widgetCFNSchemaV1}), execCfg, nil)
	if err != nil {
		t.Fatalf("first generation: %v", err)
	}

	next, err := GenerateCloudFormation("aws", serveCFNZip(t, map[string]string{"aws-widget-thing.json": widgetCFNSchemaV2AddsField}), execCfg, prev)
	if err != nil {
		t.Fatalf("second generation: %v", err)
	}
	if next.Version != "1.1.0" {
		t.Errorf("real minor bump: version = %q, want 1.1.0", next.Version)
	}
}

// ---------------------------------------------------------------------
// smithy
// ---------------------------------------------------------------------

// realSQSSmithyModel is a real, previously-fetched SQS Smithy model
// (internal/smithy/testdata/sqs.json, fetched live from aws/api-models-aws
// -- the same real fixture internal/smithy's own tests already use), not
// a synthetic model invented to fit this test.
func realSQSSmithyModel(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("../smithy/testdata/sqs.json")
	if err != nil {
		t.Fatalf("read real SQS Smithy fixture: %v", err)
	}
	return data
}

func serveBytes(t *testing.T, body []byte) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestGenerateSmithy_RealSQSFixture_FirstEverSnapshot(t *testing.T) {
	url := serveBytes(t, realSQSSmithyModel(t))
	snap, err := GenerateSmithy("aws", "aws", url, "AmazonSQS", config.Provider{BaseURL: "https://sqs.us-east-1.amazonaws.com"}, nil)
	if err != nil {
		t.Fatalf("GenerateSmithy: %v", err)
	}
	if snap.Version != "1.0.0" {
		t.Errorf("first-ever snapshot version = %q, want 1.0.0", snap.Version)
	}
	if snap.SchemaSource != SchemaSourceSmithy {
		t.Errorf("SchemaSource = %q, want %q", snap.SchemaSource, SchemaSourceSmithy)
	}
	if snap.WireName != "aws" {
		t.Errorf("WireName = %q, want %q (not stored on generate, or lost)", snap.WireName, "aws")
	}
	if snap.TargetPrefix != "AmazonSQS" {
		t.Errorf("TargetPrefix = %q, want %q", snap.TargetPrefix, "AmazonSQS")
	}

	built, err := LoadSmithy(snap)
	if err != nil {
		t.Fatalf("LoadSmithy: %v", err)
	}
	if len(built) == 0 {
		t.Fatal("LoadSmithy reconstructed zero resources from a real, resource-shaped SQS model")
	}
}

func TestGenerateSmithy_WireNameFallsBackToProvider_WhenSnapshotPredatesTheField(t *testing.T) {
	// A real format-1 snapshot (predates UBI-182's WireName field) would
	// unmarshal WireName as "" -- LoadSmithy must fall back to Provider,
	// matching config.Provider.WireName's own "defaults to name"
	// convention, not fail outright on an old-shaped snapshot.
	url := serveBytes(t, realSQSSmithyModel(t))
	snap, err := GenerateSmithy("aws", "aws", url, "AmazonSQS", config.Provider{BaseURL: "https://sqs.us-east-1.amazonaws.com"}, nil)
	if err != nil {
		t.Fatalf("GenerateSmithy: %v", err)
	}
	snap.WireName = "" // simulate a real, pre-UBI-182 snapshot

	built, err := LoadSmithy(snap)
	if err != nil {
		t.Fatalf("LoadSmithy with empty WireName: %v", err)
	}
	if len(built) == 0 {
		t.Fatal("LoadSmithy with empty WireName (fallback to Provider) reconstructed zero resources")
	}
}

// ---------------------------------------------------------------------
// discovery_docs
// ---------------------------------------------------------------------

// widgetDiscoveryDocV1/V2AddsField mirror the real structural shape
// internal/discoverydoc's own tests already confirm live against Google
// Cloud Pub/Sub (buildPubSubShapedDoc) -- the real "schemas"/"resources"/
// "methods" wire vocabulary internal/discoverydoc.Document's own JSON
// tags expect, not an invented shape.
const widgetDiscoveryDocV1 = `{
  "name": "widget",
  "schemas": {
    "Widget": {
      "type": "object",
      "properties": {
        "name": {"type": "string", "description": "The widget's own name."},
        "id": {"type": "string", "readOnly": true}
      }
    }
  },
  "resources": {
    "widgets": {
      "methods": {
        "get":    {"httpMethod": "GET",  "flatPath": "v1/widgets/{widgetsId}", "response": {"$ref": "Widget"}},
        "create": {"httpMethod": "POST", "flatPath": "v1/widgets",             "request": {"$ref": "Widget"}, "response": {"$ref": "Widget"}}
      }
    }
  }
}`

const widgetDiscoveryDocV2AddsField = `{
  "name": "widget",
  "schemas": {
    "Widget": {
      "type": "object",
      "properties": {
        "name": {"type": "string", "description": "The widget's own name."},
        "id": {"type": "string", "readOnly": true},
        "color": {"type": "string"}
      }
    }
  },
  "resources": {
    "widgets": {
      "methods": {
        "get":    {"httpMethod": "GET",  "flatPath": "v1/widgets/{widgetsId}", "response": {"$ref": "Widget"}},
        "create": {"httpMethod": "POST", "flatPath": "v1/widgets",             "request": {"$ref": "Widget"}, "response": {"$ref": "Widget"}}
      }
    }
  }
}`

func TestGenerateDiscoveryDoc_FirstEverSnapshot_Real100(t *testing.T) {
	url := serveSpec(t, widgetDiscoveryDocV1)
	snap, err := GenerateDiscoveryDoc("widget", url, "", config.Provider{BaseURL: "https://widget.googleapis.com"}, nil)
	if err != nil {
		t.Fatalf("GenerateDiscoveryDoc: %v", err)
	}
	if snap.Version != "1.0.0" {
		t.Errorf("first-ever snapshot version = %q, want 1.0.0", snap.Version)
	}
	if snap.SchemaSource != SchemaSourceDiscoveryDoc {
		t.Errorf("SchemaSource = %q, want %q", snap.SchemaSource, SchemaSourceDiscoveryDoc)
	}

	built, err := LoadDiscoveryDoc(snap)
	if err != nil {
		t.Fatalf("LoadDiscoveryDoc: %v", err)
	}
	if len(built) == 0 {
		t.Fatal("LoadDiscoveryDoc reconstructed zero resources from a real, CRUD-shaped document")
	}
}

func TestGenerateDiscoveryDoc_AdditiveChange_RealMinorBump(t *testing.T) {
	execCfg := config.Provider{BaseURL: "https://widget.googleapis.com"}
	prev, err := GenerateDiscoveryDoc("widget", serveSpec(t, widgetDiscoveryDocV1), "", execCfg, nil)
	if err != nil {
		t.Fatalf("first generation: %v", err)
	}

	next, err := GenerateDiscoveryDoc("widget", serveSpec(t, widgetDiscoveryDocV2AddsField), "", execCfg, prev)
	if err != nil {
		t.Fatalf("second generation: %v", err)
	}
	if next.Version != "1.1.0" {
		t.Errorf("real minor bump: version = %q, want 1.1.0", next.Version)
	}
}
