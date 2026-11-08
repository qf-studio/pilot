# fix(executor): a rejected push must not count as success, and an autopilot-fix continuation must push its rebased branch with a lease on the footer SHA

**Status**: ✅ MERGED 2026-10-16 10:24Z → [#5729](https://github.com/qf-studio/pilot/issues/5729) → [PR#5730](https://github.com/qf-studio/pilot/pull/5730) (8ffdd66e9), reviewed APPROVE-w-notes pre-merge (11/11 PASS, 3/3 pins kill). Rides the 16:00 Berlin train. Open note, not filed: footer-branch worktree base resolution did not log on GH-415.
**Created**: 2026-10-16
**Repo**: qf-studio/pilot

Blocked by: none

## Context

The push step in `internal/executor/runner.go` (the retry loop around `git.Push`, GH-3785/GH-4866) has a shortcut from GH-1389: when `git push` returns an error but `RemoteBranchExists` reports the branch on origin, the error is logged at Warn as "Push reported error but branch exists on remote, continuing" and cleared. That shortcut was meant for a chdir false positive where the data had already landed. It also fires when the push was genuinely rejected, because `RemoteBranchExists` in `internal/executor/git.go` only checks that a head with that name exists, not that it points at the local commit. Observed 2026-10-16 09:34Z on console GH-415 (TASK-562, an `autopilot-fix` continuation that rebased pilot/GH-403 onto main): the session produced the correct rebase (two commits on top of main), the runner ran `git push -u origin pilot/GH-403`, GitHub answered `! [rejected] pilot/GH-403 -> pilot/GH-403 (non-fast-forward)`, the shortcut swallowed it, the runner adopted PR #408 and logged "Task completed successfully" with the PR URL. PR #408 still pointed at the old head; nothing had been pushed. The work was recovered by hand from the box with `git push --force-with-lease=refs/heads/pilot/GH-403:<old head> origin fb83d43:refs/heads/pilot/GH-403`. The earlier continuation of this shape (pilot #5661 → PR #5660, 10-13) only succeeded because that session force-pushed on its own. A rebase continuation can never fast-forward, so the runner must push it with a lease on the SHA the `autopilot-meta` footer recorded (`task.FixFromSHA`, parsed in `cmd/pilot/handlers.go`), and only then. Not verified: the daemon log for GH-415 shows the worktree cut at `base_ref=origin/main` and no "Resolved autopilot-fix worktree base" line; the session rebased by itself, so the outcome was right, but the base resolution for footer branches deserves a look while in this code.

## Scope

- `internal/executor/git.go`: add `RemoteBranchHead(ctx, branchName) (sha string, ok bool)` built on `git ls-remote --heads origin <branch>`, and `LocalHead(ctx) (sha string, err error)`. Keep `RemoteBranchExists` for its other callers.
- `internal/executor/runner.go` push loop: replace the "branch exists on remote" shortcut with "remote head equals local HEAD". Only that case clears the error (the GH-1389 chdir false positive). Any other rejection stays an error and goes through the existing failure path and reason extraction (GH-5715), with the git output in the log line. A non-fast-forward rejection is not retried as if transient.
- Autopilot-fix continuation (`task.FromPR > 0` and `task.FixFromSHA != ""`): when the first push is rejected non-fast-forward and the remote head equals `task.FixFromSHA`, push once more with `git push --force-with-lease=refs/heads/<branch>:<FixFromSHA> origin <branch>` and log at Warn with the old and new SHAs. When the remote head is neither the local HEAD nor `FixFromSHA`, do not force; fail with a reason that names all three SHAs.
- Tests in a new file under `internal/executor/` next to `runner_gh5715_test.go`, driven through the existing fake-git seams used by `TestPushFailureReason` and `TestRunner_PushRetryExhausted_RecordsDeadManEvents`:
  - TestPushRejectedNonFastForward_NotSwallowed: rejected push, remote head differs from local HEAD, no FixFromSHA → the push error surfaces, the task does not complete as success, no PR adoption.
  - TestPushChdirFalsePositive_AcceptedWhenHeadsMatch: push returns an error, remote head equals local HEAD → accepted, as today.
  - TestPushAutopilotFixContinuation_ForceWithLeaseOnFooterSHA: FromPR set, FixFromSHA equals the remote head, first push rejected → exactly one force-with-lease push with that SHA in the lease, then success.
  - TestPushAutopilotFixContinuation_NoForceWhenRemoteMoved: FromPR set, remote head differs from FixFromSHA and from local HEAD → no force push, error surfaces naming the SHAs.
  - A `git.go` unit test for `RemoteBranchHead` against a local bare remote, in `internal/executor/git_test.go` next to `TestRemoteBranchExists_WithRemote`.

## Acceptance

- `go build ./...` passes; paste the output into the PR body.
- `go vet ./internal/executor/` passes; paste the output into the PR body.
- `go test ./internal/executor/ -run 'TestPush' -count=1 -v` prints `--- PASS` for TestPushRejectedNonFastForward_NotSwallowed, TestPushChdirFalsePositive_AcceptedWhenHeadsMatch, TestPushAutopilotFixContinuation_ForceWithLeaseOnFooterSHA, TestPushAutopilotFixContinuation_NoForceWhenRemoteMoved, TestPushFailureReason and TestPush_StripsAnthropicCoAuthorFromWholeBranch; paste the lines into the PR body.
- `go test ./internal/executor/ -run 'TestRemoteBranch' -count=1 -v` prints `--- PASS` for the new RemoteBranchHead test and the two existing RemoteBranchExists tests; paste the lines into the PR body.
- `go test ./internal/executor/ -run 'TestRunner_PushRetryExhausted' -count=1 -v` prints `--- PASS`; paste the line into the PR body.
- Pin: restore the old shortcut (clear the error whenever the remote branch exists) -> TestPushRejectedNonFastForward_NotSwallowed fails.
- Pin: force-push without checking the remote head against FixFromSHA -> TestPushAutopilotFixContinuation_NoForceWhenRemoteMoved fails.
- Pin: drop the lease and use a plain force -> TestPushAutopilotFixContinuation_ForceWithLeaseOnFooterSHA fails on the recorded push arguments.

## Out of scope

- Changing how the worktree base is resolved for footer branches (noted above, separate issue if confirmed).
- The console side; PR #408 was recovered by hand.
- `git push --force` without a lease anywhere.

## Refs

- `internal/executor/runner.go` (push retry loop, "Push reported error but branch exists on remote"), `internal/executor/git.go` (Push, RemoteBranchExists, ResolveFixContinuationBaseRef), `cmd/pilot/handlers.go` (resolveAutopilotFixBranch, FixFromSHA), `internal/executor/runner_gh5715_test.go`, `internal/executor/git_test.go`
- Incident: console GH-415 / PR #408, 2026-10-16 09:34Z; precedent pilot #5661 / PR #5660 (10-13, session force-pushed itself)
- Plan: [TASK-562](PILOT-CLOUD-TASKS-MOVED.md)
- Pilot issue: https://github.com/qf-studio/pilot/issues/5729
