package config

import "testing"

func TestParseDuration(t *testing.T) {
	if d, ok, err := ParseDuration(""); err != nil || ok || d != 0 {
		t.Fatalf("empty: d=%v ok=%v err=%v", d, ok, err)
	}
	d, ok, err := ParseDuration("30s")
	if err != nil || !ok || d.Seconds() != 30 {
		t.Fatalf("30s: d=%v ok=%v err=%v", d, ok, err)
	}
	if _, _, err := ParseDuration("not-a-duration"); err == nil {
		t.Fatal("expected an error for a malformed duration")
	}
}

func TestLoadNamed_RejectsMalformedRetryDuration(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `
[dynamic_providers.github]
schema_source = "openapi"
schema_url = "https://example.invalid/openapi.json"
base_url = "https://api.github.com"

[dynamic_providers.github.retry]
initial_backoff = "not-a-duration"
`)
	if _, err := LoadNamed(dir, "github"); err == nil {
		t.Fatal("expected a config-load-time error for a malformed retry duration")
	}
}

func TestLoadNamed_RejectsAsyncEnabledWithoutPollPath(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `
[dynamic_providers.github]
schema_source = "openapi"
schema_url = "https://example.invalid/openapi.json"
base_url = "https://api.github.com"

[dynamic_providers.github.resources.github_widget.async]
enabled = true
operation_id_field = "id"
`)
	if _, err := LoadNamed(dir, "github"); err == nil {
		t.Fatal("expected an error for async enabled with no poll_path_template")
	}
}

func TestLoadNamed_RealExecutionConfigShape(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `
[dynamic_providers.github]
schema_source = "openapi"
schema_url = "https://example.invalid/openapi.json"
base_url = "https://api.github.com"

[dynamic_providers.github.retry]
max_attempts = 7
initial_backoff = "100ms"
max_backoff = "20s"
jitter = false
respect_retry_after = true
rate_limit_reset_header = "X-RateLimit-Reset"

[dynamic_providers.github.timeouts]
create = "90s"
read = "20s"
default = "30s"

[dynamic_providers.github.resources.github_widget.async]
enabled = true
operation_id_field = "id"
poll_path_template = "/jobs/{operation_id}"
status_field = "status"
terminal_success_values = ["succeeded"]
terminal_failure_values = ["failed", "cancelled"]
poll_interval = "2s"
poll_timeout = "5m"

[dynamic_providers.github.resources.github_widget.drift]
ignore = ["updated_at"]
[dynamic_providers.github.resources.github_widget.drift.normalize]
homepage = "lowercase"
`)
	p, err := LoadNamed(dir, "github")
	if err != nil {
		t.Fatal(err)
	}
	if p.Retry.MaxAttempts != 7 || p.Retry.InitialBackoff != "100ms" {
		t.Fatalf("retry: %+v", p.Retry)
	}
	if p.Retry.Jitter == nil || *p.Retry.Jitter != false {
		t.Fatalf("jitter: %+v", p.Retry.Jitter)
	}
	if p.Timeouts.Create != "90s" || p.Timeouts.Default != "30s" {
		t.Fatalf("timeouts: %+v", p.Timeouts)
	}
	rc, ok := p.Resources["github_widget"]
	if !ok {
		t.Fatalf("expected github_widget resource config, got %+v", p.Resources)
	}
	if !rc.Async.Enabled || rc.Async.PollPathTemplate != "/jobs/{operation_id}" {
		t.Fatalf("async: %+v", rc.Async)
	}
	if len(rc.Async.TerminalFailureValues) != 2 {
		t.Fatalf("terminal failure values: %+v", rc.Async.TerminalFailureValues)
	}
	if len(rc.Drift.Ignore) != 1 || rc.Drift.Ignore[0] != "updated_at" {
		t.Fatalf("drift ignore: %+v", rc.Drift.Ignore)
	}
	if rc.Drift.Normalize["homepage"] != "lowercase" {
		t.Fatalf("drift normalize: %+v", rc.Drift.Normalize)
	}
}
