# Pitfall: no_op is a legitimate terminal outcome but was invisible to every "is this task done" dispatch guard

## Summary
GH-4347: the canary sandbox's `duplicate-pr` assertion failed 3 scheduled runs in a row (#4265) — ledger showed GH-82 (a decomposed epic sub-issue) dispatched 6x in one cycle, all six runs terminating `no_op`. Root cause was NOT a tight concurrency race (rows were minutes apart, matching poll-interval + subprocess runtime) — it was that `no_op` ("nothing to change" — a legitimate, common epic sub-issue outcome) never satisfied `Store.HasCompletedExecution` (requires `status='completed'` + a commit/PR deliverable), so every re-dispatch guard that consulted it stayed blind forever: the SDK poller's own pre-dispatch `ExecutionChecker.HasCompletedExecution` check (poll time) and `dispatcher.go`'s `hasTerminalSuccessLedger` pickup guard (dispatch time) both re-treated the same no_op'd issue as a fresh candidate on every tick.

## Context
`internal/executor/dispatcher.go`'s `childCompletionEvidence` already encoded the correct domain knowledge ("no_op with no error is a legitimate completion") for the TASK-401 decomposed-parent guard, but that definition was never propagated to the two admission-time guards above, which both called `Store.HasCompletedExecution` directly. A comment on `hasTerminalSuccessLedger` even claimed it and the poller's `ExecutionChecker` were "the same guard" — true when both wrapped `internal/adapters/github/poller.go` (removed in the M7 4d.6 SDK-poller cutover, GH-4171), but the invariant silently broke when the poller migrated to `sdkcore.ExecutionChecker` backed directly by `*memory.Store`.

## Details
Fix: `Store.HasTerminalCompletion(taskID, projectPath)` (new, `internal/memory/store.go`) — an ANY-row check (not "latest row only", see below) OR'ing the existing deliverable-completion definition with a no_op-with-no-error clause. `executor.HasTerminalCompletion` wraps it; `dispatcher.go`'s `hasTerminalSuccessLedger` and a new `cmd/pilot` adapter `terminalCompletionChecker` (wired at `poller_github.go`'s `pollerDeps.ExecutionChecker`) both delegate to it, so poll-time and pickup-time guards agree.

**Trap discovered while testing the fix:** `childCompletionEvidence`'s own no_op fallback checks only `GetLatestExecutionByTaskID`'s *most recent* row — correct for its call site (a decomposed child's one prior attempt) but WRONG for a general "should this admission be refused" check, because the scenario being guarded against is exactly "a fresh `queued` duplicate row already exists alongside the earlier no_op row" — the fresh row sorts as "latest" and hides the terminal no_op. `Store.HasTerminalCompletion` scans every row for the task_id instead of trusting recency ordering.

Separately hardened `Dispatcher.QueueTask`'s check-then-act (IsTaskQueued SELECT, then insert) with a `dispatchMu sync.Mutex` — a genuine but narrower TOCTOU race, not what produced this incident's 6x, but explicitly requested by the task spec and cheap to close.

`Store.HasCompletedExecution` itself was deliberately left untouched — TASK-359 established its strict "has a deliverable" contract is load-bearing elsewhere (`TestTaskCompletionInvariant`); broadening it directly was already refuted history (see `learn_task359_layer1_shipped.md`).

## Recommended Approach
When adding a new "is task X done" admission/re-arm gate, use `executor.HasTerminalCompletion` (or `Store.HasTerminalCompletion`), not `Store.HasCompletedExecution` directly, unless you specifically need the strict "has a deliverable" definition (e.g. release/PR-surfacing logic). When a comment claims two guards "share a definition", verify both call sites still call the SAME function — a re-wiring (like the SDK poller cutover) can silently split them without either call site's own tests catching it, since each side still compiles and passes in isolation.

## Related
- Tasks: TASK-401 (decomposed-parent cross-task-id guard), TASK-404 (ExecutionLifecycle), TASK-359 (HasCompletedExecution invariant)
- Files: internal/executor/dispatcher.go, internal/memory/store.go, cmd/pilot/main.go, cmd/pilot/poller_github.go
- Issue: GH-4347
