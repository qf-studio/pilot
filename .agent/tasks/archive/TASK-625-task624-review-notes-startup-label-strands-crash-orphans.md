# TASK-625: TASK-624 review notes — startup label guard strands crash orphans, pr_created heal on a closed PR, issue-body cut inside details, location with a space, closing keyword without a space

**Status**: ✅ MERGED + RELEASED — #5991 → PR#5993 merged 20:01Z (pre-PR AWN 8 minor; post-merge AWN), in v2.305.0 on the box 21:13Z. Unfiled note: `startupRowIsDone` calls HasTerminalCompletion on `latest.TaskID` from the fuzzy lookup, so an issue with no exact row can borrow another task's done status (GH-599 ← GH-5991); `cutAtBoundary`'s no-boundary branch returns the marker without a leading space so `TrimSuffix(" (truncated)")` misses it ("(truncated) (truncated)"); a claim that opens with a parenthesis right after path:line loses it. Was: 🚀 DISPATCHED 2026-10-31 → [#5991](https://github.com/qf-studio/pilot/issues/5991) (`pilot` + `no-decompose`); PR#5990 (D4) merged 19:27Z, probes clean. Founder 10-31: "Approve + file follow-up".
**Origin**: the pre-PR and post-merge reviews of TASK-624's four PRs (#5987 D1, #5988 D2, #5989 D3, #5990 D4). Each verdict was APPROVE-WITH-NOTES (D4 pre-PR: REQUEST-CHANGES on a spec error — the `pr_reviews` row has no NOT-VERIFIED field, so "the ledger row carries the full set" was unsatisfiable; the founder approved the PR). Five notes are real; one is a regression.

| # | From | Note | Verified 10-31 |
|---|---|---|---|
| F1a | #5988 post-merge | `startupKeepReason` (`internal/adapters/github/cleanup.go`) keeps `pilot-in-progress` when the latest row has any terminal status, including `stalled`. `dispatcher.Start` runs before `StartupRecover` and stalls every claimed running row without PR evidence. The SDK poller skips issues labelled `pilot-in-progress` (studio-sdk `poller.go:760`, `:1075`). So a crash orphan with no PR is never re-offered after a restart; before #5988 the boot strip was the only release (the periodic cleanup already keeps the label on finished rows since GH-5299). **Regression; bites on the next restart with a running task.** | yes, read on origin/main after #5988 |
| F1b | #5988 post-merge | `healOpenPRExecution` (`internal/executor/dispatcher.go:534`) heals a row to completed from any `pr_created` event URL even when the open-PR check just returned "" — i.e. the PR was closed unmerged. The healed row satisfies `HasCompletedExecution` and gets no retry. | yes |
| F1c | #5990 pre-PR | The single 16 KB body cut in `CreatePostMergeReviewIssue` runs on the finished markdown; it can land inside a `<details>` block and drops the Acceptance Criteria and Refs sections that follow the defects. | from the review; PR not yet on main |
| F1d | #5990 pre-PR | `parsePostMergeDefect` takes the first whitespace-free token as Location, so `x.go:7 (func Foo), claim, evidence` yields Claim "(func Foo)" and Evidence "claim, evidence". | from the review |
| F1e | #5989 post-merge | The closing-keyword regex in `PRDeliversIssue` (`internal/executor/base_presence.go:411`) requires whitespace before `#`, so `Fixes:#5917` and `closes#5917` do not match. | yes |
| F1f | #5987 post-merge | `stuckPromotionLogged` (`internal/autopilot/ci_monitor.go:72`) only grows; keys are never deleted. | yes |

Not taken: D3's per-candidate GraphQL + REST cost (bounded by the search's 100 hits; the hold runs once per queue tick on held tasks only) · D1's "once per run name" vs name+ID dedupe · test-shape notes.

## Legs — pilot (daemon)

### LEG F1 — repo: qf-studio/pilot — title: fix(executor): startup label guard keeps pilot-in-progress only on a done row or an open PR, the boot heal from a pr_created event requires the PR to be open or merged, the follow-up issue body is cut per section, a defect location with a space keeps its claim, and the closing keyword matches without a space
probe: present qf-studio/pilot ^func \(c \*Cleaner\) startupKeepReason\(
probe: present qf-studio/pilot ^func \(d \*Dispatcher\) healOpenPRExecution\(
probe: present qf-studio/pilot ^func PRDeliversIssue\(
probe: present qf-studio/pilot ^func cutAtBoundary\(
probe: present qf-studio/pilot ^func parsePostMergeDefect\(
probe: present qf-studio/pilot stuckPromotionLogged
probe: absent  qf-studio/pilot ^func \(c \*Cleaner\) startupRowIsDone\(

Depends on: #5986

## Context

The four TASK-624 fixes merged on 10-31 as PR#5987, PR#5988, PR#5989 and PR#5990; their reviews left five real notes and one regression. (a) Cleaner.startupKeepReason in `internal/adapters/github/cleanup.go` keeps the pilot-in-progress label when the latest execution row has any terminal status; because Dispatcher.Start runs before Cleaner.StartupRecover and reconcileOrphanedExecutions stalls every claimed running row without PR evidence, a crash orphan with no PR now keeps the label, and the SDK poller skips issues that carry it, so the issue is never re-offered after a restart — before PR#5988 the boot strip was the only release, since the periodic cleanup already keeps the label on finished rows. The store exposes HasTerminalCompletion, which is true only for a completed row with a deliverable, a clean no_op, canceled, superseded or needs_human; that is the predicate the guard should use. (b) Dispatcher.healOpenPRExecution in `internal/executor/dispatcher.go` returns the URL from the latest pr_created event even when the open-PR check on the branch found nothing, so a PR that was closed unmerged heals the row to completed and the task gets no retry; the event detail producers include the pre-push merge check and the timeout salvage. (c) FeedbackLoop.CreatePostMergeReviewIssue in `internal/autopilot/feedback_loop.go` applies the 16 KB bound once to the finished markdown, so the cut can land inside a details block and drops the Acceptance Criteria and Refs sections that follow the defects. (d) parsePostMergeDefect in `internal/executor/post_merge_reviewer.go` takes the first whitespace-free token as Location and the rest as the claim before the separator split, so a location written as "x.go:7 (func Foo), claim, evidence" yields Claim "(func Foo)" and pushes the real claim into Evidence. (e) The closing-keyword regex in PRDeliversIssue in `internal/executor/base_presence.go` requires whitespace before the hash, so "Fixes:#5917" and "closes#5917" do not match. (f) stuckPromotionLogged in `internal/autopilot/ci_monitor.go` is a sync.Map that only grows.

## Implementation

1. Add Cleaner.startupRowIsDone(taskID) that returns true when the store's HasTerminalCompletion is true for the task, and use it in startupKeepReason in place of the isPilotTerminalStatus check; a stalled, failed, infra or skipped latest row falls through to the open-PR check and, with no open PR, the label is stripped as before PR#5988. Keep the open-PR branch unchanged.
2. In healOpenPRExecution, when the open-PR check returns no URL and a pr_created event carries one, verify that PR before healing: resolve its state through the same git operations the open-PR check uses (open or merged heals; closed unmerged does not), and when the state cannot be read, do not heal and log at Warn with the URL. A merged PR heals with the merged URL.
3. In CreatePostMergeReviewIssue, bound the body per section instead of once at the end: cut the Defects section with cutAtBoundary to its own budget before the Acceptance Criteria and Refs sections are appended, close any open details block before the cut marker, and keep the two trailing sections intact; keep a final hard bound at 60 KB (GitHub's limit is 65536) as a safety net only.
4. In parsePostMergeDefect, accept a parenthesised suffix directly after the path:line token, such as "x.go:7 (func Foo)", as part of the Location (normalised away by the existing location rule) rather than as the claim, and only then move the remaining text into Claim.
5. In PRDeliversIssue, make the whitespace before the hash optional, so the keyword, an optional colon, optional spaces and the hash all match; keep the word boundary after the number.
6. In mapCheckRun, delete the stuckPromotionLogged key once the run is observed completed with status completed, so the map tracks only currently stuck runs.
7. Tests, table-driven beside the existing ones. In `internal/adapters/github/cleanup_test.go`: TestCleaner_StartupRecover_StalledRowWithoutPRStripsLabel (stalled latest row, no open PR: label removed, OnStartupRecovered called) and TestCleaner_StartupRecover_DoneRowKeepsLabel (completed row with pr_url: label kept); update TestCleaner_StartupRecover_FinishedRowKeepsLabel to use a done row. In `internal/executor/dispatcher_test.go`: TestDispatcher_ReconcileOrphanedExecutions_ClosedPRFromEventDoesNotHeal (pr_created event, open-PR check empty, PR state closed: row stalled). In `internal/autopilot/post_merge_review_test.go`: TestCreatePostMergeReviewIssue_KeepsTrailingSectionsUnderCut (defects over budget: body still ends with the Refs section and has balanced details tags). In `internal/executor/post_merge_reviewer_test.go`: a "x.go:7 (func Foo), claim, evidence" row in TestParsePostMergeDefectSeparators expecting Location x.go:7 and Claim "claim". In `internal/executor/base_presence_test.go`: "Fixes:#5917" and "closes#5917" rows in TestPRDeliversIssue expecting a match and "closes #59170" expecting no match. In `internal/autopilot/ci_monitor_test.go`: TestCIMonitor_StuckPromotionLogCleared (a run seen stuck then completed leaves no key).

## Acceptance

- After a restart, an issue whose latest row is stalled and whose branch has no open PR loses pilot-in-progress and is re-offered.
- After a restart, an issue whose latest row is completed with a PR URL keeps pilot-in-progress.
- A running row whose only PR evidence is a pr_created event for a closed unmerged PR is stalled, not healed.
- `go test -race ./internal/adapters/github/ -run TestCleaner_StartupRecover` passes; paste the output into the PR body.
- `go test -race ./internal/executor/ -run TestDispatcher_ReconcileOrphanedExecutions` passes; paste the output into the PR body.
- `go test -race ./internal/autopilot/ -run TestCreatePostMergeReviewIssue` passes; paste the output into the PR body.
- `go test -race ./internal/executor/ -run TestParsePostMergeDefectSeparators` passes; paste the output into the PR body.
- `go test -race ./internal/executor/ -run TestPRDeliversIssue` passes; paste the output into the PR body.
- keep the isPilotTerminalStatus check in startupKeepReason -> TestCleaner_StartupRecover_StalledRowWithoutPRStripsLabel fails
- heal from the pr_created URL without the state check -> TestDispatcher_ReconcileOrphanedExecutions_ClosedPRFromEventDoesNotHeal fails
- apply the 16 KB cut once at the end of the body -> TestCreatePostMergeReviewIssue_KeepsTrailingSectionsUnderCut fails

## Out of scope

- The periodic cleanup's GH-5299 guard (unchanged: a finished row keeps the label there by design, the dispatcher re-picks from the ledger).
- D3's per-candidate lookup cost and the search page size.
- The pre-PR review PR-body section and the NOT-VERIFIED render cap.

## Refs

- .agent/tasks/TASK-625-task624-review-notes-startup-label-strands-crash-orphans.md
- .agent/tasks/TASK-624-daemon-defects-stuck-checkrun-restart-repick-hold-binding-body-cut.md

## Dispatched

- F1 → https://github.com/qf-studio/pilot/issues/5991

## Refs

- .agent/tasks/TASK-624-daemon-defects-stuck-checkrun-restart-repick-hold-binding-body-cut.md
- PR#5987 / PR#5988 / PR#5989 / PR#5990 review comments (post-merge-reviewer, pre-pr-reviewer)

**Last Updated**: 2026-10-31
