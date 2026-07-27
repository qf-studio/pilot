# TASK-424: Memory-doc guards diff against stale local base ref — make all three legs origin-relative

**Created**: 2026-07-27 · **Status**: 🚀 Dispatched to Pilot · **Last Updated**: 2026-07-27

## Problem

The TASK-410 memory-guard series (`StripUnindexedMemoryDocs`,
`RestoreDeletedIndexedMemoryDocs` at `internal/executor/git.go:502`,
`EnforceMemoryDocDeletionGuard` at `internal/executor/git.go:589`) diffs the
worktree against the **local** `baseBranch` ref. Worktrees are cut from
`origin/main`, and nothing fast-forwards the shared local `main` on every
path (GH-4566 class — the exact bug #4568 fixed for the no-commits guards).

Consequence: a memory file merged to `origin/main` after the local ref last
synced is invisible to every guard leg — it is not deleted relative to the
stale local base, and it is not indexed in the stale local base's
`graph.json`. The deletion only materializes at squash-merge on GitHub,
where no guard runs. This is how strikes 4 (#4534/#4535, 6 files) and 5
(#4551) got past a guard stack that was already shipped.

## Required behavior

Mirror #4568's `CountNewCommitsAgainstOrigin` pattern
(`internal/executor/git.go`) for the memory guards:

1. Resolve the guard base as `origin/<baseBranch>`: fetch
   `origin <baseBranch>` first (best-effort), then use `origin/<baseBranch>`
   for (a) the `deletedMemoryDocs` diff, (b) `loadMemoryGraphAtRef` in
   `EnforceMemoryDocDeletionGuard`, and (c) the `git checkout <base> --`
   restore source in `RestoreDeletedIndexedMemoryDocs`.
2. Fall back to the local `baseBranch` when `origin/<baseBranch>` does not
   resolve (no remote — unit-test worktrees), preserving current behavior.
3. Prefer one shared helper (e.g. `resolveGuardBaseRef`) so the three legs
   cannot drift apart; callers in `runner.go:1708/1722`, `runner.go:4387/4402`,
   and `runner_decompose.go:430/444` should not need changes if resolution
   happens inside `GitOperations`.

## Acceptance criteria

- [ ] Unit test: memory file exists on `origin/<base>` but NOT on the stale
      local `<base>`; worktree deletes it → restore leg restores it from
      `origin/<base>` and the veto leg sees it as indexed (reads
      `origin/<base>`'s graph.json).
- [ ] Unit test: no `origin` remote → guards fall back to local base ref,
      existing behavior unchanged.
- [ ] Existing GH-4496 / GH-4397 / GH-4566 tests stay green.
- [ ] `go test ./...` green.

## Refs

- Pilot issue: https://github.com/qf-studio/pilot/issues/4573
- Precedent: #4568 (`CountNewCommitsAgainstOrigin`), GH-4566
- Guard series: TASK-410, GH-4387, GH-4398, GH-4496 (#4497)
- Incidents: #4484, #4500/#4506, #4534/#4535 (4th), #4551 (5th)
