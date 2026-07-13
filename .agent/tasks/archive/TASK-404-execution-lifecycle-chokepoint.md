# TASK-404: Execution lifecycle chokepoint — one API for create/transition, kill the FK-787 class permanently

**Status**: ✅ SHIPPED overnight 2026-07-12→13 — B1 #4243→PR #4248 · B2 #4244→PR #4257, both merged. Delivery notes: #4243's runs were orphan-reaped twice ("no live process") across daemon restarts then healed to completed post-merge; issue closed carrying stale `pilot-retry-ready` (removed manually 2026-07-13). #4244 produced a superseded duplicate PR #4254/#4255 (closed) before #4257 merged. Review merged PRs; live verification of the chokepoint rides the next deployed build.
**Type**: refactor (invariant enforcement — replace guard-at-call-site with make-invalid-states-unrepresentable)
**Context**: The July defect family (TASK-394 epic path, #4205 CLI path, false dashboard status TASK-399, guard extended to 4 sites in #4229) shares one root cause: execution-row creation and status transitions are hand-rolled at every call site. When a path forgets a step, work becomes invisible (metrics/status/dashboard) and `execution_events` inserts FK-fail (787).

## Current state (research, 2026-07-12)

- **Create sites (3 production)**: `dispatcher.go:547` (decomposed parent), `dispatcher.go:605` (queued single — the only one threading `exec.ID → task.ExecutionID` via `buildTaskFromExecution` at `dispatcher.go:1105`), `epic.go:1988` (sub-issue, TASK-394's fix). Plus one-shot `SaveDeclinedExecution` (`cmd/pilot/main.go:3397`).
- **Transition sites**: `MarkExecutionCompleted` ×4, `UpdateExecutionStatus` ×6, `DeleteExecution` ×2, `SelfHealExecutionAfterMerge` ×1 — scattered across dispatcher.go/epic.go/controller.go.
- **Ledger wrappers ×4** (Runner/Dispatcher/ProjectWorker/Controller `recordExecutionEvent`), each duplicating nil-store/skip guards; only Controller's (`controller.go:719`) validates row existence first. Runner's uses `task.LogExecutionID()` (`runner.go:436`) which falls back to bare `task.ID` — the FK-787 generator.
- **Status vocabulary is free text** — terminal set exists only as an `if` chain (`store.go:1371`) + `TerminalStatus` classifier (`runner.go:232-256`). No Go enum, no DB constraint.
- **Template to generalize**: `finalizeSubIssueExecution` (`runner.go:1809-1845`) — TASK-394's mini-chokepoint (create-before-run + single finalize branching completed/other-terminal + metrics).
- **Suspected dead APIs**: `UpdateExecutionStatusByTaskID` (zero production callers), `cancelled` status (terminal-listed, no write site) — confirm and remove or document during B1.

## Plan (2 Pilot issues)

- **B1 — Lifecycle API + migrate all paths** (blocked by #4205, same-file `cmd/pilot/commands.go`): introduce `ExecutionLifecycle` (thin type over `memory.Store`): `Begin(task, initialStatus) → execID` (creates row, threads `task.ExecutionID`), `Finish(execID, result)` (wraps `TerminalStatus` + `MarkExecutionCompleted`/`UpdateExecutionStatus` + `SaveExecutionMetrics`), `Transition(execID, status)` for queued→running. Add Go const set for the status vocabulary. Migrate: both dispatcher create sites, epic sub-issue path, and the CLI path (#4205 will have wired it ad-hoc — fold into the chokepoint). Add the missing CLI-path regression test (none exists today — `cmd/pilot/trace_test.go` seeds rows directly).
- **B2 — Standardize ledger wrappers** (blocked by B1): collapse the 4 `recordExecutionEvent` wrappers onto the Controller's validate-first pattern (skip+warn when no row) so a missing row can never FK-fail again, only fail-loud log.

## Must NOT change

- `HasCompletedExecution` (`store.go:828`) — mem-027: tightening it broke direct-commit rows and `TestTaskCompletionInvariant`.
- DB schema (no CHECK constraint migration in this task — Go-level enum only; schema constraint is a possible follow-up once vocabulary is confirmed stable).
- Status vocabulary semantics — same strings, same classifier precedence (`TestTerminalStatus` stays green).
- Dashboard/adapter read paths (they consume `exec.Status` strings).

## Refs

- Create/transition/wrapper inventory: research pass 2026-07-12 (this doc §Current state, all file:line refs verified on `origin/main` @ `7920826e`)
- Prior art: TASK-394 (`.agent/tasks/TASK-394-epic-subissue-execution-ledger.md`) — FK-787 mechanism + mem-026/027 constraints
- CLI gap being folded in: #4205
- Invariant test to keep green: `internal/executor/task_completion_invariant_test.go`
