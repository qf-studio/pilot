# fix(metrics): pilot_success_rate semantics — exclude rate_limited, stop collapsing non-failures into failed, add issue-level success metric (TASK-392)

**Status**: 🚀 Dispatched to Pilot → [#4070](https://github.com/qf-studio/pilot/issues/4070)
**Last Updated**: 2026-07-08
**Created**: 2026-07-08
**Priority**: MEDIUM — dashboards show red (64.2%) for a system with ~100% eventual delivery
**Assignee**: Pilot

## Context

Observed live 2026-07-08 (Prometheus `:9093`): `pilot_success_rate` = 0.6419… while issue-level eventual success is ~100% (`pilot_failed_queue_depth` = 0, nothing stuck). `pilot_issues_processed_total`: success=1723, failed=927, rate_limited=34. Two stacking defects:

1. **Per-attempt semantics.** `Metrics.Snapshot()` (`internal/autopilot/metrics.go:343-350`) computes `success / Σ(all IssuesProcessed)`. Every retried attempt is its own `executions` row (~1.55 attempts per shipped issue), so retries permanently inflate the denominator. `rate_limited` attempts also count against the rate despite not being a quality signal.
2. **Hydration mis-collapses non-failures into "failed".** `metrics_hydrator.go:52-54` computes `failed := Total - Succeeded - RateLimited`, lumping `declined/no_op/skipped/stalled/infra` into "failed" — contradicting the dispatcher's own taxonomy (`internal/executor/dispatcher.go:855-857`: *"declined / no-op / stalled get their own status so the dashboard's failed count reflects genuine failures"*) and `LifetimeTaskCounts.NonFailure()` (`internal/memory/store.go:2101-2105`).

Blast radius: `deploy/grafana/grafterm-pilot.json` "Success Rate" gauge (70% red threshold — currently red); grot dashboards; alert metadata `success_rate` (`metrics_alerter.go:181-182`). Any "cost per issue" derived from `pilot_issues_processed_total` is really cost-per-attempt ($0.56 vs $0.88 per shipped issue at time of check).

## Fix

1. **Denominator**: exclude `rate_limited` from `SuccessRate` in `Snapshot()` (would read 65.0% on the observed data). Decide + document whether `declined/no_op/skipped/stalled/infra` belong in the denominator at all (recommendation: exclude — mirror the dispatcher's "genuine failures" taxonomy).
2. **Hydrator**: stop collapsing non-failure statuses into `failed` (`metrics_hydrator.go:52-54`). `GetLifetimeTaskCounts` (`store.go:2110-2136`) needs per-status counts (or reuse `LifetimeTaskCounts.NonFailure()`), so hydrated `IssuesProcessed` mirrors live recording keys.
3. **New issue-level metric**: add a gauge pair for unique-issue outcomes, e.g. `pilot_issues_shipped_total` / `pilot_issues_attempted_total`, backed by a new store query with task_id dedupe:
   `SELECT COUNT(DISTINCT task_id) FROM executions WHERE status='completed'` vs `COUNT(DISTINCT task_id) FROM executions`. Expose issue-level success rate alongside (not replacing) the per-attempt rate; per-attempt is still useful as an efficiency signal — name/label them so dashboards can't confuse the two.

## Acceptance Criteria

- [ ] Unit test: retried-then-shipped task (2 failed rows + 1 completed row, same task_id) → issue-level success 100%, per-attempt rate unchanged semantics but with `rate_limited` excluded.
- [ ] Hydrated baselines keep `declined/no_op/skipped/stalled/infra` out of `failed`.
- [ ] New store query covered by a table-driven test (dedupe across attempts, mixed statuses).
- [ ] `deploy/grafana/grafterm-pilot.json` Success Rate widget repointed to the issue-level metric (or thresholds annotated) — small JSON edit, include in the same PR.
- [ ] Full short suite + lint green.

## Constraints

- Single PR. Do not decompose.
- Should land on the aggregated multi-controller view from TASK-390 (soft ordering — not code-blocked, but verify against the aggregate if TASK-390 merged first).

## Refs

- Siblings: TASK-390 (export silo — blocker for visibility), TASK-391 (`RecordPRConflicting`), #4029 (PR-family hydration re-scope)
- Prior art: GH-4041/PR #4043 (introduced the hydrator), TASK-379 (`execution_events` ledger)
- Pilot issue: https://github.com/qf-studio/pilot/issues/4070
