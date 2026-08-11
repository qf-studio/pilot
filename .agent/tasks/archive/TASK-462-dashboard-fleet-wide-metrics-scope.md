# feat(dashboard): fleet-wide metrics scope by default, per-project via dashboard.metrics_scope_path

**Status**: ✅ **MERGED — PR#4830 (2026-08-10). Post-merge review 2026-08-11: notes-only, spec implemented exactly** (provenance clean, wiring verified yaml→config→main.go→every store query, real two-project SQLite wiring test). Hardening notes → [#4832](https://github.com/qf-studio/pilot/issues/4832) (sentinel overload in `SetProjectPath` · uncanonicalized path match → silent zero rows · eval panel ignores scope; unlabeled, dispatch TBD). Canary-flag defect from § Out of scope filed → [#4833](https://github.com/qf-studio/pilot/issues/4833). **ARCHIVED 2026-08-11.**
**Created**: 2026-08-10
**Last Updated**: 2026-08-11
**Type**: feat (dashboard + config wiring)

## Problem

The TUI dashboard's store-backed panels (cost card, 9-way task breakdown,
recent executions, lifetime tokens, sparklines) silently filter every query
to ONE project, while the daemon serves ~10 repos. The Prometheus
`pilot_window_*` gauges are fleet-wide. Result: the dash reads $4.70
cost/shipped (pilot repo only: $1,155.11 / 246 delivered, 30d) while grom
reads $3.68 ($1,879.10 / 510 delivered fleet-wide) — verified against the
box DB 2026-08-10; both correct per their own hidden scope.

Root cause: `metricsScopePath` is a fossil of the single-project era.

- `projectPath` at boot = `--project` flag → config `default_project` → cwd
  (`cmd/pilot/main.go:507-518`, `~` expansion at 516-518).
- Both dashboard-mode construction sites call only
  `model.SetProjectPath(projectPath)` (`cmd/pilot/main.go:997` gateway mode,
  `cmd/pilot/main.go:2617` polling mode), and `SetProjectPath` seeds
  `metricsScopePath` with the same value (`internal/dashboard/tui.go:953-955`).
- `SetMetricsScopePath` (`tui.go:961-963`, added in `6471b6b5` exactly to
  separate git-graph path from metrics scope) has ZERO callers in main.go.
- No config knob exists: `DashboardConfig` has only
  `RefreshInterval`/`ShowLogs`/`StatsWindowDays`
  (`internal/config/config.go:379-387`).

The store layer already treats `""` as fleet-wide in every relevant query —
`GetWindowedStats` (`internal/memory/store.go:3873-3877`),
`GetRecentExecutions` (`store.go:1603`), `GetLifetimeTokens`
(`store.go:3734`) — and the eval-query path only adds a project filter when
non-empty (`tui.go:887-888`). So this is wiring, not query work.

## Spec

### 1. Config field

Add to `DashboardConfig` (`internal/config/config.go:379-387`):

```go
// MetricsScopePath filters the TUI's store-backed metrics panels (cost
// card, task breakdown, recent executions, lifetime tokens, sparklines)
// to one project path. Empty (default) = fleet-wide, matching the
// pilot_window_* Prometheus gauges. The git-graph panel is unaffected —
// it follows the daemon's project path.
MetricsScopePath string `yaml:"metrics_scope_path"`
```

Apply the same `~` expansion `projectPath` gets (`main.go:516-518`) when the
field is read. No default in `DefaultConfig()` — zero value IS the default.

### 2. Wiring (the actual fix)

At BOTH dashboard construction sites — `cmd/pilot/main.go:997` (gateway
mode) and `cmd/pilot/main.go:2617` (polling mode) — after
`model.SetProjectPath(projectPath)`, add:

```go
scope := ""
if cfg.Dashboard != nil {
    scope = cfg.Dashboard.MetricsScopePath // ~-expanded
}
model.SetMetricsScopePath(scope)
```

`SetMetricsScopePath("")` overrides the seed from `SetProjectPath`
(plain setter, `tui.go:961-963`) → fleet-wide becomes the default.
`projectPath` / git-graph behavior unchanged.

### 3. Drop the drifting live recompute of CostPerTask

`tui.go:1280-1282` and `tui.go:1300-1302` recompute
`CostPerTask = TotalCostUSD / TotalTasks` on live events. Denominator is
`AttemptTotal` (all execution rows, any status) — a DIFFERENT metric than
the hydrated value `ws.CostPerDelivered` (window cost / distinct delivered
issues, `store.go:3886`). The detail line oscillates between two
definitions until the every-5th-tick re-query converges it. Delete both
recompute blocks; keep the optimistic `TotalCostUSD` / task-counter bumps.
`CostPerTask` then only ever carries the store's `CostPerDelivered`
definition (stale by ≤5 ticks, same as everything else here).

### 4. Scope label

Wherever a panel title/label renders the scoped project name from
`metricsScopePath` (eval panel title per `SetMetricsScopePath` doc comment,
`tui.go:958-960` — locate all render sites), an empty scope must render as
`all projects`, not an empty string.

### 5. Docs

Document `metrics_scope_path` in `configs/pilot.example.yaml` under
`dashboard:` with the fleet-wide-default + per-project-opt-in semantics.

## Tests

- Table-driven test: `SetProjectPath("/x")` then `SetMetricsScopePath("")`
  → hydrate/store queries receive `""`; `SetMetricsScopePath("/y")` →
  queries receive `/y`; `SetProjectPath` alone → seeds scope (existing
  behavior, `gitgraph_test.go` already covers the split — extend, don't
  duplicate).
- updateTokensMsg / addCompletedTaskMsg no longer mutate `CostPerTask`
  (adapt the existing GH-4735 tests at `tui_test.go:353/425` if they assert
  the recompute).
- Config round-trip: `metrics_scope_path` parses; absent → `""`.

## Acceptance

- Daemon with no `metrics_scope_path` set: TUI cost card total ≈
  `pilot_window_cost_usd` and per-issue detail ≈
  `pilot_window_cost_per_delivered_usd` (same `GetWindowedStats("")`
  population; ≤5-tick staleness aside).
- `metrics_scope_path: /Users/aleks.petrov/Projects/startups/pilot` restores
  today's per-project numbers.
- Git-graph panel unchanged in both cases.
- `make test` + `make lint` green.

## Out of scope

- Prometheus gauges (already fleet-wide; no project label added).
- Runtime scope-cycling key in the TUI (config-only for now).
- Canary-flag gap: `pilot-canary-sandbox` has 480 rows in the 30d window
  with `is_canary=0`, leaking $36.92/37 issues into fleet averages —
  separate defect, file separately.

## Refs

- Pilot issue: https://github.com/qf-studio/pilot/issues/4829
- Scope-split commit: `6471b6b5` (refactor(dashboard): separate git-graph
  project path from metrics scope)
- Windowed stats: GH-4735 / `b36ce7cb`
