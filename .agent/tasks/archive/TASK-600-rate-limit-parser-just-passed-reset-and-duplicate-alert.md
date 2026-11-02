# TASK-600: a just-passed reset rolls +24 h into a 6-hour daemon-wide hold; one usage-limit failure fires two alerts

**Status**: ✅ Merged 2026-10-27 11:21Z — #5879 → PR#5884 (failed the argument-discarding-mocks gate) → autopilot fix #5885 → **PR#5892** (`Fixes #5879`). **Post-merge review not posted.**
**Spec refs**: TASK-596 / #5862 → PR#5874 (usage-limit admission pause), review comment on PR#5874; `internal/executor/ratelimit.go` (`parseRateLimitErrorAt`, "if reset time is in the past, it's tomorrow"), `internal/executor/usage_limit_pause.go` (`armUsageLimitPause`), `internal/executor/runner.go` (`AlertEventTypeRateLimit` emission on `BackendError` type rate_limit).
**Origin**: founder 2026-10-27: "File one pilot + no-decompose issue for PR#5874 review defect 1 … also suppress the duplicate rate_limit alert when the pause arms."

## Legs — pilot (daemon)

### LEG H1 — repo: qf-studio/pilot — title: fix(executor): a reset time that has just passed must count as already reset, not tomorrow — otherwise the usage-limit pause holds every project for max_pause; and the pause alert replaces the per-run rate_limit alert

## Context
The post-merge review of PR#5874 (the usage-limit admission pause from #5862) found two defects.

First, the reset-time parser in `internal/executor/ratelimit.go` builds the reset instant from the clock's calendar day and, when that instant is before now, adds 24 hours ("if reset time is in the past, it's tomorrow"). A usage-limit error says "resets 2pm (UTC)" at the moment Claude hits the wall; the run finalizes minutes later, after the finalize window, recovery steps and the worker loop. When the reset passes in between, the parser returns tomorrow 2pm, and armUsageLimitPause in `internal/executor/usage_limit_pause.go` caps that to max_pause (default 6 hours) from now. Before PR#5874 this only delayed one task's own re-queue; now it holds admission on every project for six hours when the limit had in fact just reset. The guard in armUsageLimitPause that checks whether the candidate resume is after now is unreachable, because the parser never returns a past time. No test covers a reset near the boundary.

Second, one usage-limit failure now emits two alerts: the runner emits the per-run rate_limit alert when the backend error type is rate_limit (`internal/executor/runner.go`, "Backend hit rate limit"), and the dispatcher emits usage_limit_pause once per pause episode. Operators see two Slack messages for one event.

probe: present qf-studio/pilot resetTime = resetTime\.Add\(24 \* time\.Hour\)
probe: present qf-studio/pilot func \(d \*Dispatcher\) armUsageLimitPause\(
probe: present qf-studio/pilot alertType = AlertEventTypeRateLimit
probe: absent  qf-studio/pilot AlreadyReset
probe: absent  qf-studio/pilot pause alert replaces

## Scope
- Parser: add a window constant (recentResetWindow, 2 hours). When the calendar-day reset instant is before now but within that window, return it as is with a new RateLimitInfo field AlreadyReset true; only a reset older than the window rolls to the next day. ParseRateLimitError keeps its signature; parseRateLimitErrorAt gets the same rule.
- Pause: when AlreadyReset is true, armUsageLimitPause resumes at now plus the scheduler's RetryBuffer (not fallback_pause, not max_pause) and logs reset_at with a note that the reset had already passed. The existing fallback and cap rules stay for the other cases.
- Scheduler: the single-task re-queue that uses the same parser (`internal/executor/scheduler.go`, RetryBuffer) must also treat AlreadyReset as "retry after RetryBuffer", so one task is not pushed to tomorrow either.
- Alert: when the usage-limit pause is enabled (UsageLimitPauseConfig.IsEnabled), the runner does not emit the per-run rate_limit alert for a rate_limit backend error; the dispatcher's usage_limit_pause alert is the one notification for the episode. With the pause disabled the runner alert stays as today. Document this in the config comment for usage_limit_pause in `configs/pilot.example.yaml`.

## Acceptance
- [ ] Parser test, table-driven: "resets 2pm (UTC)" at 14:03 UTC yields reset 14:00 today with AlreadyReset true; at 16:30 UTC yields tomorrow 14:00 with AlreadyReset false; at 13:50 yields today 14:00 with AlreadyReset false. Remove the recentResetWindow rule -> TestParseRateLimitError_JustPassedResetIsAlreadyReset fails.
- [ ] Pause test with the fake clock: a rate_limited run whose error names a reset three minutes in the past pauses admission for RetryBuffer only, not fallback_pause or max_pause. Make armUsageLimitPause ignore AlreadyReset -> TestDispatcher_UsageLimitPauseJustPassedResetIsShort fails.
- [ ] Scheduler test: the same error re-queues the task after RetryBuffer, not tomorrow.
- [ ] Alert test: with the pause enabled, a rate_limit backend error produces exactly one alert event, of type usage_limit_pause; with the pause disabled, exactly one, of type rate_limit. Remove the suppression -> TestRunner_RateLimitAlertSuppressedWhenPauseArms fails.
- [ ] `go test -race ./internal/executor/ -run TestParseRateLimitError` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/executor/ -run TestDispatcher_UsageLimit` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/executor/ -run TestRunner_RateLimitAlert` passes; paste the output into the PR body.

## Out of scope
- New reset wordings ("resets in N hours"): a separate parser change.
- Persisting the pause across restarts, and the operator pause CLI (TASK-596 P1).
- The Stop-leaves-owner-set note from the review (not reachable today).

## Refs
- Review: https://github.com/qf-studio/pilot/pull/5874#issuecomment-6437389321
- H1 → - H1 → https://github.com/qf-studio/pilot/issues/5879
- Feature: TASK-596 → #5862 → PR#5874 (`076bb48f4`)

**Last Updated**: 2026-10-27
