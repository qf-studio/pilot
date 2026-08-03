# Guard executor worktrees against deleting graph-indexed memory files

**Created**: 2026-07-16 · **Status**: 🚀 Dispatched to Pilot · **Last Updated**: 2026-07-16

## Problem

Executor sessions repeatedly delete `.agent/knowledge/memories/**` files that ARE
indexed in `.agent/knowledge/graph.json`, judging them "unindexed" during
end-of-run hygiene. The graph node survives, the file is gone → the Knowledge
Graph Drift Gate (`scripts/check-graph.py`, "Broken file links") fails the PR's
CI → autopilot approval never fires → the issue wedges in awaiting.

There is **no in-repo code** that produces these deletions — it is the Claude
session's own judgment at finalize time (commit messages like
`chore(memory): strip unindexed memory doc(s) added during execution`).
Nothing deterministic stops it.

## Evidence (recurring class, 3+ incidents)

- **PR #4385** (GH-4376): commit `28c54ef4` deleted `mem-158.md` (indexed on
  main since #4373) → drift gate red, issue stuck awaiting. Fixed manually by
  revert `8db96c2b` 2026-07-16.
- **PR #4375**: had to restore `pitfall_sqlite_time_bind_default_format.md`
  (mem-153) + `pitfall_noop_terminal_state_invisible_to_dispatch_guards.md`
  (mem-154) — indexed in graph.json but stripped/never committed by earlier
  executions (#4345 / #4350 era).
- Drift-gate history: bd750650 / #4355 ("drift gate red on main since #4350").

## Required behavior

Add a **deterministic, in-tree guard** in the executor's finalize path
(before push / PR creation) that:

1. Detects, in the worktree's staged/committed diff vs the branch base, any
   **deletion** of a file under `.agent/knowledge/memories/` that is still
   referenced by a node in `.agent/knowledge/graph.json` (the file/path field
   of `nodes.memories.*`).
2. On detection: **restore the file(s)** (fail-safe — `git checkout <base> -- <path>`
   or equivalent) and log a WARN naming the file(s) and the graph node id(s);
   alternatively hard-fail the push with an actionable error. Restoring is
   preferred: it converts a wedged-PR class into a no-op.
3. Emits an execution event (existing `recordExecutionEvent` machinery) so the
   intervention is visible in `pilot trace` / the ledger.

### Suggested placement

- `internal/executor/git.go` push/PR-create path (same layer as the pre-push
  merge check), or the finalize step in `runner.go`. Mirror check-graph.py's
  "Broken file links" invariant in Go — do NOT shell out to python (CI-only
  dependency).
- Graph shape: `graph.json` → `nodes.memories.<mem-id>.file` (absolute
  repo-relative, e.g. `.agent/knowledge/memories/pitfalls/mem-158.md`); some
  legacy nodes use `path` relative to `.agent/knowledge/`. Handle both keys.

### Acceptance criteria

- [ ] Unit test: worktree diff deleting an indexed memory file → file restored
      (or push blocked), WARN logged, execution event recorded.
- [ ] Unit test: deleting a genuinely unindexed memory file is still allowed.
- [ ] Unit test: graph nodes using legacy `path` key are also protected.
- [ ] No behavior change when `.agent/knowledge/graph.json` is absent.
- [ ] `go test ./...` green; drift gate self-check unaffected.

## Refs

- Pilot issue: https://github.com/qf-studio/pilot/issues/4387
- Incident PR: https://github.com/qf-studio/pilot/pull/4385
- Restore precedent: https://github.com/qf-studio/pilot/pull/4375
- Drift gate: `scripts/check-graph.py`, `.github/workflows` CI job
  "Knowledge Graph Drift Gate" (TASK-386)
