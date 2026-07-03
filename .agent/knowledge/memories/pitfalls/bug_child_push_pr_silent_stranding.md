---
name: child worker push/PR-create failure was un-retried and left committed work reachable only via a bare sha
description: A sub-issue's push/PR step (internal/executor/runner.go, ~:3560-3760) ran git push and gh pr create exactly once each. On failure it set result.Success=false with a message that WAS informative (git.go wraps stderr via "%w: %s"), but there was no retry and no independent ref pinning the commit — the only recovery anchor was the branch inside the worktree, which the unconditional `defer cleanupWorktree()` (runner.go ~:1573) deletes on return. GH-3764's push/PR step failed silently in this way: 6 commits (616a3095..05c8271b) vanished from any reachable branch and had to be salvaged from the shared git object store manually into PR #3773.
type: pitfall
---
**Symptom:** epic parent reports `sub-issue N committed work (sha X) but produced no PR` (the
TASK-356 #1 work-loss guard, `internal/executor/epic.go` ~:1937) with no detail beyond the
sha — or, for the non-decomposed direct path, the task just fails with `push failed: <err>` /
`PR creation failed: <err>` and the worktree (and with it the only reachable ref to the
commits) is deleted moments later by the unconditional `defer cleanupWorktree()`.

**Root cause:** two independent gaps compounded:
1. **No retry** on `git.Push` / `git.CreatePR` (`internal/executor/runner.go`) — a single
   transient failure (network blip, API rate limit, momentary `gh` auth hiccup) was treated
   as permanent.
2. **No recovery anchor** — the branch created inside the worktree (`task.Branch`) is the
   *only* ref to the commits until push succeeds. `WorktreeManager.cleanupWorktree`
   (`internal/executor/worktree.go` ~:143) runs `git worktree remove --force` unconditionally
   on return, and while branch refs themselves survive worktree removal (they live in the
   shared repo, not the per-worktree admin area), nothing was pinning them independent of
   task-retry bookkeeping — a later `git.DeleteBranch` on a "stale branch" recreate path, or
   simple GC pressure, could still take them.

**Fix (GH-3785):**
- `git.Push` / `git.CreatePR` / `PRCreator.CreatePR` now retry up to `gitPushRetryAttempts` /
  `prCreateRetryAttempts` (3, 300ms apart) before giving up.
- On exhausted retries, `GitOperations.CreateRecoveryRef(ctx, taskID, fromRef)` pins the
  commit under `refs/pilot-recovery/<sanitized-task-id>` — a ref namespace outside
  `refs/heads/`, shared across worktrees, so it survives `git worktree remove` and stays
  fetchable (`git fetch <repo> refs/pilot-recovery/<id>`) even if the branch itself is later
  deleted. `formatGitStepFailureWithRecovery` (runner.go) puts the failing step name
  (`push` / `pr-create` / `mr-create`), attempt count, raw stderr, and
  `branch=... sha=... recovery_ref=...` into `result.Error` in one message.
- The epic-level work-loss guard (epic.go ~:1937) also pins a recovery ref (via
  `subTaskRepoPath`, using `refs/heads/<branch>` as the source since the epic runs outside
  the child's worktree) so even the anomalous Success=true/no-PR path gets a real recovery
  ref, not just a bare sha.

**How to apply:** any future "child does X but the parent only reports a bare identifier"
report is a signal to check whether the failing step (1) retries transient failures and
(2) pins its output to a ref/anchor that survives normal cleanup, *before* assuming the
step itself is broken — the step's own error may already be correct and just discarded.

Related: [[bug_autopilot_epic_orphan_child_prs]] (a different epic work-loss shape — orphaned
PRs rather than un-pushed commits), TASK-382 D2/D3 (siblings: D3/[GH-3786] fixed a false
epic-parent-failure race via `reconcileChildOutcome`; this fix is the complementary
"genuine push/PR failure must not be silent or unrecoverable" half).
