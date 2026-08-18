// Codec support -- UBI-158 Phase 4 Checkpoint 2's own real, additive
// extension point. Do/doOnce (restexec.go) are untouched by this file: every
// Phase 1-3 OpenAPI-sourced provider keeps using them exactly as before.
// DoWithCodec is their protocol-generic sibling, needed because a
// Smithy-sourced resource's real wire protocol (restXml/awsJson1_x/
// awsQuery/ec2Query -- see internal/smithy/wireexec) is not always "JSON
// body in, JSON body out" the way Do hardcodes: some encode the whole
// request as XML, some skip a URL path entirely (a fixed POST to "/" with
// an operation name carried in a header or form field instead), some decode
// an XML response. What every real protocol genuinely shares -- and what
// this file exists to keep from being reimplemented per protocol -- is
// retry/backoff (RetryPolicy, unchanged), Authenticator application, and
// APIError/statusCode classification (IsTerminal/IsNotFound, unchanged):
// only the wire encoding/decoding step itself varies.
package restexec

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// RequestBuilder builds one real *http.Request for a single attempt --
// called fresh on every retry (a *bytes.Reader-backed body, per net/http's
// own documented behavior, can't be safely reused across sends once
// consumed). ctx is the per-attempt context DoWithCodec's own caller
// supplied to it, already carrying whatever deadline withOperationTimeout
// (dynserver) established.
type RequestBuilder func(ctx context.Context) (*http.Request, error)

// ResponseDecoder decodes one real, successful (status < 400 -- doOnceWithCodec
// already turned a >=400 response into an *APIError before ever calling
// this) HTTP response body into the same generic Go value shape
// encoding/json.Unmarshal-into-any produces (string/float64/bool/
// map[string]any/[]any/nil) -- internal/wire.FromJSON's own real input
// shape, so a Smithy-sourced resource's XML/JSON-RPC/Query response decodes
// through the exact same wire.go code Phase 1's OpenAPI resources already
// use, regardless of which real wire protocol produced it.
type ResponseDecoder func(statusCode int, raw []byte, header http.Header) (any, error)

// DoWithCodec is Do's own protocol-generic counterpart, sharing this
// Client's real retry/backoff/auth logic byte-for-byte (the same loop
// structure Do itself uses) while letting build/decode vary per real wire
// protocol. See doOnceWithCodec for the per-attempt mechanics.
func (c *Client) DoWithCodec(ctx context.Context, build RequestBuilder, decode ResponseDecoder) (statusCode int, decoded any, header http.Header, err error) {
	var lastHeader http.Header
	var lastStatus int

	for attempt := 0; attempt < c.Retry.maxAttempts(); attempt++ {
		if attempt > 0 {
			wait := c.Retry.backoff(attempt-1, lastStatus, lastHeader)
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return 0, nil, nil, fmt.Errorf("restexec: %w", ctx.Err())
			}
		}

		statusCode, decoded, lastHeader, err = c.doOnceWithCodec(ctx, build, decode)
		lastStatus = statusCode
		if err == nil {
			return statusCode, decoded, lastHeader, nil
		}
		if statusCode != 0 && !isRetryableStatus(statusCode) {
			return statusCode, decoded, lastHeader, err
		}
	}
	return statusCode, decoded, lastHeader, err
}

// doOnceWithCodec performs exactly one attempt -- build the real request,
// apply auth (identical to doOnce's own real Authenticator.Apply call),
// send it, and either wrap a >=400 response as the identical real *APIError
// doOnce produces (so IsNotFound/IsTerminal/classifyRESTError all keep
// working unchanged regardless of which protocol produced the failure) or
// decode a real success body via decode.
func (c *Client) doOnceWithCodec(ctx context.Context, build RequestBuilder, decode ResponseDecoder) (statusCode int, decoded any, header http.Header, err error) {
	req, err := build(ctx)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("restexec: build request: %w", err)
	}
	if c.Authenticator != nil {
		if err := c.Authenticator.Apply(req); err != nil {
			return 0, nil, nil, fmt.Errorf("restexec: apply auth: %w", err)
		}
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("restexec: %s %s: %w", req.Method, req.URL.Path, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, resp.Header, fmt.Errorf("restexec: read response body: %w", err)
	}

	if resp.StatusCode >= 400 {
		return resp.StatusCode, nil, resp.Header, &APIError{StatusCode: resp.StatusCode, Method: req.Method, Path: req.URL.Path, Body: string(raw), Header: resp.Header}
	}

	decoded, err = decode(resp.StatusCode, raw, resp.Header)
	if err != nil {
		return resp.StatusCode, nil, resp.Header, fmt.Errorf("restexec: decode response: %w", err)
	}
	return resp.StatusCode, decoded, resp.Header, nil
}
