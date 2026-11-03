# TASK-630: Dashboard shows epic sub-issues as "(no project)" with the id as title — register children with the Monitor

**Status**: ✅ MERGED 2026-11-02 19:46Z — [pilot#6014](https://github.com/qf-studio/pilot/issues/6014) → PR#6015; ships in v2.309.0 (cutting 11-03). Done check: the next epic on the box shows its children under the project on the grid.
**Created**: 2026-11-02
**Assignee**: Pilot

---

## Context

**Problem**: on the hosted daemon's grid dashboard (2026-11-02 18:53Z) the running epic children GH-288, GH-470 and GH-471 sat under a "(no project)" group with their task id as the title, while their parents (#469, ui#287) sat under the right project. The `executions` rows on the box carry `task_title` and `project_path` for all three, so the data is right and the Monitor state is wrong.

**Cause** (navigator-research seam map, 2026-11-02):
- `internal/executor/epic.go:2897` ExecuteSubIssues builds `subTask{ID, Title, ProjectPath: subTaskRepoPath}` and `:2991` `subLifecycle.Begin` writes the executions row, then `:3065` `r.executeWithOptions(ctx, subTask, true)` (also `:2337` on the takeover path). Nothing registers the child with the Monitor.
- `internal/executor/runner.go:3941` `r.monitor.Start(task.ID)` → `internal/executor/monitor.go:376` Start is a silent no-op for an unknown id.
- The first progress callback hits `internal/executor/monitor.go:395` UpdateProgress, which creates `TaskState{ID: taskID, Title: taskID, Status: Running}` with no `ProjectPath`/`ProjectName`.
- `cmd/pilot/commands.go:2941` convertTaskStatesToDisplay copies Title/ProjectPath/ProjectName straight through; `internal/dashboard/queue_groups.go:59` falls back to "(no project)".
- The dispatcher pattern to mirror: `cmd/pilot/handler_common.go:261` `Monitor.Register(taskID, title, info.URL)` followed by `Monitor.SetProjectInfo(taskID, projectPath, filepath.Base(projectPath))` (GH-2167). `SetProjectInfo` (`monitor.go:354`) is a no-op for an unregistered id.
- Same gap in the in-memory decomposition path: `internal/executor/runner_decompose.go:133` `RegisterSubtask(subtask.ID, subtask.Title, "", parentTask.ID)` with no `SetProjectInfo`; `:122` sets `subtask.ProjectPath = executionPath`, which may be a worktree path — use `parentTask.ProjectPath` for the display name.
- `HydrateFromStore` (`monitor.go:162`) repairs titles and project info from the store at startup only.

**Goal**: every child that the daemon runs in-process shows its title and project on the dashboard from its first progress event.

---

## Known pitfalls

- `Monitor.Register` overwrites the map entry unconditionally. A child may already be registered (poller pickup of a labelled sub-issue, or a retry). The fix registers only when the id is unknown and always sets project info.
- `TestMonitorUpdateProgressCreatesUnknownTask` (`internal/executor/monitor_test.go:278`) pins the Title == ID fallback for a truly unknown id. Keep that behaviour; the fix makes the epic children known.

---

## Acceptance

- [ ] An epic child executed by ExecuteSubIssues appears in `Monitor.GetAll()` with its sub-issue title, issue URL, `ProjectPath` = the child's repo path and `ProjectName` = its base name, before the first progress event.
- [ ] An in-memory decomposition child (runner_decompose path) carries `ProjectPath`/`ProjectName` of the parent's repo path.
- [ ] A child id that is already registered keeps its state; only project info is (re)set.
- [ ] The grid groups both kinds of children under their project; "(no project)" is no longer produced for them.

---

## Implementation (one PR)

1. `internal/executor/epic.go` ExecuteSubIssues (before `subLifecycle.Begin` at `:2991`) and the takeover path at `:2337`: when `r.monitor != nil`, if the id is unknown call `RegisterSubtask(taskID, issue.Subtask.Title, issue.URL, parent.ID)`, then `SetProjectInfo(taskID, subTaskRepoPath, filepath.Base(subTaskRepoPath))`. Add a small `Monitor.Has(taskID) bool` (or an idempotent `EnsureSubtask`) rather than reading the map from epic.go.
2. `internal/executor/runner_decompose.go:133`: after `RegisterSubtask`, call `SetProjectInfo(subtask.ID, parentTask.ProjectPath, filepath.Base(parentTask.ProjectPath))`.
3. Tests: `internal/executor/monitor_test.go` — a registered child keeps title and project through `Start` and `UpdateProgress`; an idempotent re-register does not reset status. An ExecuteSubIssues test with a fake monitor asserts the registration happens before the backend runs. `internal/dashboard/queue_groups_test.go:220` gains a case with a registered child under its project header.

---

## Out of scope

Changing the "(no project)" fallback itself, re-hydration from the store at runtime, the orchestrator path.

---

## Verify

```bash
go test -race ./internal/executor/ -run TestMonitor
go test -race ./internal/executor/ -run TestExecuteSubIssues
go test -race ./internal/dashboard/ -run TestRenderTasks
```

---

## Done

- [ ] PR merged; the next epic on the box shows its children under the project on the grid (screenshot in the task doc).

---

## Refs

- Issue → https://github.com/qf-studio/pilot/issues/6014
- Observed on the box grid 2026-11-02 18:53Z (GH-288, GH-470, GH-471 from TASK-585 legs C4 and U10)
- TASK-579 (archived) — queue grouped by project, the "(no project)" fallback spec
- GH-2167 (project info on Monitor states), GH-5354 (RegisterSubtask parent liveness)

---

**Last Updated**: 2026-11-02
