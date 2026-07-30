---
name: takeover-execution-never-finalized-stale-subexecid
description: After ErrClaimLost→takeover, the epic loop's local subExecID is empty, so every finalizeSubIssueExecution call silently no-ops — takeover execution rows stay `running` forever and only the 2h orphan sweep closes them, even when the run shipped a PR.
type: pitfall
---

**Incident (2026-07-29, pilot-console-ui GH-25/26; fix: pilot#4619).** `reconcileSelfOwnedTakeover` obtains a new execution row via `beginWithGenerationRetry` (which correctly stamps `subTask.ExecutionID = newExecID`), runs the child inline — but the epic loop `executeSubIssuesTracked` finalizes via its loop-local `subExecID`, captured from the original `Begin()` call, which is `""` on the ErrClaimLost branch. `finalizeSubIssueExecution` silently no-ops on empty execID, at all five call sites. Result: GH-25's takeover created PR#28, gates green, yet the row sat `running` until `orphan eviction after 2h0m0s stuck`; the autopilot board showed a phantom "stalled". The entire takeover mechanism had zero test coverage at incident time.

**Why:** the design assumed the loop-local execution ID captured before dispatch still identifies the row that ran the work; the takeover path replaces the row and stamps the new ID into the Task, but nothing reads it back.

**How to apply:** any code path that REPLACES an execution row mid-flight must re-derive downstream IDs from the task's authoritative field (`subTask.ExecutionID` / `LogExecutionID()`), never from a stale local. When adding a "helper silently no-ops on empty ID" guard, treat every silent no-op as a latent finalize-leak — log it at least. Sibling defect: autopilot's `StageFailed` transition records an audit event but never finalizes the source execution row either (fixed pilot#4620 — the failed-side twin of `SelfHealExecutionAfterMerge`). Related: [[epic-waiter-misreads-takeover-stalled-row]].
