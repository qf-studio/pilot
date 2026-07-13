# Pitfall: task_id is only unique within a project, never globally

## Summary
`executions.task_id` (e.g. "GH-10") is only unique per repo/project — any DB lookup keyed on bare `task_id` without an `AND project_path = ?` filter can silently pick up a different project's row and short-circuit dispatch or mask completion evidence.

## Context
GH-4276: a canary sandbox project's fresh epic issue #10 shared its `task_id` ("GH-10") with several other configured projects that already had completed "GH-10" rows. `IsTaskQueued(taskID)` and `GetLatestExecutionByTaskID(taskID)` (no substring-scoped variant) were unscoped by `project_path`, so a stale/in-flight row from an unrelated project could (a) make `IsTaskQueued` see an unrelated project's task as "active" and block dispatch, and (b) make `childCompletionEvidence` pick the wrong project's row (via `created_at DESC` ordering across all projects) and report false-negative "incomplete" for a child that had genuinely shipped in its own project.

## Details
Confirmed already-scoped (no fix needed): `HasCompletedExecution`, `GetDecomposedChildTaskIDs`, `GetExecutionStatusByTaskID(Excluding)`, `SelfHealExecutionAfterMerge`, `ReclassifyCompletionAsFailed` — all take `projectPath` and filter by it in SQL.

Fixed in GH-4276 (previously unscoped):
- `Store.IsTaskQueued(taskID)` → `IsTaskQueued(taskID, projectPath)` — used by `Dispatcher.IsActive`, `Dispatcher.QueueTask`'s duplicate check, and the GitHub poller's `TaskChecker` adapter (`storeTaskChecker` in `cmd/pilot/main.go`, which closes over `projectPath` at construction since the upstream `studio-sdk` `TaskChecker` interface has no room for one).
- `Store.GetLatestExecutionByTaskIDExcluding(taskID, excludeID)` → added `projectPath` param directly (only 2 production callers, both had it in scope).
- Added `Store.GetLatestExecutionByTaskIDForProject(taskID, projectPath)` as the scoped counterpart to the original `GetLatestExecutionByTaskID` — the original stays unscoped on purpose for the `pilot logs <task-id>` CLI diagnostic (a human often doesn't know which project a task belongs to).
- `autopilot.approvalPersister` interface method renamed `GetLatestExecutionByTaskID` → `GetLatestExecutionByTaskIDForProject(taskID, projectPath)`.

## Recommended Approach
Any new DB lookup on the dispatch/epic/short-circuit path that takes a `task_id` must also take a `project_path` and filter on it in the SQL `WHERE` clause — never rely on "most recent" ordering alone to disambiguate across projects. When adding a CLI-only diagnostic lookup that's deliberately cross-project, name it distinctly (or comment it explicitly) so it isn't copy-pasted into a dispatch-path guard.

## Related
- GH-4276, TASK-401 (`.agent/tasks/TASK-401-epic-parent-duplicate-reimplementation.md`), GH-4229
- `internal/memory/store.go`, `internal/executor/dispatcher.go`, `internal/executor/epic.go`, `internal/autopilot/controller.go`

---
**Captured**: 2026-07-13
**Confidence**: 90%
**Concepts**: pilot, executor, memory, dispatcher, multi-project, task_id, epic
