# TASK-623: Reviewer two passes — a hunk pass and an adjacent pass, defects united by location, verdict from the union

**Status**: 🟢 DONE + LIVE — **run 7 (16:23–16:55Z, v2.304.0, `--passes 2`): recall 51.0 % again (A 34.6 / B 48.1 / union 51.0; 19 of 31 found by both), RC 3/3, false 0.07, 0 pass failures, 138 s, 9.6 files. Box on `passes: 2` for both reviewers since 16:21Z** (backup `config.yaml.bak-passes2-20261030`). Residue for an optional P3 (not planned): ±4-line key tolerance + case-fold (30 pairs un-united), single-pass location rewrite, render-only NV cap (cap eats signal), intra-pass-B collision on 3 ui PRs, empty-claim guard. ✅ P2 MERGED 10-30 16:01Z — pilot#5980 → PR#5981 (run 24m50, gates 3m38, shadow 1m54 / 4 files). Merge was held 12 min by a GitHub-side stuck check-run (`Knowledge Graph Drift Gate` status=in_progress with conclusion=success — the 10-28 daemon-defect class #2, recurring); `gh run rerun --job` cleared it. Pre-PR and post-merge both APPROVE-WITH-NOTES, three shared minors, unfiled: pre-merge budget check allows pass B with 19/20 of a timeout; the one-usable-pass branch does not rewrite Location to path:line; a column suffix `a.go:42:10` keys on the column; post-merge only: checkout time comes out of the 2×timeout budget so pass B can run short. None blocks run 7. Next: release → box `passes: 2` on both reviewers → run 7. · ✅ P1 MERGED + MEASURED — **run 6 (13:35–14:13Z, v2.303.0, `--passes 2`): recall 51.0 % — BAR MET** (pass A 34.6 %, pass B 42.3 %, union 51.0 %; RC 3/3, false 0.07, 162 s, 10.4 files). Six union-mechanics gaps to fix in one leg before the box switch (per-pass deadline = full timeout — pass A died on the 83 KB pilot#5868 diff at 150 s; location normalisation + range tolerance — 17 pairs not united; NOT-VERIFIED cap; union-rule verdict for one usable pass; no-files blocking demotion; `:\d+` line regex). Then box `passes: 2` on both reviewers; run 7 to confirm. P1 MERGED 2026-10-30 13:15Z — pilot#5977 → PR#5978 (run 25m15, gates 3m14, shadow 58 s / 3 files). Pre-PR and post-merge both APPROVE-WITH-NOTES with the SAME four minor items, two of them real union gaps, **unfiled**: (1) `post_merge_passes.go:128` — when only one pass is usable its own model verdict is returned without the union rule (APPROVE with minor defects stays APPROVE); (2) `:220` — a [blocking] defect from a pass that read no files keeps its severity in the union, so a [blocking] can sit under APPROVE-WITH-NOTES and still be filed by the feedback loop / fix round; (3) `:239` — NOT-VERIFIED union uncapped (up to 6 vs the prompt's 3); (4) `:160` — defectKey only recognises `:\d+` lines. Neither (1) nor (2) triggers when both passes run with a checkout, so **run 6 can go first**; file the four as one fix after. Next: release → box → run 6 with `--passes 2`. Was: 🚀 P1 DISPATCHED 2026-10-30 → pilot#5977 (`pilot`+`no-decompose`). Was: 📋 PLANNED 2026-10-30 (founder "two passes" after calibration run 5). Measure = run 6 on the same 14 PRs with `--passes 2`; pass bar unchanged (RC 3/3 · recall ≥ 0.5 · false ≤ 1); also report pass-A-only and pass-B-only recall from the same JSONs.
**Origin**: runs 4 and 5 (`system/bench/post_merge_reviewer_calibration_2026-10.md`): one pass finds 18–19 of 52 hand defects at ~5 files and ~70 s whatever the prompt tells it to look at. Run 5's adjacent-code item moved 5 misses to hits and 7 hits to misses (in-hunk items displaced): a fixed finding budget redistributed. The union of run 4 and run 5 outputs would have hit 24 + the partials — that is the number this task buys with a second pass.
**Spec refs**: `internal/executor/post_merge_reviewer.go` (`Review`, `PostMergeReview`, `PostMergeDefect{Severity, Location, Claim, Evidence}`, `parsePostMergeReview`, `enforcePostMergeCitations`, `downgradePostMergeNoFilesRead`, `postMergeChecklistItemAdjacent`, `SetMaxDiffChars` setter idiom, `postMergeReviewMaxTurns`), callers `internal/autopilot/post_merge_review.go` (daemon), `internal/executor/pre_pr_review.go` (pre-PR), `cmd/pilot/review_merge.go` (CLI JSON: `verdict`, `defects`, `not_verified`, `notes`, `files_read`, `duration_ms`), config `internal/autopilot/types.go` (`PostMergeReviewConfig`), `internal/executor/backend.go` (`PrePRReviewConfig`), `internal/config/config.go` (`validatePrePRReview`).

## Why two passes and not more turns
- Run 5 raised max-turns 30 → 40; files_read stayed 5.0 → 5.1 and time 68 → 72 s. The model stops when it has a verdict, not when the budget ends. A bigger budget alone is unlikely to change what it opens.
- Two passes with two different reading orders are the cheapest way to make the model open both the hunk and the one-hop neighbourhood. Cost: ~2× time (~140 s) and ~2× tokens per review. Pre-PR has a 6 m budget, post-merge 5 m: both fit two 70 s passes.
- The union must be deduplicated by location so one PR does not get the same defect twice, and the verdict must come from the union's severities, not from either pass's VERDICT line.

## Legs — pilot (daemon)

### LEG P1 — repo: qf-studio/pilot — title: feat(executor): post-merge and pre-PR reviewer run two passes — a hunk pass and an adjacent pass — and unite the defects by location with the verdict derived from the union; passes knob on both configs and the CLI
probe: present qf-studio/pilot ^func \(r \*PostMergeReviewer\) Review\(
probe: present qf-studio/pilot ^const postMergeChecklistItemAdjacent
probe: present qf-studio/pilot ^func \(r \*PostMergeReviewer\) SetMaxDiffChars\(
probe: absent  qf-studio/pilot ^func \(r \*PostMergeReviewer\) SetPasses\(

## Context

The reviewer in `internal/executor/post_merge_reviewer.go` runs one claude invocation per review: Review builds the system prompt (checklist items 1–5, item 2b from TASK-622 when a checkout exists, memories, the pre-merge sentence swap), runs it on the confined checkout, parses the VERDICT / DEFECTS / NOT-VERIFIED / NOTES text with parsePostMergeReview, enforces citations, downgrades when no file was read, and returns one PostMergeReview with FilesRead and Turns. Five calibration runs on the same 14 hand-reviewed PRs show the pass finds 18 to 19 of 52 hand defects at about 5 files read and 70 seconds, independent of the prompt: run 5's adjacent-code item (2b) turned 5 misses into hits and 7 hits into misses. The two reading orders find different defects; their union would have scored about 24 hits. The same Review method serves the daemon (`internal/autopilot/post_merge_review.go`), the pre-PR review (`internal/executor/pre_pr_review.go`, shadow mode, 6 minute budget) and the calibration CLI (`cmd/pilot/review_merge.go`, `--json`). Defects carry Severity, Location (file:line), Claim and Evidence; the verdict rule in the prompt is REQUEST-CHANGES only with a blocking defect, APPROVE-WITH-NOTES with minor defects or a non-empty NOT-VERIFIED list, APPROVE otherwise.

## Implementation

1. Add a `passes` setting (1 or 2, default 1) to PostMergeReviewConfig in `internal/autopilot/types.go` and to PrePRReviewConfig in `internal/executor/backend.go`, validated in `internal/config/config.go` beside the existing fields, documented in `configs/pilot.example.yaml` next to each block; a SetPasses setter on PostMergeReviewer in the SetMaxDiffChars idiom; the three construction sites pass the value through; `pilot review-merge` gains `--passes` (default from config, override 1 or 2).
2. Split the prompt into two variants built from the existing pieces: pass A (hunk) = the run-4 prompt — checklist items 1 to 5 with the TASK-619 item 2 and no item 2b; pass B (adjacent) = the same prompt with item 2b in place of item 2 and one added sentence: "The hunk itself has already been reviewed; spend your reads on the sibling functions, the untouched branches and the preflight or guard before each changed call." Keep every other sentence identical so the citation, severity and output rules apply to both.
3. In Review, when passes is 2, run pass A and then pass B on the same checkout and confinement (one checkout, one HOME, two claude invocations, each with its own max-turns and a share of the timeout: pass A gets the first half of the remaining time, pass B the rest, and a pass that times out contributes nothing but is recorded). Parse, enforce citations and apply the no-files-read downgrade per pass as today.
4. Unite: defects from both passes keyed by normalised Location (path with the checkout prefix stripped, line kept; a defect without a line keys on path plus the first 40 characters of the claim); on a key collision keep the higher severity and the longer evidence. NOT-VERIFIED lines are united as a set; NOTES are concatenated with a pass label. The verdict is derived from the union by the prompt's own rule (blocking present → REQUEST-CHANGES; any defect or any NOT-VERIFIED → APPROVE-WITH-NOTES; else APPROVE); the per-pass VERDICT lines are kept only in the raw output. Downgraded stays true when both passes were downgraded.
5. Record per-pass stats on PostMergeReview (FilesReadByPass, TurnsByPass, DurationByPass, DefectsByPass, and the overlap count) and surface them in the CLI JSON as `passes`, `files_read_by_pass`, `duration_ms_by_pass`, `defects_by_pass`, `overlap`; the daemon log line gains passes and overlap; the pre-PR execution event gains passes.
6. Tests, table-driven with the existing fake runner idioms: union keeps the higher severity on a collision; union of a blocking from pass B and minors from pass A yields REQUEST-CHANGES even when pass A said APPROVE; passes=1 produces byte-identical prompt and result to today; pass B timeout leaves pass A's result with passes recorded as 2 and the second pass marked failed; the pre-merge variant applies to both passes; the CLI JSON carries the per-pass fields.

## Acceptance

- With passes set to 2, one review makes exactly two claude invocations against one checkout, and the returned defects are the deduplicated union of both.
- With passes set to 1 (the default), the prompt and the result are unchanged from today.
- `go test -race ./internal/executor/ -run TestPostMergeReviewTwoPass` passes; paste the output into the PR body.
- `go test -race ./internal/executor/ -run TestPostMergeReviewer_PromptContract` passes; paste the output into the PR body.
- `go test -race ./cmd/pilot/ -run TestReviewMergePasses` passes; paste the output into the PR body.
- derive the verdict from pass A instead of the union -> TestPostMergeReviewTwoPass_VerdictFromUnion fails
- key the union on claim text instead of location -> TestPostMergeReviewTwoPass_UnionDedupesByLocation fails
- run pass B on a second checkout -> TestPostMergeReviewTwoPass_OneCheckout fails

## Out of scope

- Changing the checklist text of either pass beyond the one added sentence.
- Sampling (TASK-620 L6), enforce mode, the fix round.
- The box config switch (operator step below).

## Refs

- .agent/tasks/TASK-623-reviewer-two-pass-hunk-and-adjacent-union.md
- .agent/system/bench/post_merge_reviewer_calibration_2026-10.md
- .agent/tasks/TASK-622-reviewer-adjacent-code-tracing-and-met-failure-case.md

### LEG P2 — repo: qf-studio/pilot — title: fix(executor): two-pass reviewer — each pass gets the full review timeout, defect keys use the path token and first line number, NOT-VERIFIED union capped, union verdict rule on a single usable pass, no-files blocking demoted
probe: present qf-studio/pilot ^func uniteTwoPassReviews\(
probe: present qf-studio/pilot ^func defectKey\(
probe: present qf-studio/pilot time\.Until\(dl\)/2
probe: present qf-studio/pilot ^const MaxPostMergeNotVerified
probe: absent  qf-studio/pilot ^func derivePostMergeVerdict\(

## Context

Run 6 of the reviewer calibration (`.agent/system/bench/post_merge_reviewer_calibration_2026-10.md`) ran `pilot review-merge --passes 2` on the 14 hand-reviewed PRs and reached 51.0 percent recall, the first time the 0.5 bar was met. The same run and the reviews of PR#5978 (pre-PR and post-merge, both APPROVE-WITH-NOTES with the same four items) show six gaps in `internal/executor/post_merge_passes.go` that cost recall or corrupt the output. (1) reviewTwoPass gives each pass half the remaining context deadline (the time.Until(dl)/2 expression); on the 83 KB pilot#5868 diff pass A was killed at 150 seconds (`pass_failed [true,false]`, 0 files read) and the two hand defects only the hunk pass finds were lost. (2) defectKey keys on the whole Location string; pass A often writes the claim into the location, and `:589` does not match `:589-590`, so 17 same-finding pairs in run 6 were not united and the posted list carries duplicates while `overlap` understates agreement. (3) uniteTwoPassReviews appends NOT-VERIFIED lines from both passes with no cap, so a two-pass review carries up to six entries against the prompt's three (MaxPostMergeNotVerified in `internal/executor/post_merge_reviewer.go`). (4) When only one pass is usable, reviewTwoPass returns that pass's own model verdict (the len(usable) == 1 case) without re-deriving it by the union rule, so APPROVE with minor defects stays APPROVE; TestPostMergeReviewTwoPass_PassBTimeout in `internal/executor/post_merge_passes_test.go` locks this in. (5) A [blocking] defect from a pass that read no files keeps its severity in the union; the union only refuses to set the blocking flag, so a [blocking] line can sit under APPROVE-WITH-NOTES and the feedback loop and the fix round still act on BlockingDefects. (6) postMergeDefectLineRe only recognises a trailing `:\d+` (with ranges), so `a.go:L42` or `a.go:42 (func X)` falls back to path-plus-claim keying.

## Implementation

1. Timeout: give each pass the full review timeout, not half. In reviewTwoPass derive the per-pass budget as the reviewer's timeout bounded by the remaining context deadline; at the three call sites (`internal/autopilot/post_merge_review.go`, `internal/executor/pre_pr_review.go`, `cmd/pilot/review_merge.go`) size the review context to passes times the timeout. In the pre-PR path, when fewer than one timeout remains before pass B, skip it and record PassFailed with the reason "budget" in the log line instead of running a doomed pass.
2. Key normalisation: in defectKey take the location's first whitespace-delimited token as the path (a path has no spaces), strip the checkout prefix and a leading ./ as today, then take the first run of digits after the last colon as the line (accepting `:42`, `:L42`, `:42-50`, `:42,43`); key on path plus that first line; keep the path-plus-40-claim-characters fallback only when no digits follow the path. Store the normalised location back on the kept defect so the posted line reads `path:line`.
3. NOT-VERIFIED cap: in uniteTwoPassReviews stop appending at MaxPostMergeNotVerified entries, pass A's first; when entries were dropped, replace the last kept one with a count line in the same style the untraced cap uses.
4. Single usable pass: add derivePostMergeVerdict(review) that applies the prompt's rule (any blocking defect from a pass that read files → REQUEST-CHANGES; any defect or any NOT-VERIFIED → APPROVE-WITH-NOTES; else APPROVE) and call it from both the one-usable-pass branch and uniteTwoPassReviews, so the verdict never comes from a pass's VERDICT line when passes is 2. Fix TestPostMergeReviewTwoPass_PassBTimeout to expect the derived verdict.
5. No-files demotion: in the union loop, when the contributing pass has NoFilesRead, write the defect into the union as [minor] with the evidence suffixed "(pass read no files)"; on a collision such a defect never raises the kept severity.
6. Tests, table-driven beside the existing TestPostMergeReviewTwoPass cases: each pass's context deadline equals the full timeout (fake runner records the deadline); keys for `a.go:589 - the claim`, `a.go:589-590`, `a.go:L589`, `a.go:589 (func X)` all collide and `a.go:590` does not; the union carries at most three NOT-VERIFIED lines and the count line; a single usable pass whose text says APPROVE with two minor defects yields APPROVE-WITH-NOTES; a no-files pass's [blocking] arrives as [minor] and does not raise a kept defect; pre-PR skips pass B under budget and records the reason.

## Acceptance

- `go test -race ./internal/executor/ -run TestPostMergeReviewTwoPass` passes; paste the output into the PR body.
- `go test -race ./internal/executor/ -run TestPrePRReview` passes; paste the output into the PR body.
- Re-running the run-6 scoring arithmetic on a union of the run-6 raw pass outputs is out of scope; instead show in the PR body the defectKey table for the four location forms above.
- halve the per-pass deadline again -> TestPostMergeReviewTwoPass_FullTimeoutPerPass fails
- key on the whole location string -> TestPostMergeReviewTwoPass_KeyNormalisesLocation fails
- return the single pass's own verdict -> TestPostMergeReviewTwoPass_SinglePassDerivedVerdict fails
- keep [blocking] from a no-files pass -> TestPostMergeReviewTwoPass_NoFilesBlockingDemoted fails

## Out of scope

- The prompt text of either pass, the passes knob shape, the CLI JSON field names.
- The box config switch (operator step 3 below) and run 7.

## Refs

- .agent/tasks/TASK-623-reviewer-two-pass-hunk-and-adjacent-union.md
- .agent/system/bench/post_merge_reviewer_calibration_2026-10.md

## Operator steps (after the leg merges and a release lands)

1. ✅ Run 6 DONE 10-30 14:13Z — 51.0 %. Was: Run 6 on the box: copy `~/calibration-v5/calib.sh` to `~/calibration-v6/` (sed the path) and add `--passes 2` to the review-merge command; same 14 PRs; pull the JSONs; score with the run-5 scorer prompt verbatim. Report recall for the union and, from the per-pass fields, for pass A alone and pass B alone.
2. Pass bar: RC 3/3 · recall ≥ 0.5 · false ≤ 1. Expected from the run-4/run-5 union: about 24 HIT + partials ≈ 50–52 %.
3. If the union clears the bar: box `post_merge_review.passes: 2` and `executor.pre_pr_review.passes: 2`, then watch mean time (expect ~140 s; cap 20/day unchanged) and the pre-PR 6 m budget.
4. If the union does not clear the bar: the remaining misses are two hops out or in notes-only hand reviews; that is a different class and not another pass.

## Refs

- P1 → https://github.com/qf-studio/pilot/issues/5977
- P2 → https://github.com/qf-studio/pilot/issues/5980
