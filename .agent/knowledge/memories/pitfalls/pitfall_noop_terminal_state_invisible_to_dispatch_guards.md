# Pitfall: no_op is terminal but invisible to "is this task done" dispatch guards

## Summary
`no_op` is a legitimate terminal outcome (common for epic sub-issues) but was invisible to every "is this task done" dispatch guard — `Store.HasCompletedExecution` requires `status='completed'` plus a deliverable, so the SDK poller's `ExecutionChecker` and dispatcher.go's `hasTerminalSuccessLedger` both re-treated no_op'd issues as fresh candidates forever (GH-4347: GH-82 dispatched 6x in one cycle).

## Context
RCA of GH-4347 duplicate-dispatch storm on the canary sandbox, 2026-07-15.

## Details
The completion predicate encoded one narrow definition of "done": completed + PR/commit deliverable. Epic sub-issues that resolve to no_op (work already done by a sibling, nothing to change) terminate legitimately without a deliverable — every admission gate using the narrow predicate saw them as never-run and re-admitted them each poll cycle. Fix: `Store.HasTerminalCompletion` — ANY-row check ORing deliverable-completion with no_op-with-no-error.

## Recommended Approach
New admission gates must use `executor.HasTerminalCompletion`, not `HasCompletedExecution` directly. When adding a terminal status to the execution vocabulary, sweep every dispatch/admission guard for completion predicates that don't know about it — a terminal state only one guard can see is a duplicate-dispatch bug waiting to fire.

## Third instance (GH-4381, 2026-07-16)

Same class, a different call site: `Runner.reconcileChildOutcome`'s
"externally-owned child" wait (`internal/executor/epic.go`) — the poll loop
introduced for TASK-407/GH-4349 claim routing (#4363) — grew its own third
copy of the terminal-status classification (`childExecutionNonTerminalStatuses`,
a `queued`/`pending`/`running` set) instead of consulting dispatcher.go's
`terminalExecutionStatuses`/`isTerminalExecutionStatus` (the single definition
#4373 had just introduced specifically so `WaitForExecution` and the GH-4372
retry decider couldn't drift apart — this wait was the drift).

Worse, it also hit the ordering trap `Store.HasTerminalCompletion` was built
to avoid: it read only the *latest* row (`GetExecutionStatusByTaskIDExcluding`,
`ORDER BY created_at DESC LIMIT 1`). A child whose real execution had already
reached the terminal `no_op` outcome, but which then had a fresh "queued"
duplicate row appear alongside it (a re-pick from another dispatch channel),
had that duplicate sort as "latest" and hide the terminal `no_op` — the wait
timed out with `reconcileChildOutcome: timed out waiting for externally-owned
child execution to reach a terminal state`, failing the parent epic even
though the child was legitimately done. Observed live on pilot-canary-sandbox
GH-91 (parent) / GH-92 (child), 2026-07-16 18:46–18:53Z.

Fix: replaced the status-string lookup with `Store.ListExecutionsByTaskIDExcluding`
(scans every row, not just latest) + `isTerminalExecutionStatus` to find the
most recent *terminal* row, whatever its position in created_at order. Added
`TestTerminalStatusInventory_NoStrayStatusSets` (AST-scans
`internal/executor/*.go` for map literals keyed on 2+ execution-status
strings outside `dispatcher.go`) so a 4th copy fails CI instead of a sandbox.

**Updated rule**: a "sub-issue" or "is task X done" wait must (1) consult
`isTerminalExecutionStatus`/`HasTerminalCompletion`, never a locally-defined
status set, and (2) scan every row for the task, never just the row a plain
`ORDER BY created_at DESC LIMIT 1` returns — a fresh duplicate row can always
sort ahead of an older terminal one.

## Related
- `internal/memory/store.go`
- `internal/executor/dispatcher.go`
- `internal/executor/epic.go` (`reconcileChildOutcome`, `findTerminalChildExecution`)
- `internal/executor/terminal_status_inventory_test.go`
- GH-4347, GH-4381

---
**Captured**: 2026-07-15 (updated 2026-07-16 with 3rd instance, GH-4381)
**Confidence**: 90%
**Concepts**: dispatcher, sdk-poller, ledger, canary, epic-reconcile

> Note: file reconstructed 2026-07-16 from its graph.json entry — the original
> was indexed but never committed (drift-gate FAIL on main); summary preserved verbatim.
