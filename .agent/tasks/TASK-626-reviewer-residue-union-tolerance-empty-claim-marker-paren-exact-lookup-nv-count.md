# TASK-626: Reviewer residue — union line tolerance and case-fold, empty-claim guard, truncation marker, parenthesised hint kept, exact task lookup at startup, NOT-VERIFIED count as an extra bullet, counter and log fixes

**Status**: ✅ MERGED + RELEASED — #5996 → PR#5997 merged 10-31 22:09Z (pre-PR AWN; post-merge AWN, 10 minor notes untriaged, no fix issue); v2.306.0 cut by hand 11-01 13:15Z. Was: 🚀 DISPATCHED 2026-10-31 → [#5996](https://github.com/qf-studio/pilot/issues/5996) (`pilot` + `no-decompose`, pre-flight clean on aadfa658e). Founder 10-31: "Yes, dispatch it" (Pilot idle after v2.305.0; each run feeds the TASK-620 enforce sample).
**Origin**: TASK-623 P3 residue (bench report §"Decision inputs after seven runs": 30 same-finding pairs un-united by ±1–4 lines or case; intra-pass-B collisions on 3 ui PRs; ui#246 defect 7 empty claim) · TASK-625's three unfiled pre-PR notes on PR#5993 · PR#5950's post-merge notes (10-29). Traced 2026-10-31 on main aadfa658e.

| # | Item | Site (file:line on aadfa658e) | Today |
|---|---|---|---|
| R1 | Union line tolerance + case-fold | `internal/executor/post_merge_passes.go:245` `defectKey` = path token + first digit run after the last colon; `:315` `uniteTwoPassReviews` keeps one `index map[string]int` shared by both passes; `:258` `overlapCount` same map rule | `a.go:589` and `a.go:590` never unite; path never case-folded; the one-usable-pass branch of `reviewTwoPass` (`:134-141`) does not dedupe at all |
| R2 | Empty claim | `parsePostMergeDefect` accepts `a.go:1` with an empty Claim; the union collision (`post_merge_passes.go:342`) copies an empty pass-B Claim over a real pass-A Claim; `internal/autopilot/post_merge_review.go:419` `formatPostMergeDefects` and `internal/executor/pre_pr_review_render.go:71` print `- [blocking] at a.go:10 — ` with a dangling dash | only `formatPostMergeIssueDefects` guards |
| R3 | Truncation marker | `internal/executor/post_merge_reviewer.go:803` `postMergeTruncatedMarker = " (truncated)"`; the no-boundary branch (`:834`) returns it trimmed, `"(truncated)"`; `internal/autopilot/post_merge_review.go:461` `cutPostMergeDefects` does `TrimSuffix(cut, " (truncated)")` then re-appends → `"(truncated) (truncated)"`; `sanitizePostMergeField` / `prePRField` on a space-free over-cap field return only the marker and lose the content | edge cases, but two of them lose content |
| R4 | Parenthesised hint | `post_merge_reviewer.go:726` `postMergeLocationParenRegex` drops `(…)` after path:line; `"a.go:3 (nil map) panics on write, ev"` → Claim "panics on write" | the hint is lost, not moved |
| R5 | Exact task lookup | `internal/adapters/github/cleanup.go:686` `startupRowIsDone` and the stale-label gate at `:429` call `GetLatestExecutionByTaskID(taskID, "")`; its SQL (`internal/memory/store.go:944`) is `task_id = ? OR task_id LIKE '%taskID%'` with no project scope → GH-15 with no row borrows GH-150's (or another repo's GH-15) done status and keeps a crash-orphan label | no exact variant returns an `*Execution` |
| R6 | NOT-VERIFIED count line | `internal/autopilot/post_merge_review.go:523` `formatPostMergeNotVerified` (PR#5994) renders `Max-1` real entries and puts the count line in the last slot → with 3 model items + 1 untraced the third model item is hidden; `hidden` is computed from the slice, not from what was written; `RenderPrePRReviewSection` (`pre_pr_review_render.go:75`) has no cap | PR#5950's note, still true at render |
| R7 | Counter + log (PR#5950 notes) | `post_merge_review.go:286` writes `no_files_read` into the same counter as the verdict labels (a downgraded review counts twice in a sum); the "downgraded to APPROVE-WITH-NOTES" log line runs before `enforcePostMergeCitations`, so a citation downgrade is never logged | |

Not taken: PR#5950's "uncited [blocking] met defect is lowered" — by design (`enforcePostMergeCitations` moves it to NOT-VERIFIED; a defect that cites a path:line in its Location is never affected).

## Legs — pilot (daemon)

### LEG R1 — repo: qf-studio/pilot — title: fix(executor): two-pass union unites defects within four lines on the same case-folded path and dedupes a single usable pass; empty claims never render a dangling dash or overwrite a real claim; the truncation marker is one constant; a parenthesised hint joins the claim; startup done-check uses an exact task lookup; NOT-VERIFIED count is an extra bullet; no_files_read has its own counter and the citation downgrade is logged
probe: present qf-studio/pilot ^func defectKey\(
probe: present qf-studio/pilot ^func uniteTwoPassReviews\(
probe: present qf-studio/pilot ^func formatPostMergeDefects\(
probe: present qf-studio/pilot postMergeTruncatedMarker
probe: present qf-studio/pilot postMergeLocationParenRegex
probe: present qf-studio/pilot ^func \(c \*Cleaner\) startupRowIsDone\(
probe: present qf-studio/pilot ^func formatPostMergeNotVerified\(
probe: present qf-studio/pilot ^func \(s \*Store\) GetLatestExecutionByTaskID\(
probe: absent  qf-studio/pilot postMergeLineTolerance *= *[0-9]
probe: absent  qf-studio/pilot ^func \(s \*Store\) GetLatestExecutionByExactTaskID\(

## Context

Seven items of reviewer residue, each traced on main. (1) In `internal/executor/post_merge_passes.go`, defectKey keys a defect on the path token plus the first run of digits after the last colon, and uniteTwoPassReviews unites the two passes through one map shared by both, so the same finding at a.go:589 and a.go:590 stays two defects, a path that differs only by case stays two keys, and the one-usable-pass branch of reviewTwoPass never dedupes; calibration run 7 counted 30 same-finding pairs un-united for exactly these reasons, and overlapCount uses the same rule. (2) parsePostMergeDefect in `internal/executor/post_merge_reviewer.go` accepts a line that is only a location, so a defect can carry an empty Claim; on a union collision the incoming Claim overwrites the kept one even when it is empty; formatPostMergeDefects in `internal/autopilot/post_merge_review.go` and RenderPrePRReviewSection in `internal/executor/pre_pr_review_render.go` then print the location, a dash and nothing. (3) cutAtBoundary's no-boundary branch returns the truncation marker without its leading space while cutPostMergeDefects in post_merge_review.go trims the spaced form and re-appends it, which can print the marker twice; sanitizePostMergeField and prePRField return only the marker for a space-free field over the cap, losing the content. (4) postMergeLocationParenRegex drops a parenthesised hint that follows path:line, so "a.go:3 (nil map) panics on write" loses "(nil map)". (5) Cleaner.startupRowIsDone in `internal/adapters/github/cleanup.go` and the stale-label gate in the same file look up the latest row with Store.GetLatestExecutionByTaskID and an empty project, whose SQL in `internal/memory/store.go` also matches task_id LIKE the pattern with no project scope; an issue with no exact row borrows another task's or another repo's done status and keeps its crash-orphan label. (6) formatPostMergeNotVerified renders at most MaxPostMergeNotVerified minus one real entries and puts the count line in the last slot, so a real model entry is hidden whenever the list is over the cap; the hidden count comes from the slice length, not from the lines written; the pre-PR section has no cap. (7) The post-merge review counter records no_files_read as a verdict label, and the "downgraded to APPROVE-WITH-NOTES" log line runs before enforcePostMergeCitations, so a citation downgrade is never logged.

## Implementation

1. Union tolerance. Add the constant postMergeLineTolerance = 4 in post_merge_passes.go. In defectKey, lower-case the path. In uniteTwoPassReviews, replace the shared map with per-path buckets of kept indices: a defect with a line unites with a kept entry from the other pass on the same case-folded path whose anchor line is within postMergeLineTolerance (nearest wins, earliest on a tie; the anchor stays the first entry's line, no chaining); a defect from the same pass unites only on an exact key; a defect without a line keeps today's path-plus-claim key. Apply the same matcher in overlapCount so Overlap agrees with the union. In the one-usable-pass branch of reviewTwoPass, dedupe the defects by exact key.
2. Empty claim. In the union collision, never replace a non-empty kept Claim with an empty one (severity and location may still be upgraded). In formatPostMergeDefects and RenderPrePRReviewSection, when the Claim is empty, print the location and the evidence without the dash; name the shared guard postMergeNoClaim.
3. Marker. Export PostMergeTruncatedMarker and make cutAtBoundary return that exact string in every branch (with its leading space when something precedes it, trimmed only when it is the whole result); make cutPostMergeDefects trim and re-append that constant instead of a local literal; in sanitizePostMergeField and prePRField, when the cut would leave only the marker, keep the first max minus marker-length bytes of the field at a rune boundary before the marker so content is never replaced by the marker alone.
4. Parenthesised hint. In parsePostMergeDefect, capture the parenthesised text that postMergeLocationParenRegex removes and prepend it to the Claim ("(nil map) panics on write"); name the helper splitPostMergeLocationHint. Update the two existing parser cases that expect the hint to be dropped.
5. Exact lookup. Add Store.GetLatestExecutionByExactTaskID(taskID, projectPath) in `internal/memory/store.go` with the same query minus the LIKE clause (project optional as today). Use it in startupRowIsDone and in the stale-label gate; when it returns no row, treat the issue as having no row (strip as before). Leave GetLatestExecutionByTaskID and its test unchanged for other callers.
6. NOT-VERIFIED count. In formatPostMergeNotVerified render up to MaxPostMergeNotVerified real entries and, when any remain, add one extra bullet with the count, computed from the lines actually written (an entry that sanitises to empty is neither written nor counted as hidden); name the count-line builder postMergeNotVerifiedCountLine. Apply the same cap and count bullet in RenderPrePRReviewSection.
7. Counter and log. Record no_files_read on its own counter (or a separate label dimension that is not summed with verdicts) so the verdict sum counts each review once; move the "downgraded to APPROVE-WITH-NOTES" log line after enforcePostMergeCitations in the review path so a citation downgrade is logged with its reason.
8. Tests, table-driven beside the existing ones. In `internal/executor/post_merge_passes_test.go`: TestPostMergeReviewTwoPass_UnionLineTolerance (a.go:589 from pass A and a.go:591 from pass B unite; a.go:589 and a.go:594 do not; A.go and a.go unite; two pass-A defects at lines 10 and 13 stay separate; Overlap equals the united count); TestPostMergeReviewTwoPass_SinglePassDedupes; TestPostMergeReviewTwoPass_UnionKeepsNonEmptyClaim. In `internal/autopilot/post_merge_review_test.go`: TestFormatPostMergeDefects_EmptyClaimNoDash; TestCutPostMergeDefects_MarkerOnce; TestFormatPostMergeNotVerified_CountIsExtraBullet (3 model items + 1 untraced renders all 3 and a count bullet of 1). In `internal/executor/post_merge_reviewer_test.go`: a "(nil map)" row in TestParsePostMergeDefectSeparators expecting Claim "(nil map) panics on write"; a no-space over-cap row in TestCutAtBoundary that keeps leading content. In `internal/memory/store_test.go`: TestGetLatestExecutionByExactTaskID (GH-15 with no row returns no row while GH-150 exists). In `internal/adapters/github/cleanup_test.go`: TestCleaner_StartupRecover_NoExactRowStripsLabel (GH-15 label, only a done GH-150 row: label stripped). In `internal/executor/pre_pr_review_render_test.go`: TestRenderPrePRReviewSection_EmptyClaimAndNotVerifiedCap.

## Acceptance

- Two defects on the same path within four lines from different passes render as one defect, and Overlap counts it.
- A defect with an empty Claim renders as the location followed by the evidence line, with no dash.
- An issue whose only matching row belongs to another task number loses its stale pilot-in-progress label at startup.
- `go test -race ./internal/executor/ -run TestPostMergeReviewTwoPass` passes; paste the output into the PR body.
- `go test -race ./internal/autopilot/ -run TestFormatPostMergeNotVerified` passes; paste the output into the PR body.
- `go test -race ./internal/autopilot/ -run TestCutPostMergeDefects` passes; paste the output into the PR body.
- `go test -race ./internal/memory/ -run TestGetLatestExecutionByExactTaskID` passes; paste the output into the PR body.
- `go test -race ./internal/adapters/github/ -run TestCleaner_StartupRecover` passes; paste the output into the PR body.
- `go test -race ./internal/executor/ -run TestParsePostMergeDefectSeparators` passes; paste the output into the PR body.
- compare lines exactly in the union -> TestPostMergeReviewTwoPass_UnionLineTolerance fails
- let the tolerance match within the same pass -> the lines-10-and-13 case of TestPostMergeReviewTwoPass_UnionLineTolerance fails
- keep the LIKE clause in the exact lookup -> TestGetLatestExecutionByExactTaskID fails
- put the count line in the last slot -> TestFormatPostMergeNotVerified_CountIsExtraBullet fails

## Out of scope

- The reviewer prompt text, the verdict rule and enforcePostMergeCitations' "met" rule.
- A third pass or any budget change.
- Other callers of GetLatestExecutionByTaskID.

## Refs

- .agent/tasks/TASK-626-reviewer-residue-union-tolerance-empty-claim-marker-paren-exact-lookup-nv-count.md
- .agent/tasks/TASK-623-reviewer-two-pass-hunk-and-adjacent-union.md
- .agent/tasks/TASK-625-task624-review-notes-startup-label-strands-crash-orphans.md
- .agent/system/bench/post_merge_reviewer_calibration_2026-10.md

## Dispatched

- R1 → https://github.com/qf-studio/pilot/issues/5996

## Refs

- .agent/tasks/TASK-623-reviewer-two-pass-hunk-and-adjacent-union.md (P3 residue)
- .agent/tasks/TASK-625-task624-review-notes-startup-label-strands-crash-orphans.md (unfiled notes)
- PR#5950 post-merge review comment (10-29)

**Last Updated**: 2026-10-31
