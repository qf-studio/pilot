# TASK-591: Pilot post-merge reviewer — the daemon reviews each merged Pilot PR against its issue and posts a verdict

**Status**: ✅ MERGED (PR#5868 → `018e450d0`, autopilot, 18:5xZ) + **tier-1 post-merge review POSTED 2026-10-26 ~19:40Z: APPROVE-WITH-NOTES** (https://github.com/qf-studio/pilot/pull/5868#issuecomment-6425323288). Non-blocking defects: daily cap is per controller (box bound ≈ projects × 20) · follow-up issue needs `no-decompose` + plain-text locations (Rule 3c/3b) · attempt-once drops the review on a transient failure · CIResult is pre-merge · `localMergedDiff` untested. Notes: shutdown does not await reviews; markdown-wrapped VERDICT → unknown (watch the counter); bench verdict counts mix reviewers. **Model: claude-opus-5-5 since 2026-10-29 12:17Z** (was executor default claude-sonnet-5-5; calibration run 2 showed recall 34.9 % on Sonnet — see `.agent/system/bench/post_merge_reviewer_calibration_2026-10.md`). **Knob ON since 2026-10-27 16:47Z** (`orchestrator.autopilot.post_merge_review: enabled: true, daily_cap: 20, timeout: 5m`; backup `config.yaml.bak-pmr-20261027`; graceful restart, log `autopilot post-merge reviewer enabled daily_cap=20`). Next: watch the first verdict and the `unknown` counter. Earlier: PR#5868 hand-opened 2026-10-26 18:10Z from the gate-passed head 3d8f958 of run 2 (both runs died at PR create on the expired task ctx — #5867); `pilot` label removed, queued row canceled; autopilot adopted PR#5868 18:17Z (stage → merges on green). **Post-merge: tier-1 hand review, then flip autopilot.post_merge_review.enabled on the box.** Was: dispatched → pilot#5861 (`pilot`+`no-decompose`).
**Spec refs**: TASK-460 (delivery-evidence audit: green CI is not proof the requested change shipped); pitfall mem-274; existing secondary-model idiom `internal/executor/intent_judge.go`.
**Origin**: founder call 2026-10-26 — "why are we reviewing PRs all the time": autopilot merges on green CI; manual post-merge reviews found 3 REQUEST-CHANGES and 26 follow-ups on 10 merges (10-22). Move the review into the daemon; the human reads verdicts.

## Context

Autopilot handles a merged PR in handleMerged (`internal/autopilot/controller.go`), runs the learning loop, then either drops the PR or moves it to StagePostMergeCI; PRState carries IssueNumber, HeadSHA, PostMergeSHA and a MergeNotificationPosted guard. The only review code today is `internal/autopilot/review_ledger.go`, which collects human or agent GitHub reviews into pr_reviews after merge; it generates none. The secondary-model idiom is `internal/executor/intent_judge.go`: it shells out to claude with the prompt on stdin, has a fixed system prompt and a verdict struct, and writes no executions row, so the daily ticket cap in `internal/executor/daily_cap.go` does not count it. Autopilot files issues through `internal/autopilot/feedback_loop.go` (CreatePilotIssue with the configured issue labels, dedup by exact title). Config lives on autopilot.Config in `internal/autopilot/types.go` and is validated in `internal/config/config.go`; the example file is `configs/pilot.example.yaml`.

## Legs — pilot (daemon)

### LEG R1 — repo: qf-studio/pilot — title: feat(autopilot): post-merge reviewer — after a Pilot PR merges, a model pass reviews the diff against the issue and posts VERDICT plus defects on the PR; REQUEST-CHANGES files a labeled follow-up issue

## Context
Autopilot merges on green CI. Manual post-merge reviews on 2026-10-22 found 3 REQUEST-CHANGES and 26 follow-ups across 10 merges; the dominant class is a PR that binds to wire fields its counterpart never sends (memory mem-274) while unit tests mock the adapter. The daemon has the pieces: handleMerged in `internal/autopilot/controller.go` sees every merged Pilot PR and already guards a one-time comment with PRState.MergeNotificationPosted; `internal/executor/intent_judge.go` is a secondary model pass (stdin prompt, fixed system prompt, parsed verdict) that writes no executions row and so is outside the daily ticket cap in `internal/executor/daily_cap.go`; `internal/autopilot/feedback_loop.go` files labeled issues with title dedup; `internal/autopilot/review_ledger.go` stores reviews in pr_reviews. Nothing generates a review today.

probe: present qf-studio/pilot func \(c \*Controller\) handleMerged
probe: present qf-studio/pilot MergeNotificationPosted
probe: present qf-studio/pilot func \(j \*IntentJudge\) Judge
probe: present qf-studio/pilot func \(f \*FeedbackLoop\) CreateReviewIssue
probe: absent  qf-studio/pilot PostMergeReviewPosted
probe: absent  qf-studio/pilot post_merge_review

## Scope
- Knob: autopilot.post_merge_review on autopilot.Config in `internal/autopilot/types.go` — enabled (default false), model (default: the executor's configured model), daily_cap (default 20 reviews per day, its own counter, never the ticket cap), timeout. Validated in `internal/config/config.go`, documented in `configs/pilot.example.yaml`.
- Trigger: in handleMerged, after the learning loop and before the SkipPostMergeCI branch, when enabled: run the review in a goroutine guarded by a new PRState.PostMergeReviewPosted that persists with the other PRState fields and survives a restart.
- Reviewer: new file internal/executor/post_merge_reviewer.go modelled on the intent judge (same command runner, stdin prompt, parsed output). Inputs: the issue title and body (PRState.IssueNumber), the merged diff truncated per file with the existing diff section helper, the post-merge CI result when known, and the evidence block from the PR body when present. The system prompt is a fixed checklist: (1) does the diff do what the issue asks, bullet by bullet; (2) for every wire field the diff reads or emits, name the counterpart producer or consumer file and confirm the field exists there — a field with no producer is a defect (mem-274); (3) tests: do they exercise the real adapter or a mock; (4) migrations: down path, data loss, RLS; (5) anything the issue's out-of-scope section forbids. Output contract: first line VERDICT: APPROVE or APPROVE-WITH-NOTES or REQUEST-CHANGES; then DEFECTS: lines of file:line, claim, evidence; then NOTES:.
- Posting: one PR comment with the verdict and defects through the GitHub client's comment method. On REQUEST-CHANGES, file one follow-up issue through the feedback loop with the configured issue labels plus a new label post-merge-review, title prefixed with the PR number so the exact-title dedup holds; the issue body carries the defects list and links the PR. Never re-open or revert the PR.
- Ledger: store the verdict in pr_reviews through the review ledger's upsert so bench defect-rate tracking sees it (reviewer id post-merge-reviewer).
- Budget: reviews count against their own daily_cap, not the ticket cap; when the cap is reached, log once and skip.
- Observability: a counter per verdict in the existing Prometheus registry; one log line per review with PR number, verdict, defect count and duration.

## Acceptance
- [ ] Controller test: a merged PR with the knob enabled runs the reviewer once; a second handleMerged pass for the same PR does not (PostMergeReviewPosted); with the knob disabled nothing runs. Remove the PostMergeReviewPosted guard -> TestHandleMerged_PostMergeReviewRunsOnce fails.
- [ ] Reviewer test with a fake command runner: the parsed verdict and defects match a fixture output; a malformed output yields verdict unknown and posts nothing.
- [ ] Posting test: REQUEST-CHANGES files exactly one issue with labels pilot and post-merge-review and a title that starts with the PR number; APPROVE files none; both post one comment.
- [ ] Cap test: the 21st review in a day is skipped and logged; the ticket cap counter is unchanged by reviews.
- [ ] `go test -race ./internal/autopilot/ -run PostMergeReview` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/executor/ -run PostMergeReview` passes; paste the output into the PR body.
- [ ] Do not re-run make build, make test or make lint as evidence: the daemon's quality gates already run them on this repo (21–36 min on the box), and a second run overruns the 1-hour task budget before the PR is created.

## Out of scope
- Reviewing non-Pilot PRs.
- Reverting or re-opening a merged PR.
- Replacing the human review of tier-1 merges (migrations, gate removals, auth, public routes) — the verdict is an input to that, not a substitute.

## Refs

## Refs

- R1 → https://github.com/qf-studio/pilot/issues/5861
- Pilot issue: https://github.com/qf-studio/pilot/issues/5861

**Last Updated**: 2026-10-26
