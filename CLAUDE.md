# CLAUDE.md — ubx-provider-dynamic

## What this is

The generic provider binary `ubx` launches as a separate process over tfplugin
v6 to serve any provider whose schema comes from OpenAPI (incl. Swagger 2.0,
e.g. Kubernetes), CloudFormation, Smithy, or Google Discovery Documents —
rather than a hand-written, per-vendor Terraform/OpenTofu provider binary.
Coordinating repo: `github.com/ubiquex/ubiquex` (this binary is one of several
pieces `ubx` acquires and launches; see that repo's own `provider/` package).

Two real jobs, one binary: (1) serve a provider live, translating each source
format into the identical `tfprotov6` schema shape at request time; (2)
`--generate-snapshot`/`--generate-snapshot-group`, producing the frozen,
versioned `manifest.json` + `members/*.json` a `ubx-schema-<provider>` repo
commits — see that repo's own `CLAUDE.md` for the generation side.

## Session protocol

1. Read `STATE.md` first — current state only, rewritten not appended (see
   below).
2. `STATE.md` is rewritten, not appended, as the LAST act of every session —
   only what's current: in flight, blocked, what a fresh session needs before
   touching anything. Anything that becomes history moves to `HISTORY.md`
   (narrative archive, consulted only when a session needs to know why a
   decision was made, not on every open).
3. Only reference Linear issue IDs given in the handoff prompt; never infer
   one.

## Git rules (strict)

- PR-only. Never self-merge — push a branch, open a PR, wait for the founder
  to review and merge, per this repo's own checkpoint-branch incident.
- Before pushing more commits to a branch with an open PR, confirm it is
  STILL open (`gh pr list --state open` or `gh pr view <n>`) — a merged PR's
  branch looks identical to any other from `git status` alone, and a push
  after merge lands nowhere near `main`, silently.
- NO AI attribution anywhere: no Co-Authored-By trailers, no "Generated with"
  lines, not in commit messages, not in PR bodies.
- Conventional, terse commit messages: `component: what changed`.

## Release discipline

- `.github/workflows/publish.yml` is manual `workflow_dispatch` only, never
  automatic. Cross-compiles `linux/amd64`, `linux/arm64`, `darwin/amd64`,
  `darwin/arm64`, embeds the real version via
  `-ldflags -X .../internal/snapshot.BinaryVersion=<version>`, cuts one real
  GitHub Release with a shared `SHA256SUMS`. Verify a real release via
  `gh api repos/Ubiquex/ubx-provider-dynamic/releases/tags/v<version>` after
  dispatch — never trust the workflow's own exit status alone.
- `internal/snapshot.BinaryVersion` is a build-time var (`"dev"` default when
  built without `-ldflags`) — `AssembleGroup` stamps it into every generated
  snapshot's own `MinBinaryVersion` field. A schema repo's own `hash-watch.yml`
  must build this binary from a real, tagged release (not `main` HEAD) or every
  snapshot it regenerates stamps the meaningless `"dev"` instead.
- `go build`/`go vet`/`go test ./...` clean, whole-repo, before any PR.

## Key docs

- `ubiquex`'s own `docs/architecture.md`/`docs/schema.md` — how this binary's
  output (a served/frozen schema) fits the wider proposal-ledger model.

## Architecture documentation

A change here that's architectural — a new schema source, a change to
the mixed-source dispatch layer, a change to namespace/naming
computation, a new snapshot mechanism, a change to what a snapshot
records — gets its `ubiquex-internals` page (the developer
documentation site) written or updated in the SAME PR, never a
follow-up. A bug fix inside an already-documented mechanism doesn't
qualify. Matches `ubiquex` CLAUDE.md rule 10.
