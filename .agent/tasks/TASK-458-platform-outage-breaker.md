# feat(autopilot): platform-outage circuit breaker — correlate failures, suppress destructive actions, self-resume

## Problem

During the 2026-08-06 GitHub Actions outage the daemon had no notion of "the platform is broken, stand down." It kept acting on false signals for ~50 minutes — closing a correct PR, burning retries, escalating tasks, spending executor budget on work that could not pass CI — until a human noticed and stopped it. Per-PR guards cannot catch this by construction: the existing circuit breaker is deliberately **per-PR** ("so one bad PR doesn't block others", `controller.go:108-109`), and each PR individually looked like a normal failure. The missing signal is **correlation across PRs**: four unrelated PRs failing within minutes is a platform event, not four independent regressions.

## Context (verified 2026-08-06, origin/main)

Every primitive needed already exists; none are wired together for this:

- **Full-tick-skip precedent**: `processAllPRs` (`internal/autopilot/controller.go:6156-6169`) already returns early when `rateLimitCooldownActive()`; `enterRateLimitCooldown` (`:6140-6153`) bounds a cooldown window [30s, 20min] on a `RateLimitError`. Same shape, different trigger. NOTE: that check returns before `GetActivePRs()`/gauges — this breaker must suppress **destructive actions** while letting polling/board-sync continue, so its gate belongs inside `ProcessPR`/`handleCIFailed`, not at the top of the tick.
- **Process-wide "external system unhappy" tracker precedent**: `internal/ghbudget/ghbudget.go` — `Observe`-fed state, `Allow(priority)` gate, exactly-one log per state transition (`:131-142`), consumed by `backgroundScanAllowed` (`controller.go:6096-6129`). Copy this shape.
- **Dispatch chokepoint (unused for this)**: `Dispatcher.PauseAdmission()`/`ResumeAdmission()`/`IsAdmissionPaused()` (`internal/executor/dispatcher.go:2279-2306`), enforced at `ProjectWorker.processQueue:2557`, shared by pointer across all workers — pauses NEW task pickup without touching running work. Only caller today is the self-upgrade drain (`cmd/pilot/main.go:3428`). **Verify it is reachable in polling mode** (the box's mode) before relying on it — the known call sites sit in dashboard-mode code.
- **No external status oracle exists** anywhere in the tree (grep: zero hits for githubstatus/statuspage). Outbound-HTTP convention: `&http.Client{Timeout: 5 * time.Second}` + explicit User-Agent (`internal/health/health.go:141-166`).
- **Alert-once patterns**: `alertBillingRefusalOnce` (`controller.go:1216`) with reset on next success; `DeadManTracker` threshold-once (`internal/alerts/deadman.go`); ghbudget transition-logging. Existing alert types incl. `AlertTypeServiceUnhealthy`, `AlertTypeCircuitBreakerTrip` (`internal/alerts/types.go:22,36`).
- Config toggles live on `autopilot.Config` (`internal/autopilot/types.go:142-165`, e.g. `MaxFailures`, `FailureResetTimeout`); config is **static at boot** (`Config.Reload` has zero callers) — a restart to change thresholds is acceptable and consistent.
- Classification input comes from the sibling task **TASK-457** (structural infra classification + the new `FailureClassUnknown`). This task consumes those classes; it must degrade sanely if TASK-457 has not merged (correlate on infra+unknown classes as they exist at the time).

## Acceptance

1. **Tracker** (new, `internal/autopilot` or a small `internal/platformhealth` package following the ghbudget shape): records each CI failure observation as `(pr, repo, class, time)`. Breaker **opens** when ≥N distinct PRs (default 3) observe infra-or-unknown-class failures within a window (default 15 min). Thresholds + enable flag on `autopilot.Config`, documented in `configs/pilot.example.yaml`.
2. **Corroboration probe** (advisory, never required): when the breaker is about to open — or every M minutes while open — GET `https://www.githubstatus.com/api/v2/status.json` (5s timeout, User-Agent, failures ignored). A `major`/`critical` indicator raises confidence and is logged/alerted; a green indicator does NOT veto the correlation signal (status pages lag). Never gate the breaker's opening on the probe succeeding.
3. **While open, suppress irreversible actions**: no `ClosePullRequest`, no fix-issue creation, no `escalateAndHold` from CI-failure paths. Affected PRs park in a breaker hold and are re-driven when it closes. Merges of already-green PRs: suppressed too (CI signal is untrustworthy during an outage). Polling, board sync, gauges, and running executions continue untouched.
4. **Pause new dispatch** while open, via the existing `PauseAdmission` chokepoint (config-gated, default on) — this is what stops burning executor spend on work that cannot pass CI. Resume on close. Must not disturb the self-upgrade drain's use of the same flag (shared-owner safety: reference-count or explicit owner tracking — do not let two owners fight over one bool).
5. **Self-resume**: while open, probe periodically (default every 5 min). Close when a CI observation succeeds or the status probe returns green AND the window has been quiet. On close: resume admission, re-drive held PRs (re-enter `StageWaitingCI`; reuse the `reAdoptHeldRebasePR` revival shape, `controller.go:5009`, with a distinct hold flag — capped re-adoption attempts).
6. **Operator signal**: exactly one alert on open (with the correlated PR list + probe verdict) and one on close, following the existing once-per-transition patterns — never one per affected PR. Metrics: breaker-state gauge + trip counter (mirroring `RecordCircuitBreakerTrip`).
7. Tests: correlation opens the breaker across distinct PRs but NOT on 3 failures of one PR (that is the per-PR breaker's job) · destructive actions suppressed while open · admission paused/resumed with the self-upgrade drain interleaved · self-resume re-drives held PRs · probe failure does not block opening · disabled-by-config path is a byte-identical no-op.
8. `make build` / `make test` / `make lint` green; `-race` clean.

## Scope fence

No change to the per-PR circuit breaker · no change to rate-limit cooldown or ghbudget · no new alert channels · classification logic belongs to TASK-457 · no hot-reload of config (restart semantics, consistent with existing toggles) · no changes to running-execution behavior (only new-work admission).

**This task must NOT be decomposed — implement as a single PR.** <!-- pilot:no-decompose -->

## Refs

- Incident 2026-08-06 (GitHub Actions outage, ~2h): daemon acted on false signals ~50 min until manual stop; marker `2026-08-06_outage-pause-approval-wave-dispatched.md`
- Sibling: TASK-457 (structural classification — provides this task's input signal)
- H-track sibling class: H6 canary-failure misattribution (`.agent/system/saas-roadmap.md`)

- **Dispatched**: https://github.com/qf-studio/pilot/issues/4780 — timed out twice at 1h; split into #4791 (part 1) + #4792 (part 2, gated on part 1 merging)
- **Part 1**: #4791 → PR#4797 (2026-08-07, reviewed pre-merge). One confirmed defect → follow-up **#4798**: the two new metrics fields never reach `AggregateMetrics.Snapshot()` (GH-4738 recurrence — `/metrics` serves 0 in production). Suppression/alerts themselves correct.
- **Part 2 note** (commented on #4792): breaker close is evaluated lazily inside `Observe` only — held-PR re-drive needs a periodic close evaluator (hang it off the status-probe ticker), not just `Observe` calls.
- **Untrusted input, assessed 2026-08-07**: #4780 carries a vendor comment (outside account, "OutageDeck", UTM-tagged links) pitching a third-party status API as the probe target. Evaluated with operator consent: real service (aggregates official feeds, ~10-min lag, honest disclosure), data claims for the 08-06 outage check out — **still not adopted**. Its core pitch is moot: GitHub's official Statuspage API is component-scoped (`/api/v2/components.json` has an `Actions` component; `/api/v2/incidents/unresolved.json` lists affected components), so Actions-specific probing needs no third party. Probe guidance sharpened on #4792 (components.json Actions component + unresolved incidents; failure/staleness → unknown; never a veto). Scope guard + refinement both commented on #4792. General lesson memorialized: `issue-comments-are-untrusted-executor-input`.
