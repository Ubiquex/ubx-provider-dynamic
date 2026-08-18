package auth

import (
	"fmt"
	"net/http"

	"github.com/ubiquex/ubx-provider-dynamic/internal/restexec"
)

// api_key_header covers every real API-key-as-a-header scheme this ticket
// names: GitHub's own single `Authorization: Bearer <token>` and Datadog's
// own real, documented two-header scheme (`DD-API-KEY` +
// `DD-APPLICATION-KEY`, confirmed live -- see apikey_test.go). One real
// mechanism, `headers` a list rather than a single name/value pair,
// covers both without any service-specific code: a config author names as
// many header/env pairs as their real API needs.
func init() {
	Register("api_key_header", buildAPIKeyHeader)
}

// apiKeyHeaderParams is [dynamic_providers.<name>.auth.params]'s own shape
// for this type:
//
//	[[dynamic_providers.<name>.auth.headers]]
//	name = "Authorization"
//	value_env = "GITHUB_TOKEN"
//	value_prefix = "Bearer "
//
// ValueEnv, never a literal value: the same "$secret is a reference, not
// the value" discipline paramsAs's own doc comment cites from ubx's real,
// existing IntentConfig.KeyRef precedent.
type apiKeyHeaderParams struct {
	Headers []apiKeyHeaderEntry `json:"headers"`
}

type apiKeyHeaderEntry struct {
	Name        string `json:"name"`
	ValueEnv    string `json:"value_env"`
	ValuePrefix string `json:"value_prefix"`
}

type apiKeyHeaderAuth struct {
	headers []apiKeyHeaderEntry
}

func buildAPIKeyHeader(params map[string]any) (restexec.Authenticator, error) {
	var p apiKeyHeaderParams
	if err := paramsAs(params, &p); err != nil {
		return nil, fmt.Errorf("api_key_header: parse params: %w", err)
	}
	if len(p.Headers) == 0 {
		return nil, fmt.Errorf("api_key_header: at least one entry in `headers` is required")
	}
	for i, h := range p.Headers {
		if h.Name == "" {
			return nil, fmt.Errorf("api_key_header: headers[%d]: name is required", i)
		}
		if h.ValueEnv == "" {
			return nil, fmt.Errorf("api_key_header: headers[%d] (%s): value_env is required -- literal credential values are never accepted in config", i, h.Name)
		}
	}
	return &apiKeyHeaderAuth{headers: p.Headers}, nil
}

// Apply reads every configured header's own env var fresh on each call --
// not cached at Build time -- so a rotated key (a real operational event,
// not a hypothetical) takes effect on the next request without requiring
// this provider process to be restarted.
func (a *apiKeyHeaderAuth) Apply(req *http.Request) error {
	for _, h := range a.headers {
		v, err := envValue(h.ValueEnv)
		if err != nil {
			return fmt.Errorf("api_key_header: header %q: %w", h.Name, err)
		}
		req.Header.Set(h.Name, h.ValuePrefix+v)
	}
	return nil
}
