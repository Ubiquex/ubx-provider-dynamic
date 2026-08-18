// Package restexec performs the real HTTP calls a resource's CRUD lifecycle
// needs against its own REST API, using resourcemap's own discovered
// paths/methods and wire's own tftypes<->JSON conversion.
//
// Auth is UBI-158 Phase 1's own explicit non-scope ("Auth is Phase 2, stub
// the interface but don't implement auth types yet") -- Client.Authenticator
// is the stub seam: nil in Phase 1 (every request goes out unauthenticated),
// real implementations plug in here without this package's own request-
// building logic changing at all.
package restexec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Authenticator is Phase 2's own real seam, stubbed here per Phase 1's
// explicit scope: Apply is called on every outgoing request before it's
// sent, real implementations (API key header, bearer token, OAuth2) add
// whatever the real auth type needs. Nil (Phase 1's only real
// implementation, config.Auth.Type always empty) means unauthenticated.
type Authenticator interface {
	Apply(req *http.Request) error
}

// Client performs real HTTP requests against one provider's own base_url.
type Client struct {
	BaseURL       string
	HTTPClient    *http.Client
	Authenticator Authenticator
}

// NewClient returns a Client with a real, bounded-timeout http.Client --
// never http.DefaultClient, which has no timeout at all and would let a
// hung real API leave ubx's own executor blocked indefinitely (the same
// "retryable vs terminal" discipline docs/executor.md already applies to
// tfplugin providers themselves applies here, one layer further out).
func NewClient(baseURL string, auth Authenticator) *Client {
	return &Client{BaseURL: baseURL, HTTPClient: &http.Client{}, Authenticator: auth}
}

// BuildPath substitutes an OpenAPI-style path template's {param} segments
// with real values from params, URL-escaping each one individually (never
// the whole path at once -- a literal "/" inside a param value, e.g. a
// GitHub repo full_name-style identifier, must stay a single path segment's
// own content, not get percent-encoded into %2F and silently change the
// URL's own structure).
func BuildPath(template string, params map[string]string) (string, error) {
	segs := strings.Split(template, "/")
	for i, seg := range segs {
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			name := strings.TrimSuffix(strings.TrimPrefix(seg, "{"), "}")
			val, ok := params[name]
			if !ok || val == "" {
				return "", fmt.Errorf("restexec: missing path parameter %q for %s", name, template)
			}
			segs[i] = url.PathEscape(val)
		}
	}
	return strings.Join(segs, "/"), nil
}

// Do performs one real HTTP request: method against path (already
// substituted, see BuildPath), with body marshaled to a JSON request body
// when non-nil (nil for GET/DELETE, which real REST APIs never expect a
// body on). Returns the decoded JSON response body (nil for a 204 No
// Content, or any real empty body) -- the caller (dynserver) is
// responsible for converting it into a tftypes.Value via wire.FromJSON
// against the resource's own schema.
func (c *Client) Do(ctx context.Context, method, path string, body any) (statusCode int, decoded any, err error) {
	u := strings.TrimSuffix(c.BaseURL, "/") + path

	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, fmt.Errorf("restexec: marshal request body: %w", err)
		}
		reqBody = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, u, reqBody)
	if err != nil {
		return 0, nil, fmt.Errorf("restexec: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Authenticator != nil {
		if err := c.Authenticator.Apply(req); err != nil {
			return 0, nil, fmt.Errorf("restexec: apply auth: %w", err)
		}
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("restexec: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("restexec: read response body: %w", err)
	}

	if resp.StatusCode >= 400 {
		return resp.StatusCode, nil, &APIError{StatusCode: resp.StatusCode, Method: method, Path: path, Body: string(raw)}
	}

	if len(strings.TrimSpace(string(raw))) == 0 {
		return resp.StatusCode, nil, nil
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var out any
	if err := dec.Decode(&out); err != nil {
		return resp.StatusCode, nil, fmt.Errorf("restexec: decode JSON response: %w", err)
	}
	return resp.StatusCode, out, nil
}

// APIError is a real, non-2xx REST response -- Error() includes the status
// and a truncated body so a Diagnostic built from it is actually
// actionable, not just "request failed."
type APIError struct {
	StatusCode int
	Method     string
	Path       string
	Body       string
}

func (e *APIError) Error() string {
	body := e.Body
	const max = 500
	if len(body) > max {
		body = body[:max] + "...(truncated)"
	}
	return fmt.Sprintf("%s %s: HTTP %d: %s", e.Method, e.Path, e.StatusCode, body)
}

// IsNotFound reports whether err represents a real HTTP 404 -- ReadResource's
// own "the resource is genuinely gone" signal, matching every real
// provider's own not-found convention.
func IsNotFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == 404
}
