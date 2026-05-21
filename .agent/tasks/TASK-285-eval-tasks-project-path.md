# TASK-285: Add `project_path` to `eval_tasks` + scope eval panel

**Status**: stub (blocked by TASK-284)
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
