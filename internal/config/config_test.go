package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".ubx"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".ubx", "config"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadNamed_Success(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `
[dynamic_providers.github]
schema_source = "openapi"
schema_url = "https://example.invalid/openapi.json"
base_url = "https://api.github.com"
`)
	p, err := LoadNamed(dir, "github")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "github" || p.SchemaSource != SchemaSourceOpenAPI || p.BaseURL != "https://api.github.com" {
		t.Fatalf("unexpected provider: %+v", p)
	}
}

func TestLoadNamed_WireNameOverride(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `
[dynamic_providers.datadog_v2]
schema_source = "openapi"
schema_url = "https://example.invalid/openapi.json"
base_url = "https://api.datadoghq.com"
wire_name = "datadog"
`)
	p, err := LoadNamed(dir, "datadog_v2")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "datadog_v2" {
		t.Errorf("expected table key Name to stay %q, got %q", "datadog_v2", p.Name)
	}
	if p.WireName != "datadog" {
		t.Errorf("expected WireName override %q, got %q", "datadog", p.WireName)
	}
}

func TestLoadNamed_WireNameAbsent_IsEmptyNotName(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `
[dynamic_providers.github]
schema_source = "openapi"
schema_url = "https://example.invalid/openapi.json"
base_url = "https://api.github.com"
`)
	p, err := LoadNamed(dir, "github")
	if err != nil {
		t.Fatal(err)
	}
	if p.WireName != "" {
		t.Errorf("expected empty WireName when unset (caller falls back to Name), got %q", p.WireName)
	}
}

func TestLoadNamed_WalksUpward(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, `
[dynamic_providers.datadog]
schema_source = "openapi"
schema_url = "https://example.invalid/dd.json"
base_url = "https://api.datadoghq.com"
`)
	sub := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	p, err := LoadNamed(sub, "datadog")
	if err != nil {
		t.Fatal(err)
	}
	if p.BaseURL != "https://api.datadoghq.com" {
		t.Fatalf("unexpected provider: %+v", p)
	}
}

func TestLoadNamed_UnknownName(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `
[dynamic_providers.github]
schema_source = "openapi"
schema_url = "https://example.invalid/openapi.json"
base_url = "https://api.github.com"
`)
	_, err := LoadNamed(dir, "nope")
	if err == nil {
		t.Fatal("expected error for undeclared name")
	}
}

func TestLoadNamed_MissingSchemaURL(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `
[dynamic_providers.github]
schema_source = "openapi"
base_url = "https://api.github.com"
`)
	_, err := LoadNamed(dir, "github")
	if err == nil {
		t.Fatal("expected validation error for missing schema_url")
	}
}

func TestLoadNamed_UnimplementedSchemaSource(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `
[dynamic_providers.aws]
schema_source = "inline"
base_url = "https://cloudcontrolapi.us-east-1.amazonaws.com"
`)
	_, err := LoadNamed(dir, "aws")
	if err == nil {
		t.Fatal("expected error for unimplemented schema_source")
	}
}

func TestLoadNamed_CloudFormation(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `
[dynamic_providers.aws]
schema_source = "cloudformation"
schema_url = "https://schema.cloudformation.us-east-1.amazonaws.com/CloudformationSchema.zip"
base_url = "https://cloudcontrolapi.us-east-1.amazonaws.com"
`)
	p, err := LoadNamed(dir, "aws")
	if err != nil {
		t.Fatalf("LoadNamed: %v", err)
	}
	if p.SchemaSource != SchemaSourceCloudFormation {
		t.Fatalf("SchemaSource = %q, want %q", p.SchemaSource, SchemaSourceCloudFormation)
	}
}

func TestLoadNamed_CloudFormation_DataSourcesTrue_RealFailLoud(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `
[dynamic_providers.aws]
schema_source = "cloudformation"
schema_url = "https://schema.cloudformation.us-east-1.amazonaws.com/CloudformationSchema.zip"
base_url = "https://cloudcontrolapi.us-east-1.amazonaws.com"
data_sources = true
`)
	_, err := LoadNamed(dir, "aws")
	if err == nil {
		t.Fatal("expected a real validation error: CloudFormation has no data-source concept, data_sources = true must fail loud at config-load time, not be silently ignored")
	}
}

func TestLoadNamed_RedoclyBundle(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `
[dynamic_providers.digitalocean]
schema_source = "openapi"
schema_url = "https://raw.githubusercontent.com/digitalocean/openapi/main/specification/DigitalOcean-public.v2.yaml"
base_url = "https://api.digitalocean.com"
redocly_bundle = true
`)
	p, err := LoadNamed(dir, "digitalocean")
	if err != nil {
		t.Fatalf("LoadNamed: %v", err)
	}
	if !p.RedoclyBundle {
		t.Fatal("expected RedoclyBundle = true")
	}
}

func TestLoadNamed_RedoclyBundleTrue_NonOpenAPISource_RealFailLoud(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `
[dynamic_providers.kubernetes]
schema_source = "discovery_docs"
schema_url = "https://raw.githubusercontent.com/kubernetes/kubernetes/release-1.37/api/openapi-spec/swagger.json"
base_url = "https://kubernetes.example.invalid"
redocly_bundle = true
`)
	_, err := LoadNamed(dir, "kubernetes")
	if err == nil {
		t.Fatal("expected a real validation error: redocly_bundle is only meaningful for schema_source = openapi, must fail loud at config-load time, not be silently ignored")
	}
}

func TestLoadNamed_NoConfigFile(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadNamed(dir, "github")
	if err == nil {
		t.Fatal("expected error when no .ubx/config exists")
	}
}
