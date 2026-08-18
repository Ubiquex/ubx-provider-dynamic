// UBI-158 Phase 3's own config surface: retry/backoff, per-operation
// timeouts, async/long-running operation polling, and field-level drift
// rules -- all declared in .ubx/config, never hardcoded to any one
// provider's own shape (the ticket's own explicit "not AWS-shaped"
// instruction). This package stays a dumb data holder, same discipline
// Auth's own doc comment already established in Phase 2: real
// interpretation (building a restexec.RetryPolicy, actually polling a job
// endpoint) lives in internal/dynserver, which already owns the real
// business logic these fields feed.
package config

import (
	"fmt"
	"time"
)

// RetryConfig is [dynamic_providers.<name>.retry] -- the TOML-facing
// mirror of restexec.RetryPolicy (kept a separate, string-based shape
// here rather than importing restexec's own time.Duration-typed struct
// directly, so this package never needs to know restexec's own types --
// see ParseDuration). Jitter/RespectRetryAfter are pointers so "never set"
// is distinguishable from "explicitly set to false" -- the same
// discipline IntentConfig.ShowDefaults already established elsewhere in
// ubx's own config surface, reused here rather than reinvented.
type RetryConfig struct {
	MaxAttempts    int    `toml:"max_attempts"`
	InitialBackoff string `toml:"initial_backoff"`
	MaxBackoff     string `toml:"max_backoff"`

	Jitter            *bool `toml:"jitter"`
	RespectRetryAfter *bool `toml:"respect_retry_after"`

	// RateLimitResetHeader names a real header carrying a unix timestamp
	// for when a rate limit resets (GitHub's own real, confirmed-live
	// X-RateLimit-Reset) -- empty means "this API doesn't expose one."
	RateLimitResetHeader string `toml:"rate_limit_reset_header"`
}

// TimeoutsConfig is [dynamic_providers.<name>.timeouts] -- one real,
// per-CRUD-operation-kind budget, independent of ubx core's own ambient
// --ship timeout (docs/executor.md: core sets no per-RPC deadline of its
// own around ApplyResourceChange/ReadResource; this is the layer that
// grants that granularity, since core structurally cannot). Default
// applies to any operation kind left empty.
type TimeoutsConfig struct {
	Create  string `toml:"create"`
	Read    string `toml:"read"`
	Update  string `toml:"update"`
	Delete  string `toml:"delete"`
	Default string `toml:"default"`
}

// AsyncConfig is one resource type's own
// [dynamic_providers.<name>.resources.<type>.async] table -- a real,
// generic long-running-operation shape, deliberately not modeled on any
// one real provider's own async convention (AWS CloudControl's progress
// tokens are UBI-158 Phase 4's own separate concern). A real API's create/
// update response either carries the operation identifier directly in its
// own body (OperationIDField, a dot-path) or in a response header
// (OperationIDHeader) -- exactly one of the two should be set; dynserver
// treats OperationIDHeader as authoritative if both are (a header is a
// stronger, more explicit "here is where to look" signal than guessing a
// body field's own dot-path).
type AsyncConfig struct {
	Enabled bool `toml:"enabled"`

	OperationIDField  string `toml:"operation_id_field"`
	OperationIDHeader string `toml:"operation_id_header"`

	// PollPathTemplate is an OpenAPI-style path template
	// ("/jobs/{operation_id}") -- {operation_id} is substituted with
	// whatever OperationIDField/OperationIDHeader extracted.
	PollPathTemplate string `toml:"poll_path_template"`

	// StatusField is a dot-path within the POLL response (not the initial
	// response) naming the job's own current status.
	StatusField string `toml:"status_field"`

	// TerminalSuccessValues/TerminalFailureValues are the real status
	// values (case-sensitive, as the API itself writes them) that end
	// polling -- any other value keeps polling until PollTimeout.
	TerminalSuccessValues []string `toml:"terminal_success_values"`
	TerminalFailureValues []string `toml:"terminal_failure_values"`

	PollInterval string `toml:"poll_interval"`
	PollTimeout  string `toml:"poll_timeout"`
}

// DriftConfig is one resource type's own
// [dynamic_providers.<name>.resources.<type>.drift] table: which fields a
// real API returns that should never be reported as drift (Ignore -- e.g.
// a server-stamped updated_at that changes on every read regardless of
// any real change), and which need a real, declared transform before
// comparison (Normalize -- e.g. a URL the API happens to lowercase on
// its own side).
type DriftConfig struct {
	Ignore []string `toml:"ignore"`

	// Normalize maps a field name to a normalization function name.
	// Real, small, fixed set -- see internal/dynserver's own normalizers
	// map for the actual implementations -- not an arbitrary expression
	// language; a config author names a known transform, never writes one.
	Normalize map[string]string `toml:"normalize"`
}

// ResourceConfig is [dynamic_providers.<name>.resources.<type>] -- keyed
// by this provider's own derived <provider>_<resource> type name (the
// same name resourcemap.Discover/schema.Translator produce), not by any
// OpenAPI-native identifier -- a config author determines it the same way
// they'd reference any other real ubx resource address: by running
// discovery once and reading the real, derived name back.
type ResourceConfig struct {
	Async AsyncConfig `toml:"async"`
	Drift DriftConfig `toml:"drift"`
}

// ParseDuration parses one of this package's own duration-string fields
// (RetryConfig.InitialBackoff/MaxBackoff, TimeoutsConfig's own fields,
// AsyncConfig.PollInterval/PollTimeout). Empty is "not set" (ok=false),
// never an error -- consistent with every other optional field in this
// package; the caller applies its own fallback.
func ParseDuration(s string) (d time.Duration, ok bool, err error) {
	if s == "" {
		return 0, false, nil
	}
	d, err = time.ParseDuration(s)
	if err != nil {
		return 0, false, fmt.Errorf("parse duration %q: %w", s, err)
	}
	return d, true, nil
}

// validateDurations fails config loading immediately on a malformed
// duration string, rather than surfacing a parse error the first time
// some real API call actually needs the value -- the same "fail at
// startup, not at first use" discipline Provider.validate already applies
// to schema_source/base_url.
func (p Provider) validateDurations() error {
	prefix := fmt.Sprintf("dynamic_providers.%s", p.Name)

	for _, d := range []struct{ name, value string }{
		{"retry.initial_backoff", p.Retry.InitialBackoff},
		{"retry.max_backoff", p.Retry.MaxBackoff},
		{"timeouts.create", p.Timeouts.Create},
		{"timeouts.read", p.Timeouts.Read},
		{"timeouts.update", p.Timeouts.Update},
		{"timeouts.delete", p.Timeouts.Delete},
		{"timeouts.default", p.Timeouts.Default},
	} {
		if _, _, err := ParseDuration(d.value); err != nil {
			return fmt.Errorf("%s.%s: %w", prefix, d.name, err)
		}
	}

	for typeName, rc := range p.Resources {
		for _, d := range []struct{ name, value string }{
			{"async.poll_interval", rc.Async.PollInterval},
			{"async.poll_timeout", rc.Async.PollTimeout},
		} {
			if _, _, err := ParseDuration(d.value); err != nil {
				return fmt.Errorf("%s.resources.%s.%s: %w", prefix, typeName, d.name, err)
			}
		}
		if rc.Async.Enabled && rc.Async.PollPathTemplate == "" {
			return fmt.Errorf("%s.resources.%s.async: poll_path_template is required when async is enabled", prefix, typeName)
		}
		if rc.Async.Enabled && rc.Async.OperationIDField == "" && rc.Async.OperationIDHeader == "" {
			return fmt.Errorf("%s.resources.%s.async: one of operation_id_field or operation_id_header is required when async is enabled", prefix, typeName)
		}
	}
	return nil
}
