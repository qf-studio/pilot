---
name: duplicate completed execution rows sharing one PR are self-heal, not a dedup bug
description: SelfHealExecutionAfterMerge (internal/memory/store.go) retroactively marks EVERY non-success execution row for a task_id (failed/no_op/stalled/rate_limited/infra/skipped) as completed with the merged PR's URL when that PR merges. A fail→retry→succeed cycle therefore shows two "completed" rows sharing one pr_url — by design, so the dashboard never shows shipped work as failed. Check created_at spread + execution_events before suspecting the dispatcher dedup guard.
type: pattern
---

Confirmed 2026-07-08 by nav-research investigation of GH-4050/GH-4068 (both
had two `completed` rows pointing at one PR).

**Mechanism:** on PR merge, autopilot calls `SelfHealExecutionAfterMerge`
(`internal/memory/store.go`, WHERE `status IN ('failed','no_op','stalled',
'rate_limited','infra','skipped')`, scoped by task_id + project_path). All
prior non-success attempts for the task flip to `completed` + merged pr_url
in the same instant the real completion lands — hence pairs completing
within ~2 minutes of each other.

**How to tell the cases apart:**
- Benign retry (GH-4068): first row has full `execution_events` ending in a
  genuine failure (e.g. `ci_failed`), second row starts ~90s later via
  `shouldRetryFailedIssue` (GH-2176).
- Restart-orphaned (GH-4050): first row's events just STOP (no terminal
  event — see [[pitfall_replace_restart_kills_inflight]]), implausibly long
  created→completed lifespan (7h21m observed), second row created hours
  later.

**The dedup guard is innocent:** `IsTaskQueued`/`Dispatcher.QueueTask` never
saw two active rows — the first was already terminal (`infra`/`failed`)
before the second dispatch. GH-4008's "downgrade already-active dedup" was a
log-severity change only.

Related: [[learning_selfheal_projectpath_discriminator]] (absolute-path
scoping bug in the same mechanism, earlier incident).
