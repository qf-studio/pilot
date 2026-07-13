# fix(metrics): wire Prometheus export/hydration/alerting/persistence across ALL autopilot controllers — not just the default (TASK-390)

**Status**: 🚀 Dispatched to Pilot → [#4068](https://github.com/qf-studio/pilot/issues/4068)
**Last Updated**: 2026-07-08
**Created**: 2026-07-08
**Priority**: HIGH — every PR-family metric on `:9093` reads zero fleet-wide
**Assignee**: Pilot

## Context

Every autopilot `Controller` constructs its **own** `*autopilot.Metrics` (`internal/autopilot/controller.go:357`). `runPollingMode` builds one controller per project (`cmd/pilot/main.go:1760-1769`) plus the backward-compat default (`autopilotController`, `main.go:1650`). Live recording is correctly per-repo — merges/failures/active-PR gauges land on the matching controller's `Metrics` (`main.go:2439-2461`).

But the export surface is wired to **only the default controller**:

- `gwServer.SetMetricsSource(autopilotController.Metrics())` — `main.go:1945`
- `HydrateFromStore(..., autopilotController.Metrics())` — `main.go:1954` (and `:1169`)
- `MetricsAlerter` — `main.go:2678-2681`
- `MetricsPersister` — `main.go:2684-2687`

Result: PR activity recorded on any projects-map controller never reaches `/metrics`. **Live repro on our own deployment:** `qf-studio/pilot` appears in BOTH `adapters.github.repo` (default) and `projects[]` — the projects-loop controller does the real work while `:9093` exports the idle default one. Observed 2026-07-08: `pilot_prs_merged_total`, `_failed_total`, `_conflicting_total`, `pilot_active_prs*`, `pilot_pr_time_to_merge_seconds_*` ALL zero across a session with 1,723 successful executions and a full day of autopilot merges (v2.233.x).

**This exact bug class was already fixed once** for the alerts engine — GH-3954, `main.go:2713-2721`: *"wire the alerts engine into every controller, not just the default one."* The Prometheus exporter, hydrator, alerter, and persister never got the same fix.

## Fix

1. Introduce a small aggregating `MetricsSource` (the `PrometheusExporter` currently takes exactly one source): it iterates the default controller + every entry in `autopilotControllers` and sums/merges their `Snapshot()`s (counters sum; `active_prs` gauges sum per stage; time-to-merge histograms merge observations).
2. Wire `gwServer.SetMetricsSource`, `MetricsAlerter`, and `MetricsPersister` to the aggregate (mirror the GH-3954 pattern at `main.go:2713-2721`).
3. `HydrateFromStore`: hydrate ONCE into a designated baseline (the aggregate view must not double-count if hydration lands on the default controller while per-repo controllers also hydrate) — pick one owner for the lifetime baseline and document it.
4. Keep per-controller `Metrics` objects intact — per-repo scoping of live recording is correct and must not regress (see `main.go:2439-2461`).

## Acceptance Criteria

- [ ] Unit test: two controllers, PR merged on the non-default one → aggregate snapshot (and thus `/metrics`) shows `pilot_prs_merged_total ≥ 1`.
- [ ] Unit test: `pilot_active_prs{stage}` sums across controllers each tick.
- [ ] Hydration lands exactly once in the aggregate (no double-count with per-controller baselines).
- [ ] `MetricsAlerter`/`MetricsPersister` consume the aggregate; alert metadata `success_rate`/`total_active_prs` (`metrics_alerter.go:181-182`) reflect all projects.
- [ ] No change to per-repo live recording paths.
- [ ] Full short suite + lint green.

## Constraints

- Single PR. Do not decompose.
- Do NOT add PR-family hydration here — that is #4029's re-scoped job, explicitly sequenced AFTER this fix (hydrating before this fix papers over the silo with a plausible non-zero number on the wrong object).

## Refs

- Sibling: TASK-391 (`RecordPRConflicting` dead call site), TASK-392 (`success_rate` semantics), #4029 (re-scoped: PR-family hydration from `execution_events`, blocked by this task's issue)
- Prior art: GH-3954 (`main.go:2713-2721`), GH-4041/PR #4043 (baseline hydration, deliberately skipped PR family)
- Consumers currently lied to: `deploy/grafana/grafterm-pilot.json` "Active PRs" widget; grot dashboards scraping `:9093`
- Pilot issue: https://github.com/qf-studio/pilot/issues/4068
