# Throughput histograms record zero on the live path + zero on every restart — fix GH-4130 observation and hydrate from ledger

**Status**: ✅ SHIPPED — #4211 closed `pilot-done`; hydration LIVE-VERIFIED 2026-07-13 (93/104/31 samples identical across daemon restart)
**Type**: bug (metrics truthfulness — TASK-393 M0 instrumentation is false-green)
**Related**: GH-4128 (histogram registration), GH-4130 (observation), #4093 (counter hydration precedent), mem-088/mem-093 (verify behavior, not presence)

## Problem

The TASK-393 Phase-1 throughput histograms — `pilot_time_to_pr_seconds`,
`pilot_queue_wait_seconds`, `pilot_approval_wait_seconds` — **have never recorded
a single live sample**. All three read `_count 0` on `:9091` while the older,
ledger-hydrated counters are populated (`pilot_prs_merged_total 1064`,
`pilot_pr_time_to_merge_seconds_count 65`). The entire throughput program's
before/after measurement is blind.

### Live repro (2026-07-12, evidence — do not skip)

Daemon restarted 10:34 UTC. A complete live cycle ran AFTER the restart:

- GH-4206: `running` event 10:39:05 → `pr_created` 10:44:55 (PR #4207) →
  `merged` 10:53:39 (all present in `execution_events`)
- The `executions` row for GH-4206 **has `started_at` set** (10:39:05) —
  the exact precondition the observation guard requires
- `pilot_execution_duration_seconds_count` ticked to 1 (some metrics DO fire
  post-restart)
- `pilot_time_to_pr_seconds_count` and `pilot_queue_wait_seconds_count`
  remained **0**

So every externally-observable precondition of the GH-4130 guard was satisfied
and no sample landed.

### Defect D1 — live-path observation never fires

The observation lives in `autopilot.Controller.OnPRCreated`
(`internal/autopilot/controller.go:876-886`, origin/main):

```go
if c.memoryStore != nil {
    taskID := fmt.Sprintf("GH-%d", issueNumber)
    if issueNumber == 0 { taskID = fmt.Sprintf("PR-%d", prNumber) }
    if exec, err := c.memoryStore.GetLatestExecutionByTaskID(taskID); err == nil && exec.StartedAt != nil {
        c.metrics.RecordTimeToPR(time.Since(*exec.StartedAt))
        c.metrics.RecordQueueWaitDuration(exec.StartedAt.Sub(exec.CreatedAt))
    }
}
```

The export chain past this point is verified intact: `RecordTimeToPR` appends to
`Metrics.TimeToPRDurations` (`metrics.go:377-383`), `AggregateMetrics` merges
across controllers (`metrics_aggregate.go:165`), gateway exports
(`internal/gateway/prometheus.go:277-296`). The failure is therefore at or
before the guard. Investigate in this order (each is a plausible silent eater):

1. **Is `Controller.OnPRCreated` invoked at all for executor-created PRs?**
   Live wiring: SDK poller deps hook `poller_github.go:305-309`
   (`pollerDeps.OnPRCreated = func(prEv sdkcore.PRCreatedEvent) { ... ctrl.OnPRCreated(...) }`)
   and sub-issue path `main.go:2324` (`runner.SetOnSubIssuePRCreated`). Determine
   whether the SDK `PRCreatedEvent` actually fires when the *executor finalization*
   creates the PR via the registered SDK PRCreator, or only on poller-side creates.
   Note the ledger shows TWO `pr_created` events 170ms apart for the repro
   execution — identify both writers; if neither is `Controller.OnPRCreated`'s
   caller, that's the gap.
2. **`GetLatestExecutionByTaskID` failure/scoping** — check its error path and
   any `project_path` scoping; a scan error on the `started_at` column or a
   scoped query miss silently skips (the `err == nil` guard swallows it). Add a
   debug/warn log on the skip path regardless of root cause (fail-loud policy,
   TASK-379).
3. **`issueNumber == 0` fallback** — if the live event carries no issue number,
   taskID becomes `PR-N`, which never matches a `GH-N` execution row → lookup
   misses silently.

Fix the actual root cause found. **Regression test must go through the live
entry point** (SDK `PRCreatedEvent` → poller-deps hook → controller), NOT by
calling `OnPRCreated` directly — the existing `gh4130_test.go` calls the method
directly, which is exactly why this shipped false-green.

### Defect D2 — histograms zero on every restart (no hydration)

#4093 hydrates the *counters* from `execution_events` at startup
(`internal/autopilot/metrics_hydrator.go`) — that's why `pilot_prs_merged_total`
survives restarts. The three Phase-1 histograms were never added. The pilot repo
self-upgrades (restarts) on the **daily** release train, so even a fixed D1
yields a view that wipes every day.

Extend the hydrator: reconstruct `TimeToPRDurations` (running→pr_created per
execution), `QueueWaitDurations` (created_at→started_at from `executions`), and
`ApprovalWaitDurations` (awaiting_approval→approved/merged events) from the
ledger at startup, respecting the existing `maxSamples` cap (keep the most
recent N). Timestamp format note: `execution_events.occurred_at` is stored as a
Go time string with a ` +0000 UTC` suffix — parse accordingly (naive
`julianday()`-style parsing fails on it).

## Acceptance criteria

- [ ] After ONE live PR cycle (issue picked up → PR created), `curl :9091/metrics`
  shows `pilot_time_to_pr_seconds_count` ≥ 1 and `pilot_queue_wait_seconds_count` ≥ 1.
- [ ] After a daemon restart, the three histogram counts do NOT drop to 0
  (hydrated from ledger; values plausible vs pre-restart).
- [ ] Regression test exercises the live entry point (SDK `PRCreatedEvent` →
  hook → controller → metrics), asserting a recorded sample. Direct-method tests
  may stay but do not count as the fence.
- [ ] The observation skip path logs a warning with the reason (fail-loud) —
  no more silent eating.
- [ ] `pilot_approval_wait_seconds` observation site verified live the same way
  (it shares the false-green risk) or explicitly documented if its trigger differs.
- [ ] No new GitHub API calls in poll/heal loops (mem-048); hydration reads
  SQLite only.

## Verify

```bash
go build ./... && go vet ./...
go test -race ./internal/autopilot/... ./internal/gateway/... ./internal/memory/...
# live: restart daemon → counts survive; next PR cycle increments counts.
```

## Out of scope

- The throughput accelerators themselves (TASK-393 M4–M7 lanes/concurrency/primer/trust-tier).
- Dashboard render changes.
- The M3 baseline-week process decision (unblocked by this fix, run separately).

## Refs (origin/main, forensics-verified 2026-07-12)

- Observation: `internal/autopilot/controller.go:876-886` · recorder `internal/autopilot/metrics.go:93,123,377-383`
- Live wiring: `cmd/pilot/poller_github.go:305-309,384` · `cmd/pilot/main.go:2324`
- Export (intact): `internal/autopilot/metrics_aggregate.go:165` · `internal/gateway/prometheus.go:277-296`
- Hydrator to extend: `internal/autopilot/metrics_hydrator.go` (#4093 precedent)
- False-green test: `internal/autopilot/gh4130_test.go` (calls method directly)
- Repro data: `executions` row GH-4206 (`started_at` 2026-07-12 10:39:05), PR #4207, `execution_events` 10:39–10:53 UTC
