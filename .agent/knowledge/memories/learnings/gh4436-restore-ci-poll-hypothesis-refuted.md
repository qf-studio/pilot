---
name: gh4436-restore-ci-poll-hypothesis-refuted
description: GH-4415's hypothesis ("watchers re-armed via RestoreState only listen for new check-run events, skipping the initial current-state poll") does not match the codebase — autopilot has no event/webhook-driven CI watcher at all; it is pure poll, applied identically to restored and fresh PRs
type: learning
---

# GH-4436: restore-path CI-watcher hypothesis is refuted by the code

**Task:** GH-4436 (child 1/7 of epic GH-4415), scoped to inspect
`internal/autopilot/controller.go` `RestoreState()` and `ci_monitor.go` "arm"
logic and confirm/refute the hypothesis before siblings #4437-#4442 implement
a fix for it.

## The hypothesis (from GH-4415)

> The re-armed `waiting_ci` state (`RestoreState` after restart) may take a
> different code path than fresh PR registration... if the watcher only
> reacts to *new* check-run events rather than reading current state on arm,
> an already-green PR never produces an event and dies at timeout.

Evidence cited: canary-sandbox PRs 87/90/94/98 timed out at exactly ~30m
after a 14:22Z restart despite `gh pr checks` showing green, while pointer
PRs #29/#31 (registered fresh, no restart in their window) progressed fine
under the same binary (v2.241.2).

## Finding: the hypothesized mechanism does not exist

1. **No check-run webhook handler exists anywhere in the repo.**
   `internal/adapters/github/webhook.go`'s `Handle()` only switches on
   `"issues"` and `"pull_request_review"` event types. There is no
   `check_run`/`check_suite`/`workflow_run` case, no `OnCheckRun` callback, no
   push-based CI listener of any kind (grep confirmed across
   `internal/adapters`, `internal/gateway`, `internal/autopilot`).

2. **CI status is 100% poll-based, uniformly, for every PR.**
   `Controller.Run()` (controller.go:4702) runs a single ticker that calls
   `processAllPRs` → `ProcessPR` → `handleWaitingCI` (controller.go:1438) →
   `CIMonitor.CheckCI` (ci_monitor.go:480) → `checkStatus` (ci_monitor.go:160)
   → `ghClient.ListCheckRuns` — a full "current state" GitHub API read — on
   **every single tick, for every PR in `StageWaitingCI`**, with no
   distinction based on how the PR entered `c.activePRs`.

3. **`RestoreState()` (controller.go:1071) does nothing CI-related.** It only
   copies `PRState`/`prFailureState` rows from SQLite into
   `c.activePRs`/`c.prFailures`. It never touches `c.ciMonitor`, never sets a
   flag consulted by `checkStatus`/`checkAutoDiscoveredRuns`, and does not
   distinguish "restored" state from state built by `OnPRCreated`
   (controller.go:1161) in any way visible to the CI-check path — both just
   become entries in the same map, walked by the same loop.

4. **`checkAutoDiscoveredRuns` (ci_monitor.go:279) always re-reads
   `ListCheckRuns` results passed in from `checkStatus`, fresh, every call.**
   There is no "listen for events, fall back to poll" branch — an
   already-green check-run set is aggregated to `CISuccess` on the very first
   post-restart tick, same as it would be for a freshly-registered PR.

## Conclusion

The specific mechanism GH-4415 hypothesizes (an event-driven watcher that
only reacts to *new* check-run events post-restore, skipping a "current
state" read) **cannot be the cause**, because that architecture does not
exist in this codebase — everything is poll, always current-state, always
uniform. Implementing #4437 ("extract shared current-state polling helper")
and #4438 ("invoke it immediately on restore") against this premise would be
inert: there is nothing to extract (checkStatus is already the single shared
helper) and nothing to "invoke immediately" that isn't already invoked on
the very next tick (≤30s later, per default `CIPollInterval`).

## What's NOT resolved

The canary incident itself (real, evidenced) is not explained by this
investigation — something did cause PRs 87/90/94/98 to sit at `waiting_ci`
for the full 30m post-restart despite green checks. Candidates not yet ruled
out (out of this subtask's scope, flagged for whoever re-scopes the epic):
per-PR circuit breaker state (`isPRCircuitOpen`, controller.go:3969)
restored via `LoadAllPRFailures` blocking `ProcessPR` entirely — though the
observed "CI timeout" log line proves `handleWaitingCI` *did* eventually run
and evaluate the deadline, so a persistently-open circuit isn't a clean fit
either; GitHub primary-rate-limit cooldown (`enterRateLimitCooldown`,
controller.go:4814, up to 20m) suppressing `processAllPRs` entirely across
several ticks; or a canary-specific `ci_checks.exclude`/check-name mismatch
in `matchesExclude` (ci_monitor.go:411) filtering the real checks out of
`filteredRuns`. Recommend re-diagnosing from daemon logs for the 14:22Z
restart window before proceeding with #4437-#4442 as currently scoped.

## How to apply

- Before implementing a fix for a hypothesized bug in an epic's child issue,
  verify the hypothesized *mechanism* actually exists in the code — an epic
  decomposer can generate plausible-sounding hypotheses from symptom
  correlation alone (restart timing + timeout duration) without having read
  the implementation. GH-4415's hypothesis 2 is exactly this: plausible,
  cited as "fits both facts," but wrong about the architecture.
- Related: #4415 (parent epic), #4408/#4384 (prior, unrelated combined-status
  fix this epic's evidence is comparing against), #4437-#4442 (sibling
  subtasks whose premise this finding challenges).
