package snapshot

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
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

func TestGenerateCloudFormationMember_FirstEverMember_RealMinorLevel(t *testing.T) {
	url := serveCFNZip(t, map[string]string{"aws-widget-thing.json": widgetCFNSchemaV1})
	member, _, level, err := GenerateCloudFormationMember("aws", url, ModeResource, config.Provider{BaseURL: "https://cloudcontrolapi.us-east-1.amazonaws.com"}, nil)
	if err != nil {
		t.Fatalf("GenerateCloudFormationMember: %v", err)
	}
	if level != Minor {
		t.Errorf("first-ever member level = %s, want minor", level)
	}
	if member.SchemaSource != SchemaSourceCloudFormation {
		t.Errorf("SchemaSource = %q, want %q", member.SchemaSource, SchemaSourceCloudFormation)
	}
	var probe map[string]any
	if err := json.Unmarshal(member.RawSpec, &probe); err != nil {
		t.Fatalf("RawSpec is not valid JSON: %v", err)
	}

	built, err := LoadCloudFormationMember("aws", member)
	if err != nil {
		t.Fatalf("LoadCloudFormationMember: %v", err)
	}
	if len(built) != 1 {
		t.Fatalf("LoadCloudFormationMember reconstructed %d resources, want 1", len(built))
	}
}

func TestGenerateCloudFormationMember_AdditiveChange_RealMinorLevel(t *testing.T) {
	execCfg := config.Provider{BaseURL: "https://cloudcontrolapi.us-east-1.amazonaws.com"}
	prevMember, _, _, err := GenerateCloudFormationMember("aws", serveCFNZip(t, map[string]string{"aws-widget-thing.json": widgetCFNSchemaV1}), ModeResource, execCfg, nil)
	if err != nil {
		t.Fatalf("first generation: %v", err)
	}

	_, _, level, err := GenerateCloudFormationMember("aws", serveCFNZip(t, map[string]string{"aws-widget-thing.json": widgetCFNSchemaV2AddsField}), ModeResource, execCfg, prevMember)
	if err != nil {
		t.Fatalf("second generation: %v", err)
	}
	if level != Minor {
		t.Errorf("real additive change level = %s, want minor", level)
	}
}

// TestGenerateCloudFormationMember_DataSourceMode_RealFailLoud is the
// founder's own explicit requirement made concrete: CloudFormation has
// no real data-source concept at all, so requesting ModeDataSource must
// fail immediately and loudly, never silently generate resource-shaped
// output under a data-source label.
func TestGenerateCloudFormationMember_DataSourceMode_RealFailLoud(t *testing.T) {
	url := serveCFNZip(t, map[string]string{"aws-widget-thing.json": widgetCFNSchemaV1})
	_, _, _, err := GenerateCloudFormationMember("aws", url, ModeDataSource, config.Provider{BaseURL: "https://cloudcontrolapi.us-east-1.amazonaws.com"}, nil)
	if err == nil {
		t.Fatal("expected a real error requesting ModeDataSource against cloudformation")
	}
	if !errors.Is(err, ErrUnsupportedMode) {
		t.Errorf("error doesn't wrap ErrUnsupportedMode: %v", err)
	}
}

func TestLoadCloudFormationMember_DataSourceMode_RealFailLoud(t *testing.T) {
	url := serveCFNZip(t, map[string]string{"aws-widget-thing.json": widgetCFNSchemaV1})
	member, _, _, err := GenerateCloudFormationMember("aws", url, ModeResource, config.Provider{BaseURL: "https://cloudcontrolapi.us-east-1.amazonaws.com"}, nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	member.Mode = ModeDataSource // simulate a corrupted/mismatched real container
	if _, err := LoadCloudFormationMember("aws", member); err == nil {
		t.Fatal("expected a real error loading a cloudformation member whose own Mode is data_source")
	} else if !errors.Is(err, ErrUnsupportedMode) {
		t.Errorf("error doesn't wrap ErrUnsupportedMode: %v", err)
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

func TestGenerateSmithyMember_RealSQSFixture_FirstEverMember(t *testing.T) {
	url := serveBytes(t, realSQSSmithyModel(t))
	member, _, level, err := GenerateSmithyMember("aws", "aws", url, "AmazonSQS", ModeResource, "", config.Provider{BaseURL: "https://sqs.us-east-1.amazonaws.com"}, nil)
	if err != nil {
		t.Fatalf("GenerateSmithyMember: %v", err)
	}
	if level != Minor {
		t.Errorf("first-ever member level = %s, want minor", level)
	}
	if member.SchemaSource != SchemaSourceSmithy {
		t.Errorf("SchemaSource = %q, want %q", member.SchemaSource, SchemaSourceSmithy)
	}
	if member.WireName != "aws" {
		t.Errorf("WireName = %q, want %q (not stored on generate, or lost)", member.WireName, "aws")
	}
	if member.TargetPrefix != "AmazonSQS" {
		t.Errorf("TargetPrefix = %q, want %q", member.TargetPrefix, "AmazonSQS")
	}

	resources, dataSources, err := LoadSmithyMember("aws", member)
	if err != nil {
		t.Fatalf("LoadSmithyMember: %v", err)
	}
	if len(resources) == 0 {
		t.Fatal("LoadSmithyMember reconstructed zero resources from a real, resource-shaped SQS model")
	}
	if len(dataSources) != 0 {
		t.Errorf("a resource-mode member reloaded %d DATA SOURCES, want zero", len(dataSources))
	}
}

func TestGenerateSmithyMember_DataSourceMode_RealDataSources(t *testing.T) {
	url := serveBytes(t, realSQSSmithyModel(t))
	member, schemas, level, err := GenerateSmithyMember("aws_data_sqs", "aws", url, "AmazonSQS", ModeDataSource, "", config.Provider{BaseURL: "https://sqs.us-east-1.amazonaws.com"}, nil)
	if err != nil {
		t.Fatalf("GenerateSmithyMember (data source): %v", err)
	}
	if level != Minor {
		t.Errorf("first-ever data-source member level = %s, want minor", level)
	}
	if len(schemas) == 0 {
		t.Fatal("zero translated data-source schemas from a real, resource-shaped SQS model -- SQS is real-live-confirmed to have real data-source candidates")
	}

	resources, dataSources, err := LoadSmithyMember("aws_data_sqs", member)
	if err != nil {
		t.Fatalf("LoadSmithyMember: %v", err)
	}
	if len(resources) != 0 {
		t.Errorf("a data-source-mode member reloaded %d RESOURCES, want zero", len(resources))
	}
	if len(dataSources) == 0 {
		t.Fatal("a data-source-mode member reloaded zero data sources")
	}
}

func TestGenerateSmithyMember_WireNameFallsBackToMemberName_WhenEmpty(t *testing.T) {
	url := serveBytes(t, realSQSSmithyModel(t))
	member, _, _, err := GenerateSmithyMember("aws", "aws", url, "AmazonSQS", ModeResource, "", config.Provider{BaseURL: "https://sqs.us-east-1.amazonaws.com"}, nil)
	if err != nil {
		t.Fatalf("GenerateSmithyMember: %v", err)
	}
	member.WireName = "" // simulate a real member with no explicit override

	resources, _, err := LoadSmithyMember("aws", member)
	if err != nil {
		t.Fatalf("LoadSmithyMember with empty WireName: %v", err)
	}
	if len(resources) == 0 {
		t.Fatal("LoadSmithyMember with empty WireName (fallback to member name) reconstructed zero resources")
	}
}

// ---------------------------------------------------------------------
// discovery_docs
// ---------------------------------------------------------------------

// widgetDiscoveryDocV1/V2AddsField mirror the real structural shape
// internal/discoverydoc's own tests already confirm live against Google
// Cloud Pub/Sub (buildPubSubShapedDoc) -- the real "schemas"/"resources"/
// "methods" wire vocabulary internal/discoverydoc.Document's own JSON
// tags expect, not an invented shape. Each also carries one real,
// separate, GET-only resource NODE (widgetSummaries) with no create
// method of its own -- discoverydoc.DiscoverDataSources claims a whole
// NODE at a time (get present, create absent on that SAME node), unlike
// resourcemap's own per-PATH claiming, so a "list" method living
// alongside "get"+"create" on the SAME node (as widgets itself has)
// would never be a real candidate no matter what it's named -- confirmed
// live by this fixture's own first failing draft before landing on the
// real, correct shape.
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
    },
    "widgetSummaries": {
      "methods": {
        "get": {"httpMethod": "GET", "flatPath": "v1/widgetSummaries", "response": {"$ref": "Widget"}}
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
    },
    "widgetSummaries": {
      "methods": {
        "get": {"httpMethod": "GET", "flatPath": "v1/widgetSummaries", "response": {"$ref": "Widget"}}
      }
    }
  }
}`

func TestGenerateDiscoveryDocMember_FirstEverMember_RealMinorLevel(t *testing.T) {
	url := serveSpec(t, widgetDiscoveryDocV1)
	member, _, level, err := GenerateDiscoveryDocMember("widget", url, "", ModeResource, config.Provider{BaseURL: "https://widget.googleapis.com"}, nil)
	if err != nil {
		t.Fatalf("GenerateDiscoveryDocMember: %v", err)
	}
	if level != Minor {
		t.Errorf("first-ever member level = %s, want minor", level)
	}
	if member.SchemaSource != SchemaSourceDiscoveryDoc {
		t.Errorf("SchemaSource = %q, want %q", member.SchemaSource, SchemaSourceDiscoveryDoc)
	}

	built, _, err := LoadDiscoveryDocMember("widget", member)
	if err != nil {
		t.Fatalf("LoadDiscoveryDocMember: %v", err)
	}
	if len(built) == 0 {
		t.Fatal("LoadDiscoveryDocMember reconstructed zero resources from a real, CRUD-shaped document")
	}
}

func TestGenerateDiscoveryDocMember_AdditiveChange_RealMinorLevel(t *testing.T) {
	execCfg := config.Provider{BaseURL: "https://widget.googleapis.com"}
	prevMember, _, _, err := GenerateDiscoveryDocMember("widget", serveSpec(t, widgetDiscoveryDocV1), "", ModeResource, execCfg, nil)
	if err != nil {
		t.Fatalf("first generation: %v", err)
	}

	_, _, level, err := GenerateDiscoveryDocMember("widget", serveSpec(t, widgetDiscoveryDocV2AddsField), "", ModeResource, execCfg, prevMember)
	if err != nil {
		t.Fatalf("second generation: %v", err)
	}
	if level != Minor {
		t.Errorf("real additive change level = %s, want minor", level)
	}
}

func TestGenerateDiscoveryDocMember_DataSourceMode_RealDataSources(t *testing.T) {
	url := serveSpec(t, widgetDiscoveryDocV1)
	member, schemas, level, err := GenerateDiscoveryDocMember("widget_ds", url, "", ModeDataSource, config.Provider{BaseURL: "https://widget.googleapis.com"}, nil)
	if err != nil {
		t.Fatalf("GenerateDiscoveryDocMember (data source): %v", err)
	}
	if level != Minor {
		t.Errorf("first-ever data-source member level = %s, want minor", level)
	}
	if len(schemas) == 0 {
		t.Fatal("zero translated data-source schemas -- the real, unclaimed list method should have been a real candidate")
	}

	resources, dataSources, err := LoadDiscoveryDocMember("widget_ds", member)
	if err != nil {
		t.Fatalf("LoadDiscoveryDocMember: %v", err)
	}
	if len(resources) != 0 {
		t.Errorf("a data-source-mode member reloaded %d RESOURCES, want zero", len(resources))
	}
	if len(dataSources) == 0 {
		t.Fatal("a data-source-mode member reloaded zero data sources")
	}
}
