package restexec

import (
	"errors"
	"net/http"
)

// terminalStatusCodes are real, structured client-side rejections a REST
// API can return where retrying the identical request can never turn a
// failure into a success -- the wire-level analogue of a real
// ERROR-severity tfplugin Diagnostic (docs/executor.md's own "terminal"
// classification, cli/stateadapter.go's real implementation of it).
//
// 408 (Request Timeout), 429 (Too Many Requests), and every 5xx are
// deliberately absent: Client.Do's own internal retry loop already
// targets exactly those as retryable, so reaching IsTerminal at all with
// one of those codes means the retry budget was spent without a definite
// answer -- itself an ambiguous outcome, never a definite one.
var terminalStatusCodes = map[int]bool{
	http.StatusBadRequest:                 true, // 400
	http.StatusUnauthorized:               true, // 401
	http.StatusForbidden:                  true, // 403
	http.StatusNotFound:                   true, // 404
	http.StatusMethodNotAllowed:           true, // 405
	http.StatusNotAcceptable:              true, // 406
	http.StatusConflict:                   true, // 409
	http.StatusGone:                       true, // 410
	http.StatusLengthRequired:             true, // 411
	http.StatusPreconditionFailed:         true, // 412
	http.StatusRequestEntityTooLarge:      true, // 413
	http.StatusRequestURITooLong:          true, // 414
	http.StatusUnsupportedMediaType:       true, // 415
	http.StatusUnprocessableEntity:        true, // 422
	http.StatusUnavailableForLegalReasons: true, // 451
}

// IsTerminal reports whether err represents a definite, structured
// rejection from the real API -- safe for dynserver to surface as a
// terminal tfplugin Diagnostic. Every other error (network failures,
// context deadlines, JSON decode failures, and any status code that
// isn't a clear client-side rejection) is NOT terminal: dynserver must
// propagate it as a plain Go error from the RPC method instead, so ubx
// core's own reconcile-by-query (docs/executor.md) verifies ground truth
// via ReadResource rather than trusting a guess.
//
// Defaulting to "not terminal" whenever uncertain is deliberate -- the
// identical asymmetry UBI-44's own "lying destroy" finding established in
// ubx core itself (docs/executor.md): a false negative (classified
// ambiguous when it was actually definite) costs one extra, harmless
// ReadResource call; a false positive (classified terminal when it
// wasn't) can never be corrected within the same ship invocation, since
// core never revisits a resource it has already marked failed.
func IsTerminal(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return terminalStatusCodes[apiErr.StatusCode]
}

// IsAmbiguous is IsTerminal's own complement, for callers that read
// better positively: true for a real error where the outcome genuinely
// isn't known (nil err is never ambiguous -- it's success).
func IsAmbiguous(err error) bool {
	return err != nil && !IsTerminal(err)
}

// isRetryableStatus reports whether Do's own internal retry loop should
// attempt the identical request again -- the transport-resilience
// question, answered independently of (and before) IsTerminal/IsAmbiguous
// ever see the final, retry-exhausted error.
func isRetryableStatus(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500
}
