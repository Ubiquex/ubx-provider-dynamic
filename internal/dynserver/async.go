package dynserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/ubiquex/ubx-provider-dynamic/internal/restexec"
)

// awaitAsyncOperation polls a real long-running-operation endpoint until it
// reaches one of AsyncPolicy's own configured terminal states, or
// PollTimeout elapses -- UBI-158 Phase 3's own generic async handling, not
// modeled on any one real provider's own convention (config.AsyncConfig's
// own doc comment). initialBody/initialHeader are the response from the
// create/update call that triggered the operation.
//
// Returns (nil, nil) once the operation reaches a configured success
// status -- the caller (applyCreate/applyUpdate) is then responsible for
// performing a normal read of the resource's own canonical URL to obtain
// final state, since a job-status response is rarely the full resource
// representation itself. Returns (diags, nil) for a configured failure
// status -- a real, certain outcome, safe as a terminal Diagnostic.
// Returns (nil, err) for everything genuinely uncertain (poll timeout
// elapsed with no terminal status reached, the operation ID/poll
// mechanism itself couldn't be resolved, or the poll request itself
// failed ambiguously) -- mapped onto the identical plain-error signal
// every other ambiguous outcome in this provider uses, so ubx core's own
// reconcile-by-query verifies ground truth rather than this provider
// guessing.
func (s *Server) awaitAsyncOperation(ctx context.Context, rt *ResourceType, initialBody any, initialHeader http.Header) ([]*tfprotov6.Diagnostic, error) {
	client, err := rt.requireClient()
	if err != nil {
		return nil, err
	}

	opID, err := extractOperationID(rt.Async, initialBody, initialHeader)
	if err != nil {
		return nil, fmt.Errorf("async operation: resolve operation id: %w", err)
	}

	pollPath, err := restexec.BuildPath(rt.Async.PollPathTemplate, map[string]string{"operation_id": opID})
	if err != nil {
		return nil, fmt.Errorf("async operation: build poll path: %w", err)
	}

	deadline := time.Now().Add(rt.Async.PollTimeout)
	var lastStatus string

	for {
		pollCtx, cancel := withOperationTimeout(ctx, rt.Timeouts.Read)
		_, body, _, err := client.Do(pollCtx, http.MethodGet, pollPath, nil)
		cancel()
		if err != nil {
			diags, ambiguous := classifyRESTError("poll async operation "+opID, err)
			if ambiguous != nil {
				return nil, ambiguous
			}
			return diags, nil
		}

		statusVal, err := extractDotPath(body, rt.Async.StatusField)
		if err != nil {
			return nil, fmt.Errorf("async operation %s: read status field %q: %w", opID, rt.Async.StatusField, err)
		}
		lastStatus, _ = statusVal.(string)

		switch {
		case rt.Async.TerminalSuccess[lastStatus]:
			return nil, nil
		case rt.Async.TerminalFailure[lastStatus]:
			return diagError("async operation failed", fmt.Sprintf("operation %s reached failure status %q", opID, lastStatus)), nil
		}

		if time.Now().Add(rt.Async.PollInterval).After(deadline) {
			return nil, fmt.Errorf("async operation %s: poll timeout (%s) exceeded without reaching a terminal status (last observed status: %q)", opID, rt.Async.PollTimeout, lastStatus)
		}
		select {
		case <-time.After(rt.Async.PollInterval):
		case <-ctx.Done():
			return nil, fmt.Errorf("async operation %s: %w", opID, ctx.Err())
		}
	}
}

// finalizeAfterWrite is applyCreate/applyUpdate's own shared tail: if
// rt.Async isn't enabled, merged (the create/update response, already
// decoded/normalized/carry-forward-merged) is the final state as-is. If it
// is enabled, this polls to completion (awaitAsyncOperation) and then
// performs one more real read against the resource's own canonical URL --
// a job-status response is rarely the full resource representation, so
// the actual final attributes come from an ordinary read, exactly the
// same one ReadResource itself would perform later. merged is passed
// through as the carry-forward source for that final read's own path
// params (server.go's own read-path/create-path param split still
// applies).
func (s *Server) finalizeAfterWrite(ctx context.Context, rt *ResourceType, respBody any, respHeader http.Header, merged tftypes.Value) (tftypes.Value, []*tfprotov6.Diagnostic, error) {
	if !rt.Async.Enabled {
		return merged, nil, nil
	}

	diags, err := s.awaitAsyncOperation(ctx, rt, respBody, respHeader)
	if err != nil {
		return tftypes.Value{}, nil, err
	}
	if diags != nil {
		return tftypes.Value{}, diags, nil
	}

	finalParams, err := extractStringAttrs(merged, rt.PathParams, rt.PathParamAttr)
	if err != nil {
		return tftypes.Value{}, diagError("resolve final read after async completion", err.Error()), nil
	}
	finalVal, diags, ambiguous := s.readFromAPI(ctx, rt, finalParams)
	if ambiguous != nil {
		return tftypes.Value{}, nil, ambiguous
	}
	if diags != nil {
		return tftypes.Value{}, diags, nil
	}
	if finalVal == nil {
		return tftypes.Value{}, diagError("async operation completed", "the resource could not be found by a real read afterward"), nil
	}

	finalNormalized, err := applyNormalizers(*finalVal, rt.Drift.Normalize)
	if err != nil {
		return tftypes.Value{}, diagError("normalize post-async result", err.Error()), nil
	}
	final, err := mergeCarryForward(finalNormalized, merged, carryForwardFields(rt))
	if err != nil {
		return tftypes.Value{}, diagError("merge post-async result", err.Error()), nil
	}
	return final, nil, nil
}

// extractOperationID resolves AsyncPolicy's own configured source for a
// newly-started operation's identifier: a real response header wins
// outright when configured (a stronger, more explicit signal than a body
// dot-path guess), falling back to OperationIDField when the header is
// either not configured or came back empty on this particular response.
func extractOperationID(policy AsyncPolicy, body any, header http.Header) (string, error) {
	if policy.OperationIDHeader != "" && header != nil {
		if v := header.Get(policy.OperationIDHeader); v != "" {
			return v, nil
		}
	}
	if policy.OperationIDField == "" {
		return "", fmt.Errorf("operation_id_header %q was absent/empty on this response and no operation_id_field is configured as a fallback", policy.OperationIDHeader)
	}
	v, err := extractDotPath(body, policy.OperationIDField)
	if err != nil {
		return "", err
	}
	switch t := v.(type) {
	case string:
		if t == "" {
			return "", fmt.Errorf("operation_id_field %q resolved to an empty string", policy.OperationIDField)
		}
		return t, nil
	case json.Number:
		return t.String(), nil
	default:
		return "", fmt.Errorf("operation_id_field %q resolved to a %T, expected a string or number", policy.OperationIDField, v)
	}
}

// extractDotPath reads a "a.b.c"-shaped path out of a decoded JSON value
// (restexec's own any -- nested map[string]any, produced with
// json.Decoder.UseNumber, matching valueFromResponse's own real input
// shape).
func extractDotPath(body any, path string) (any, error) {
	if path == "" {
		return nil, fmt.Errorf("empty field path")
	}
	cur := body
	for _, part := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("path %q: expected an object at %q, got %T", path, part, cur)
		}
		v, ok := m[part]
		if !ok {
			return nil, fmt.Errorf("path %q: field %q not present in the response", path, part)
		}
		cur = v
	}
	return cur, nil
}
