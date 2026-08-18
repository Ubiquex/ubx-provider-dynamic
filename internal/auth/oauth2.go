package auth

import (
	"context"
	"fmt"
	"net/http"

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

type oauth2ClientCredentialsAuth struct {
	source oauth2.TokenSource
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

	clientID, err := envValue(p.ClientIDEnv)
	if err != nil {
		return nil, fmt.Errorf("oauth2_client_credentials: client_id: %w", err)
	}
	clientSecret, err := envValue(p.ClientSecretEnv)
	if err != nil {
		return nil, fmt.Errorf("oauth2_client_credentials: client_secret: %w", err)
	}

	cfg := &clientcredentials.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		TokenURL:     p.TokenURL,
		Scopes:       p.Scopes,
	}
	// Unlike api_key_header's per-request env read, client ID/secret are
	// resolved once here, at Build time -- real OAuth2 client credentials
	// don't rotate the way a bare API key might, and TokenSource's own
	// real job (automatic access-token refresh against real elapsed time,
	// using these same client credentials) is what actually needs to stay
	// "live" across calls, not the credentials feeding it.
	return &oauth2ClientCredentialsAuth{source: cfg.TokenSource(context.Background())}, nil
}

func (a *oauth2ClientCredentialsAuth) Apply(req *http.Request) error {
	tok, err := a.source.Token()
	if err != nil {
		return fmt.Errorf("oauth2_client_credentials: fetch token: %w", err)
	}
	tok.SetAuthHeader(req)
	return nil
}
