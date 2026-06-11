# TASK-284: Scope TUI Dashboard to a Single Project

**Status**: ✅ SHIPPED to main 2026-06-10 — PR [#3523](https://github.com/qf-studio/pilot/pull/3523) squash-merged (hand-merge, daemon down, artifact-verified: store sigs 3/3 + `runDashboardMode(projectPath)` on main). Subset PR #3519 closed superseded. Release pending: next daemon release (≥v2.182.0) carries it and supersedes phantom v2.181.0. Incident history → TASK-361. Unblocks TASK-285.
**Priority**: P2
**Estimated Effort**: M (6-7.5 person-hours)
**Risk Level**: Low

## Problem

When the user runs `pilot start -p <path> --dashboard ...`, only the execution side (which project's adapters poll, which issues run) and the git-graph panel are scoped to `<path>`. The dashboard's metrics cards — recent executions, lifetime tokens/cost, lifetime task counts, 7-day sparklines, in-flight task list — aggregate across **all** projects in the SQLite store. Users running pilot in a multi-project setup cannot see per-project numbers without dropping into `pilot metrics summary --projects <path>`.

Evidence (re-verified against `main` 2026-06-09 — line anchors refreshed):
- `internal/dashboard/tui.go:713` — `SetProjectPath(projectPath)` sets `m.projectPath` + `m.defaultProjectPath` (field at :438); `defaultProjectPath` is currently read only at :783 by the git-graph path resolver.
- `internal/dashboard/tui.go:580,588,600` (`hydrateFromStore`) and `:845,872,882` (`storeRefreshCmd`) — store calls have no project filter; `loadMetricsHistory` (:658) builds a `MetricsQuery` without `Projects` (query at :667).
- `cmd/pilot/commands.go:2281` — `collectTasks()` returns all `TaskState`s; `TaskState.ProjectPath` is populated (`internal/executor/monitor.go:37`) but unused as a filter. ⚠️ `projectPath` is **not** in scope inside `runDashboardMode` (:2259) — see Step 4.

## Approach (one paragraph)

All SQLite-backed panels (`GetRecentExecutions`, `GetLifetimeTokens`, `GetLifetimeTaskCounts`) take an optional `projectPath string` parameter, following the existing `MetricsQuery.Projects []string` pattern at `internal/memory/metrics.go:24-28`. When `Model.defaultProjectPath` is non-empty the TUI threads it through; when empty (global mode) callers pass `""` and queries stay unfiltered — zero change for existing non-TUI callers. Live in-flight tasks are filtered in `collectTasks()` in `cmd/pilot/commands.go` rather than at the monitor or store layer, because `TaskState.ProjectPath` is already populated and no DB is involved. The in-memory token/task increment paths (`updateTokensMsg`, `addCompletedTaskMsg` at `tui.go:1049-1089`) are left global; the 5-second `storeRefreshCmd` overwrites the card from DB every cycle, so cross-project leak is cosmetic and bounded to <5s.

## API Changes

Three store method signatures change. All additive: empty `projectPath` = existing behavior.

| Method | Old | New | File:line |
|---|---|---|---|
| GetRecentExecutions | `(limit int)` | `(limit int, projectPath string)` | `internal/memory/store.go:714` |
| GetLifetimeTokens | `()` | `(projectPath string)` | `internal/memory/store.go:1776` |
| GetLifetimeTaskCounts | `()` | `(projectPath string)` | `internal/memory/store.go:1818` |

`GetDailyMetrics(MetricsQuery)` already accepts `Projects []string` (`metrics.go:27-31`, branches at :309/:376/:441/:489/:533) — wire-up only at `tui.go:667`.

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
  - `hydrateFromStore` (:561-651): pass `m.defaultProjectPath` to the three queries at :580 / :588 / :600
  - `storeRefreshCmd` (:841-~903): thread `projectPath` as a new parameter (it already takes `store *memory.Store`); queries at :845 / :872 / :882
  - `loadMetricsHistory` (:658-691): set `MetricsQuery.Projects` to `[]string{m.defaultProjectPath}` when non-empty (query built at :667)

### Step 4 — Live task filter (S, 0.5-1h)
- `cmd/pilot/commands.go` `collectTasks` (:2281, inside `runDashboardMode` at :2259): after gathering `allStates` from `gwMonitor.GetAll()` + `p.GetTaskStates()`, filter to states where `s.ProjectPath == projectPath` when `projectPath != ""`.
- ⚠️ **`projectPath` is NOT in scope inside `runDashboardMode`** (this corrects the original plan). It lives in the `RunE` closure in `cmd/pilot/main.go` (:116) and is resolved to an absolute path before `runDashboardMode` is invoked (`main.go:1090`). **Add `projectPath string` as a new parameter to `runDashboardMode`** and pass it at the call site, then thread it into `collectTasks` and into the Step 3 `SetProjectPath`/store calls. Simpler than reading it back off the `gwProgram` model.

### Step 5 — Gateway HTTP API scoping (S, 1h) [OQ-4]
- `internal/gateway/dashboard.go` — store the daemon's resolved `projectPath` on the `DashboardStore` struct at construction (plumb from `commands.go` where the dashboard service is wired up). Note: the `DashboardStore` **interface** (:20-28) declares 7 methods; only the 3 being changed need signature edits — the other 4 (`GetDailyMetrics`, `GetQueuedTasks`, `GetActiveExecutions`, `GetRecentLogs`) are untouched.
- Use it as the `projectPath` argument to `GetLifetimeTokens` (:99), `GetLifetimeTaskCounts` (:104), `GetRecentExecutions` (:165, :219) instead of `""`. (Call sites verified unchanged 2026-06-09.)
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
| 4 — collectTasks filter (+ `runDashboardMode` param) | S (0.5-1h) |
| 5 — Gateway HTTP API scoping | S (1h) |
| 6 — Eval panel `[global]` label | S (0.5h) |
| **Total** | **M (6-7.5h)** |

**Risk note**: Low. All changes are additive (empty string = existing behavior). The `DashboardStore` interface change is compile-time enforced — missed implementors fail to build before tests run.

## Key Files for Executor

_Anchors re-verified against `main` 2026-06-09. Store/TUI line numbers drift fast (store.go has grown ~140 lines since 2026-05-21) — confirm with `grep -n` before editing._

- `internal/memory/store.go` — :714, :1776, :1818
- `internal/memory/store_test.go` — add tests (line anchors stale; grep for the sibling tests)
- `internal/dashboard/tui.go` — `hydrateFromStore` :561-651, `loadMetricsHistory` :658-691, `storeRefreshCmd` :841-~903; `SetProjectPath` :713, `defaultProjectPath` field :438
- `cmd/pilot/commands.go` — `collectTasks` :2281 inside `runDashboardMode` :2259 (add `projectPath` param — see Step 4); call site `cmd/pilot/main.go:1090`, `projectPath` declared `main.go:116`
- `internal/executor/monitor.go:37` — `TaskState.ProjectPath` (already populated)
- `internal/gateway/dashboard.go` — interface :20-28 (7 methods), call sites :99, :104, :165, :219
- `internal/gateway/dashboard_test.go` — :24, :28, :36
- `desktop/app.go` — :74, :79, :131, :186
- `internal/adapters/telegram/commands.go:418`
- `internal/comms/commands.go:382`
