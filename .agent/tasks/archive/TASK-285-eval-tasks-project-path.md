# TASK-285: Add `project_path` to `eval_tasks` + scope eval panel

**Status**: ✅ **FULLY SHIPPED 2026-06-10** — backend via PR [#3539](https://github.com/qf-studio/pilot/pull/3539); TUI wiring recovered via [#3552](https://github.com/qf-studio/pilot/issues/3552) → PR [#3561](https://github.com/qf-studio/pilot/pull/3561) (merged 15:20Z, CI green, artifact-verified: `tui.go:2446` passes `ProjectPath: m.defaultProjectPath`). Ready to archive. Incident history below preserved for reference.

Original handoff record:
- ✅ ON MAIN via child #3536 / PR [#3539](https://github.com/qf-studio/pilot/pull/3539) (base=main, artifact-verified): `project_path` column (CREATE + ALTER `store.go:337`), `EvalTaskFilter.ProjectPath` + WHERE clause, `SaveEvalTask`/`ExtractEvalTask` write path, autopilot write site (`controller.go` `ProjectPath: c.projectPath`), CLI `--project` on eval run/list/stats, eval_test coverage.
- ❌ **MISSING**: TUI wiring — `internal/dashboard/tui.go:2446` still `ListEvalTasks(EvalTaskFilter{Limit: 200})`, no `m.defaultProjectPath`. Child #3537 was **falsely superseded** (closed "parent already shipped" — it hadn't; child had no PR so the #3527 open-PR veto couldn't fire).
- 🚀 Recovery dispatched: standalone issue [#3552](https://github.com/qf-studio/pilot/issues/3552) (fresh issue, no `Parent:` line — reopening #3537 would be re-superseded on next poll since parent is closed+`pilot-done`). Verify per mem-033 on pilot-done: `tui.go` `ListEvalTasks` call passes `ProjectPath`, base==main.
- Incident details → TASK-361 live-verification section; residual machinery holes → TASK-364.
**Priority**: P3
**Estimated Effort**: M (3-5 person-hours)
**Risk Level**: Medium (schema migration on a populated table)

## Context

Follow-up to [TASK-284](TASK-284-dashboard-project-scope.md). When TASK-284 lands, the TUI eval panel (`internal/dashboard/tui.go:2358`) will display `[global]` in its title because `eval_tasks` has no `project_path` column to filter on. This task removes that gap.

## Problem

- `eval_tasks` schema (`internal/memory/eval.go:237-244`) has no `project_path` column
- `EvalTaskFilter` (same file) only exposes `Repo`, `ExecutionID`, `SuccessOnly`, `FailedOnly` — no project field
- Dashboard cannot filter eval/bench rows by project, even after TASK-284

## Scope

1. **Schema migration** — add `project_path TEXT DEFAULT ''` column to `eval_tasks`. Use the existing migration framework in `internal/memory/migrations/` (check the latest migration number; conventional file naming).
2. **Backfill strategy** — existing rows get `''`; new rows populated from the execution context that creates the eval (find write site via `grep -rn "INSERT INTO eval_tasks"` or the `SaveEvalTask`-style helper).
3. **Filter wiring** — add `ProjectPath string` to `EvalTaskFilter`; `WHERE project_path = ?` when non-empty. Index on `project_path` if row count justifies.
4. **TUI** — remove the `[global]` label added in TASK-284 step 6; pass `m.defaultProjectPath` into `ListEvalTasks(EvalTaskFilter{ProjectPath: ..., Limit: 200})`.
5. **CLI** — `pilot eval` subcommands (check `cmd/pilot/eval*.go`) may want a `--project` flag for parity with `pilot metrics --projects`.

## Open Questions

- Does `eval` mode (bench / regression) attach to a single project, or do eval runs span multiple? Affects whether `project_path` should be `NOT NULL` for new rows.
- Are there existing eval rows with no execution context to backfill from? If yes, accept `''` for the historical set.
- Is the eval panel useful at all when scoped, or should it be hidden entirely under `-p`? (TASK-284 step 6 keeps it visible with a label — this task confirms or revisits that.)

## Out of Scope

- Eval workflow / runner changes — this is read-side only
- Per-project eval scheduling
- Renaming `eval_tasks` table or restructuring eval mode

## Dependencies

- Blocked by TASK-284 (the `[global]` label and the `m.defaultProjectPath` plumbing must exist first).

## Key Files

- `internal/memory/eval.go` — schema, `EvalTaskFilter`, `ListEvalTasks`, write site
- `internal/memory/migrations/` — add new migration file
- `internal/dashboard/tui.go` — `:2358` (`ListEvalTasks` call), eval panel title rendering
- `cmd/pilot/eval*.go` — CLI `--project` flag wiring (if in scope)
