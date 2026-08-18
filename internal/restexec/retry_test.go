package restexec

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func TestDo_RetriesOn500ThenSucceeds(t *testing.T) {
	var calls int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer ts.Close()

	c := NewClient(ts.URL, nil)
	c.Retry = RetryPolicy{MaxAttempts: 5, InitialBackoff: time.Millisecond, MaxBackoff: 10 * time.Millisecond, Jitter: false}

	status, decoded, _, err := c.Do(context.Background(), http.MethodGet, "/x", nil)
	if err != nil {
		t.Fatalf("expected eventual success, got %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if m, ok := decoded.(map[string]any); !ok || m["ok"] != true {
		t.Fatalf("decoded = %+v", decoded)
	}
	if atomic.LoadInt32(&calls) != 3 {
		t.Fatalf("expected exactly 3 real HTTP calls, got %d", calls)
	}
}

func TestDo_DoesNotRetryOn400(t *testing.T) {
	var calls int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad request"}`))
	}))
	defer ts.Close()

	c := NewClient(ts.URL, nil)
	c.Retry = RetryPolicy{MaxAttempts: 5, InitialBackoff: time.Millisecond, MaxBackoff: 10 * time.Millisecond}

	_, _, _, err := c.Do(context.Background(), http.MethodGet, "/x", nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !IsTerminal(err) {
		t.Fatalf("expected a terminal error, got %v", err)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("expected exactly 1 real HTTP call (no retries on a terminal 400), got %d", calls)
	}
}

func TestDo_ExhaustsRetryBudget_ReturnsAmbiguous(t *testing.T) {
	var calls int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer ts.Close()

	c := NewClient(ts.URL, nil)
	c.Retry = RetryPolicy{MaxAttempts: 3, InitialBackoff: time.Millisecond, MaxBackoff: 5 * time.Millisecond}

	_, _, _, err := c.Do(context.Background(), http.MethodGet, "/x", nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if IsTerminal(err) {
		t.Fatalf("expected an ambiguous error (503 exhausted its retry budget), got terminal: %v", err)
	}
	if !IsAmbiguous(err) {
		t.Fatal("expected IsAmbiguous to be true")
	}
	if atomic.LoadInt32(&calls) != 3 {
		t.Fatalf("expected exactly 3 real HTTP calls (MaxAttempts), got %d", calls)
	}
}

func TestDo_RetriesOnNetworkError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	realURL := ts.URL
	ts.Close() // closed before first use -- guarantees a real connection-refused network error

	c := NewClient(realURL, nil)
	c.Retry = RetryPolicy{MaxAttempts: 2, InitialBackoff: time.Millisecond, MaxBackoff: 5 * time.Millisecond}

	_, _, _, err := c.Do(context.Background(), http.MethodGet, "/x", nil)
	if err == nil {
		t.Fatal("expected a real network error")
	}
	if IsTerminal(err) {
		t.Fatal("a network-level failure must never classify as terminal")
	}
}

func TestDo_RespectsRealRetryAfterHeader(t *testing.T) {
	var calls int32
	var firstCallAt, secondCallAt time.Time
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			firstCallAt = time.Now()
			w.Header().Set("Retry-After", "1") // 1 real second, RFC 9110 delay-seconds form
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		secondCallAt = time.Now()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer ts.Close()

	c := NewClient(ts.URL, nil)
	// A huge exponential ceiling that would be obviously wrong if
	// Retry-After weren't actually winning -- if the real wait were
	// governed by InitialBackoff/MaxBackoff instead, it would be far
	// longer or (with these params) inconsistent with the real 1s
	// Retry-After value.
	c.Retry = RetryPolicy{MaxAttempts: 2, InitialBackoff: 50 * time.Millisecond, MaxBackoff: 100 * time.Millisecond, RespectRetryAfter: true}

	_, _, _, err := c.Do(context.Background(), http.MethodGet, "/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	gap := secondCallAt.Sub(firstCallAt)
	if gap < 900*time.Millisecond {
		t.Fatalf("expected the real ~1s Retry-After to be honored, got a %v gap (looks like MaxBackoff governed instead)", gap)
	}
}

// TestDo_RespectsConfiguredRateLimitResetHeader also documents a real,
// non-obvious property of unix-timestamp-second-granularity reset headers
// (GitHub's own real X-RateLimit-Reset convention, confirmed live against
// api.github.com in this session): a timestamp computed as "N seconds from
// now" truncates to whole seconds, so the ACTUAL wait a client computes
// from it can be anywhere from just under N seconds down to nearly 0,
// depending on where in the current second the header was generated --
// e.g. generated at X.9s, "2 seconds from now" truncates to X+2 (a real
// ~1.1s wait from a reader arriving at X.9s), while generated at X.05s it
// truncates to the same integer second X+2 (a real ~1.95s wait). This is
// why the target is 2 real seconds with a >=1s assertion, not an exact
// value -- asserting near-exact equality against a 1-second target would
// be a genuinely flaky test, not a bug in the production code being
// tested.
func TestDo_RespectsConfiguredRateLimitResetHeader(t *testing.T) {
	var calls int32
	var firstCallAt, secondCallAt time.Time
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			firstCallAt = time.Now()
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(2*time.Second).Unix(), 10))
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		secondCallAt = time.Now()
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	c := NewClient(ts.URL, nil)
	c.Retry = RetryPolicy{
		MaxAttempts:          2,
		InitialBackoff:       50 * time.Millisecond,
		MaxBackoff:           100 * time.Millisecond,
		RespectRetryAfter:    true, // present but no Retry-After header this time -- must fall through
		RateLimitResetHeader: "X-RateLimit-Reset",
	}

	_, _, _, err := c.Do(context.Background(), http.MethodGet, "/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	gap := secondCallAt.Sub(firstCallAt)
	if gap < 900*time.Millisecond {
		t.Fatalf("expected the real rate-limit-reset (>=~1s given second-granularity truncation) to be honored, got a %v gap -- looks like MaxBackoff governed instead", gap)
	}
}
