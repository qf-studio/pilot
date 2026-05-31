# TASK-335 (B3): CI gating ignores the commit-status API — status-only repos auto-merge unverified

**Wave:** 2 · **Pilot** · **Severity:** HIGH (reliability) · **Issue:** #3326 · **Audit ref:** TASK-322 finding B3

---

## Problem
All CI status determination (`checkStatus`, `checkAutoDiscoveredRuns`, `GetCIStatus` used by
`verifyCIBeforeMerge`) reads **only** the GitHub Check Runs API via `ghClient.ListCheckRuns`. The legacy
commit-status API is never consulted: `ghClient.GetCombinedStatus` exists but has **no production callers**.
Check Runs and Commit Statuses are separate GitHub APIs. Providers like CircleCI, Jenkins, Travis,
Buildkite, and some Codecov configs report exclusively via the statuses API. For such a repo `ListCheckRuns`
returns empty → in default auto mode this hits the grace-period branch in `checkAutoDiscoveredRuns` which
logs "grace period expired with no CI checks, treating as success" and returns `CISuccess`. The PR then
merges with zero actual CI verification — a silent wrong-action. The pre-merge re-check shares the blind spot.

## Approach
When `ListCheckRuns` yields zero (filtered) runs, query `ghClient.GetCombinedStatus` **before** the
grace-period "treat as success" fallback. Map combined state: `failure`/`error` → `CIFailure`,
`pending` → `CIPending`, `success` → `CISuccess`. Only treat as "no CI configured" (success) when **both**
check-runs **and** combined statuses are empty.

## Files to modify
- `internal/autopilot/ci_monitor.go:126-158, 222-254`
- `internal/autopilot/ci_monitor_test.go`

## Test Strategy
- Empty check-runs + failing combined status → asserts `CIFailure`.
- Empty check-runs + empty combined status → asserts success (genuine no-CI repo).

## Effort
S–M. One PR. Distinct file from TASK-336 (controller.go) — parallel-safe within `internal/autopilot`.

## Out of Scope
- Premature-CIFailure debounce (Wave-3 finding B4).
