---
name: epic-waiter-misreads-takeover-stalled-row
description: The epic child-outcome lookup returns the first TERMINAL execution row it finds, so a GH-4536 force-stalled-for-takeover row (bookkeeping, not a real stall) reads as child failure while the takeover run is actively executing — epic fails and re-implements the child's scope.
type: pitfall
---

**Incident (2026-07-29, pilot-console-ui GH-21/25/26; fix: pilot#4619).** After the epic parent lost dispatch claims to the poller, the GH-4536/TASK-419 takeover machinery force-stalled the parent's queued child rows (`reclaimSelfOwnedQueuedChild`) and started replacement executions. On re-entry the epic's `findTerminalChildExecution`/`findChildExecutionState` (internal/executor/epic.go) scanned ALL rows for the task_id and returned the first terminal one — the superseded force-stalled row — and `resolveChildTerminalOutcome` turned it into a hard epic failure while the takeover run was mid-claude. The parent then re-picked and re-implemented the child's scope (TASK-401 "epic re-implements child work" class, new vector). This is also what made the epic-lifecycle canary's `child-count` assertion fail from 07-28 while `duplicate-pr` passed.

**Why:** the all-rows terminal scan was built for GH-4381 (a newer *queued* duplicate must not hide an older genuine terminal row) and was never adapted for "older row is terminal only as takeover bookkeeping, newest row is actively running". `terminalExecutionStatuses` has no notion of "administratively retired".

**How to apply:** when reading a task's outcome from multi-row execution history, check the NEWEST row first — if it is actively `running`, the task is running, regardless of older terminal rows. Never treat `stalled` as a uniform terminal verdict; check WHY (takeover marker text, supersession, claim generation) before failing a parent on it. Precedent for the right shape: `sweepStalledEpicChildren` checks `HasExecutionEventStage(StagePRCreated)` before stamping stalled (GH-4564). Related: [[takeover-execution-never-finalized-stale-subexecid]], [[stall-status-reused-as-hold-marker]].
