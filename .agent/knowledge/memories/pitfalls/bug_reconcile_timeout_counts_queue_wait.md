# Pitfall: reconcileChildOutcome's 5m ceiling counted child queue-wait, not just execution time

## Summary
`Runner.reconcileChildOutcome` (`internal/executor/epic.go`) polls a child sub-issue's execution row until it reaches a terminal status, bounded by `childOutcomeReconcileTimeout` (default 5m). The deadline was anchored to when the *parent* started polling, not to when the *child* actually started executing. Pilot dispatches one task per project at a time (ProjectWorker, TASK-393 single-lane serialization) — a child epic sub-issue landing behind other queued work on a busy project can legitimately sit `queued` for well over 5 minutes before a worker ever picks it up. That queue-wait ate the same budget meant to detect a genuinely stalled/silent execution, so any epic whose child landed behind other work on a busy project failed structurally regardless of whether the child itself was healthy.

## Context
GH-4413, 2026-07-17. Pointer epic GH-9 (TASK-04) failed at 12:58:36Z: `reconcileChildOutcome: gave up waiting for terminal child execution state` for child GH-26, which was queued behind the busy pointer project worker — not stuck. GH-26 completed fine standalone moments later. 5 timeout instances logged in one day (00:13, 00:59, 01:18, 09:47, 12:58), the last two on binaries that already carried #4388 (the sibling no_op-blindness fix, mem-154 class). This was the residual failure mode behind the #4388 verification gate.

## Details
The old code computed `deadline := time.Now().Add(timeout)` once, before entering the poll loop, and fired that same deadline regardless of whether the child's own execution row was still `queued` (nobody executing it yet) or `running` (actively executing, possibly stuck). Fix: track the deadline lazily, only once the child's execution row is observed `running` — anchored to that row's `started_at` column (GH-4033's "worker actually began running" stamp, set by `UpdateExecutionStatus`'s transition to `running`) rather than to when this call started polling. While `queued`, there is no ceiling at all; a queued row behind a live project worker is legitimately waiting its turn (same reasoning as `recoverStaleQueuedTasks`'s `StaleQueuedThreshold` guard, GH-2331 — that sweep only reaps queued rows with a *dead* claim, never a live one), and a queued row with a dead claim gets reaped to a terminal status by that same sweep, which the reconcile poll picks up on its next terminal-row check. `ctx` cancellation remains the only bound while queued.

Implementation: `findChildExecutionState` (epic.go) is `findTerminalChildExecution`'s sibling — same terminal-row scan, plus reports whether the newest non-terminal row is `running` and its `started_at`. Note `SaveExecution` does NOT persist `started_at` (only `UpdateExecutionStatus`'s transition to `"running"` does, via `CURRENT_TIMESTAMP`), so a row written directly with `Status: "running"` (e.g. the epic loop's own inline sub-issue rows, GH-4141) has a nil `started_at` — callers fall back to treating "now" (first observation) as the effective start in that case.

## Recommended Approach
Any future timeout/deadline guarding a *dispatched* child (something that goes through admission/queueing before it executes) must distinguish "hasn't started" from "is stuck" — anchor the ceiling to the child's own execution-start signal, not to when the watcher began watching. Don't reuse a single wall-clock deadline across a wait that spans both queue-admission and execution phases; they have structurally different failure modes (queue-wait is bounded by sibling task count and worker throughput, execution-hang is bounded by the work itself).

Test note: SQLite's `CURRENT_TIMESTAMP` has whole-second resolution, so tests asserting on a `started_at`-anchored deadline need enough margin (~1.5-2s minimum) over that rounding error to stay deterministic — see `TestExecuteSubIssues_ClaimLost_QueuedBeyondTimeoutStillSucceeds` / `TestExecuteSubIssues_ClaimLost_RunningSilentlyTimesOutFromStartedAt` in `internal/executor/epic_child_reconcile_test.go`.

## Related
- #4388 (fixed the no_op-blindness flavor of this same gate; this was the queue-wait flavor)
- TASK-393 (ProjectWorker single-lane decision — this timeout interacts directly with lane concurrency)
- GH-2331 (`recoverStaleQueuedTasks` / `StaleQueuedThreshold` — established that a queued row behind a live worker must not be treated as stuck)
- GH-4033 (`started_at` stamp on the `running` transition)
- `internal/executor/epic.go` (`reconcileChildOutcome`, `findChildExecutionState`)
