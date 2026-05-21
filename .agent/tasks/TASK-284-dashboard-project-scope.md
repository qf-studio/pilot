# TASK-284: Scope TUI Dashboard to a Single Project

**Status**: ready (decisions resolved, awaiting handoff to Pilot)
**Priority**: P2
**Estimated Effort**: M (5.5-7 person-hours)
**Risk Level**: Low

## Problem

When the user runs `pilot start -p <path> --dashboard ...`, only the execution side (which project's adapters poll, which issues run) and the git-graph panel are scoped to `<path>`. The dashboard's metrics cards — recent executions, lifetime tokens/cost, lifetime task counts, 7-day sparklines, in-flight task list — aggregate across **all** projects in the SQLite store. Users running pilot in a multi-project setup cannot see per-project numbers without dropping into `pilot metrics summary --projects <path>`.

Evidence (from research, navigator-research agent):
- `internal/dashboard/tui.go:592` — `model.SetProjectPath(projectPath)` is wired but only used by the git-graph panel.
- `internal/dashboard/tui.go:575,583,595,652,834,860,870` — store calls have no project filter.
- `cmd/pilot/commands.go:2269` — `collectTasks()` returns all `TaskState`s; `TaskState.ProjectPath` is populated but unused as a filter.

## Approach (one paragraph)

All SQLite-backed panels (`GetRecentExecutions`, `GetLifetimeTokens`, `GetLifetimeTaskCounts`) take an optional `projectPath string` parameter, following the existing `MetricsQuery.Projects []string` pattern at `internal/memory/metrics.go:24-28`. When `Model.defaultProjectPath` is non-empty the TUI threads it through; when empty (global mode) callers pass `""` and queries stay unfiltered — zero change for existing non-TUI callers. Live in-flight tasks are filtered in `collectTasks()` in `cmd/pilot/commands.go` rather than at the monitor or store layer, because `TaskState.ProjectPath` is already populated and no DB is involved. The in-memory token/task increment paths (`updateTokensMsg`, `addCompletedTaskMsg` at `tui.go:1049-1089`) are left global; the 5-second `storeRefreshCmd` overwrites the card from DB every cycle, so cross-project leak is cosmetic and bounded to <5s.

## API Changes

Three store method signatures change. All additive: empty `projectPath` = existing behavior.

| Method | Old | New | File:line |
|---|---|---|---|
| GetRecentExecutions | `(limit int)` | `(limit int, projectPath string)` | `internal/memory/store.go:626` |
| GetLifetimeTokens | `()` | `(projectPath string)` | `internal/memory/store.go:1649` |
| GetLifetimeTaskCounts | `()` | `(projectPath string)` | `internal/memory/store.go:1677` |

`GetDailyMetrics(MetricsQuery)` already accepts `Projects []string` — wire-up only at `tui.go:652`.

Callers outside the TUI that need signature updates (all pass `""`, no behavior change):
- `internal/gateway/dashboard.go` — `DashboardStore` interface + sites `:99, :104, :165, :219`
- `internal/gateway/dashboard_test.go` — mock signatures `:24, :28, :36`
- `internal/adapters/telegram/commands.go:418`
- `internal/comms/commands.go:382`
- `desktop/app.go:74, :79, :131, :186`

## Implementation Steps

### Step 1 — Store layer + tests (M, 2-3h)
- `internal/memory/store.go` — extend three methods, append `AND project_path = ?` when non-empty
- `internal/memory/store_test.go` — add `TestGetRecentExecutions_ProjectFilter`, `TestGetLifetimeTokens_ProjectFilter`, `TestGetLifetimeTaskCounts_ProjectFilter` with multi-project fixture; extend `TestGetLifetimeTokens_ExcludesZeroTokenRows` to verify zero-token exclusion still applies under a filter

### Step 2 — Non-TUI caller updates (S, 0.5h)
- Update `DashboardStore` interface + four call sites in `internal/gateway/dashboard.go`
- Update mock signatures in `internal/gateway/dashboard_test.go`
- Pass `""` from `telegram/commands.go`, `comms/commands.go`, `desktop/app.go`

### Step 3 — TUI wire-up (S, 1h)
- `internal/dashboard/tui.go`:
  - `hydrateFromStore` (~555-634): pass `m.defaultProjectPath` to all three lifetime queries
  - `storeRefreshCmd` (~830-886): thread `projectPath` as a new parameter (it already takes `store *memory.Store`)
  - `loadMetricsHistory` (~647-680): set `MetricsQuery.Projects` to `[]string{m.defaultProjectPath}` when non-empty

### Step 4 — Live task filter (S, 0.5h)
- `cmd/pilot/commands.go` (~2269-2276): after gathering `allStates` from `gwMonitor.GetAll()` + `p.GetTaskStates()`, filter to states where `s.ProjectPath == projectPath` when `projectPath != ""`. `projectPath` already in scope.

### Step 5 — Gateway HTTP API scoping (S, 1h) [OQ-4]
- `internal/gateway/dashboard.go` — store the daemon's resolved `projectPath` on the `DashboardStore` struct at construction (plumb from `commands.go` where the dashboard service is wired up).
- Use it as the `projectPath` argument to `GetRecentExecutions`, `GetLifetimeTokens`, `GetLifetimeTaskCounts` at `:99, :104, :165, :219` instead of `""`.
- `internal/gateway/dashboard_test.go` — add one test asserting endpoints scope when constructed with a non-empty `projectPath`.
- No URL changes; no `?project=` query param. The daemon is single-project at startup, so all HTTP consumers inherit the same scope as the TUI.

### Step 6 — Eval panel `[global]` label (S, 0.5h) [OQ-2]
- `internal/dashboard/tui.go` — wherever the eval panel header is rendered (search for `ListEvalTasks` callers around `:2358`), append ` [global]` to the panel title when `m.defaultProjectPath != ""`.
- No backend changes. Schema migration tracked separately as TASK-285.

## Decisions (resolved 2026-05-21)

1. **OQ-1 — In-memory callback drift**: **Accept eventual consistency.** No callback filtering. The 5s `storeRefreshCmd` overwrites the card and corrects any cross-project leak. Cosmetic only.
2. **OQ-2 — Eval panel**: **Label the panel `[global]` when `defaultProjectPath != ""`.** No schema work in this task. Eval-task project scoping tracked as follow-up [TASK-285] (schema migration adding `project_path` to `eval_tasks` + filter wiring).
3. **OQ-3 — Opt-out flag**: **Always-on when `-p` is set.** No new CLI flag. If demand surfaces, add `--dashboard-scope=all` later.
4. **OQ-4 — Gateway HTTP `/api/dashboard/*`**: **In scope.** Endpoints hit the same store methods — pass the daemon's resolved project path through so JSON consumers (web dashboard, mobile, future API clients) see the same scoping as the TUI. No `?project=` query param; the daemon is already scoped at startup.

## Test Strategy

**Unit (must add):** see Step 1 store tests.
**Integration:** existing `TestModel*` tests in `internal/dashboard/` cover the dashboard paths; updating `DashboardStore` mock signatures in `internal/gateway/dashboard_test.go` is enforced by compilation.
**Manual smoke:** run `pilot start -p /path/to/A --dashboard` with a second project B receiving issues in another terminal. Confirm metrics card and task list show only A. Restart without `-p`; confirm global totals are unchanged.

## Out of Scope

- Multi-project dashboard with a runtime project picker UI
- Schema migration adding `project_path` to `eval_tasks` — tracked as **TASK-285** (follow-up)
- Per-project daily session rows (`sessions` table stays global)
- `desktop/app.go` receiving a project filter (Electron app, separate surface)
- `--dashboard-scope=all` opt-out flag (defer until requested)

## Effort Roll-Up

| Step | Effort |
|---|---|
| 1 — Store layer + tests | M (2-3h) |
| 2 — Non-TUI caller updates | S (0.5h) |
| 3 — TUI wire-up | S (1h) |
| 4 — collectTasks filter | S (0.5h) |
| 5 — Gateway HTTP API scoping | S (1h) |
| 6 — Eval panel `[global]` label | S (0.5h) |
| **Total** | **M (5.5-7h)** |

**Risk note**: Low. All changes are additive (empty string = existing behavior). The `DashboardStore` interface change is compile-time enforced — missed implementors fail to build before tests run.

## Key Files for Executor

- `internal/memory/store.go` — :626, :1649, :1677
- `internal/memory/store_test.go` — add tests after :78, :991, :1065
- `internal/dashboard/tui.go` — :555-634, :647-680, :830-886
- `cmd/pilot/commands.go` — :2269-2276
- `internal/gateway/dashboard.go` — interface :20-27, call sites :99, :104, :165, :219
- `internal/gateway/dashboard_test.go` — :24, :28, :36
- `desktop/app.go` — :74, :79, :131, :186
- `internal/adapters/telegram/commands.go:418`
- `internal/comms/commands.go:382`
