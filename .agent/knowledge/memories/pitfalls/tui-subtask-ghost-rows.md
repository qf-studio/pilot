---
name: tui-subtask-ghost-rows
description: TUI shows internal subtasks (GH-N-1…N-k) as "running 100%" forever — subtask completion never closes the in-memory dashboard entry and no ledger row exists to reconcile against; restart clears
type: pitfall
---

# TUI ghost rows: internal subtasks stuck at "running 100%"

**What happened (2026-07-18):** Dashboard showed GH-4454-1…4 "running 100%"
for 3+ hours after all four subtasks completed (10:10–10:12Z, work merged as
PR #4455 at 11:06Z). The "4 running" header count included them, masking the
real lane state.

## Mechanism
- When a single execution internally decomposes into subtasks
  (`Subtask completed subtask_id=GH-4454-4 index=4 total=4`), the TUI
  registers a per-subtask progress row in memory at subtask start.
- Subtask completion does NOT call the monitor's completion hook — only real
  executions transition dashboard rows.
- Subtasks have **no `executions` ledger rows**, so no ledger reconciliation
  ever corrects the display. The rows live until daemon restart.

## Rule
`GH-N-i` suffixed rows in the TUI are display-only artifacts. Verify against
the ledger (`executions` has only the parent `GH-N`) and the parent's PR
before believing "running". Trust ledger over dashboard — standing rule.

Fix not yet filed as of 2026-07-18. Related: GH-4368 (icon/status ladder
disagreement — sibling display-trust bug), [[hard-cap-rearm-in-memory-gate]].
