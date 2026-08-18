package config

import (
	"testing"

	"github.com/ubiquex/ubx-provider-dynamic/internal/auth"
)

// TestAuth_RealTOMLShape_ParsesIntoAuthBuild is a real, end-to-end
// regression test for the exact TOML nesting internal/auth's own real
// types expect -- written after this session got it wrong once in the
// README's own worked example (`[[dynamic_providers.<name>.auth.headers]]`,
// missing the `params` segment `Auth.Params`'s own `toml:"params"` tag
// requires) and only caught it by actually running the parse, not by
// re-reading the struct tags. Keeps that exact mistake from silently
// recurring in docs or code.
func TestAuth_RealTOMLShape_ParsesIntoAuthBuild(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `
[dynamic_providers.github]
schema_source = "openapi"
schema_url = "https://example.invalid/openapi.json"
base_url = "https://api.github.com"

[dynamic_providers.github.auth]
type = "api_key_header"

[[dynamic_providers.github.auth.params.headers]]
name = "Authorization"
value_env = "GITHUB_TOKEN"
value_prefix = "Bearer "
`)
	p, err := LoadNamed(dir, "github")
	if err != nil {
		t.Fatal(err)
	}
	if p.Auth.Type != "api_key_header" {
		t.Fatalf("Auth.Type = %q", p.Auth.Type)
	}

	a, err := auth.Build(p.Auth.Type, p.Auth.Params)
	if err != nil {
		t.Fatalf("auth.Build: %v (params: %+v)", err, p.Auth.Params)
	}
	if a == nil {
		t.Fatal("expected a real, non-nil Authenticator")
	}
}
