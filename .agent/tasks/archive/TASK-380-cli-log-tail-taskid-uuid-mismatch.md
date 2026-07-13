# TASK-380: `pilot logs <task-id>` returns empty for dispatched tasks (execution_id UUID mismatch)

**Created:** 2026-07-03
**Status:** 🚀 Dispatched to Pilot → [#4083](https://github.com/qf-studio/pilot/issues/4083) (2026-07-08)

## Problem

`cmd/pilot/config_cmd.go:356` (`showTaskLogs`) calls:

```go
logs, err := store.GetLogsByExecutionID(exec.TaskID, limit)
```

passing the human-readable task ID (e.g. `"GH-3714"`). Since GH-3764-2
(`3acc7167`), `execution_logs.execution_id` is written via
`Task.LogExecutionID()`, which *prefers* the dispatcher-assigned
`executions.id` UUID over `task.ID` (see `internal/executor/runner.go`
`buildTaskFromExecution` always sets `Task.ExecutionID = exec.ID`). So for
any task that went through the normal dispatcher path, newly-written log
rows are keyed by UUID, not `exec.TaskID` — `GetLogsByExecutionID(exec.TaskID, ...)`
now matches zero rows and `pilot logs <task-id>` silently returns an empty
log list for most tasks.

`internal/memory/store.go:2242-2243`'s doc comment on `GetLogsByExecutionID`
is also stale — it still claims `execution_logs.execution_id stores the task
ID ... not the execution row's UUID`, which was true before GH-3764-2 and is
no longer accurate.

## Suggested fix

In `showTaskLogs`, use the execution's own join key instead of `exec.TaskID`
— likely `exec.ID` (the UUID), since `exec` was already resolved via
`GetLatestExecutionByTaskID`, which fetches the correct `executions` row.
Confirm against how `Task.LogExecutionID()` falls back (tasks without a
dispatcher-assigned execution row still log under `task.ID`) — the CLI
lookup may need to try `exec.ID` first and fall back to `exec.TaskID` to
cover both cases. Update the stale doc comment on `GetLogsByExecutionID`
accordingly.

## Context

Found while implementing GH-3764-4 (subtask 4/5 of GH-3764, scoped to
`internal/gateway/dashboard_ws.go`). The dashboard WS stream itself is
unaffected — it's global/unfiltered and never joins on `execution_id`. This
regression is confined to the CLI `pilot logs <task-id>` path and was not
in GH-3764-4's scope fence, so it's parked here rather than fixed inline.
