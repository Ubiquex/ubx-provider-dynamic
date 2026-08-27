# STATE.md — current state

> Rewritten, not appended, as the LAST act of every session. See `HISTORY.md`
> for the narrative — why decisions were made, not what's true right now.

## In flight

Nothing in flight in this repo specifically as of 2026-08-27.

## Blocked

Nothing blocked. Zero open PRs (`gh pr list --repo Ubiquex/ubx-provider-dynamic
--state open`, checked directly).

## Current release

Latest published: `v1.0.1` (verify directly — `gh api
repos/Ubiquex/ubx-provider-dynamic/releases/latest`, don't trust this file if
it's gone stale). Carries:

- `internal/snapshot.MinBinaryVersion` stamped at generation time from the
  build's own `BinaryVersion` (ldflags target), not inferred from
  `schema_format` — a table keyed by format alone can't distinguish a
  pre-fix from a post-fix snapshot at the same format number (AWS's own real
  mixed-source case proved this).
- `AssembleGroup` forces at least a Patch-level version bump when a prior
  snapshot's `MinBinaryVersion` differs from this build's, even if no member's
  own translated content changed — otherwise a regeneration whose only real
  change is picking up a new `MinBinaryVersion` gets silently discarded by
  every schema repo's own `hash-watch.yml` "is this newer" gate.
- `provider.AcquireDynamicProviderBinary` (in `ubiquex`) is this binary's own
  real acquisition path — mirror-then-cache-then-verify, matching
  `Acquire`/`AcquireSchema`'s existing discipline. `UBX_PROVIDER_DYNAMIC_REPO`
  is checked first only as an explicit local-checkout dev override, never
  required on the normal path.

## Before touching anything

- A schema repo's `hash-watch.yml` must build this binary from a real, tagged
  release, not `main` HEAD — confirm with `-ldflags` and a `--version` check
  before trusting a generation run (this bit six repos at once, UBI-194).
- Never self-merge in this repo. See `CLAUDE.md`.
