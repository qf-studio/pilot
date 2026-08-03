---
name: pilot_stalled_status_is_retry_not_cancel
description: Marking an execution row 'stalled' is a recovery signal that makes the dispatcher claim generation+1 exempt from the repick cap — it is not a cancel and spins a generation loop
type: pitfall
created: 2026-08-03
---

# `stalled` means "dead owner, retry me" — never use it to cancel

**What happened (2026-08-03, GH-4655).** To stop a task an operator (1) closed the GitHub
issue as `not planned`, then (2) marked its queued execution row `status='stalled'` — the
workaround documented for orphaned rows. The dispatcher immediately logged:

```
dispatch re-pick: prior claim was stall-killed — claiming next generation
without counting toward repick hard cap   task_id=GH-4655 generation=3
consecutive_stall_drops=1
```

Each stall produced a fresh generation, **explicitly exempt from the repick hard cap**.
The poller compounded it by re-dispatching the issue at 12:03:31Z — 8 minutes after it
was closed — via "Execution failed without PR, unmarking for retry" (no issue-state check
on that path; that gap is #4656).

**Why.** `stalled` exists for crash/orphan recovery: it tells the dispatcher the previous
owner is gone so the work may be re-claimed. Cancellation semantics ("never run this
again") have no representation in the status vocabulary at all — hence #4678.

**How to apply.**

- Do NOT mark rows `stalled` to cancel work. It is a recovery signal and will loop.
- **Update (2026-08-03, #4678 shipped): use `pilot task cancel <task-id> [--project <path>]
  [--reason "..."]` instead.** It writes a NEW terminal status, `canceled` (single-L —
  distinct from the older, confirmed-dead `cancelled` double-L value store.go's
  terminal-status list keeps only for historical defensiveness), through the
  `ExecutionLifecycle.Cancel` chokepoint. A `canceled` row is never re-picked
  (`nextRetryGeneration` treats it as terminal-and-done, no generation+1, no
  hard-cap exemption) and `HasTerminalCompletion` counts it as done, so the poller's
  "unmarking for retry" path leaves it alone too. If the task's latest execution is
  currently RUNNING, cancel refuses (names the execution id) rather than killing the
  backend process — v1, no PID/handle is tracked to do that safely yet.
- The BOTH-close-and-stall workaround below is now only relevant for a task from
  BEFORE #4678 shipped, or in an environment running an older binary. Prefer
  `pilot task cancel` whenever it's available. Old workaround: close the issue
  (stops fresh poller dispatch) AND stall the queued row, then **verify settlement**
  by re-reading `execution_claims` — expect one more generation to appear before it
  stops. Budget for it and re-check rather than assuming the first stall worked.
- Closing a GitHub issue alone does NOT stop queued or running executions: nothing
  revalidates issue state at pickup or before PR creation (#4656) — `pilot task cancel`
  doesn't change that either; still close the issue too so the poller doesn't keep
  finding it as a candidate at all.

Related: [[pilot_issue_missing_no_decompose_fragments_single_fix]] ·
`.agent/tasks/TASK-437-duplicate-execution-race-prevention.md` · #4678 (cancel verb,
shipped) · #4656 (issue-state revalidation) · `.claude/skills/pilot-aws` troubleshooting
table
