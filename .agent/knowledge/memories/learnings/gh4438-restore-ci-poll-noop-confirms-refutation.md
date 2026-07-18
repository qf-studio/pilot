---
name: gh4438-restore-ci-poll-noop-confirms-refutation
description: GH-4438 ("call the new shared check-runs helper from RestoreState before starting the 30m timer") is a no-op — independently re-confirms GH-4436's refutation that RestoreState/waiting_ci uses any event-driven watcher/timer-start mechanism; also GH-4437's helper (fetchCheckRuns) never merged (PR #4445 closed, CI failed)
type: learning
---

# GH-4438: restore-on-boot CI poll fix is a no-op (re-confirms GH-4436)

**Task:** GH-4438 (child of epic GH-4415), scoped to "call the new shared
helper from the restore-on-boot path immediately after re-arming a
waiting_ci watcher, before starting the 30m timer, or short-circuit the
timer entirely if checks are already terminal."

## Two independent problems with this task's premise

1. **The dependency doesn't exist on `main`.** GH-4437 ("extract shared
   check-runs current-state polling helper") was closed as `pilot-failed`.
   Its PR (#4445, branch `pilot/GH-4437`) added `CIMonitor.fetchCheckRuns`
   but was closed without merging — CI (`test` check) failed. Follow-up
   issue #4446 (`pilot-needs-clarification`) is still open/unresolved. This
   worktree's `internal/autopilot/ci_monitor.go` has no `fetchCheckRuns`
   method (grep confirmed at time of GH-4438 execution, 2026-07-18).

2. **The hypothesized mechanism doesn't exist in the architecture — verified
   independently, not just cited from GH-4436.** Read directly
   (2026-07-18):
   - `RestoreState()` (`internal/autopilot/controller.go:1071`) only copies
     `PRState`/`prFailureState` rows from SQLite into `c.activePRs` /
     `c.prFailures`. It does not "arm" a watcher, does not touch
     `c.ciMonitor`, and does not start a timer.
   - `handleWaitingCI()` (`controller.go:1438`) is reached uniformly for
     every PR in `StageWaitingCI` — restored or freshly registered — via
     `processAllPRs` → `ProcessPR` → `handleWaitingCI`, on every tick. It
     lazily initializes `CIWaitStartedAt` only if zero
     (`controller.go:1440-1442`), checks it against `ciTimeout`
     (`controller.go:1453`), then always calls `CIMonitor.CheckCI` →
     `checkStatus` → `ListCheckRuns` (a genuine current-state read) in the
     same call, every time. There is no "listen for new events, skip
     current-state read" branch to short-circuit.
   - `CIWaitStartedAt` is persisted and restored by
     `state_store.go:609/686/942`, not reset to zero at restart — GH-4130
     already fixed that class of bug, and `ScanExistingPRs`
     (`controller.go:4220-4224`) explicitly guards against re-clobbering it
     via `OnPRCreated`. So there is no "timer starts fresh after restart"
     bug either, independent of the check-runs polling question.

## Conclusion

Nothing to call, nowhere sensible to call it. Implementing GH-4438 as
literally scoped would mean wiring a call to a helper function that isn't
in the codebase, into a restore path that has no watcher/timer object to
short-circuit. This is a second, independent confirmation of GH-4436's
finding — the epic GH-4415's decomposition was built on a plausible but
incorrect hypothesis about the codebase's CI-watching architecture (it's
uniform poll-on-every-tick, not event-driven-with-fallback).

## What's still unresolved

Same as GH-4436 flagged: the canary incident (PRs 87/90/94/98 timing out at
~30m post-restart despite green checks) is real and unexplained by this
investigation. Untried candidates: `isPRCircuitOpen`
(`controller.go:3969`), `enterRateLimitCooldown`
(`controller.go:4814`, up to 20m cooldown suppressing `processAllPRs`
entirely), or a canary-specific `ci_checks.exclude` match
(`matchesExclude`, `ci_monitor.go:411`). None of these are in GH-4438's
scope fence.

## How to apply

- Sibling subtasks GH-4439/GH-4440/GH-4441/GH-4442 (same epic GH-4415) are
  very likely to hit this same refuted premise if they assume an
  event-driven CI watcher exists. Check this memory (and
  `gh4436-restore-ci-poll-hypothesis-refuted`, which did not survive on
  `main` because PR #4443 also closed unmerged — re-derive from this file
  and `.agent/tasks/gh-4438.md` if that memory is still missing) before
  spending investigation budget re-deriving the same conclusion.
- If re-scoping GH-4415: point the epic at the actual unresolved
  candidates (circuit breaker / rate-limit cooldown / exclude-filter) with
  a fresh investigation subtask, not at "extract the polling helper" /
  "call it on restore," both of which are now confirmed dead ends twice
  over.
- Related: #4415 (parent epic), #4436/#4437 (siblings, both closed
  `pilot-failed`, evidence not preserved on `main`), #4443/#4445 (their
  unmerged PRs, evidence source for this memory), #4444/#4446 (their stuck
  fix-CI follow-ups).
