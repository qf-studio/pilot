---
name: auto-preserved-wip-recover-by-cherry-pick-continuation
description: When an executor run ends "worktree had uncommitted work at no-op classification — auto-preserved as <sha>", the work is usually complete; the dispatcher still re-picks from origin/main and the fresh run's plain push collides with the preserved branch. Recover by pinning the sha under a preserve/ ref, verifying it, and filing a cherry-pick continuation issue.
type: pitfall
---

# Auto-preserved WIP: recover by cherry-pick continuation, never by re-pick

**What happened (2026-09-13, console#308):** a ~1500-line one-PR spec ran 5×
under the 1 h `complex` budget on Sonnet. Runs 1+3 timed out, 2+4 exited 1
mid-stream, run 5 **finished the whole spec but never committed** — the
daemon classified it no-op, found the worktree dirty and auto-preserved it
as `0e704c0` on `pilot/GH-308` ("needs manual review, not a genuine no-op").
36 s later the dispatcher re-picked generation 5 anyway, from `origin/main`
(`git worktree add -B <branch> … origin/main` resets the local branch), and
the executor's plain `git push -u origin <branch>` cannot land on the
diverged remote → run 6 doomed too.

## Rule
1. On an "auto-preserved as <sha>" failure: **remove the trigger label first**
   (stop the loop), then `git push origin <sha>:refs/heads/preserve/<task>-wip-<sha>`
   so no later run can overwrite it.
2. Verify the sha in a throwaway worktree: build, vet, `go test ./...`, lint,
   repo gates, grep for every named test in the spec. It is often complete.
3. File a **continuation issue** (`pilot` + `no-decompose`): cherry-pick the
   sha, gap-fix only, run the mutation pins, paste evidence, `Closes #orig`.
   Do not open the PR by hand from an interactive session.
4. Size rule for one-PR specs: ~500 lines of diff per issue, or raise
   `timeouts.complex` on the box (config + restart, operator consent).

## Refs
- console#308 → #310; box daemon.log 12:34–16:22Z; executor `git_freshness.go`
  (producer), `worktree.go` `CreateWorktreeWithBranch` (`-B … origin/main`),
  `git.go` push (no force). Executor follow-up filed on pilot.
- Related: [[hardening-issue-wording-triggers-model-refusal]] (declined claims),
  [[stalled-rearm-sweep-ignores-body-edit-loops-forever]]
