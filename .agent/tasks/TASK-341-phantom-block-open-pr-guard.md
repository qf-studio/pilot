# TASK-341: Stop phantom `pilot-blocked` on a no-op re-dispatch when an OPEN PR already exists

**Severity:** HIGH (queue integrity) · **Manual-candidate** (edits Pilot's own dispatch-result classifier) · **Audit ref:** TASK-321 follow-up

---

## Problem
`pilot-done` + issue-close are deferred to merge time (GH-3139/TASK-301). So between *PR created* and *PR
merged*, an issue still looks like a fresh candidate (`pilot` label, no `pilot-done`, not closed,
`pilot-in-progress` removed). The poller re-picks it; the re-dispatch runs in a fresh worktree off `main`,
finds the work already on the `pilot/GH-NNNN` branch, and produces `no new commit produced — worktree HEAD
matches base branch parent` (`runner.go:2398`). `cmd/pilot/handlers.go:430` then classifies that no-op: the
TASK-321 guard only treats an **already-merged** PR (`issueAlreadyMerged`, handlers.go:61 — searches *merged*
PRs only) as benign → done; an **open** PR falls through to the `else` branch (handlers.go:452) →
`pilot-blocked`. Net: a healthy issue with a green open PR gets false-flagged blocked.

Observed this session on 4/6 Wave-2 items (#3326/#3327/#3330/#3331): each had a green PR yet was marked
`pilot-blocked`; cleared manually. Prior fixes (#3277 handler, #3300 poller `hasMergedWork`, #3279 marker
retention) all cover only the already-*merged* case — the open-PR-awaiting-merge window was never handled.

## Approach
Two layers (the first is the actual fix; the second is defense-in-depth):
1. **`cmd/pilot/handlers.go`** — broaden the no-op benign check from `issueAlreadyMerged` to also detect an
   **open** `pilot/GH-NNNN` PR for the issue (e.g. `SearchPRsForIssue` filtered to open, or an open-branch
   lookup). On a `no new commit` no-op when an open PR exists, treat it as **awaiting-merge** — leave it for
   the autopilot merge flow; do NOT add `pilot-blocked`. Only fall through to `pilot-blocked` when there is
   neither a merged nor an open PR (a genuine no-op).
2. **`internal/adapters/github/poller.go`** — in `checkForNewIssues` and `findOldestUnprocessedIssue`, skip
   dispatching an issue that already has an open `pilot/GH-NNNN` PR (new skip reason, e.g.
   `ReasonHasOpenPR`), so the redundant run never starts.

## Files to modify
- `cmd/pilot/handlers.go` (classifier at ~430 + `issueAlreadyMerged` at ~61) + `handlers_spec_test.go`/equivalent
- `internal/adapters/github/poller.go` (+ `internal/adapters/skipreason`) + poller test
- a GitHub client helper to find an OPEN PR for an issue/branch if one doesn't already exist

## Test Strategy
- Handler: a `no new commit` result + an **open** PR for the issue → issue is NOT labeled `pilot-blocked`
  (treated as awaiting-merge); with neither open nor merged PR → still `pilot-blocked`; with a merged PR →
  done (unchanged).
- Poller: an issue with an open `pilot/GH-N` PR is skipped (not dispatched) in both sequential and parallel paths.

## ⚠️ Manual-candidate
This edits how Pilot classifies its **own** execution results — the self-modifying-executor case that the
session learnings flag for MANUAL handling. If delegated to Pilot, it may phantom-block *itself*; review
carefully and prefer taking it manually (worktree + tests + `-race`).

## Out of Scope
- Reworking the GH-3139 defer-done-to-merge decision (intentional; keep it).
