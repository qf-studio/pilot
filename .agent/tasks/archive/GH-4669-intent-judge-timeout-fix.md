# GH-4669

**Created:** 2026-08-03

## Problem

GitHub Issue GH-4669: fix(executor): intent judge 100% dead since 07-16 cutover — 4,321 context_deadline kills, fail-open hid it; diagnose, restore-or-retire, and alert on judge failure streaks

## Context

Found 2026-08-03 while diagnosing the GH-4648 incident: **the pre-flight intent judge has failed on every single invocation since the 2026-07-16 AWS cutover** — a 17-day, 100% failure rate hidden by fail-open.

Box evidence (daemon.log, single unrotated file since 07-16):

- `grep -c 'pre-flight judge error' daemon.log` → **4321**
- First: `2026-07-16T23:56:11Z ... repo=qf-studio/pointer issue=8 error="intent judge subprocess: signal: killed (cause=context_deadline peak_rss_mb=252)"`
- Latest: `2026-08-03T10:30:28Z ... repo=qf-studio/pilot issue=4664 ... (cause=context_deadline peak_rss_mb=262)`
- Zero observed judge-success lines over the same window.
- Signature is uniform: killed at context_deadline, peak RSS ~250–270MB, every repo, every poll cycle.

Consequences: (1) the judge's protection has been silently absent for 17 days — every issue dispatches unjudged; (2) each poll cycle burns a doomed subprocess spawn+kill (~4.3k so far) — pure CPU/API churn on the box.

Root cause is NOT yet established — that diagnosis is part of this issue. Candidate causes to check, in order: the judge subprocess's deadline is too short for the box's model-call latency (t3.xlarge + shared load; laptop-era constant?); env/auth for the child model call on the box (which credentials does the judge child inherit — note the box moved to `~/.claude/.credentials.json`-based auth at cutover); model routing changes; the subprocess deadline not accounting for cold starts. The 07-16 cutover date being the exact onset strongly suggests a box-environment factor, not a code regression.

## Acceptance

1. **Diagnose**: instrument or reproduce on the box (SSM) to identify why every judge subprocess exceeds its deadline. Record the finding in the PR description with evidence.
2. **Restore or explicitly retire**: either (a) the judge completes and produces verdicts on the box — verified by log lines from real dispatches after deploy; or (b) if restoring is impractical, add a config flag to disable the pre-flight judge cleanly (no subprocess spawn at all when disabled), default reflecting the decision, and log ONE startup-level WARN instead of 300+/day poll-cycle warnings.
3. **Fail-open becomes observable**: a counter metric (e.g. `pilot_intent_judge_failures_total`) exposed on :9091 and an alert-engine rule so a >N-consecutive-failures streak pages instead of hiding for 17 days. This part is mandatory regardless of (a)/(b).
4. Regression test: judge subprocess timeout path increments the metric and fails open exactly once per dispatch (no retry storm).
5. `make build`, `make test`, `make lint` green.

## Implementation

Judge invocation lives in the github-sdk-poller pre-flight path (component `github-sdk-poller`, "pre-flight judge error (fail-open)") with the subprocess spawn in `internal/executor` intent-judge code. Deadline constants and child env construction are the first places to look. Metrics: mirror existing pilot_* gauge/counter registration.

Out of scope: heartbeat-kill of main executions (separate issue #4668), judge prompt/quality changes.

## Resolution (2026-08-03)

**Root cause**: too-short deadlines, not contention/env/auth/routing/binary resolution.

Ruled out on the live box (this session runs on `i-0e0c1ca34e7b561f9`, the production Pilot daemon host):
- CPU contention: 8 concurrent trivial `claude` calls all completed in 6-10s.
- Env/model-routing leakage: read the daemon's real `/proc/<pid>/environ` — neither `ANTHROPIC_MODEL` nor `ANTHROPIC_BASE_URL` is set.
- Shorter parent context deadline: traced the SDK poller's `Start(ctx)` chain back to the daemon's long-lived root context — no shorter deadline anywhere in the chain.
- Stale/wrong `claude` binary: only one `claude` resolves on PATH (`/usr/bin/claude`, v2.1.104).

Confirmed via direct reproduction: realistic-size prompts (5.8KB and ~27KB, matching real issue bodies/diffs) reliably take **18.7-22.5s** with the real `claude` CLI subprocess on this box, vs. 5-9s for trivial prompts. The old `preflightTimeout` (20s) and `judgeTimeout` (30s) left near-zero margin over that baseline, so any real (non-trivial) issue reliably blew the deadline — a self-inflicted 100% failure rate, not an outage.

**Fix**: raised both deadlines to 60s (config-overridable via `intent_judge.timeout` / `pre_flight_judge.timeout`), plus made the fail-open path observable per acceptance criterion 3:
- `internal/executor/intent_judge.go`: `NewIntentJudge` defaults `judgeTimeout`/`preflightTimeout` to 60s each; added `SetJudgeTimeout`/`SetPreflightTimeout` setters.
- `internal/executor/backend.go`: `IntentJudgeConfig.Timeout` / `PreFlightJudgeConfig.Timeout` (string, `time.ParseDuration`), default `"60s"`.
- `internal/executor/runner.go`: wires `config.IntentJudge.Timeout` into the post-hoc judge.
- `cmd/pilot/poller_github.go`: wires `pre_flight_judge.timeout` into the pre-flight judge; `sdkPreFlightJudge` is now a pointer receiver with its own consecutive-failure counter, firing `alerts.EventTypeIntentJudgeFailureStreak` exactly once when the streak hits `judgeFailureStreakAlertThreshold` (10), resetting on success.
- `internal/alerts/{types,engine}.go`: new `AlertTypeIntentJudgeFailureStreak` / `EventTypeIntentJudgeFailureStreak`, default rule (severity Critical), `handleIntentJudgeFailureStreak`.
- Existing `pilot_intent_judge_failures_total` (GH-4377) is unchanged — it already exists; this issue adds the alert on top of it.

Went with acceptance option (a) "restore" rather than (b) "retire a disable flag", since the root cause was directly fixable.

Regression tests added: `internal/executor/intent_judge_test.go` (`TestNewIntentJudge_Defaults` updated to 60s/60s, `TestIntentJudge_SetTimeouts`), `internal/executor/runner_test.go` (`TestNewRunnerWithConfig_IntentJudgeTimeout`/`_IntentJudgeDefaultTimeout`), `cmd/pilot/poller_github_test.go` (`TestSdkPreFlightJudge_JudgeIssue_TimeoutIncrementsMetricOnce` — real subprocess timeout, single metric increment; `TestSdkPreFlightJudge_FiresStreakAlertExactlyOnceAtThreshold` — fires once, no retry storm past threshold; `TestSdkPreFlightJudge_SuccessResetsStreak`), `internal/alerts/{engine_test,types_test}.go` (`TestHandleIntentJudgeFailureStreak`, default-rules count).

`make build`, `go test ./...`, `gofmt -l`, `go vet ./...` all green. `golangci-lint` is not installed on this box (`make lint` no-ops); no lint-only changes were made that would need it beyond what `go vet`/`gofmt` already cover.

## Refs

- Incident record: `.agent/tasks/TASK-437-duplicate-execution-race-prevention.md` (found during its diagnosis)
- Context: TASK-409 / S6-lite cutover 2026-07-16 (onset date), #4391 (box rate-pool pressure), GH-4401 (subprocess limits history — RLIMIT disabled since 07-17, so not the cause window)

## Acceptance Criteria

