# TASK-321 PR-4: Parallel poller fresh-candidate `hasMergedWork` guard

**Wave:** 1 (S) · **Pilot** · **Severity:** HIGH · **TASK-321 fold** (parallel-mode parity for the
GH-3269 sequential fix) · **Audit ref:** TASK-322 §high "Parallel poller still lacks the fresh-candidate
hasMergedWork guard" + §medium "No parallel-mode test asserts a fresh already-merged candidate is skipped"

---

## Problem

GH-3269 (commit `f3c48fa7`, TASK-321 PR-2) added an **unconditional** fresh-candidate merged-work guard
to `findOldestUnprocessedIssue` (sequential): `if !processed && p.hasMergedWork(ctx, issue) { continue }`
at `poller.go:852`. The parallel path `checkForNewIssues` did **not** get the equivalent guard —
`hasMergedWork` at ~1152 lives only inside the `if processed { … }` retry block (which ends ~1156).

A fresh candidate in parallel mode (daemon restart, or after `unmarkProcessed` deleted the durable row on
a failed no-op run) is dispatched even though its PR already merged → the exact "no new commit produced"
phantom-blocked failure TASK-321 set out to eliminate. The GH-3269 fix corrected sequential mode but left
**parallel mode — the production default for concurrency>1 — exposed**, inverting rather than removing
the asymmetry. There is also no parallel-mode regression test, which is what let the gap ship.

## Approach

### Step 1 — Add the guard to the parallel path (S, ~30 min)
After the `if processed { … }` block in `checkForNewIssues` (~line 1156):
```go
if !processed && p.hasMergedWork(ctx, issue) {
    p.recordSkip(skipreason.ReasonHasMergedWork)
    continue
}
```
Match the sequential site (`:852`) exactly — same reason counter, same unconditional placement for fresh
candidates.

### Step 2 — Parallel-mode regression test (S, ~45 min)
Add `TestPoller_Parallel_FreshCandidate_MergedWorkGuard`, mirroring
`TestPoller_Sequential_FreshCandidate_MergedWorkGuard`:
- stub the search API so issue #N's PR is reported merged,
- run `poller.checkForNewIssues` + `WaitForActive`,
- assert `OnIssue` was **never** called for #N.

## Files to modify
- `internal/adapters/github/poller.go` (`checkForNewIssues`, ~1156)
- `internal/adapters/github/poller_gh3269_test.go` (new parallel-mode test)

## Test Strategy
- Unit: the new parallel-mode test + confirm the existing sequential GH-3269 tests still pass.
- This locks the phantom-redispatch regression for **both** dispatch modes.

## Effort
S (~1.5h). One PR. Branch name suggestion: `fix/task321-pr4-parallel-merged-guard`.

## Out of Scope
- Board-source-in-parallel-mode (`projectBoardSource` consulted in `checkForNewIssues`) — that's a
  TASK-319 follow-up (Wave 2 C2), a different concern that happens to live in the same function.
