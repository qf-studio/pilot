---
name: pitfall_task_id_not_globally_unique_across_projects
description: Any DB lookup keyed by bare task_id (no project_path) can be poisoned by a same-numbered task in a different project — audit every new task_id-keyed query for this before shipping.
metadata:
  type: pitfall
---

# Pitfall: task_id is not globally unique — every lookup must be (task_id, project_path)

## Summary

`task_id` (e.g. "GH-10") is only unique **within** a project. Pilot tracks 7+
configured projects in one `executions` table, so a bare `WHERE task_id = ?`
query — or a Go function signature that only takes `taskID` — will match rows
from unrelated projects. A fresh repo's issue numbering restarts at #1, so
this collides immediately with every other project's existing history
(P0 for TASK-405 SaaS onboarding, where every new tenant repo starts at #1).

## Details (GH-4276)

`HasCompletedExecution` and `GetDecomposedChildTaskIDs` were already fixed to
take `(taskID, projectPath)` under TASK-401/GH-4229. Two more unscoped sites
were found on the exact same dispatch/short-circuit path and fixed in GH-4276:

1. **`Store.IsTaskQueued(taskID)`** (`internal/memory/store.go`) — no
   `project_path` filter at all. Sat directly on `Dispatcher.QueueTask`'s
   pre-decompose duplicate check (`dispatcher.go`): a task_id queued/running
   in project A would reject dispatch for the identical task_id in project B
   with `ErrTaskAlreadyActive`, **before the decomposer ever ran**. Also fed
   the SDK poller's retry-grace gate via `storeTaskChecker` (`cmd/pilot/main.go`),
   whose wrapped interface (`sdkcore.TaskChecker.IsTaskQueued(taskID) bool`) is
   externally fixed at one argument — the project scoping has to happen
   inside the wrapper via a stored `projectPath` field, not the interface.
2. **`GetLatestExecutionByTaskID`/`...Excluding`** used to backfill a PR URL
   at 4 call sites in `dispatcher.go`, *after* an already project-scoped
   `HasCompletedExecution`/`decomposedChildrenAllComplete` guard confirmed
   completion — but the backfill query itself ignored project_path, so it
   could silently stamp a different project's PR URL onto this project's row.

`GetDecomposedChildren(taskID)` (store.go) is *also* unscoped by design
(doc comment says so explicitly) but is dead code — no production caller —
so it was left alone; flag it if it ever grows a caller.

## Recommended Approach

Before adding or reviewing any function that queries `executions` /
`execution_events` by task_id:

1. Does the signature take a `projectPath` parameter? If not, ask why not.
2. If the caller only has the result's `.ProjectPath` to check post-hoc
   (can't change the query), verify every call site actually checks it
   before trusting fields like `PRUrl` — don't just check `err == nil`.
3. If the function feeds an external interface with a fixed single-arg
   signature (e.g. an SDK-defined interface), scope it in the *wrapper*
   struct instead (store the projectPath as a field), not in the interface.

## Related

- GH-4276 (this fix), TASK-401 / GH-4229 (`HasCompletedExecution` /
  `GetDecomposedChildTaskIDs` scoping)
- `internal/memory/store.go` (`IsTaskQueued`, `HasCompletedExecution`,
  `GetDecomposedChildTaskIDs`, `GetLatestExecutionByTaskID`)
- `internal/executor/dispatcher.go` (`QueueTask`, `IsActive`,
  `decomposedChildrenAllComplete`, `childCompletionEvidence`)
- `cmd/pilot/main.go` (`storeTaskChecker`), `cmd/pilot/poller_github.go`
- TASK-405 (`saas-roadmap.md`) — S1 fresh-repo onboarding is the concrete
  trigger: every new tenant repo's issue #1 collides with existing rows.

---
**Captured**: 2026-07-13
**Confidence**: 90%
**Concepts**: pilot, memory, executor, dispatcher, multi-project, task-id-scoping, dispatch-guard
