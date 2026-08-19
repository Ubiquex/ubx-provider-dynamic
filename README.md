# ubx-provider-dynamic

UBI-158's Dynamic Provider: a real `tfplugin` v6 provider binary that
derives its own resource schema and CRUD behavior at runtime from a real
OpenAPI 3.x spec, instead of shipping hand-written, per-service Go code.

Launched by `ubx` exactly like any HashiCorp provider binary
(`provider.Acquire` / `provider.Launch`, same subprocess-launch mechanism,
same tfplugin gRPC handshake) — zero special-casing in `ubx` core.

## Status: Phase 5 (the conformance gate)

All five UBI-158 phases now real and live-verified. Phases 1-4 built the
engine (OpenAPI + Smithy schema sources, pluggable auth including real
SigV4, execution semantics, real per-protocol wire execution). Phase 5
applied `ubiquex`'s own existing, real adversarial conformance harness
(`conformance/`, UBI-50/UBI-58) against this binary -- the same real,
falsifiable probe suite (identity-shape, sensitive-echo, destroy-honesty,
drift-detectability) every hand-written HashiCorp provider is held to, run
against `github_full_repository` (OpenAPI-sourced) and `aws_sqs_queue`
(Smithy-sourced), real create/destroy cycles included. That live run
surfaced and fixed two genuine, structural bugs neither visible from
schema inspection alone -- a path-parameter/response-attribute name
collision, and a hard `terraform-plugin-go` library constraint
(`tftypes.Object`'s `OptionalAttributes` cannot survive a real msgpack
round trip) -- see `internal/dynserver/build.go` and
`internal/schema/translate.go`'s own doc comments for the full, real
findings. See `internal/smithy`'s own doc comments and
`internal/smithy/wireexec`/`internal/smithy/server` for the real wire
protocol and tfplugin-server implementations.

Real protocol distribution across all 430 real AWS service models (direct
sampling of every real model file, not GitHub code search -- see
`internal/smithy/wireexec`'s own doc comment): `restJson1` 256 (59.5%),
`awsJson1_1` 102 (23.7%), `awsJson1_0` 50 (11.6%), `awsQuery` 15 (3.5%),
`restXml` 4 (0.9%, but includes S3), `ec2Query` 1 (0.2%, EC2 only). One
real service (`partnercentral-revenue-measurement`) declares no protocol
trait at all, and `cloudwatch` declares two (`awsJson1_0` + `awsQuery`).
All six real, model-declared protocols are handled -- restJson1/restXml
share one real REST-binding codec (httpLabel/httpQuery/httpHeader member
traits), awsJson1_0/awsJson1_1 share one real JSON-RPC codec, awsQuery/
ec2Query share one real form-encoded-Query codec -- dispatch is always
driven by the model's own declared protocol, never guessed.

Real, deliberate deviation from the original ticket's own assumption,
confirmed this session (not guessed): AWS's own published Smithy models
carry no field at all for awsJson1_0/awsJson1_1's real `X-Amz-Target`
header prefix (confirmed by diffing real `aws-cli --debug` traces against
the real model files -- SQS's real prefix is `AmazonSQS`, DynamoDB's is
`DynamoDB_20120810`, neither derivable from the other or from the model).
`target_prefix` is therefore required, explicit `.ubx/config` for any
Smithy-sourced provider whose model resolves to awsJson1_0/awsJson1_1 --
see `config.Provider.TargetPrefix`'s own doc comment.

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
7. **Smithy schema source** (`internal/smithy`, UBI-158 Phase 4 Checkpoint 1)
   — `schema_source = "smithy"`: AWS's own real, official, daily-published
   service models (`github.com/aws/api-models-aws`, confirmed live). Real
   findings, not assumed: AWS's own published models declare no Smithy
   `resource` shapes in practice, so resources are grouped by a real,
   AWS-wide verb+noun operation-naming convention instead
   (`Create`/`Run` + `Get`/`Describe` + `Update`/`Modify`/`Put`/`Set` +
   `Delete`); Smithy shapes convert into the *exact same* `openapi3.Schema`
   tree Phase 1's translator already consumes, unchanged (`toschema.go`),
   including Smithy's own `oneOf`-shaped tagged unions activating that
   translator's existing lossy-union-collapse path for free. The naming
   compatibility layer (`naming.go`) resolves a real HashiCorp-compatible
   name via a formula (`aws_<endpointPrefix>_<noun>`) checked against a
   real, embedded snapshot of `hashicorp/aws`'s own live resource names
   (dumped via `ubx`'s own `provider.Acquire`/`Launch`) — with a documented,
   confirmed-live exception: EC2's own naming is genuinely irregular
   (`aws_instance`/`aws_vpc` drop the service prefix entirely; EBS gets its
   own distinct prefix) and needs a bare-name fallback or an honestly
   unresolved result, not a forced formula. See `internal/smithy`'s own
   package doc comments for the full, real findings.
8. **Wire protocols** (`internal/smithy/wireexec`, UBI-158 Phase 4
   Checkpoint 2) -- real request/response execution for all six real
   AWS wire protocols, dispatched on the Smithy model's own declared
   protocol trait, sharing `restexec.Client`'s real retry/backoff/auth
   logic via its `DoWithCodec` extension point rather than reimplementing
   it per protocol. A real, load-bearing finding: the wire member name a
   real AWS request/response body uses is the Smithy shape's own member
   name (`QueueUrl`), never the schema's snake_case attribute name
   (`queue_url`) -- re-derived at request/response time from the model via
   the same `uschema.ToSnakeCase` the translator itself used, rather than
   threading a parallel name map through `BuiltResource`.
9. **SigV4 signing** (`internal/auth/sigv4.go`, UBI-158 Phase 4 Checkpoint
   2) -- real AWS SigV4 via `aws-sdk-go-v2/aws/signer/v4`, all three real
   credential sources (`env`/`profile`/`instance_role`) via
   `aws-sdk-go-v2/config`'s own real, standard credential chain, wrapped in
   `aws.CredentialsCache` for real, automatic refresh. Confirmed live: the
   `Authenticator` interface (`Apply(req *http.Request) error`) needed no
   change at all -- `req.GetBody`, already populated for every real
   `*bytes.Reader`-backed request this repo builds, is exactly what a real
   signer needs to hash the body and restore it afterward.

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

Smithy (`schema_url` points directly at one real AWS service's own model
file; `target_prefix` is required for awsJson1_0/awsJson1_1-protocol
services like SQS -- see item 7's own doc comment above):

```toml
[dynamic_providers.aws]
schema_source = "smithy"
schema_url = "https://raw.githubusercontent.com/aws/api-models-aws/main/models/sqs/service/2012-11-05/sqs-2012-11-05.json"
base_url = "https://sqs.us-east-1.amazonaws.com"
target_prefix = "AmazonSQS"

[dynamic_providers.aws.auth]
type = "aws_sigv4"
[dynamic_providers.aws.auth.params]
region = "us-east-1"
service = "sqs"
credential_source = "profile"
```

This now serves real CRUD requests (Checkpoint 2) -- prints real
discovery/naming results to stderr, then actually reads/creates/updates/
destroys against real AWS via the resolved real wire protocol.

## Conformance

The `ubiquex` monorepo's own `conformance/` package (UBI-50/UBI-58) can run
its full four-probe adversarial suite against this binary directly --
`conformance/dynamic_provider_live_test.go`, requiring a local
`ubx-provider-dynamic` checkout (`UBX_PROVIDER_DYNAMIC_REPO`, defaulting to
a sibling directory) and real credentials for whichever target provider
(`GITHUB_TOKEN` with `delete_repo` scope for the destroy-honesty probe;
real AWS credentials for `aws_sqs_queue`). See that file's own doc comment
for the full real design, and the `ubiquex` monorepo's own STATE.md for
real findings from the first live run.

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
