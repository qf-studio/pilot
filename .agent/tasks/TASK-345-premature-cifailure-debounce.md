# TASK-345: premature CIFailure on incomplete check suites closes PRs early (B4)

## Context

In both auto-mode aggregation (`checkAutoDiscoveredRuns`) and manual-mode `aggregateStatus`
(`internal/autopilot/ci_monitor.go`), `hasFailure` takes precedence over `hasPending`: as soon as
ANY single check run reports a failure conclusion, the function returns `CIFailure` even while sibling
checks are still queued/in-progress (`ci_monitor.go:201` `if hasFailure {` is evaluated before
`:204 if hasPending {`; same shape at `:276-285`). `handleWaitingCI` then transitions to
`StageCIFailed` immediately, which **closes the PR** (`controller.go:860 ClosePullRequest`), spawns a
fix-issue, and marks the board Failed. GitHub commonly (a) auto-reruns flaky checks and (b) reports
one matrix leg's failure before the others finish (fail-fast). With no debounce, a transient or
not-yet-final failure causes irreversible PR closure plus a spurious, costly fix-issue cascade.

## Approach

Do not declare `CIFailure` while any required/filtered check is still pending: return `CIFailure`
only when `hasFailure && !hasPending`; otherwise return `CIPending` so the suite can finish (and flaky
checks can auto-rerun). Apply to both `checkAutoDiscoveredRuns` and `aggregateStatus`. Optionally add
a single confirmation re-poll before acting on a terminal failure.

## Acceptance

- [ ] `checkAutoDiscoveredRuns` returns `CIPending` (not `CIFailure`) when at least one check failed but at least one is still pending/in-progress.
- [ ] `aggregateStatus` applies the same `hasFailure && !hasPending` rule.
- [ ] Test: one `failure` + one `in_progress` check yields `CIPending`; one `failure` + all-others-`completed` yields `CIFailure`.
- [ ] No premature `StageCIFailed`/`ClosePullRequest` transition while checks are pending.
- [ ] `make test` green for `internal/autopilot`; `make lint` clean.

## Refs

- Findings ledger: `.agent/tasks/TASK-322-security-audit-findings.md` (B4, medium)
- Kickoff: `.agent/tasks/TASK-342-wave3-kickoff.md`
- File: `internal/autopilot/ci_monitor.go:262-281, 304-325`; consumer `internal/autopilot/controller.go:860`
