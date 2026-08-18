package dynserver

import (
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/ubiquex/ubx-provider-dynamic/internal/config"
	"github.com/ubiquex/ubx-provider-dynamic/internal/restexec"
)

// defaultOperationTimeout is the real fallback whenever a provider's own
// [dynamic_providers.<name>.timeouts] table (or one operation kind within
// it) is absent -- generous enough for a real, slow REST API's own
// ordinary create/update latency without being so large that a genuinely
// hung request ties up a ubx ship run for the majority of its own default
// 5-minute ambient budget (cli/ship.go's own --timeout default, confirmed
// in this session's own research).
const defaultOperationTimeout = 30 * time.Second

// Timeouts is TimeoutsConfig resolved into real time.Duration values, one
// per CRUD operation kind, independent of ubx core's own ambient --ship
// deadline (docs/executor.md: core sets no per-RPC timeout of its own --
// this is the layer that supplies that granularity, since core
// structurally cannot).
type Timeouts struct {
	Create, Read, Update, Delete time.Duration
}

func resolveTimeouts(cfg config.TimeoutsConfig) (Timeouts, error) {
	def := defaultOperationTimeout
	if d, ok, err := config.ParseDuration(cfg.Default); err != nil {
		return Timeouts{}, err
	} else if ok {
		def = d
	}

	resolve := func(s string) (time.Duration, error) {
		d, ok, err := config.ParseDuration(s)
		if err != nil {
			return 0, err
		}
		if !ok {
			return def, nil
		}
		return d, nil
	}

	var t Timeouts
	var err error
	if t.Create, err = resolve(cfg.Create); err != nil {
		return Timeouts{}, err
	}
	if t.Read, err = resolve(cfg.Read); err != nil {
		return Timeouts{}, err
	}
	if t.Update, err = resolve(cfg.Update); err != nil {
		return Timeouts{}, err
	}
	if t.Delete, err = resolve(cfg.Delete); err != nil {
		return Timeouts{}, err
	}
	return t, nil
}

// ResolveRetryPolicy converts RetryConfig into a real restexec.RetryPolicy,
// falling back to restexec.DefaultRetryPolicy for anything left unset.
// Exported (unlike this file's other resolve* helpers) because it's
// provider-wide, not per-resource-type -- main.go calls it directly to
// build the restexec.Client's own Retry field, once, before Build ever
// runs (Build's own job is per-resource-type policy attached to each
// ResourceType, never the Client itself).
func ResolveRetryPolicy(cfg config.RetryConfig) (restexec.RetryPolicy, error) {
	p := restexec.DefaultRetryPolicy()

	if cfg.MaxAttempts > 0 {
		p.MaxAttempts = cfg.MaxAttempts
	}
	if d, ok, err := config.ParseDuration(cfg.InitialBackoff); err != nil {
		return restexec.RetryPolicy{}, err
	} else if ok {
		p.InitialBackoff = d
	}
	if d, ok, err := config.ParseDuration(cfg.MaxBackoff); err != nil {
		return restexec.RetryPolicy{}, err
	} else if ok {
		p.MaxBackoff = d
	}
	if cfg.Jitter != nil {
		p.Jitter = *cfg.Jitter
	}
	if cfg.RespectRetryAfter != nil {
		p.RespectRetryAfter = *cfg.RespectRetryAfter
	}
	if cfg.RateLimitResetHeader != "" {
		p.RateLimitResetHeader = cfg.RateLimitResetHeader
	}
	return p, nil
}

// AsyncPolicy is AsyncConfig resolved into the real shape the poll loop
// (async.go) needs -- see config.AsyncConfig's own doc comment for what
// each field means; this is purely the parsed-duration/set-shaped mirror.
type AsyncPolicy struct {
	Enabled bool

	OperationIDField  string
	OperationIDHeader string
	PollPathTemplate  string
	StatusField       string

	TerminalSuccess map[string]bool
	TerminalFailure map[string]bool

	PollInterval time.Duration
	PollTimeout  time.Duration
}

const (
	defaultPollInterval = 5 * time.Second
	defaultPollTimeout  = 10 * time.Minute
)

func resolveAsyncPolicy(cfg config.AsyncConfig) (AsyncPolicy, error) {
	if !cfg.Enabled {
		return AsyncPolicy{}, nil
	}
	p := AsyncPolicy{
		Enabled:           true,
		OperationIDField:  cfg.OperationIDField,
		OperationIDHeader: cfg.OperationIDHeader,
		PollPathTemplate:  cfg.PollPathTemplate,
		StatusField:       cfg.StatusField,
		TerminalSuccess:   toSet(cfg.TerminalSuccessValues),
		TerminalFailure:   toSet(cfg.TerminalFailureValues),
		PollInterval:      defaultPollInterval,
		PollTimeout:       defaultPollTimeout,
	}
	if d, ok, err := config.ParseDuration(cfg.PollInterval); err != nil {
		return AsyncPolicy{}, err
	} else if ok {
		p.PollInterval = d
	}
	if d, ok, err := config.ParseDuration(cfg.PollTimeout); err != nil {
		return AsyncPolicy{}, err
	} else if ok {
		p.PollTimeout = d
	}
	return p, nil
}

func toSet(values []string) map[string]bool {
	m := make(map[string]bool, len(values))
	for _, v := range values {
		m[v] = true
	}
	return m
}

// Normalizer transforms one attribute's own value before it's ever
// recorded into resource state -- see DriftPolicy's own doc comment for
// why applying it unconditionally, every time, is what makes it actually
// prevent false-positive drift (not just at comparison time).
type Normalizer func(tftypes.Value) (tftypes.Value, error)

// normalizerRegistry is the real, fixed, small set of transforms a config
// author can name -- deliberately not an arbitrary expression language
// (config.DriftConfig's own doc comment): a config declares a known
// transform, it never writes one.
var normalizerRegistry = map[string]Normalizer{
	"lowercase": stringNormalizer(strings.ToLower),
	"uppercase": stringNormalizer(strings.ToUpper),
	"trim":      stringNormalizer(strings.TrimSpace),
}

func stringNormalizer(f func(string) string) Normalizer {
	return func(v tftypes.Value) (tftypes.Value, error) {
		if v.IsNull() || !v.IsKnown() {
			return v, nil
		}
		if !v.Type().Is(tftypes.String) {
			return tftypes.Value{}, fmt.Errorf("normalization requires a string attribute, got %s", v.Type())
		}
		var s string
		if err := v.As(&s); err != nil {
			return tftypes.Value{}, err
		}
		return tftypes.NewValue(v.Type(), f(s)), nil
	}
}

// DriftPolicy is DriftConfig resolved: Ignore feeds directly into the same
// carry-forward mechanism path parameters already use (ReadResource/Apply's
// own NewState construction never reports drift on a field it always
// echoes back from prior state, regardless of reason); Normalize is applied
// unconditionally to every fresh value written into state -- every
// recorded value is therefore already in canonical form from the moment
// it's first written, so a later comparison against another
// identically-normalized value naturally converges, with no separate
// "normalize before comparing" step required anywhere else.
type DriftPolicy struct {
	Ignore    []string
	Normalize map[string]Normalizer
}

// applyNormalizers runs every configured normalizer against v's own
// top-level attributes, returning a new Value with those fields replaced.
// Applied unconditionally to every fresh API response before it's ever
// merged into state (applyCreate/applyUpdate/ReadResource) -- see
// DriftPolicy's own doc comment for why applying it every time, not just
// at comparison time, is what actually prevents false-positive drift.
func applyNormalizers(v tftypes.Value, normalize map[string]Normalizer) (tftypes.Value, error) {
	if len(normalize) == 0 {
		return v, nil
	}
	var m map[string]tftypes.Value
	if err := v.As(&m); err != nil {
		return tftypes.Value{}, fmt.Errorf("normalize: value is not an object: %w", err)
	}
	for field, fn := range normalize {
		cur, ok := m[field]
		if !ok {
			continue // field genuinely absent from this response -- nothing to normalize
		}
		nv, err := fn(cur)
		if err != nil {
			return tftypes.Value{}, fmt.Errorf("normalize field %q: %w", field, err)
		}
		m[field] = nv
	}
	return tftypes.NewValue(v.Type(), m), nil
}

func resolveDriftPolicy(cfg config.DriftConfig) (DriftPolicy, error) {
	p := DriftPolicy{Ignore: cfg.Ignore}
	if len(cfg.Normalize) == 0 {
		return p, nil
	}
	p.Normalize = make(map[string]Normalizer, len(cfg.Normalize))
	for field, fn := range cfg.Normalize {
		norm, ok := normalizerRegistry[fn]
		if !ok {
			names := make([]string, 0, len(normalizerRegistry))
			for n := range normalizerRegistry {
				names = append(names, n)
			}
			return DriftPolicy{}, fmt.Errorf("drift.normalize.%s: unrecognized normalizer %q (want one of: %v)", field, fn, names)
		}
		p.Normalize[field] = norm
	}
	return p, nil
}
