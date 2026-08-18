package restexec

import (
	"context"
	"testing"
	"time"
)

func TestExponential_RespectsCeiling(t *testing.T) {
	p := RetryPolicy{InitialBackoff: 100 * time.Millisecond, MaxBackoff: 2 * time.Second, Jitter: false}
	for attempt := 0; attempt < 20; attempt++ {
		d := p.exponential(attempt)
		if d > p.MaxBackoff {
			t.Fatalf("attempt %d: %v exceeds MaxBackoff %v", attempt, d, p.MaxBackoff)
		}
		if d < 0 {
			t.Fatalf("attempt %d: negative backoff %v", attempt, d)
		}
	}
}

func TestExponential_Grows(t *testing.T) {
	p := RetryPolicy{InitialBackoff: 100 * time.Millisecond, MaxBackoff: 10 * time.Second, Jitter: false}
	prev := time.Duration(0)
	for attempt := 0; attempt < 5; attempt++ {
		d := p.exponential(attempt)
		if d <= prev {
			t.Fatalf("attempt %d: expected growth, got %v after %v", attempt, d, prev)
		}
		prev = d
	}
}

func TestExponential_JitterStaysWithinCeiling(t *testing.T) {
	p := RetryPolicy{InitialBackoff: 500 * time.Millisecond, MaxBackoff: 1 * time.Second, Jitter: true}
	for i := 0; i < 200; i++ {
		d := p.exponential(10) // deep enough attempt that the unjittered value is already at the ceiling
		if d > p.MaxBackoff {
			t.Fatalf("jittered value %v exceeded MaxBackoff %v", d, p.MaxBackoff)
		}
		if d < 0 {
			t.Fatalf("jittered value went negative: %v", d)
		}
	}
}

func TestIsTerminal_ClassifiesRealStatusCodes(t *testing.T) {
	cases := map[int]bool{
		400: true, 401: true, 403: true, 404: true, 409: true, 422: true,
		408: false, 429: false, 500: false, 502: false, 503: false,
	}
	for status, wantTerminal := range cases {
		err := &APIError{StatusCode: status}
		if got := IsTerminal(err); got != wantTerminal {
			t.Errorf("status %d: IsTerminal = %v, want %v", status, got, wantTerminal)
		}
		if got := IsAmbiguous(err); got == wantTerminal {
			t.Errorf("status %d: IsAmbiguous = %v, want %v", status, got, !wantTerminal)
		}
	}
}

func TestIsTerminal_NonAPIErrorIsNeverTerminal(t *testing.T) {
	if IsTerminal(context.DeadlineExceeded) {
		t.Fatal("a plain non-APIError error must never classify as terminal")
	}
}
