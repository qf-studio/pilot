> **RESOLVED/SUPERSEDED (2026-07-05):** TASK-284 shipped dashboard scoping (tui.go populates Projects)

---
name: TUI dashboard aggregates leak across projects
description: -p flag scopes execution + git graph only; metrics cards, sparklines, recent executions, in-flight tasks all query/refresh globally.
type: pitfall
---
`pilot start -p <path>` is **not** end-to-end project scoping. Today it scopes:
- Adapter polling (only that project's issues get queued)
- Execution path (runner uses that working directory)
- Git-graph panel (only place `model.SetProjectPath(projectPath)` at `cmd/pilot/main.go:592` is consumed)

It does **not** scope any of the dashboard's data panels:
- `GetRecentExecutions(20)` — `internal/dashboard/tui.go:575,834` — no `WHERE project_path`
- `GetLifetimeTokens()` — `tui.go:583,860` — sums all projects
- `GetLifetimeTaskCounts()` — `tui.go:595,870` — counts all projects
- `GetDailyMetrics(MetricsQuery{...})` — `tui.go:652` — `Projects` field present but never populated
- `collectTasks()` in `cmd/pilot/commands.go:2269` — returns all in-flight `TaskState`s; `TaskState.ProjectPath` is set (`runner.go:1542`) but unused as a filter

**Plus an in-memory drift trap:** `metricsCard` totals are incremented in real time on every `updateTokensMsg` / `addCompletedTaskMsg` (`tui.go:1049-1089`) from callbacks that fire for every task on the daemon, regardless of project. Even after the store methods are project-filtered, the live increments will mix projects between DB refreshes. The `storeRefreshCmd` overwrites the card every 5s and corrects drift — so the leak is bounded and cosmetic, but real.

**How to apply:** when adding any new dashboard panel that reads from `executions` (or pushes through a runner/monitor callback), default to project-scoped reads using the `MetricsQuery.Projects` pattern, and accept the 5s callback drift unless the panel is precision-critical. Don't trust the existing 7-day sparklines or lifetime cards to reflect "this project" when running in multi-project mode — they don't, by design until TASK-284 lands.

Related: [[pattern_executions_project_filter]], [[decision_dashboard_scope_always_on]].
