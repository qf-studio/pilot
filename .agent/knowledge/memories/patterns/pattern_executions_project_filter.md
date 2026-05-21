---
name: project-scoping pattern for executions-backed queries
description: Reuse MetricsQuery.Projects []string with `AND project_path IN (?)`; do not invent new filter shapes for the executions table.
type: pattern
---
For any new query against the `executions` table that needs per-project scoping, **reuse the existing `MetricsQuery.Projects []string` idiom** from `internal/memory/metrics.go:24-28`. Do not invent a different filter shape.

**Why:** The pattern is already wired into six store methods — `GetMetricsSummary`, `GetDailyMetrics`, `GetProjectMetrics`, `GetFailureReasons`, `GetPeakUsageHours`, `ExportMetrics` (all in `internal/memory/metrics.go:232-535`) — plus `BriefQuery.Projects` (`store.go:814`) for the same table. The CLI surface (`pilot metrics summary --projects`, `pilot usage projects`) speaks the same vocabulary. The pattern extractor (`memory/extractor.go:761,792`) uses it to compute per-project confidence. The SQL is uniform: `WHERE ... AND project_path IN (?, ?, ...)` only when `len(Projects) > 0`. Empty slice = global (existing behavior).

**How to apply:**
- For methods that already take a query struct (like `MetricsQuery`/`BriefQuery`), add the filter as a field on the struct and use `len(...)>0` to conditionally append the IN-clause.
- For parameter-less methods (`GetLifetimeTokens`, `GetLifetimeTaskCounts` at `store.go:1649,1677`), prefer an explicit `projectPath string` parameter over variadic — the second arg is always meaningful and `""` reads as "no filter".
- The `executions` table has `project_path` column and `idx_executions_project` index — filtering is index-covered, no performance concern.
- Do not add filtering to tables that lack the column: `sessions` (daily session totals), `eval_tasks` (eval/bench), `usage_events` (uses `project_id`, different semantics). Those need schema changes first.

**Anti-pattern:** new variadic args, new functional options, new query structs. Use what's there.

Related: [[pitfall_dashboard_global_aggregates]], [[decision_dashboard_scope_always_on]].
