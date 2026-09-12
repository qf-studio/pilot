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

## Fourth instance (GH-5359, 2026-09-07) — the guard that *produces* no_op was wrong

Not a missing consumer of `no_op` this time but a false producer: the runner's
PR guard (`internal/executor/runner.go`) treated a failed `git merge-base`
against `origin/main` as "no commits relative to base" and ended the run
`no_op` — for GH-5351 run 1 that happened *after* the run had committed, pushed
and opened PR #5356 (autopilot had already adopted it). The dispatcher then
re-picked the issue as generation 1 as if nothing had been delivered; only the
prior adoption prevented a duplicate Claude run.

Fix (#5359 → PR#5369 died on a CI-only fixture bug → #5370 → **PR#5373 merged
2026-09-08, v2.273.2**): `resolveEmptyBranchWithFallback` (`runner.go:2614`)
distinguishes "diff computed and empty" from "diff could not be computed" with
three tiers — merge-base, then `CountNewCommitsAgainstOrigin` after a fresh
fetch, then `FindOpenPRByBranch`. Commits + open PR → adopt the PR, end
`completed`; nothing confirmable by all three → hard failure, never `no_op`.
Each tier logs at Info with branch/base/PR. Tests: `runner_gh5359_test.go`.

**Updated rule (producer side)**: a terminal `no_op` may only be written when
emptiness was *positively established*; an error while measuring emptiness must
fail closed (error status), because every downstream guard treats `no_op` as
"legitimately nothing to do" and will not re-examine it.

## Related
- `internal/memory/store.go`
- `internal/executor/dispatcher.go`
- `internal/executor/epic.go` (`reconcileChildOutcome`, `findTerminalChildExecution`)
- `internal/executor/terminal_status_inventory_test.go`
- GH-4347, GH-4381, GH-5359 (PR#5373)
- `internal/executor/runner.go` (`resolveEmptyBranchWithFallback`), `internal/executor/runner_gh5359_test.go`

---
**Captured**: 2026-07-15 (updated 2026-07-16 with 3rd instance, GH-4381; 2026-09-12 with 4th instance, GH-5359)
**Confidence**: 90%
**Concepts**: dispatcher, sdk-poller, ledger, canary, epic-reconcile

> Note: file reconstructed 2026-07-16 from its graph.json entry — the original
> was indexed but never committed (drift-gate FAIL on main); summary preserved verbatim.
