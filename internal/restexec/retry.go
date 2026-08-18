package restexec

import (
	"math/rand"
	"net/http"
	"strconv"
	"time"
)

// RetryPolicy governs restexec's own transport-level resilience -- retrying
// the SAME HTTP request when a real API signals a transient condition
// (429, a 5xx, a dropped connection), driven by whatever real signal the
// response itself carries (Retry-After, a configurable rate-limit-reset
// header) rather than blind fixed timing. This is a genuinely different,
// lower-level concern from ubx core's own reconcile-by-query
// (docs/executor.md): core's mechanism resolves "did the operation I
// already sent actually take effect" by reading the resource back; this
// mechanism decides whether to resend the REQUEST ITSELF because the
// wire-level attempt didn't get a real answer at all. See
// internal/dynserver's own doc comments for how the two compose (this
// package never talks to ubx core; it only ever needs to hand dynserver a
// clean classification -- IsTerminal/IsAmbiguous -- once its own retry
// budget here is spent).
type RetryPolicy struct {
	// MaxAttempts is the total number of HTTP attempts for one logical
	// call, including the first -- 1 means "no retries." Real REST
	// clients (AWS SDK, GitHub's own Octokit, Stripe's SDKs) default in
	// the 3-5 range; this package defaults to 5, deliberately on the
	// generous side since a Dynamic-Provider-backed create/apply is a
	// comparatively rare, already-latency-tolerant operation (ubx's own
	// ambient ship budget is minutes, not milliseconds).
	MaxAttempts int

	// InitialBackoff and MaxBackoff bound the exponential-with-jitter
	// schedule used when no real Retry-After/rate-limit-reset signal is
	// present on the response (or for a network-level failure, which
	// carries no headers at all).
	InitialBackoff time.Duration
	MaxBackoff     time.Duration

	// Jitter adds up to +/-50% randomization to each computed backoff --
	// the standard "full jitter" mitigation for many concurrent clients
	// retrying in lockstep (AWS's own well-known architecture-blog
	// guidance, not this package's own invention).
	Jitter bool

	// RespectRetryAfter honors a real, standard HTTP Retry-After header
	// (RFC 9110 §10.2.3, either delay-seconds or an HTTP-date) when a 429
	// or 5xx response carries one -- the single most reliable real signal
	// an API can give, since it names the server's own real recovery
	// estimate rather than a guess.
	RespectRetryAfter bool

	// RateLimitResetHeader, if set, names a real header carrying a unix
	// timestamp for when a rate limit resets (GitHub's own real, confirmed
	// live: `X-RateLimit-Reset`) -- consulted only on a 429 with no
	// Retry-After present. Empty means "this API doesn't expose one, don't
	// look" -- confirmed live against Datadog's own real /api/v1/validate
	// error response, which carries no rate-limit headers at all on a 403,
	// unlike GitHub's own real response headers (present on every request,
	// success or not) -- real APIs genuinely differ here, which is exactly
	// why this is a config field, not a hardcoded header name.
	RateLimitResetHeader string
}

// DefaultRetryPolicy is a reasonable, real-world-shaped default -- used
// whenever a provider's own [dynamic_providers.<name>.retry] table is
// absent or partially specified.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxAttempts:       5,
		InitialBackoff:    200 * time.Millisecond,
		MaxBackoff:        30 * time.Second,
		Jitter:            true,
		RespectRetryAfter: true,
	}
}

func (p RetryPolicy) maxAttempts() int {
	if p.MaxAttempts <= 0 {
		return 1
	}
	return p.MaxAttempts
}

// backoff computes how long to wait before the next attempt (attempt is
// 0-indexed: the wait BEFORE attempt N, N>=1). If lastResp carries a real
// Retry-After or (on a 429) a configured rate-limit-reset header, that
// real signal wins outright over the computed exponential schedule -- an
// API telling you exactly when it'll be ready is always a better answer
// than a guess, so it is deliberately NOT clamped to MaxBackoff: MaxBackoff
// bounds how long this package will blindly guess, not how long it will
// wait when the server has told it, in real, standard, structured terms,
// exactly how long to wait. The only real ceiling on a real signal is the
// caller's own context deadline, which Do's own select on ctx.Done()
// already enforces independently of this function.
func (p RetryPolicy) backoff(attempt int, lastStatus int, lastHeader http.Header) time.Duration {
	if lastHeader != nil {
		if p.RespectRetryAfter {
			if d, ok := parseRetryAfter(lastHeader.Get("Retry-After")); ok {
				return d
			}
		}
		if lastStatus == http.StatusTooManyRequests && p.RateLimitResetHeader != "" {
			if d, ok := parseRateLimitReset(lastHeader.Get(p.RateLimitResetHeader)); ok {
				return d
			}
		}
	}
	return p.exponential(attempt)
}

func (p RetryPolicy) exponential(attempt int) time.Duration {
	initial := p.InitialBackoff
	if initial <= 0 {
		initial = DefaultRetryPolicy().InitialBackoff
	}
	maxB := p.MaxBackoff
	if maxB <= 0 {
		maxB = DefaultRetryPolicy().MaxBackoff
	}

	d := initial
	for i := 0; i < attempt; i++ {
		d *= 2
		if d >= maxB {
			d = maxB
			break
		}
	}
	if p.Jitter {
		// Full jitter: uniform in [0.5*d, 1.5*d), clamped back to maxB --
		// jitter can otherwise push a value already at the ceiling past it.
		delta := time.Duration(rand.Int63n(int64(d))) - d/2
		d += delta
		if d < 0 {
			d = 0
		}
	}
	return clampDuration(d, maxB)
}

func clampDuration(d, max time.Duration) time.Duration {
	if max > 0 && d > max {
		return max
	}
	if d < 0 {
		return 0
	}
	return d
}

// parseRetryAfter handles both real Retry-After forms RFC 9110 §10.2.3
// permits: delay-seconds ("120") and an HTTP-date. Empty/unparseable
// returns ok=false so the caller falls through to its own schedule.
func parseRetryAfter(v string) (time.Duration, bool) {
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			return 0, false
		}
		return time.Duration(secs) * time.Second, true
	}
	if t, err := http.ParseTime(v); err == nil {
		d := time.Until(t)
		if d < 0 {
			d = 0
		}
		return d, true
	}
	return 0, false
}

// parseRateLimitReset interprets header as a unix timestamp (GitHub's own
// real X-RateLimit-Reset convention) -- the only shape confirmed live
// against a real API in this session; a provider whose own rate-limit
// header uses a different shape (a delay in seconds, like Retry-After)
// should configure Retry-After handling instead, or point
// RateLimitResetHeader at a header this function can actually parse.
func parseRateLimitReset(v string) (time.Duration, bool) {
	if v == "" {
		return 0, false
	}
	unix, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, false
	}
	d := time.Until(time.Unix(unix, 0))
	if d < 0 {
		return 0, false
	}
	return d, true
}
