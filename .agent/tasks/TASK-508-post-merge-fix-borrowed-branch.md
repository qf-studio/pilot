# TASK-508: Post-merge CI fix issues borrow the merged PR branch and short-circuit as delivered

**Status**: ✅ **A1 MERGED 11-05 22:26Z → [PR#6132](https://github.com/qf-studio/pilot/pull/6132)** (pre-PR enforce APPROVE-w-notes 0/4; post-merge APPROVE-w-notes, minors: PostMergeCIStartedAt is not restarted after the code-class re-run, so the 30-min timeout can push the PR to StageFailed with no fix issue — same shape on the infra path; the run-permalink match misses when truncateKeepingTail cuts the first check's "Full log:" line; handleMerged spends one GetPullRequest per merge). Unreleased → next tag. **Follow-up A2 (unfiled): restart PostMergeCIStartedAt after any post-merge re-run, code and infra paths.** Was: 🚀 11-05: A1 DISPATCHED → [#6125](https://github.com/qf-studio/pilot/issues/6125) (`pilot` + `no-decompose`): post-merge CI keys on the PR's own MergeCommitSHA instead of the main head, one code-class re-run before a fix issue, fix-issue dedup by workflow-run permalink. Trigger: #6107 + #6108 both filed for run 42062140402 (a GitHub `commit_refs` rejection on docs-sync) → PR#6114 merged, PR#6115 closed as a duplicate. The ownership check moved to TASK-640 (#6123). Was: 🟡 10-20: #5564 shipped (`pilot-done`); #5551 closed. **Kept active: the three follow-ups below are still unfiled.** Prior: 🚀 DISPATCHED 2026-10-01 → [#5564](https://github.com/qf-studio/pilot/issues/5564) (`pilot`, `no-decompose`, `bug`). One PR, three changes (A emitter, B dispatcher safety net, C host-checker reason). **Post-merge review of PR#5569 DONE 2026-10-02: APPROVE-w-notes** (legs A/B/C verified on main; notes: two other merged-PR short-circuits in dispatcher.go unguarded for a borrowed origin · close/label failures only logged · supersede path GitHub-only · borrow is in-memory, lost on restart).
**Created**: 2026-10-01 · **Owner**: Navigator plans, Pilot executes · **Related**: TASK-460 (false-success class), TASK-507 (Jev gate 3 reads post-merge failures)

## Why
Six `fix(ci): resolve post-merge CI failure from PR #N` issues in 60 days; one real fix PR. #5551 (10-01) left open and polled every 5.5 min forever; #5560 (10-01) and #5385 (09-08) closed `pilot-done` with the ORIGINAL merged PR as "delivery" ("Time to merge 10s"). Claude never ran on any of them. RCA 2026-10-01 via nav-research + box daemon.log + GitHub events; pitfalls `ci-post-merge-fix-issue-borrows-merged-branch-short-circuits`, `completed-row-without-live-execution-polled-forever-as-stalled`.

## Root cause chain
1. `generateBody` emits `branch:/pr:/sha:` in the autopilot-meta footer for every failure type, incl. `ci_post_merge` (PR merged, branch deleted).
2. `resolveAutopilotFixBranch` keys on `branch:` only → borrows the merged branch, `RecordBorrowedBranch(fromPR=original)`.
3. Dispatcher merged-PR short-circuit → `MarkExecutionCompleted(originalPRURL)` directly, bypassing lifecycle; `buildTaskFromExecution` never sets `FromPR` so the GH-5400 `isBorrowedOriginPR` rule cannot protect it.
4a. Startup cleaner strips `pilot-in-progress`; poller re-admits; `HasCompletedExecutionReason` falls through all re-arm probes to `recordClaimLostDrop` + "stalled: awaiting re-arm evidence" (variant A, #5551).
4b. `registerPR` binds the issue to the merged PR; `checkExternalMergeOrClose` closes it `pilot-done` with no ownership check (variant B, #5560/#5385).

## Fix (in #5564)
- **A** post-merge footer = `iteration:N source:M` only (`iteration:`/`source:` regexes are independent of `branch:`); pre-merge + review footers byte-identical.
- **B** short-circuit: if `BorrowedBranch(taskID).fromPR == extractPRNumberFromURL(mergedURL)` → `Finish(ExecStatusSuperseded)`, empty pr_url, comment + `pilot-superseded` + close; no `recordExternalMerge`. Known limit: in-memory borrow registry, silent after restart (A removes the source).
- **C** genuine completed/no_op row → `"completed execution exists"`, no claim-lost drop.

## Same-day review pass (10-01 pm)
PR#5558/#5559/#5561/#5562/#5563 all reviewed APPROVE-w-notes with verdict comments; follow-ups filed: #5565 (gate-3 line at DEBUG), #5566 (redaction slash leak), #5567 (test pins), #5568 (evidence capture).

## Follow-ups (not in #5564)
- Flake guard: re-run the failed job once before filing a post-merge fix issue; decline as flake if green (extend `maybeRetryPostMergeInfraFailure` to code-class). Unfiled.
  - **Counter-evidence 2026-10-20:** GH-5784 (post-merge red on PR#5782's merge commit) was a pre-existing race in `runEpicParentPass`, not caused by #5782; the auto-fix PR#5786 fixed a real bug (review APPROVE-w-notes). A blind rerun-and-decline would have hidden it. Refine before filing: when the failing test does not cover the merged diff, file a separate "pre-existing flake exposed on merge commit" issue instead of attributing it to the PR.
- Ownership check in `checkExternalMergeOrClose`: refuse a PR that merged before the issue was created (needs one `GetIssue` for `CreatedAt`). Unfiled.
- Dedup against human-filed issues for the same failing test (`findExistingFixIssue` is exact-title only). Unfiled.
- Operator: close #5551 by hand.

## Refs
- `.agent/knowledge/memories/pitfalls/ci-post-merge-fix-issue-borrows-merged-branch-short-circuits.md`
- `.agent/knowledge/memories/pitfalls/completed-row-without-live-execution-polled-forever-as-stalled.md`
- Seams: `internal/autopilot/feedback_loop.go`, `cmd/pilot/handlers.go`, `internal/executor/dispatcher.go`, `cmd/pilot/main.go`

## Legs

### LEG A1 — repo: qf-studio/pilot — title: fix(autopilot): post-merge CI monitors the PR's own merge commit, re-runs a code-class failure once before a fix issue, and dedups fix issues by workflow run

## Context

On 2026-11-05 two PRs merged 23 s apart (PR 6105 at 16:12:22Z, PR 6104 at 16:12:45Z). The docs-sync workflow run on the second merge commit failed on a GitHub-side rejection (`remote: fatal error in commit_refs`). Autopilot filed one post-merge fix issue per PR (6107 for 6104, 6108 for 6105) and both quoted the same run, 42062140402. Pilot shipped the same workflow retry twice (PR 6114 merged, PR 6115 closed as a duplicate).

Verified on main: handlePostMergeCI in `internal/autopilot/controller.go` (near line 6782) fills an empty PostMergeSHA from getMainBranchSHA, the current head of main, so a PR whose first post-merge tick runs after a later merge watches the later merge's CI. Two call sites already set PostMergeSHA from the PR's MergeCommitSHA (the scan-recovery path near line 10145 and the external-merge path near line 11004); handleMerged does not. The code-class branch of handlePostMergeCI files a fix issue on the first red (near line 189 of the function); only the infra class gets maybeRetryPostMergeInfraFailure and rerunInfraFailures. findExistingFixIssue in `internal/autopilot/feedback_loop.go` (near line 676) matches open issues by exact title only, and the title carries the PR number, so two PRs never collide. The fix-issue body carries the run permalink as the line "Full log: <url>" built in `internal/autopilot/ci_failure_excerpt.go` (near line 184).

probe: present qf-studio/pilot func[[:space:]]\(c[[:space:]]\*Controller\)[[:space:]]handlePostMergeCI\(
probe: present qf-studio/pilot func[[:space:]]\(c[[:space:]]\*Controller\)[[:space:]]getMainBranchSHA\(
probe: present qf-studio/pilot func[[:space:]]\(c[[:space:]]\*Controller\)[[:space:]]rerunInfraFailures\(
probe: present qf-studio/pilot func[[:space:]]\(f[[:space:]]\*FeedbackLoop\)[[:space:]]findExistingFixIssue\(
probe: present qf-studio/pilot Full log:
probe: absent qf-studio/pilot TestHandlePostMergeCI_UsesMergeCommitNotMainHead

## Scope

- `internal/autopilot/controller.go`, handlePostMergeCI: when PostMergeSHA is empty, take the merge commit from the PR (GetPullRequest on the PR number, field MergeCommitSHA). Fall back to getMainBranchSHA only when that fetch fails or returns an empty SHA, and log the fallback at WARN with the attribute post_merge_sha_source=main_head; the normal path logs post_merge_sha_source=merge_commit at INFO once. handleMerged sets PostMergeSHA from the merged PR's MergeCommitSHA when it has the PR in hand, so the first tick never needs the fetch.
- `internal/autopilot/controller.go`, the code-class branch of handlePostMergeCI: before a fix issue is filed, re-run the failed jobs once per PostMergeSHA with rerunInfraFailures, record the attempt in two new PRState fields PostMergeCodeRerunCount and PostMergeCodeRerunSHA in `internal/autopilot/types.go` (persisted like the infra pair), return nil so CheckCI re-reads next tick, and file the fix issue only when the run is red again on the same SHA. The re-run metric label is code_retry. When no job can be re-run the branch files as today.
- `internal/autopilot/feedback_loop.go`: the post-merge fix-issue path passes the failing run permalink(s) to findExistingFixIssue; an open issue with the autopilot-fix label whose body contains the same "actions/runs/<id>" path counts as existing, in addition to the exact-title match. On a run match the loop posts one comment on the existing issue ("also attributed to PR #N, run <id>") and suppresses the new issue; RecordSpawnedFixIssue backfills the store as it does for the title match. Pre-merge and other failure types keep the exact-title match only.
- Tests, named exactly: TestHandlePostMergeCI_UsesMergeCommitNotMainHead in `internal/autopilot/controller_test.go` (fake GitHub client where main head differs from the PR's MergeCommitSHA; PostMergeSHA equals the merge commit; the fallback case logs main_head), TestHandlePostMergeCI_CodeClassRerunsOnceBeforeFixIssue in `internal/autopilot/controller_test.go` (first red: one re-run, no issue, stage unchanged; second red on the same SHA: the issue is filed; a new SHA resets the count), TestFindExistingFixIssue_MatchesByRunPermalink in `internal/autopilot/feedback_loop_test.go` (two titles, one run id, the second lookup finds the first issue; a different run id does not match).

## Acceptance

- [ ] `go test -race ./internal/autopilot/ -run 'TestHandlePostMergeCI_UsesMergeCommitNotMainHead|TestHandlePostMergeCI_CodeClassRerunsOnceBeforeFixIssue|TestFindExistingFixIssue_MatchesByRunPermalink'` passes; paste the output into the PR body.
- [ ] keep getMainBranchSHA as the first choice for an empty PostMergeSHA -> TestHandlePostMergeCI_UsesMergeCommitNotMainHead fails
- [ ] file the fix issue on the first code-class red -> TestHandlePostMergeCI_CodeClassRerunsOnceBeforeFixIssue fails
- [ ] match open fix issues by title only -> TestFindExistingFixIssue_MatchesByRunPermalink fails
- [ ] Do not re-run make build, make test or make lint as evidence: the daemon's quality gates already run them on this repo.

## Out of scope

The LLM failure classifier and its rubric in `internal/autopilot/ci_failure_classifier.go` (the commit_refs rejection stays code-class until the re-run clears it); the ownership check for a PR merged before its issue existed (TASK-640, issue 6123); the pre-merge CI fix path; any change to the fix-issue title format; the GH-5351 guard.

## Refs

- https://github.com/qf-studio/pilot/issues/6107
- https://github.com/qf-studio/pilot/issues/6108
- https://github.com/qf-studio/pilot/pull/6114
- https://github.com/qf-studio/pilot/actions/runs/42062140402
- #5564

## Refs

- A1 → https://github.com/qf-studio/pilot/issues/6125
