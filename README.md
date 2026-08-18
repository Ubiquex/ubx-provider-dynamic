# ubx-provider-dynamic

UBI-158's Dynamic Provider: a real `tfplugin` v6 provider binary that
derives its own resource schema and CRUD behavior at runtime from a real
OpenAPI 3.x spec, instead of shipping hand-written, per-service Go code.

Launched by `ubx` exactly like any HashiCorp provider binary
(`provider.Acquire` / `provider.Launch`, same subprocess-launch mechanism,
same tfplugin gRPC handshake) — zero special-casing in `ubx` core.

## Status: Phase 3

Layers 1-4, auth, and execution semantics (retry/backoff, per-operation
timeouts, async polling, field-level drift). SigV4 itself, Smithy, and the
conformance gate remain out of scope.

1. **tfplugin server** (`internal/dynserver`, `cmd/ubx-provider-dynamic`) —
   the real gRPC surface, served via `terraform-plugin-go`'s own
   `tfprotov6`/`tf6server` (protocol v6, matching what `ubx`'s own executor
   negotiates against a dual v5/v6-capable client).
2. **Schema translation** (`internal/schema`) — OpenAPI 3.x schemas into
   tfplugin's type system, using protocol v6's real nested-attributes
   feature. Every lossy decision (`oneOf`/`anyOf` collapse, free-form
   objects, mixed-type unions, self-referential schemas) is recorded as a
   `Note`, never silently flattened.
3. **Resource mapping** (`internal/resourcemap`) — OpenAPI paths/operations
   into CRUD resources, paired by response-schema identity (not path
   structure — real APIs, GitHub's own included, create a resource at a
   different path than they read it from). Deterministic
   `<provider>_<resource>` naming.
4. **Config layer** (`internal/config`) — reads a stack's own
   `.ubx/config`, a `[dynamic_providers.<name>]` table (`schema_source`,
   `schema_url`, `base_url`, `auth`). See the package doc comment for why
   this table is named differently from the ticket's original
   `[providers.<name>]` text, and why it's read directly off disk rather
   than delivered via `ConfigureProvider`.
5. **Auth** (`internal/auth`) — pluggable, not a fixed enum: each real type
   self-registers a `Factory` (the same shape `database/sql` drivers use).
   Real implementations: `api_key_header` (one or more header/env pairs —
   covers GitHub's single `Authorization: Bearer` and Datadog's real
   two-header `DD-API-KEY`/`DD-APPLICATION-KEY` scheme with no
   service-specific code) and `oauth2_client_credentials` (RFC 6749 §4.4,
   built on `golang.org/x/oauth2/clientcredentials`). `aws_sigv4` is
   registered with its real config shape (`region`/`service`/
   `credential_source`) but `Apply` refuses until Phase 4 — see
   `internal/auth/sigv4.go`'s own doc comment for why the existing
   `Authenticator` interface already needs no change to support it.
   Credentials are always an env var *reference* (`value_env`,
   `client_secret_env`, ...), never a literal value in config.
6. **Execution semantics** (`internal/restexec`, `internal/dynserver`) —
   real transport-level retry/backoff (`Retry-After`, a configurable
   rate-limit-reset header, exponential-with-jitter fallback), per-CRUD-
   operation timeouts independent of `ubx`'s own ambient `--ship` budget,
   generic async/long-running-operation polling (poll-until-terminal,
   declared per resource type in config, not modeled on any one real
   provider), and field-level drift rules (`ignore`/`normalize`, declared
   per resource type). The load-bearing correctness piece: every real REST
   failure is classified terminal (a real, structured rejection — reported
   as a tfplugin Diagnostic) or ambiguous (returned as a plain Go error
   from the RPC method instead) — see `internal/dynserver/server.go`'s own
   `classifyRESTError` doc comment for why this specific split is what lets
   `ubx` core's own reconcile-by-query (`docs/executor.md` in the
   `ubiquex` monorepo) do its job instead of this provider guessing.

`internal/wire` (tftypes ↔ plain JSON) and `internal/restexec` (real HTTP
CRUD execution) are the layer-4/6 glue a Dynamic Provider needs that a real
HashiCorp provider never does — a real provider's own SDK speaks Go
structs on its side of the wire, not raw untyped JSON.

## Running it

```
export UBX_DYNAMIC_PROVIDER_NAME=github
cd /path/to/a/stack/with/.ubx/config
go run ./cmd/ubx-provider-dynamic
```

`.ubx/config`:

```toml
[dynamic_providers.github]
schema_source = "openapi"
schema_url = "https://raw.githubusercontent.com/github/rest-api-description/main/descriptions/api.github.com/api.github.com.json"
base_url = "https://api.github.com"

[dynamic_providers.github.auth]
type = "api_key_header"
[[dynamic_providers.github.auth.params.headers]]
name = "Authorization"
value_env = "GITHUB_TOKEN"
value_prefix = "Bearer "

[dynamic_providers.datadog]
schema_source = "openapi"
schema_url = "https://raw.githubusercontent.com/DataDog/datadog-api-client-go/master/.generator/schemas/v1/openapi.yaml"
base_url = "https://api.datadoghq.com"

[dynamic_providers.datadog.auth]
type = "api_key_header"
[[dynamic_providers.datadog.auth.params.headers]]
name = "DD-API-KEY"
value_env = "DATADOG_API_KEY"
[[dynamic_providers.datadog.auth.params.headers]]
name = "DD-APPLICATION-KEY"
value_env = "DATADOG_APP_KEY"
```

(the `params` segment is required — `config.Auth.Params` decodes from TOML's own `params` key, confirmed by an end-to-end parse test, `internal/config/auth_integration_test.go`.)

`oauth2_client_credentials` example:

```toml
[dynamic_providers.<name>.auth]
type = "oauth2_client_credentials"
[dynamic_providers.<name>.auth.params]
token_url = "https://example.com/oauth/token"
client_id_env = "EXAMPLE_CLIENT_ID"
client_secret_env = "EXAMPLE_CLIENT_SECRET"
scopes = ["read", "write"]
```

Execution semantics example:

```toml
[dynamic_providers.<name>.retry]
max_attempts = 5
initial_backoff = "200ms"
max_backoff = "30s"
jitter = true
respect_retry_after = true
rate_limit_reset_header = "X-RateLimit-Reset"   # real, confirmed live against GitHub's own API

[dynamic_providers.<name>.timeouts]
create = "90s"
read = "20s"
default = "30s"

[dynamic_providers.<name>.resources.<derived_type_name>.async]
enabled = true
operation_id_field = "operation_id"       # or operation_id_header = "Location"
poll_path_template = "/jobs/{operation_id}"
status_field = "status"
terminal_success_values = ["succeeded"]
terminal_failure_values = ["failed", "cancelled"]
poll_interval = "5s"
poll_timeout = "10m"

[dynamic_providers.<name>.resources.<derived_type_name>.drift]
ignore = ["updated_at"]
[dynamic_providers.<name>.resources.<derived_type_name>.drift.normalize]
homepage = "lowercase"
```

`<derived_type_name>` is this provider's own `<provider>_<resource>` name
(e.g. `github_full_repository`) — run discovery once (any command, or the
live validation tests) to see the real, derived names for a given spec.

## Testing

```
go test ./...
```

stays hermetic (no network) by default. Live validation against real,
published OpenAPI specs is gated separately:

```
UBX_LIVE_VALIDATION=1 go test ./internal/dynserver/... -run TestLive -v
```

## Constraints

- Never self-merge.
- No em dashes.
- Real tests, no transport-level mocking.
- Every claim verified against real, current source and real specs, not
  memory.
