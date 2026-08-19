// Package config reads ubx-provider-dynamic's own configuration out of a
// stack's existing .ubx/config file.
//
// Real, deliberate deviation from UBI-158's own ticket text, flagged here
// rather than silently reconciled: the ticket says to read a
// "[providers.<name>]" block. ubx core's own .ubx/config (cli/config.go,
// UBI-43) already owns a top-level [providers] table with an incompatible
// shape -- map[string]string, source -> pinned version, used by every
// provider (hashicorp/aws, this binary, anything else) to resolve which
// binary/version provider.Acquire launches -- plus a parallel
// [provider_configs] table whose per-source blob is delivered to the
// launched provider process via the standard ConfigureProvider RPC, not
// read from disk by the provider itself.
//
// That existing mechanism cannot supply what this layer actually needs:
// GetProviderSchema is called before ConfigureProvider ever runs (see
// provider/provider.go's v6Provider.Schema, called with zero arguments),
// so a Dynamic-Provider-backed resource's schema -- which depends on
// *which* OpenAPI spec this instance of the binary represents -- must be
// known before any RPC arrives at all. A table living inside
// ubx core's own [providers]/[provider_configs] namespace, delivered only
// after Configure, cannot do that. This package therefore reads its own,
// separate, non-colliding table -- [dynamic_providers.<name>] -- directly
// off disk, at process startup, exactly like a real provider binary reads
// its own ambient environment (AWS_PROFILE, GOOGLE_APPLICATION_CREDENTIALS)
// before Terraform ever calls it. See Provider.Name's own doc comment for
// how a launched process learns which <name> is its own.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/BurntSushi/toml"
)

// SchemaSourceType is schema_source's declared value -- which tier this
// provider's schema is derived from. SchemaSourceOpenAPI (Phase 1) and
// SchemaSourceSmithy (Phase 4) have real implementations; SchemaSourceInline
// and SchemaSourceAWSCCAPI are named so a config referencing them fails
// with a clear "not yet implemented" error rather than an
// unrecognized-value one.
//
// Real, deliberate deviation from UBI-158's own original three-tier design
// (openapi/inline/aws_ccapi), flagged here rather than silently folded in:
// this session's own Phase 4 prompt named "Smithy" as AWS's real schema
// source, not "aws_ccapi" (the CloudFormation Cloud Control API registry
// tier the original ticket described) -- these are two genuinely
// different, real AWS schema sources (Smithy models AWS itself generates
// its own SDKs from vs. the separate CloudFormation resource-provider
// schema registry), not the same tier under two names. SchemaSourceSmithy
// is therefore a real, new fourth value, and SchemaSourceAWSCCAPI is left
// exactly as it was -- a real, distinct, still-unimplemented tier of its
// own, not superseded by this phase.
type SchemaSourceType string

const (
	SchemaSourceOpenAPI      SchemaSourceType = "openapi"
	SchemaSourceSmithy       SchemaSourceType = "smithy"
	SchemaSourceInline       SchemaSourceType = "inline"
	SchemaSourceAWSCCAPI     SchemaSourceType = "aws_ccapi"
	SchemaSourceDiscoveryDoc SchemaSourceType = "discovery_docs"
)

// Auth is a stub, per UBI-158 Phase 1's own explicit scope ("Auth is Phase
// 2, stub the interface but don't implement auth types yet"). The shape
// (a discriminated Type plus a free-form Params bag) is settled so Phase 2
// is additive -- filling Params' real meaning per Type -- not a reshape of
// this table. Real, semantic validation of Type/Params (e.g. "params.headers
// must be non-empty for api_key_header") is deliberately NOT done here --
// that's internal/auth.Build's own job (Phase 2), keeping this package a
// dumb data holder rather than needing to know about every real auth type
// that exists. An empty Type means no authentication at all, unchanged from
// Phase 1.
type Auth struct {
	Type   string         `toml:"type"`
	Params map[string]any `toml:"params"`
}

// Provider is one [dynamic_providers.<name>] table's parsed, validated
// content.
//
// Name is the TOML key itself (dynamic_providers.<name>), not a field
// inside the table -- Load fills it in from the map key on the way out.
// Phase 1 leaves open exactly how a launched subprocess -- given zero
// special-cased args/env from ubx core's own generic provider.Acquire/
// Launch call site -- learns which <name> is its own; main.go resolves it
// via the UBX_DYNAMIC_PROVIDER_NAME environment variable (see main.go's own
// doc comment), which provider.Launch's existing, already-generic
// WithEnv option can supply without ubx core special-casing this binary at
// all. That wiring is Phase 5 (integration) work, not exercised here --
// Phase 1's own validation invokes this binary directly, setting the env
// var itself.
type Provider struct {
	Name         string
	SchemaSource SchemaSourceType `toml:"schema_source"`
	SchemaURL    string           `toml:"schema_url"`
	BaseURL      string           `toml:"base_url"`
	Auth         Auth             `toml:"auth"`

	// TargetPrefix is UBI-158 Phase 4 Checkpoint 2's own real, confirmed
	// finding: AWS's own published Smithy models (schema_source = "smithy")
	// carry NO field at all for the awsJson1_0/awsJson1_1 wire protocols'
	// own real X-Amz-Target header prefix -- confirmed directly this
	// session by diffing real aws-cli --debug request traces against the
	// real, same-session-fetched Smithy model files for three structurally
	// different services: SQS's real target prefix is "AmazonSQS" (a real
	// "Amazon"+sdkId shape), DynamoDB's is "DynamoDB_20120810" (service
	// name + real API version, NOT the same shape at all), neither of which
	// appears anywhere in either service's own model JSON. Guessing this
	// (e.g. always "Amazon"+sdkId) would silently misfire against
	// DynamoDB-shaped services -- AWS's own UnknownOperationException is
	// the real, loud failure a wrong guess would produce, but this
	// provider does not guess at all: TargetPrefix is required, explicit
	// config for any Smithy-sourced provider whose model resolves to
	// awsJson1_0/awsJson1_1 (validated once the model is loaded and its
	// real protocol is known -- see smithy.Build's own caller in main.go --
	// not here, since this table's own validate() runs before any model
	// fetch). Unused for every other real protocol (restJson1/restXml
	// derive everything from the model's own smithy.api#http trait;
	// awsQuery/ec2Query derive their own Action/Version framing from the
	// model's own operation name and service Version field, both real,
	// present fields).
	TargetPrefix string `toml:"target_prefix"`

	// Retry/Timeouts/Resources are UBI-158 Phase 3's own execution-
	// semantics config -- see execution.go's own doc comment. All
	// optional: an absent [retry]/[timeouts] table, or an absent
	// [resources.<type>] entry for a given resource, falls back to
	// dynserver's own real, documented defaults.
	Retry     RetryConfig               `toml:"retry"`
	Timeouts  TimeoutsConfig            `toml:"timeouts"`
	Resources map[string]ResourceConfig `toml:"resources"`
}

func (p Provider) validate() error {
	if p.SchemaSource == "" {
		return fmt.Errorf("dynamic_providers.%s: schema_source is required", p.Name)
	}
	switch p.SchemaSource {
	case SchemaSourceOpenAPI, SchemaSourceSmithy, SchemaSourceDiscoveryDoc:
		// implemented
	case SchemaSourceInline, SchemaSourceAWSCCAPI:
		return fmt.Errorf("dynamic_providers.%s: schema_source %q is a real UBI-158 tier but not yet implemented", p.Name, p.SchemaSource)
	default:
		return fmt.Errorf("dynamic_providers.%s: unrecognized schema_source %q (want one of: openapi, smithy, discovery_docs, inline, aws_ccapi)", p.Name, p.SchemaSource)
	}
	if (p.SchemaSource == SchemaSourceOpenAPI || p.SchemaSource == SchemaSourceSmithy || p.SchemaSource == SchemaSourceDiscoveryDoc) && p.SchemaURL == "" {
		return fmt.Errorf("dynamic_providers.%s: schema_url is required when schema_source is %q", p.Name, p.SchemaSource)
	}
	if p.BaseURL == "" {
		return fmt.Errorf("dynamic_providers.%s: base_url is required", p.Name)
	}
	return p.validateDurations()
}

// file is .ubx/config's shape, restricted to the one table this package
// owns -- every other real key (cli/config.go's [providers], [provider_configs],
// [ledger], ...) is silently ignored via toml.Decode's default behavior of
// only populating fields this struct actually declares.
type file struct {
	DynamicProviders map[string]Provider `toml:"dynamic_providers"`
}

// configFileName mirrors cli/config.go's own legacy, extensionless name --
// the real, current file this binary shares with ubx core, not a new file
// of its own.
const configFileName = ".ubx/config"

// Load parses dir's own .ubx/config (dir searched upward -- see findConfigFile)
// and returns every [dynamic_providers.<name>] table it declares, validated.
func Load(dir string) (map[string]Provider, error) {
	path, err := findConfigFile(dir)
	if err != nil {
		return nil, err
	}

	var f file
	if _, err := toml.DecodeFile(path, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	out := make(map[string]Provider, len(f.DynamicProviders))
	for name, p := range f.DynamicProviders {
		p.Name = name
		if err := p.validate(); err != nil {
			return nil, err
		}
		out[name] = p
	}
	return out, nil
}

// LoadNamed loads dir's .ubx/config and returns exactly the
// [dynamic_providers.<name>] table named name, erroring with the real set
// of declared names (sorted, deterministic -- CLAUDE.md's own standing
// determinism rule) if name isn't one of them.
func LoadNamed(dir, name string) (Provider, error) {
	all, err := Load(dir)
	if err != nil {
		return Provider{}, err
	}
	p, ok := all[name]
	if !ok {
		names := make([]string, 0, len(all))
		for n := range all {
			names = append(names, n)
		}
		sort.Strings(names)
		return Provider{}, fmt.Errorf("no [dynamic_providers.%s] table in %s (declared: %v)", name, configFileName, names)
	}
	return p, nil
}

// findConfigFile walks upward from dir looking for .ubx/config, stopping at
// the first directory that either has one or is a filesystem root. This is
// deliberately a simpler walk than ubx core's own multi-format cascade
// (cli/configcascade.go: config.hcl -> config.toml -> config -> config.yaml,
// merged across every directory up to a git boundary) -- this binary only
// ever needs the one, real, current .ubx/config file's own
// [dynamic_providers] table, never a multi-file merge, so re-implementing
// that whole cascade here would be scope this package doesn't need.
func findConfigFile(dir string) (string, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", dir, err)
	}
	for {
		candidate := filepath.Join(dir, configFileName)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no %s found in %q or any parent directory", configFileName, dir)
		}
		dir = parent
	}
}
