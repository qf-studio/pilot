---
name: epic-decompose-discards-child-work
description: Epic auto-decomposition could silently lose a child's completed work and record a false `completed` no-op against a foreign base SHA. The parent epic runs in an empty orchestrator worktree; a child that commits real work but fails to produce a PR has that work discarded on worktree cleanup. Fixed in PR #3383 (TASK-356) with two guards.
type: pitfall
---

Epic auto-decomposition (`internal/executor/epic.go` `ExecuteSubIssues` + the epic finalization block in `runner.go`) could **silently discard a child's completed work** and record a false `completed` no-op.

**The failure (studio-sdk #16 → child #17):**

1. Parent epic #16 dispatched into an **empty orchestrator worktree** (`pilot/GH-16`) — the parent only orchestrates; it writes no code.
2. Child #17 executed the real port in its **own** worktree (`pilot/GH-17`) — ~7 files over 26 min.
3. Child #17 was **closed with no merge**, its branch **never pushed**, worktree reset to base.
4. Parent #16 recorded `status=completed`, `commit_sha=<base/main HEAD>` (a **foreign SHA**), 0 files, no PR. The card orphaned In Progress. **26 minutes of real work — gone**, and the false-positive `completed` masked the loss.

**Two root causes (both fixed in PR #3383):**

1. `ExecuteSubIssues` only checked `result.Success`. A child runs with `CreatePR=true`, so a successful child that produced real commits (`CommitSHA != ""`) but **no PR** (`PRUrl == ""`) had its work stranded in a worktree cleanup discards — yet the loop closed the child issue and reported epic success. **Fix:** a work-loss guard fails loud and halts the epic, leaving the child issue **open for recovery** instead of closing it. (Truly empty children are already failed upstream by the ghost-SHA guard, so the guard keys precisely on *committed-but-undelivered* work.)
2. The epic finalization read the parent branch `CommitSHA` **before** the no-commits guard. The orchestrator worktree's `HEAD == base HEAD`, so it recorded the **foreign base SHA** as the epic deliverable. **Fix:** harvest the SHA only **after** `CountNewCommits > 0`.

**Why:** a *thorough* spec (written to defeat the no-op false-positive) ironically trips epic detection (`detectEpic`: epic keyword AND `phaseCount>=3` OR `checkboxCount>=5` OR `wordCount>200`) and routes the task into the work-losing decomposition path. So the better the spec, the more likely the loss.

**How to apply:**

- The **`no-decompose` label** (`decompose.go` `NoDecomposeLabel`) is the reliable escape hatch — it downgrades epic→complex so the task runs **directly** in one worktree (verified: studio-sdk #18/#20/#22/#24 all ran direct, produced real PRs). Prefer it for large connector ports.
- The proven recipe for landing a large port via the board loop: split into ~plane-sized (≤~1.5k LOC) sub-issues whose intermediate states compile, lean spec (no epic keywords, ≤4 checkboxes) **+ the `no-decompose` label**, each runs direct ~20 min → scoped PR.
- PR #3383 converts silent loss into a **loud, recoverable failure** + removes the false-positive completion — it does **not** yet auto-recover the stranded branch (fix direction (a), still open). If you see an epic child halt with "committed work but produced no PR", the child issue is intentionally left open; re-dispatch it with `no-decompose`.
- Same worktree/execution-attribution family as [[verify-branch-and-working-tree-before-destructive-ops]]; related to the no-op false-positive class (TASK-355) and retry-path worktree (TASK-323).
