# ubx-provider-dynamic

UBI-158's Dynamic Provider: a real `tfplugin` v6 provider binary that
derives its own resource schema and CRUD behavior at runtime from a real
OpenAPI 3.x spec, instead of shipping hand-written, per-service Go code.

Launched by `ubx` exactly like any HashiCorp provider binary
(`provider.Acquire` / `provider.Launch`, same subprocess-launch mechanism,
same tfplugin gRPC handshake) — zero special-casing in `ubx` core.

## Status: Phase 1

Layers 1-4 only. Auth, async execution, drift rules, Smithy, and the
conformance gate are explicitly out of scope for this phase.

1. **tfplugin server** (`internal/dynserver`, `cmd/ubx-provider-dynamic`) —
   the real gRPC surface, served via `terraform-plugin-go`'s own
   `tfprotov6`/`tf6server` (protocol v6, matching what `ubx`'s own executor
   negotiates against a dual v5/v6-capable client).
2. **Schema translation** (`internal/schema`) — OpenAPI 3.x schemas into
   tfplugin's type system, using protocol v6's real nested-attributes
   feature. Every lossy decision (`oneOf`/`anyOf` collapse, free-form
   objects, mixed-type unions) is recorded as a `Note`, never silently
   flattened.
3. **Resource mapping** (`internal/resourcemap`) — OpenAPI paths/operations
   into CRUD resources, paired by response-schema identity (not path
   structure — real APIs, GitHub's own included, create a resource at a
   different path than they read it from). Deterministic
   `<provider>_<resource>` naming.
4. **Config layer** (`internal/config`) — reads a stack's own
   `.ubx/config`, a `[dynamic_providers.<name>]` table (`schema_source`,
   `schema_url`, `base_url`). See the package doc comment for why this
   table is named differently from the ticket's original
   `[providers.<name>]` text, and why it's read directly off disk rather
   than delivered via `ConfigureProvider`.

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
```

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
