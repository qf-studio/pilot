# Pitfall: Missing IsEpic guard mislabels parent results

## Summary
Epic-parent results legitimately have empty `CommitSHA` and `PRUrl` (children carry those), but the result-handling path treated empty values as failure and mislabeled the parent issue; guard with `IsEpic` check before applying the empty-value failure heuristic.

## Context
Surfaced during workshop dry-run alongside the vacuous-truth bug. Patched in v2.149.3 (commit fd0f7b69) as part of the empty-set guard work.

## Details
In `cmd/pilot/handlers.go` and `cmd/pilot/commands.go`, the path that turned execution results into issue labels/comments checked for empty `CommitSHA`/`PRUrl` as a failure signal. For epic parents this is the expected shape — children produce the commits and PRs, the parent aggregates. Without an `IsEpic` guard, every successful epic run was being labeled as failed. Fix added an `IsEpic` short-circuit before the empty-value failure check.

## Recommended Approach
When epic parent issues are reported failed despite their children succeeding:
1. Check `cmd/pilot/handlers.go` and `cmd/pilot/commands.go` for failure-detection paths that inspect `CommitSHA`/`PRUrl`.
2. Verify each such path has an `IsEpic` guard branching before the empty-value check.
3. This is a result-classification bug, not an execution bug — children may have run fine and still be labeled wrong.

## Related
- v2.149.3 commit fd0f7b69
- `cmd/pilot/handlers.go`, `cmd/pilot/commands.go`
- mem-pilot-001, mem-pilot-002, mem-pilot-003 (other v2.149.x patterns)

---
**Captured**: 2026-05-26
**Confidence**: 90%
**Concepts**: pilot, executor, debugging, epic, result-classification, guard
