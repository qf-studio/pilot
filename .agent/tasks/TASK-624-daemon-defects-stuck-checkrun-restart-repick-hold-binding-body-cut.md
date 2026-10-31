# TASK-624: Four daemon defects from the 10-28 reviewer loop — stuck check-run, restart re-pick, prerequisite-hold PR binding, follow-up body cut

**Status**: ✅ ALL FOUR MERGED 2026-10-31 — D1 #5983 → PR#5987 (15:36Z) · D2 #5984 → PR#5988 (15:56Z) · D3 #5985 → PR#5989 (16:24Z) · D4 #5986 → PR#5990 (19:27Z, held `pilot-review-rc` by the L5 path: pre-PR REQUEST-CHANGES on a spec error — the `pr_reviews` row has no NOT-VERIFIED field; founder approved). Pre-PR and post-merge agreed APPROVE-WITH-NOTES on D1–D3 (3 of 3). Review notes → [TASK-625](TASK-625-task624-review-notes-startup-label-strands-crash-orphans.md) #5991, incl. one regression from D2 (startup label guard keeps `pilot-in-progress` on a `stalled` row → crash orphans never re-offered). Operator steps below still open; not yet released. Was: 🚀 DISPATCHED 2026-10-31 (all `pilot` + `no-decompose`, pre-flight clean on bed2e5fd7).
**Origin**: the 10-28 evening fix-of-fix chain (#5931 → #5943) and PR#5981 on 10-30 (marker `2026-10-29_reviewer-opus-calibration-runs-1-3-gates-fast-5946-5947-queued-compact.md`, "Four daemon defects seen 10-28 evening, NOT filed"). Founder decision 10-31: file as one task, four legs; Pilot is idle and the TASK-620 enforce read needs 20 two-pass shadow runs.
**Traced 2026-10-31** (four read-only code traces on origin/main bed2e5fd7; every claim below is a file:line on that commit):

| # | Defect | Root cause (file:line) | Note |
|---|---|---|---|
| D1 | Check-run `status=in_progress` + `conclusion=success` keeps the PR in waiting_ci until the CI timeout (12 min lost on PR#5981; also 10-28) | `internal/autopilot/ci_monitor.go:808` `mapCheckStatus` switches on `status` only; `conclusion` is read only under `completed`. Three direct `run.Status != CheckRunCompleted` reads at `:403`, `:449`, `:1210` bypass it. | The marker's claim that the timeout *parks* the PR is wrong: `handleWaitingCI` (`controller.go:3677-3686`) sets `StageFailed` + `pilot-failed`. No rerun exists for a non-failed job (`RerunFailedJobs` only reruns failed/cancelled). |
| D2 | A restart re-dispatches an issue whose run already has an open PR | `internal/executor/dispatcher.go:416` `reconcileOrphanedExecutions` stalls every claimed running row; it heals only on a *merged* PR (`:448-466`). Its sibling `recoverStaleRunningTasks` has the open-PR skip (`:804`, `staleRunningOpenPRCheck`); the boot step never got it. Then `internal/adapters/github/cleanup.go:601` `StartupRecover` strips `pilot-in-progress` with no PR or finished-row check, and `nextRetryGeneration` (`:1898`) grants generation+1 because `stalled` is not "done". | Window: the runner records the PR (`runner.go:7483` `pr_created` event) then runs a tail (pre-PR review label, webhook, nav sync); `pr_url` is written only at `Persist` (`lifecycle.go:365`). A restart in that window leaves a running row with an empty `pr_url` and a `pr_created` event. |
| D3 | The prerequisite hold binds a PR to an issue by *mention* ("#5917's attached PR #5931"; #5931 closes #5927) | `cmd/pilot/github_base_presence_probe.go:25` `LinkedPRNumbers` returns every hit of `SearchPRsForIssue` (`internal/adapters/github/client.go:1398`, query `repo:o/r is:pr #N`) unfiltered; same query in the gh fallback `internal/executor/base_presence.go:304`. | Reusable: `(*Client).ClosingIssueNumbers` (`client.go:991`, GraphQL `closingIssuesReferences`), `GetPullRequest` (`:580`, Body + Head.Ref). Three duplicate closing-ref regexes live in autopilot (`conflict_stacked.go:38`, `scope_release.go:22`, `controller.go:1128`) and autopilot imports executor, so executor cannot reuse them. |
| D4 | Follow-up issue body carries defect lines cut mid-sentence (#5937; both cut lines are exactly 200 bytes) | `internal/executor/post_merge_reviewer.go:735` `parsePostMergeDefect` leaves claim text in `Location` when the separator is ` — `, ` – `, `: ` or absent; `internal/autopilot/post_merge_review.go:413` cuts `Location` at a literal 200 bytes with "…". Every cap is a byte cut (`sanitizePostMergeField` `:380`, `prePRField` `pre_pr_review_render.go:71`). The two-pass union (`post_merge_passes.go:304-306`) rewrites `Location` to `path:line` and drops the trailing claim text silently; the one-usable-pass branch (`:134-141`) does not rewrite at all. | Calibration doc `:352`: "The `file` field still carries claim text on 16 entries (13 pass A)" — live after P2. `MaxPostMergeNotVerified = 3` (`post_merge_reviewer.go:94`) cuts the *data* in three places (parser `:595`, citations `:681`, union `post_merge_passes.go:335`); the comment is the only render site. |

## Why D2 before D3
D2's duplicate run on 10-28 was stopped only because D3 mis-attributed PR#5931 to #5917 and held. Fixing D3 alone removes that accidental protection. D3 carries `Depends on: #{{D2}}` so the SDK poller holds it until D2's issue closes.

## Legs — pilot (daemon)

### LEG D1 — repo: qf-studio/pilot — title: fix(autopilot): a check-run with a terminal conclusion counts as completed even when GitHub leaves its status in_progress; one checkRunTerminal helper replaces the three direct status reads
probe: present qf-studio/pilot ^func \(m \*CIMonitor\) mapCheckStatus\(
probe: present qf-studio/pilot ^func \(m \*CIMonitor\) failedUnlistedChecks\(
probe: present qf-studio/pilot ^func \(m \*CIMonitor\) requiredCheckMismatch\(
probe: absent  qf-studio/pilot ^func checkRunTerminal\(

## Context

GitHub sometimes finishes an Actions job, writes `conclusion: success` on its check-run, and never flips `status` from `in_progress` to `completed`. The autopilot CI monitor in `internal/autopilot/ci_monitor.go` classifies each check-run in mapCheckStatus, which switches on status alone: queued and in_progress map to CIRunning, and the conclusion is read only inside the completed case. So a stuck run stays "running" through checkRequiredChecks, checkAllRuns, checkAutoDiscoveredRuns and GetCheckStatus, and handleWaitingCI in `internal/autopilot/controller.go` keeps the PR in waiting_ci until the CI timeout (the smaller of autopilot.ci_wait_timeout and the environment ci_timeout), then sets StageFailed with the pilot-failed label. Three more sites read run.Status against CheckRunCompleted directly and would also miss the stuck run: failedUnlistedChecks, requiredCheckMismatch and GetCheckLogs. This happened on 10-28 evening and again on 10-30 on PR#5981 (job "Knowledge Graph Drift Gate", run 41082510705, job 124155026674): the operator ran gh run rerun on the job and the PR merged two minutes later, after 12 minutes lost. The GitHub Checks API sets conclusion only when a run has finished, so a non-empty conclusion is a reliable terminal signal.

## Implementation

1. Add a package-level helper checkRunTerminal that takes a check-run and reports whether it is finished: status is completed, or the conclusion is non-empty. Place it beside mapCheckStatus in `internal/autopilot/ci_monitor.go` with a comment that names the GitHub behaviour and PR#5981.
2. In mapCheckStatus, before the switch, treat a run with a non-empty conclusion as completed so the existing conclusion mapping applies (success, skipped and neutral to CISuccess; the ciFailureConclusions table to CIFailure; anything else to CIPending). Log at Debug once per run name when the status was promoted, with the original status and the conclusion.
3. Replace the three direct status comparisons in failedUnlistedChecks, requiredCheckMismatch and GetCheckLogs with checkRunTerminal so a stuck failed run also blocks the merge and yields logs.
4. Tests, table-driven in `internal/autopilot/ci_monitor_test.go`: add rows to TestCIMonitor_MapCheckStatus for in_progress with success (CISuccess), in_progress with failure (CIFailure), queued with success (CISuccess), and in_progress with an empty conclusion (CIRunning, unchanged); TestCIMonitor_FailedUnlistedChecks_StuckFailure where an unlisted run is in_progress with failure and blocks; TestCIMonitor_RequiredCheckMismatch_StuckSuccess where a stuck success run does not trigger the mismatch. In `internal/autopilot/gh4851_ci_wait_confirm_poll_test.go` add TestHandleWaitingCI_StuckConclusion_ReachesCIPassed: past the deadline, the only required run is in_progress with success and the PR reaches StageCIPassed instead of the timeout error.

## Acceptance

- A required check-run with status in_progress and conclusion success makes CheckCI return CISuccess on the same tick.
- A required check-run with status in_progress and an empty conclusion still returns CIRunning.
- `go test -race ./internal/autopilot/ -run TestCIMonitor_MapCheckStatus` passes; paste the output into the PR body.
- `go test -race ./internal/autopilot/ -run TestHandleWaitingCI_StuckConclusion` passes; paste the output into the PR body.
- `go test -race ./internal/autopilot/ -run TestCIMonitor_FailedUnlistedChecks` passes; paste the output into the PR body.
- remove the conclusion promotion from mapCheckStatus -> the in_progress-with-success row of TestCIMonitor_MapCheckStatus fails
- leave failedUnlistedChecks on the direct status read -> the stuck-failure case of TestCIMonitor_FailedUnlistedChecks fails

## Out of scope

- Calling the GitHub rerun endpoint for a stuck job (the conclusion is already terminal; no API call is needed).
- The platform breaker and the CI timeout values.
- Infra-failure reruns (maybeRetryInfraFailure) and their budget.

## Refs

- .agent/tasks/TASK-624-daemon-defects-stuck-checkrun-restart-repick-hold-binding-body-cut.md
- .agent/tasks/TASK-623-reviewer-two-pass-hunk-and-adjacent-union.md

### LEG D2 — repo: qf-studio/pilot — title: fix(executor): boot reconciliation heals a running row with an open PR to completed instead of stalling it, and startup label recovery keeps pilot-in-progress on an issue whose branch has an open PR
probe: present qf-studio/pilot ^func \(d \*Dispatcher\) reconcileOrphanedExecutions\(
probe: present qf-studio/pilot ^func \(c \*Cleaner\) StartupRecover\(
probe: present qf-studio/pilot staleRunningOpenPRCheck
probe: present qf-studio/pilot HasExecutionEventStage\(
probe: absent  qf-studio/pilot ^func \(d \*Dispatcher\) healOpenPRExecution\(

## Context

After a daemon restart or hot self-upgrade on 10-28 evening the dispatcher re-dispatched an issue whose run had already pushed a branch and opened a PR that was waiting in CI. Only an unrelated hold stopped the duplicate run. The gap is in three steps. First, Dispatcher.reconcileOrphanedExecutions in `internal/executor/dispatcher.go` runs at the top of Start and turns every claimed queued or running row into stalled; the only PR it looks for is a merged one (the heal through mergedPRCheckBranch), while its sibling recoverStaleRunningTasks skips a row whose branch has an open PR through the staleRunningOpenPRCheck hook. Second, Cleaner.StartupRecover in `internal/adapters/github/cleanup.go` strips pilot-in-progress from every open issue without a running row younger than 30 minutes; unlike the periodic cleanup it does not keep the label when the latest row is finished, and it never asks whether the issue's branch has an open PR, and its OnStartupRecovered callback clears the poller's processed entry so the issue is offered again. Third, a stalled row is finished but not done, so nextRetryGeneration grants generation+1 under the stall re-pick cap. The window exists because the runner records the PR (the pr_created execution event in `internal/executor/runner.go`) and then runs a tail (pre-PR review label, webhook, Navigator sync) before ProjectWorker persists the row with its pr_url through ExecutionLifecycle.Persist; a restart in that window leaves a running row with an empty pr_url and a pr_created event. The executions table has pr_url, commit_sha and task_branch columns and no pr_number column; Store.HasExecutionEventStage reads the event; Store.MarkExecutionCompleted stamps pr_url, which Store.HasTerminalCompletion requires to count a completed row as done.

## Implementation

1. Add Dispatcher.healOpenPRExecution in `internal/executor/dispatcher.go`: given a claimed running row, return the open PR URL when staleRunningOpenPRCheck finds an open PR on the row's task branch, or when the row has a pr_created event (read the PR URL from the event payload when present, otherwise from the open-PR check). Call it in reconcileOrphanedExecutions right after the merged-PR heal, for running rows only: on a hit, mark the row completed with that pr_url through MarkExecutionCompleted, record a completed stage event, log at Info with task id, branch and PR URL, and skip the stall and the backoff reset for that row.
2. In Cleaner.StartupRecover in `internal/adapters/github/cleanup.go`, before stripping pilot-in-progress, keep the label and skip the OnStartupRecovered callback when either the latest execution row for the issue is finished (the same guard the periodic cleanup applies since GH-5299) or FindOpenPRByBranch reports an open PR on the pilot branch for that issue number. Log the kept label at Info with the reason.
3. Leave adoption of the open PR to the autopilot's existing reconcileOrphanPRs sweep; do not call OnPRCreated from the dispatcher.
4. Tests, table-driven. In `internal/executor/dispatcher_test.go`, modelled on TestRecoverStaleRunningTasks_SkipsWhenOpenPRExists and TestDispatcher_BootOrphanReconciliation_EnablesGenerationRetry: TestDispatcher_ReconcileOrphanedExecutions_OpenPRHealsToCompleted (a running row with an open PR on its branch is completed with that pr_url and nextRetryGeneration returns no retry); TestDispatcher_ReconcileOrphanedExecutions_PRCreatedEventHealsToCompleted (a running row with a pr_created event and no reachable PR check is completed); the existing stall case for a row with neither stays green. In `internal/adapters/github/cleanup_test.go`, beside TestCleaner_StartupRecover_StaleExecutionStripped: TestCleaner_StartupRecover_OpenPRKeepsLabel and TestCleaner_StartupRecover_FinishedRowKeepsLabel, each asserting the label stays and the callback is not invoked.

## Acceptance

- After a restart, a running row whose task branch has an open PR ends as completed with that PR URL and is not re-dispatched.
- After a restart, an issue whose pilot branch has an open PR keeps pilot-in-progress.
- `go test -race ./internal/executor/ -run TestDispatcher_ReconcileOrphanedExecutions` passes; paste the output into the PR body.
- `go test -race ./internal/adapters/github/ -run TestCleaner_StartupRecover` passes; paste the output into the PR body.
- `go test -race ./internal/executor/ -run TestNextRetryGeneration` passes; paste the output into the PR body.
- drop the open-PR heal from reconcileOrphanedExecutions -> TestDispatcher_ReconcileOrphanedExecutions_OpenPRHealsToCompleted fails
- strip the label regardless of the open PR in StartupRecover -> TestCleaner_StartupRecover_OpenPRKeepsLabel fails

## Out of scope

- Changing which statuses are terminal or done (terminalExecutionStatuses, HasTerminalCompletion).
- The periodic stale-running loop (already correct) and sweepStalledRearm.
- Shortening the post-PR tail in the runner.

## Refs

- .agent/tasks/TASK-624-daemon-defects-stuck-checkrun-restart-repick-hold-binding-body-cut.md
- .agent/tasks/TASK-404-execution-lifecycle-chokepoint.md

### LEG D3 — repo: qf-studio/pilot — title: fix(executor): the prerequisite hold attaches a PR to an issue only by closing reference or pilot branch, not by mention
probe: present qf-studio/pilot ^func \(p githubBasePresenceProbe\) LinkedPRNumbers\(
probe: present qf-studio/pilot ^func \(ghCLIBasePresenceProbe\) LinkedPRNumbers\(
probe: present qf-studio/pilot ^func \(c \*Client\) ClosingIssueNumbers\(
probe: present qf-studio/pilot ^func \(c \*Client\) SearchPRsForIssue\(
probe: absent  qf-studio/pilot ^func prDeliversIssue\(

Depends on: #{{D2}}

## Context

The base-presence hold in `internal/executor/base_presence.go` holds a task whose body says "Depends on #N" while #N has an open PR. To find #N's PRs, basePresenceChecker.checkRef calls the probe's LinkedPRNumbers and holds on any returned PR that is open, with the reason "referenced issue #N's attached PR #P is still open (not merged)". Both probes answer with a GitHub search: the production probe in `cmd/pilot/github_base_presence_probe.go` returns every hit of Client.SearchPRsForIssue (query repo:o/r is:pr #N, from `internal/adapters/github/client.go`), and the gh fallback in base_presence.go runs the same query through gh api. That query matches any PR that mentions #N in its title, body or comments. On 10-28 the hold logged "#5917's attached PR #5931" while PR#5931 closed #5927 and only mentioned #5917, so #5920 was held on the wrong PR. The correct binding is one of: the PR's closingIssuesReferences list contains N (Client.ClosingIssueNumbers already queries it), the PR body carries a GitHub closing keyword for #N (closes, closed, fixes, fixed, resolves, resolved, with an optional colon), or the PR's head branch is pilot/GH-N or starts with pilot/GH-N- (Client.GetPullRequest returns Body and Head.Ref). The executor package must not import autopilot or the GitHub adapter, so the three closing-reference regexes that already exist in autopilot cannot be reused there. This leg lands after #{{D2}} because the mention-based hold is what stopped the 10-28 duplicate run.

## Implementation

1. Add a pure function prDeliversIssue in `internal/executor/base_presence.go` that takes the PR's head branch, body and closing-reference numbers and an issue number, and reports whether the PR delivers that issue by any of the three rules above. The closing-keyword regex accepts an optional colon after the keyword and is case-insensitive.
2. In githubBasePresenceProbe.LinkedPRNumbers in `cmd/pilot/github_base_presence_probe.go`, keep SearchPRsForIssue as the candidate source, then for each candidate fetch ClosingIssueNumbers and GetPullRequest and keep only candidates for which prDeliversIssue is true. A lookup error on one candidate drops that candidate and logs at Warn; it never produces a hold.
3. In ghCLIBasePresenceProbe.LinkedPRNumbers in base_presence.go, apply the same filter with gh pr view on each candidate reading headRefName, body and closingIssuesReferences.
4. Update the BasePresenceProbe.LinkedPRNumbers doc comment to say "PRs that deliver issue N" and list the three rules.
5. Tests. In `cmd/pilot/github_base_presence_probe_test.go` extend TestGithubBasePresenceProbe_LinkedPRNumbers with an httptest server whose search returns a mention-only PR (body "see #5917", branch pilot/GH-5927) and a delivering PR (branch pilot/GH-5917), stubbing the pulls and graphql routes, and expect only the delivering PR. In `internal/executor/base_presence_test.go` add the table test TestPRDeliversIssue covering: closing-reference list hit; body "Closes #N"; body "Fixes: #N"; branch pilot/GH-N; branch pilot/GH-N-retry; a mention-only body; a branch pilot/GH-N1 for issue N (no match).

## Acceptance

- A PR that only mentions #N in its body is not returned by LinkedPRNumbers for N.
- A PR whose head branch is pilot/GH-N or whose body closes #N is returned.
- `go test -race ./cmd/pilot/ -run TestGithubBasePresenceProbe_LinkedPRNumbers` passes; paste the output into the PR body.
- `go test -race ./internal/executor/ -run TestPRDeliversIssue` passes; paste the output into the PR body.
- `go test -race ./internal/executor/ -run TestBasePresenceChecker_Check` passes; paste the output into the PR body.
- return the raw search results without the filter -> the mention-only case of TestGithubBasePresenceProbe_LinkedPRNumbers fails
- match the branch by prefix pilot/GH-N without the boundary -> the pilot/GH-N1 row of TestPRDeliversIssue fails

## Out of scope

- The same mention-based lookup in verifyChildrenShippedForClose in `internal/autopilot/epic_reconcile.go` (fallback after a branch lookup; separate leg if it bites).
- Unifying the three autopilot closing-reference regexes.
- The hold's escalation cycle count.

## Refs

- .agent/tasks/TASK-624-daemon-defects-stuck-checkrun-restart-repick-hold-binding-body-cut.md

### LEG D4 — repo: qf-studio/pilot — title: fix(executor): reviewer defect lines keep the claim out of the location field and every rendered field is cut at a sentence or word boundary with an explicit marker, so follow-up issues never carry a mid-sentence cut
probe: present qf-studio/pilot ^func parsePostMergeDefect\(
probe: present qf-studio/pilot ^func sanitizePostMergeField\(
probe: present qf-studio/pilot ^func formatPostMergeDefects\(
probe: present qf-studio/pilot ^func prePRField\(
probe: absent  qf-studio/pilot ^func cutAtBoundary\(

## Context

When the post-merge reviewer returns REQUEST-CHANGES, the feedback loop files a follow-up issue through FeedbackLoop.CreatePostMergeReviewIssue in `internal/autopilot/feedback_loop.go`, whose Defects section comes from formatPostMergeDefects in `internal/autopilot/post_merge_review.go`. On 10-28 issue #5937 carried two defect lines cut mid-sentence; both are exactly 200 bytes. The cause has two halves. The parser parsePostMergeDefect in `internal/executor/post_merge_reviewer.go` splits a defect line on the first of the postMergeDefectSeparators (comma, pipe, space-hyphen-space); when the model writes "file:line — claim" with an em dash, an en dash, "file:line: claim" or no separator, the claim text stays inside Location. The renderer then cuts Location at a literal 200 bytes and appends an ellipsis; sanitizePostMergeField and the pre-PR twin prePRField in `internal/executor/pre_pr_review_render.go` cut Claim and Evidence the same way at maxPostMergeFieldChars, by bytes, with no boundary. The two-pass union in `internal/executor/post_merge_passes.go` makes it worse in a different way: uniteTwoPassReviews rewrites Location to path:line through normalisePostMergeLocation and silently drops the claim text that was in it, so the rendered claim starts mid-sentence or is empty, while the one-usable-pass branch of reviewTwoPass does no rewrite and the 200-byte cut applies. The calibration report confirms the field still carries claim text on 16 of the run-7 entries. NOT-VERIFIED lines are capped at MaxPostMergeNotVerified in the parser, in enforcePostMergeCitations and in the union, which drops data that the comment, the CLI JSON and the ledger row never see.

## Implementation

1. In parsePostMergeDefect, take Location as the leading whitespace-free path token with its optional :line suffix (the same token rule normalisePostMergeLocation applies) and move any remaining text on the line into Claim, before the separator split; add the em dash, the en dash and ": " to postMergeDefectSeparators. Apply the same normalisation in the one-usable-pass branch of reviewTwoPass so both paths produce identical defects.
2. Add cutAtBoundary(s, max) beside sanitizePostMergeField: when s exceeds max bytes, cut at the last sentence end (". ", "; " or a newline) within max, otherwise at the last space, and append " (truncated)"; never cut inside a rune or a word. Use it in sanitizePostMergeField, in prePRField and for the location cap, and name that cap maxPostMergeLocationChars = 200.
3. In CreatePostMergeReviewIssue render each defect's full Claim; when Evidence exceeds maxPostMergeFieldChars, put it in a collapsed details block under a one-line summary instead of cutting it; bound the whole body at 16 KB with cutAtBoundary applied once at the end. The PR comment keeps its per-field caps.
4. Keep the full NOT-VERIFIED set on PostMergeReview and apply MaxPostMergeNotVerified only when rendering the comment (the untraced count line stays as the last rendered slot); the CLI JSON and the ledger row carry the full set.
5. Tests. In `internal/executor/post_merge_reviewer_test.go` add em-dash, en-dash, colon and no-separator rows to TestParsePostMergeDefectSeparators asserting Location is path:line and Claim holds the rest. In `internal/autopilot/post_merge_review_test.go` add TestCreatePostMergeReviewIssue_NoMidSentenceCut using the two verbatim 200-byte lines from the PR#5936 review comment as fixtures and asserting no line ends inside a word and every cut line ends with "(truncated)". In `internal/executor/post_merge_passes_test.go` add a union case where a pass-A defect written as "a.go:42 — the claim" keeps "the claim" after normalisation. Add TestCutAtBoundary as a table test in `internal/executor/post_merge_reviewer_test.go` (sentence end, word end, multi-byte rune at the cut, string under max unchanged); place cutAtBoundary in the executor package so the autopilot renderer and the pre-PR renderer share it.

## Acceptance

- A defect line "a.go:42 — the claim, the evidence" parses to Location a.go:42 and a Claim that starts with "the claim".
- No rendered defect field ends inside a word; a cut field ends with "(truncated)".
- `go test -race ./internal/executor/ -run TestParsePostMergeDefectSeparators` passes; paste the output into the PR body.
- `go test -race ./internal/autopilot/ -run TestCreatePostMergeReviewIssue` passes; paste the output into the PR body.
- `go test -race ./internal/executor/ -run TestPostMergeReviewTwoPass` passes; paste the output into the PR body.
- `go test -race ./internal/executor/ -run TestCutAtBoundary` passes; paste the output into the PR body.
- restore the byte cut in sanitizePostMergeField -> TestCreatePostMergeReviewIssue_NoMidSentenceCut fails
- leave claim text in Location for an em-dash line -> the em-dash row of TestParsePostMergeDefectSeparators fails

## Out of scope

- The ±4-line key tolerance and case-fold for the union (TASK-623 P3 residue).
- The reviewer prompt text and the verdict rule.
- The 2000-rune raw-log cap (logs only).

## Refs

- .agent/tasks/TASK-624-daemon-defects-stuck-checkrun-restart-repick-hold-binding-body-cut.md
- .agent/tasks/TASK-623-reviewer-two-pass-hunk-and-adjacent-union.md
- .agent/system/bench/post_merge_reviewer_calibration_2026-10.md

## Dispatched

- D1 → https://github.com/qf-studio/pilot/issues/5983
- D2 → https://github.com/qf-studio/pilot/issues/5984
- D3 → https://github.com/qf-studio/pilot/issues/5985
- D4 → https://github.com/qf-studio/pilot/issues/5986

## Operator steps (after the legs merge and a release lands)

- D1: watch the next stuck check-run; the daemon log should show the Debug promotion line and the PR should pass CI on the same tick. Keep `gh run rerun <run> --job <job>` as the manual fallback until one live instance is observed.
- D2: the next hot self-upgrade with a run in its post-PR tail should log the Info heal line and not re-dispatch; check `pilot-board` for a duplicate GH-N row.
- D4: the first REQUEST-CHANGES follow-up issue after the release should have no line ending in "…".

## Refs

- .agent/.context-markers/2026-10-29_reviewer-opus-calibration-runs-1-3-gates-fast-5946-5947-queued-compact.md (defect list, "Four daemon defects seen 10-28 evening")
- .agent/tasks/TASK-620-pre-pr-reviewer-replaces-self-review-and-judge.md (enforce read needs 20 two-pass runs — these legs feed the sample)
- .agent/tasks/TASK-623-reviewer-two-pass-hunk-and-adjacent-union.md
- .agent/tasks/TASK-508-post-merge-fix-borrowed-branch.md (adjacent unfiled autopilot follow-ups)

**Last Updated**: 2026-10-31
