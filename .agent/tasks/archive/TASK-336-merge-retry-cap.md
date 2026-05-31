# TASK-336 (B5): Unbounded merge-retry loop — cap MergeAttempts, escalate non-conflict failures

**Wave:** 2 · **Pilot** · **Severity:** HIGH (reliability) · **Issue:** #3327 · **Audit ref:** TASK-322 finding B5

---

## Problem
`handleMerging` increments `prState.MergeAttempts` and logs it, but the value is **never compared to any
limit** — purely cosmetic. The only bound on merge retries is the per-PR circuit breaker (`MaxFailures`
default 3), but `isPRCircuitOpen` **auto-resets** after `FailureResetTimeout` (default 30 min). For a merge
that fails persistently for a **non-conflict** reason (branch protection requiring a review/status Pilot
can never satisfy, a 405 "not mergeable", a required check that stays red), the PR stays in `StageMerging`,
fails ~3×, the breaker opens, 30 min later auto-resets, and the cycle repeats **indefinitely** — burning API
quota with no human-actionable failure. Conflicts are already handled separately via `handleMergeConflict`.

## Approach
Enforce a hard cap. Add config `MaxMergeAttempts` (e.g. default 5). When
`prState.MergeAttempts >= c.config.MaxMergeAttempts`, transition to `StageFailed` with an escalation
comment/notification instead of returning a retryable error. Keep the circuit breaker for transient backoff,
but make the per-PR merge cap **terminal** so non-conflict failures escalate to a human rather than looping.

## Files to modify
- `internal/autopilot/controller.go:1193-1218 (handleMerging), 1942-1951 (isPRCircuitOpen)`
- `internal/autopilot/types.go` / `config.go` — add `MaxMergeAttempts`
- `internal/autopilot/controller_test.go`

## Test Strategy
- Persistent non-conflict merge error → reaches `StageFailed` after `MaxMergeAttempts` (not an infinite loop).
- Transient-then-success path still merges.

## Effort
M. One PR. Distinct file from TASK-335 (ci_monitor.go).

## Out of Scope / coordinate
- Do **not** disturb the per-PR `*PRState` mutex added by M2 (#3301). Reviewer must confirm locking intact.
