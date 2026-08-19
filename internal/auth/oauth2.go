package auth

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"

	"github.com/ubiquex/ubx-provider-dynamic/internal/restexec"
)

// oauth2_client_credentials is the real RFC 6749 §4.4 two-legged flow --
// built on golang.org/x/oauth2/clientcredentials, the real, official Go
// implementation (already a transitive dependency of terraform-plugin-go's
// own tf6server, confirmed via `go get` reporting an upgrade rather than a
// new module), not hand-rolled token-endpoint logic.
func init() {
	Register("oauth2_client_credentials", buildOAuth2ClientCredentials)
}

// oauth2ClientCredentialsParams is this type's own
// [dynamic_providers.<name>.auth.params] shape:
//
//	type = "oauth2_client_credentials"
//	[dynamic_providers.<name>.auth.params]
//	token_url = "https://example.com/oauth/token"
//	client_id_env = "EXAMPLE_CLIENT_ID"
//	client_secret_env = "EXAMPLE_CLIENT_SECRET"
//	scopes = ["read", "write"]
type oauth2ClientCredentialsParams struct {
	TokenURL        string   `json:"token_url"`
	ClientIDEnv     string   `json:"client_id_env"`
	ClientSecretEnv string   `json:"client_secret_env"`
	Scopes          []string `json:"scopes"`
}

// oauth2ClientCredentialsAuth defers real credential RESOLUTION
// (reading client_id_env/client_secret_env, building the real
// TokenSource) to the first real Apply call, via once -- never at
// Build time. Real, structural finding, checkpoint 9: Build() runs
// unconditionally for every declared [dynamic_providers.<name>.auth]
// table the moment this binary starts, INCLUDING a real, honest
// schema-only launch (GetProviderSchema, never Configure) -- confirmed
// live against Azure's own real central-config entry, which failed to
// even START SERVING (a real "plugin exited before completing
// handshake") on a machine with no AZURE_CLIENT_ID/AZURE_CLIENT_SECRET
// set, despite GetProviderSchema itself never needing a credential at
// all. api_key_header (github/datadog's own real config) never had
// this problem -- its own Apply already re-reads its env var fresh on
// every call, never validating presence until a real request is
// actually being signed. This type now matches that same real,
// generic discipline: structural config correctness (token_url/
// client_id_env/client_secret_env non-empty -- a real config mistake,
// legitimate to fail fast on) stays eager in Build; the real,
// resolvable-or-not CREDENTIAL itself is deferred to Apply, exactly
// once (sync.Once), so a real, successful first exchange still gets
// the SAME real caching/refresh behavior the original eager-at-Build
// design was written to preserve (golang.org/x/oauth2's own
// TokenSource, built once, reused for every subsequent call) -- only
// the TIMING of that one real build moved, not its own real semantics.
type oauth2ClientCredentialsAuth struct {
	params oauth2ClientCredentialsParams

	once    sync.Once
	source  oauth2.TokenSource
	initErr error
}

func buildOAuth2ClientCredentials(params map[string]any) (restexec.Authenticator, error) {
	var p oauth2ClientCredentialsParams
	if err := paramsAs(params, &p); err != nil {
		return nil, fmt.Errorf("oauth2_client_credentials: parse params: %w", err)
	}
	if p.TokenURL == "" {
		return nil, fmt.Errorf("oauth2_client_credentials: token_url is required")
	}
	if p.ClientIDEnv == "" {
		return nil, fmt.Errorf("oauth2_client_credentials: client_id_env is required")
	}
	if p.ClientSecretEnv == "" {
		return nil, fmt.Errorf("oauth2_client_credentials: client_secret_env is required -- literal credential values are never accepted in config")
	}
	return &oauth2ClientCredentialsAuth{params: p}, nil
}

func (a *oauth2ClientCredentialsAuth) Apply(req *http.Request) error {
	a.once.Do(func() {
		clientID, err := envValue(a.params.ClientIDEnv)
		if err != nil {
			a.initErr = fmt.Errorf("oauth2_client_credentials: client_id: %w", err)
			return
		}
		clientSecret, err := envValue(a.params.ClientSecretEnv)
		if err != nil {
			a.initErr = fmt.Errorf("oauth2_client_credentials: client_secret: %w", err)
			return
		}
		cfg := &clientcredentials.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			TokenURL:     a.params.TokenURL,
			Scopes:       a.params.Scopes,
		}
		a.source = cfg.TokenSource(context.Background())
	})
	if a.initErr != nil {
		return a.initErr
	}

	tok, err := a.source.Token()
	if err != nil {
		return fmt.Errorf("oauth2_client_credentials: fetch token: %w", err)
	}
	tok.SetAuthHeader(req)
	return nil
}
