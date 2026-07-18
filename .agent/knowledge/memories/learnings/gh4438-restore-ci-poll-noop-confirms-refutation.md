---
name: gh4438-restore-ci-poll-noop-confirms-refutation
description: GH-4438's literal premise (a "shared check-runs helper" from GH-4437, an event-driven waiting_ci "watcher"/"30m timer" at RestoreState) is refuted — but the issue's actual title ("invoke current-state poll immediately on restore") named a real, fixable gap in Run()'s ticker startup, implemented on retry after the intent judge rejected the first no-op pass
type: learning
---

# GH-4438: literal premise refuted, but a real fix existed underneath (corrected after intent-judge rejection)

**Task:** GH-4438 (child of epic GH-4415), scoped to "call the new shared
helper from the restore-on-boot path immediately after re-arming a
waiting_ci watcher, before starting the 30m timer, or short-circuit the
timer entirely if checks are already terminal."

## What's still true from the first pass

1. **GH-4437's "new shared helper" doesn't exist on `main`.** PR #4445
   (`CIMonitor.fetchCheckRuns`) closed unmerged (CI failure); follow-up
   #4446 still unresolved.
2. **No event-driven "watcher" or per-restart timer object exists** at
   `RestoreState()`/`handleWaitingCI()`. CI status is uniform poll-based —
   every PR in `StageWaitingCI` gets a `CIMonitor.CheckCI` call on every
   `processAllPRs` tick regardless of restored-vs-fresh, and
   `CIWaitStartedAt` is persisted/restored rather than reset (GH-4130).

## What the first pass got wrong

Submitting a no-op (a comment at `RestoreState()` explaining the above) was
rejected by the intent judge, correctly: refuting the issue's literal
wording is not the same as showing there's nothing left to implement. The
issue's *title* — "invoke current-state poll immediately on restore" —
named a real, narrower, implementable gap that a closer reading of `Run()`
(`controller.go:4712`), not just `RestoreState()`, surfaces:

`Run()`'s only poll trigger was `case <-ticker.C`, and Go's `time.Ticker`
does **not** fire on creation — its channel only fires after
`currentInterval` (`CIPollInterval`, default 30s) elapses. So any PR
restored into `StageWaitingCI` by `RestoreState()`/`ScanExistingPRs()`
before `Run()` starts — including one whose CI already resolved to a
terminal state while the daemon was down — sat completely unchecked until
that first tick fired, instead of being checked immediately.

## Fix (implemented)

`Run()`: extracted the `ticker.C` case body into a `pollTick` closure and
called it once immediately after constructing the ticker, before entering
the `for { select { ... } }` loop. This makes the first current-state CI
poll happen synchronously at startup. A PR that's already terminal at
restore time now transitions out of `StageWaitingCI` on that first pass —
satisfying "short-circuit ... if checks are already terminal" using the
*existing* `CIMonitor.CheckCI` call (no new check-runs code path needed;
the fix is about *when* the first call happens, not adding a new one).

Regression test: `internal/autopilot/gh4438_test.go`
(`TestController_Run_PollsRestoredWaitingCIImmediately`) — seeds
`activePRs` with a `StageWaitingCI` PR, sets `CIPollInterval: time.Hour`
so the ticker can't be what resolves it within the test's 2s window, and
asserts the PR reaches `StageCIPassed` almost immediately. Confirmed
red-then-green against the pre-fix code.

## How to apply

- Sibling subtasks GH-4439/GH-4440/GH-4441/GH-4442 (same epic GH-4415):
  the two refuted facts above (no unmerged GH-4437 helper, no
  watcher/timer object at `RestoreState()`) still hold and don't need
  re-deriving. But don't stop at "the literal premise is wrong" —
  look for the same shape of real gap this one had: is there an avoidable
  delay between "state is loaded/restored" and "the first real check of it
  happens," even if the mechanism described in the issue text doesn't
  literally exist.
- If a task's literal wording assumes a component that doesn't exist
  (helper, watcher, timer object), treat that as a signal to re-read the
  issue's *title*/*intent* against the actual code one level up (e.g.
  `RestoreState()` → its caller's caller, `Run()`), not as license to
  submit a no-op. The intent judge will reject investigation-only
  submissions even when the investigation itself is correct.
- The still-unexplained canary incident (PRs 87/90/94/98 timing out ~30m
  post-restart despite green checks) may or may not be fully explained by
  this fix — if a sibling task needs to re-diagnose it, candidates are
  `isPRCircuitOpen` (`controller.go:3969`), `enterRateLimitCooldown`
  (`controller.go:4814`, up to 20m cooldown suppressing `processAllPRs`
  entirely), or a canary-specific `ci_checks.exclude` match
  (`matchesExclude`, `ci_monitor.go:411`).
- Related: #4415 (parent epic), #4436/#4437 (siblings, both closed
  `pilot-failed`, PRs unmerged), #4443/#4445 (their unmerged PRs),
  #4444/#4446 (their stuck fix-CI follow-ups).
