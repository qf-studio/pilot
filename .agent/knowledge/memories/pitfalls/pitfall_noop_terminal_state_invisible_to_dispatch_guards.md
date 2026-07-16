# Pitfall: no_op is terminal but invisible to "is this task done" dispatch guards

## Summary
`no_op` is a legitimate terminal outcome (common for epic sub-issues) but was invisible to every "is this task done" dispatch guard — `Store.HasCompletedExecution` requires `status='completed'` plus a deliverable, so the SDK poller's `ExecutionChecker` and dispatcher.go's `hasTerminalSuccessLedger` both re-treated no_op'd issues as fresh candidates forever (GH-4347: GH-82 dispatched 6x in one cycle).

## Context
RCA of GH-4347 duplicate-dispatch storm on the canary sandbox, 2026-07-15.

## Details
The completion predicate encoded one narrow definition of "done": completed + PR/commit deliverable. Epic sub-issues that resolve to no_op (work already done by a sibling, nothing to change) terminate legitimately without a deliverable — every admission gate using the narrow predicate saw them as never-run and re-admitted them each poll cycle. Fix: `Store.HasTerminalCompletion` — ANY-row check ORing deliverable-completion with no_op-with-no-error.

## Recommended Approach
New admission gates must use `executor.HasTerminalCompletion`, not `HasCompletedExecution` directly. When adding a terminal status to the execution vocabulary, sweep every dispatch/admission guard for completion predicates that don't know about it — a terminal state only one guard can see is a duplicate-dispatch bug waiting to fire.

## Related
- `internal/memory/store.go`
- `internal/executor/dispatcher.go`
- GH-4347

---
**Captured**: 2026-07-15
**Confidence**: 90%
**Concepts**: dispatcher, sdk-poller, ledger, canary

> Note: file reconstructed 2026-07-16 from its graph.json entry — the original
> was indexed but never committed (drift-gate FAIL on main); summary preserved verbatim.
