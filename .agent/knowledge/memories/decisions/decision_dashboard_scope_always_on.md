---
name: dashboard project scope is always-on when -p is set
description: No --dashboard-scope flag; gateway HTTP inherits daemon scope (no ?project= param); in-memory callback drift accepted as eventual consistency.
type: decision
---
For TASK-284 (dashboard project scoping), four design questions were resolved on 2026-05-21:

1. **In-memory callback drift accepted as eventual consistency.** `updateTokensMsg`/`addCompletedTaskMsg` paths (`tui.go:1049-1089`) stay global. The 5s `storeRefreshCmd` corrects drift. Cosmetic-only leak. Rejected: per-callback project filter (more code, no real win).

2. **Eval panel: `[global]` label, no schema migration in TASK-284.** `eval_tasks` table has no `project_path` column. The TUI will append `[global]` to the panel title when `defaultProjectPath != ""` so users see the scope mismatch explicitly. Schema migration deferred to **TASK-285**.

3. **Always-on scope when `-p` is set; no opt-out flag.** Rejected: `--dashboard-scope=all` escape hatch. Reasoning: if you want global metrics, run the daemon without `-p`. Add a flag only if real demand surfaces.

4. **Gateway HTTP `/api/dashboard/*` inherits daemon scope automatically.** The same store calls power the HTTP API; pass the daemon's resolved `projectPath` through at `DashboardStore` construction time. No `?project=` query param — the daemon is single-project at startup, so all HTTP consumers (web dashboard, mobile, future API clients) see the same scope as the TUI without per-request plumbing.

**Why these shapes:** the daemon is already single-project when `-p` is set (adapter polling, execution). Making the dashboard match preserves the mental model — "what `-p` selects, the whole UI shows." A per-view scope toggle would invite confusion ("why does the card show numbers for a project I'm not running?"). Eventual consistency on the 5s window is the right trade because the alternative (filtering every callback site) inflates the change surface for no user-visible win.

Related: [[pattern_executions_project_filter]], [[pitfall_dashboard_global_aggregates]].
