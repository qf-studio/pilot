# TASK-334: Lock the IsTaskShipped / HasCompletedExecution invariant with the `{pr_url, error}` row

**Wave:** 2 (XS) · **Pilot** · **Severity:** HIGH (test-gap; code already fixed) · **Audit ref:**
TASK-322 §high "HasCompletedExecution and IsTaskShipped diverge when a completed row has a PR URL AND a
non-empty error"

---

## Problem

The two halves of the "is this task shipped?" invariant can disagree for the row
`{status='completed', pr_url!='', error!=''}`. The code guard already landed on `main`
(`internal/executor/task_shipped.go:21` now checks `error == ""` first), but the invariant test never
exercises the `error + pr_url` combination, so the agreement is unverified and could silently regress.

## Approach
- Add a table row `{Status: "completed", PRUrl: "...", Error: "x"}` → `wantShipped: false` to
  `TestTaskCompletionInvariant` (`internal/executor/task_completion_invariant_test.go`), and the
  equivalent case to the `IsTaskShipped` unit table.
- Assert `IsTaskShipped(row)` and `HasCompletedExecution(issue, project)` (with that row in the DB)
  **agree** (both → not shipped) for this combination.

## Files to modify
- `internal/executor/task_completion_invariant_test.go`
- `internal/executor/task_shipped_test.go` (if a separate unit table exists)

## Test Strategy
- The new row makes the cross-site invariant test fail if either site ever stops guarding on `error`.
  No production code change expected (guard already present) — if the test surfaces a real divergence,
  fix the offending site in-scope.

## Effort
XS (~30 min). One PR. Test-only.

## Out of Scope
- Any broader refactor of the completion predicate.
