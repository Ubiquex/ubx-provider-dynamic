# HISTORY.md — narrative archive

> Consulted only when a session needs to know why a decision was made, not on
> every open. For what's current, read `STATE.md` instead.

This file is new as of UBI-183 (2026-08-27) — this repo had no `STATE.md` to
carry forward, so there's no prior narrative to relocate here. Real history
predating this file lives in `ubiquex`'s own `HISTORY.md` (search for `UBI-158`,
`UBI-175`, `UBI-182`, `UBI-186`, `UBI-189`, `UBI-193` — this binary's own
generic-engine build, per-source snapshot generation, and Azure external-ref
bundling work all happened in sessions coordinated from there) and in this
repo's own real `git log`/merged-PR history, which is authoritative for what
actually shipped and when — this file does not attempt to re-derive it.

## UBI-194 (2026-08-27): publish and acquire this binary itself

Real gap: `ubx` launched this binary as a separate process, but it only ever
existed via a local checkout pointed at by `UBX_PROVIDER_DYNAMIC_REPO` — no
schema repo's real pin was resolvable by anyone outside this project.

Version-resolution design went through two real rounds before landing:
an explicit table keyed by `schema_format` was rejected (AWS's own pre-#24 and
post-#24 snapshots both declare the same format number, so a table entry can't
tell them apart). Landed on `Snapshot.MinBinaryVersion`, stamped by
`AssembleGroup` at generation time from the real build's own `BinaryVersion` —
exact by construction, self-heals on every real regeneration, no table to
maintain.

`PR #27`: `MinBinaryVersion` field + `BinaryVersion` build var + `--version`
flag + `publish.yml` (this repo's first real release process). Merged.

A real, live gap found while actually attempting the first post-#27
regeneration (not hypothetical): a snapshot regenerated against unchanged
upstream content kept the SAME version number (`NextVersion` on `NoChange`
returns the input unmodified), so a caller's own "is this newer" gate saw no
change and discarded the freshly-stamped `MinBinaryVersion` — confirmed via a
real `ubx-schema-kubernetes` `hash-watch.yml` run that skipped its own "Open
PR" step entirely. `PR #28`: `AssembleGroup` now forces at least a Patch bump
when only `MinBinaryVersion` changed. Merged, `v1.0.1` cut, Kubernetes
regenerated past the fallback and confirmed via a real, live, zero-network pin
resolution — no more `UBX_PROVIDER_DYNAMIC_REPO` needed anywhere in that path.
