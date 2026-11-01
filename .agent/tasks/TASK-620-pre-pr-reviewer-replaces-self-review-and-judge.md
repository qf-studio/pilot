# TASK-620: the reviewer moves before the PR — one in-run review after the gates with a resumed fix round; self-review and the in-run intent judge retire; post-merge becomes a sampled audit

**Status**: 🟢 SHADOW LIVE ON TWO PASSES · **L8 (minors sink) DISPATCHED 2026-11-01 → [#6007](https://github.com/qf-studio/pilot/issues/6007)** (`pilot`+`no-decompose`; file_minors default off, box opt-in after merge) — 10-30 16:21Z the box runs `pre_pr_review.passes: 2` (same prompt and budget as the post-merge reviewer, which measured 51.0 % recall twice in runs 6–7; pre-PR max 197 s inside the 6 m budget). **Founder 10-30 17:20Z: KEEP SHADOW.** Criterion 2 read against the ledger: 11 single-pass shadow runs over 1 working day (need ≥ 20 PRs, ≥ 5 days); error/unknown/exhausted 0 of 11; verdict-class agreement 8 of 9 paired PRs (89 %); pre-PR RC rate 1 of 11 (9 %, band 10–40); median added ≈ 1 min; zero CreatePR deadline failures. **The two-pass sample counts from 16:21Z 10-30** (all earlier shadow runs were single-pass). Rule: flip to enforce after 20 two-pass shadow runs or 5 working days (≈ 2026-11-03), whichever first, if the four rate criteria still hold; the one disagreement so far (PR#5966, a blocking defect the single pass missed) is the case enforce exists for. Earlier: 🟢 L1 MERGED + SHADOW LIVE 2026-10-29 — pilot#5949 → PR#5951 merged 14:42Z (Opus APPROVE-w-notes: runner call to runPrePRReview untested end-to-end; judge-off gate untested; mode≠off + missing claude binary = no in-run review at all; memory recall gated on MemoryInjection — fold into L2/L3 scope). v2.299.0 by the train 15:00Z; box config `executor.pre_pr_review: {mode: shadow, model: claude-opus-5-5, timeout: 6m}` loaded by the hot-upgrade reload 15:08:46Z (`Pre-PR review initialized mode=shadow`; backup `config.yaml.bak-prepr-shadow-20261029`). **L2 → pilot#5956 → PR#5957 MERGED 16:05Z** (run 15:28→15:51Z, 22m54). **FIRST SHADOW VERDICT 15:50:39Z on that run: `mode=shadow verdict=REQUEST-CHANGES blocking=1 minor=1 files_read=6 duration=50.6s`** — the run pushed anyway (shadow) and autopilot merged. **Gap found: L1 persists counts only** (the `pre_pr_review` execution event carries mode/verdict/blocking/minor/files_read/duration_ms, no defect text), so the shadow verdict cannot be compared with the post-merge one until L2's PR-body section is on the box. **Post-merge Opus on PR#5957: REQUEST-CHANGES ×2 blocking** — `RecordPrePRReview` (the `pilot_pre_pr_reviews_total` counter) is never called in production; the prometheus test injects the increment itself; 2 minor (misplaced godoc; "Pre-PR review: verdict" goes to the log line, not the learning record) → daemon fix #5958 → **PR#5959 merged 16:38Z** (post-merge Opus APPROVE-w-notes: gateway prometheus test loop checks nothing; the wiring is covered in executor). GH-5958's own shadow verdict: `APPROVE-WITH-NOTES blocking=0 minor=2 files_read=6 49.9s`, gates 2m47. **v2.300.0 cut by hand 16:51Z** (founder call; main green at a371c0cfc) → **box hot-upgraded 16:55:27Z, `Pre-PR review initialized mode=shadow model=claude-opus-5-5` — L1+L2 LIVE.** **L3 → pilot#5961 + L5 → pilot#5962 DISPATCHED 17:10Z** (`pilot`+`no-decompose`; the daemon serialises them on this repo; their own PRs are the first with the `## Pre-PR review` section). **L5 #5962 → PR#5963 merged 17:49Z — FIRST END-TO-END L2 PROOF + FIRST SIDE-BY-SIDE:** run 9m43 (scope 17/61, gates 2m49); PR body carries `## Pre-PR review` (Mode shadow · Verdict APPROVE-WITH-NOTES · Fix none · 3 minor · 2 not-verified), no label (correct, not RC); `pr_reviews` rows: `pre-pr-reviewer` APPROVE-w-notes 17:38:35 (58 s, 3 files) and `post-merge-reviewer` APPROVE-w-notes 17:50:36. **Both found the same two defects** (pre-PR reason appended after the live-smoke segment breaks `liveSmokeBulletsFromReason`'s last-segment assumption → fake live-smoke bullet in metadata + Telegram; the `pre_pr_review_hold` event has no test); pre-PR alone: the gate ignores the `pilot-review-rc` label; post-merge alone: no combined-hold test case. **L3 #5961 → PR#5964 (CI `lint` failed: unused test field; box lint gate passed — see TASK-616) → autopilot #5965 → PR#5966 MERGED 19:01Z** (title-type gate escalated, founder approved in Slack 18:59Z). **FIRST PRE-PR MISS OF A BLOCKING DEFECT:** pre-PR on GH-5961 (76 s, 7 files) and on GH-5965 (46 s, 4 files) both APPROVE-w-notes; post-merge Opus 19:03Z REQUEST-CHANGES [blocking] — `runReviewFix` creates `gitCtx` with the 30 s `selfReviewHygieneTimeout` (pre_pr_fix.go:144) and reuses it after the fix session for `GetCurrentCommitSHA` (:176), so after any real fix (up to 10 m) the HEAD read fails and the round is reset + recorded failed; the test's fake backend returns instantly so it never shows → daemon fix #5968 → **PR#5970 merged 20:15Z** (Opus APPROVE-w-notes: the test lowers `reviewFixGitTimeout` to 100 ms package-wide — flake risk on slow runners). L3 chain complete on main; L7 PR#5969 merged 19:54Z. **v2.301.0 cut by hand 20:28Z; box 20:36:45Z after the GH-4683 drain (admission paused 20:32Z, GH-5971 finished first) — L1+L2+L3+L5+L7 LIVE, mode still shadow.** L3's fix round is inert in shadow (it only runs in enforce). GH-5971 (TASK-621) shadow verdict APPROVE-w-notes 0/1, 39 s → PR#5973. Same reviewer, same prompt (one sentence swapped), same turn budget — the miss is run variance / fewer files opened, not a design gap; 0/2 pre-PR vs 1/1 post-merge on this defect is a calibration row. Enforce exit criteria NOT met by this. The ordering defect → **L7 #5967 filed 18:05Z** (`pilot`+`no-decompose`: pre-PR append moved above live-smoke, parser cuts at the pre-PR prefix, combined-hold test, hold-event test with a real store). Pre-PR not-verified: no producer writes `Fix: fixed` until L3; an RC-only hold that times out follows `default_action` (not fail-closed like live-smoke) — founder question. After both merge + a release: box `pre_pr_review.mode: enforce` only when the exit criteria below are met (founder call); then L4 (#5961 dep) and L6 (#5962 dep), both gated on the enforce exit criteria. Was: Then L5 ∥ L3 (`#5961`/`#5962` placeholders remain; L2's number is filled in); plan approved by the founder (plan file `~/.claude/plans/let-s-use-nav-research-pure-twilight.md`). Dispatch order L1 → L2 (shadow live) → L5 ∥ L3 → enforce → L4 → L6.
**Spec refs**: nav-research 2026-10-29 (in-run phases) + stage-machine map; `.agent/system/bench/post_merge_reviewer_calibration_2026-10.md` (runs 1–3); TASK-591 (reviewer), TASK-612/613/614/618/619 (reviewer chain); `internal/executor/runner.go` 6614–6838 (today's self-review ∥ judge block), `internal/executor/post_merge_reviewer.go`, `internal/executor/post_merge_checkout.go`, `internal/autopilot/controller.go` handleCIPassed 3760–3890, `internal/autopilot/post_merge_review.go`.
**Origin**: founder 2026-10-29 "why review after CI at all?" — gates, CI, evidence and self-review check the PR against itself; only the reviewer checks it against the system; a post-merge REQUEST-CHANGES costs a new issue cycle (25–45 min; four in a row on 10-28).

## Founder decisions (2026-10-29)
1. Shadow mode already replaces self-review and the in-run intent judge (net +3 min per run).
2. A clean fix round (REVIEW_FIXED + re-gate green) merges on green; the post-merge audit force-samples it. No approval hold.
3. The 12-minute remaining-budget floor for the fix round is accepted; `executor.timeout.complex` stays 60 m.

## Why (verified at main 5d3ecc6e8)
- Placement "daemon review holds the merge" is dead: isTrustedReviewer (controller.go ~5000) drops bot and own-login reviews (GH-5326); no change-request review call in the client; the review-fix path closes the PR and opens a new one.
- Placement "in-run, before push" has the plumbing: confined read-only checkout (post_merge_checkout.go), session resume (`use_session_resume: true` on the box; backend_claudecode.go ~558), gate re-check idiom (GH-4876), reset idiom (runner.go ~6327), escalate-only approval chain in handleCIPassed, ledger `pr_reviews` reachable from the executor (`Runner.logStore`), `ghAddLabels` for PR labels.
- Today's self-review: fresh `claude -p`, full tools, advisory, 2 m. Intent judge: Haiku, box cap 8,000 chars, no tools, verdict never reaches the PR.
- Budget: complex 60 m; the review (6 m) runs on the finalize ctx; the fix (≤10 m) only with ≥12 m left; the re-gate (5 m) on a fresh ctx; everything after already runs on detached windows (GH-5346/5867/5891). Worst case +21 m, APPROVE case +3 m net.

## Legs — pilot (daemon)

### LEG L1 — repo: qf-studio/pilot — title: feat(executor): pre-PR review in shadow mode — executor.pre_pr_review config, pre-merge prompt variant, runPrePRReview after the quality gates records a verdict and skips self-review and the in-run judge
probe: present qf-studio/pilot type IntentJudgeConfig struct
probe: present qf-studio/pilot func createPostMergeCheckout\(
probe: present qf-studio/pilot has ALREADY merged
probe: absent  qf-studio/pilot type PrePRReviewConfig struct
probe: absent  qf-studio/pilot func \(r \*Runner\) runPrePRReview\(

## Context

Pilot runs a self-review phase and an intent judge after the quality gates (`internal/executor/runner.go`, the parallel block around lines 6614–6838) and, since v2.296.0, a post-merge reviewer after autopilot merges (`internal/executor/post_merge_reviewer.go`, run by `internal/autopilot/post_merge_review.go`). Three calibration runs show the post-merge reviewer is the only stage that checks a change against the system (callers, wire producers, conventions), and that a REQUEST-CHANGES after merge costs a new issue cycle. The founder decided the review moves before the push, inside the run. This leg adds the review in shadow mode: it runs after the gates, records a verdict, writes nothing to GitHub, and already replaces the self-review phase and the in-run intent judge when its mode is not off. Default off: no behaviour change. The reviewer is the existing PostMergeReviewer with a pre-merge prompt variant and the existing detached read-only checkout from `internal/executor/post_merge_checkout.go`.

## Implementation

1. Config: new PrePRReviewConfig on BackendConfig in `internal/executor/backend.go` beside IntentJudgeConfig: mode (off, shadow, enforce; default off), model (empty = executor default), timeout (default 6m), fix_timeout (default 10m, used by a later leg), max_fix_rounds (default 1), max_diff_chars (0 = the reviewer default 200000), skip_complexity (default [trivial]). Validate in `internal/config/config.go` in the executor block: mode in the set, durations non-negative. Add EffectiveSkipSelfReview() returning true when mode is not off or skip_self_review is set.
2. Prompt variant: add PreMerge to PostMergeReviewInput in `internal/executor/post_merge_reviewer.go`. When set, Review() replaces the sentence that says the pull request has already merged with one that says the change is about to be pushed and has passed the quality gates, drops the paragraph about the PR body and its Evidence block, and renders CIResult under a heading named Gate Results. Everything else in the prompt stays byte-identical so calibration still applies.
3. Reviewer wiring: a PrePRReviewer interface (Review(ctx, PostMergeReviewInput) (*PostMergeReview, error)) satisfied by *PostMergeReviewer; build it in NewRunnerWithConfig beside the intent-judge wiring with the configured model, timeout and max_diff_chars, with the same claude LookPath warning. New file in prose: internal/executor/pre_pr_review.go with PrePRReviewOutcome (Mode, Verdict, Review, Skipped reason, Err, Duration, FilesRead) and runPrePRReview(ctx, task, executionPath, git, state, gates).
4. runPrePRReview: inputs are the task title, the issue body (task description), the full diff against origin and the base branch from the git helper, the current commit SHA from GetCurrentCommitSHA (never the literal HEAD; createPostMergeCheckout wants a resolved SHA), memories via graphrecall.RecallRelevant and the executor's renderMemoryBlock keyed on title plus description plus touched file paths, and CIResult as one line per QualityGateResult (name, passed, duration). Call Review() on a context with the configured timeout derived from the re-derived finalize context. Record one execution event with stage pre_pr_review carrying mode, verdict, blocking and minor counts, files read, duration; store the outcome on ExecutionResult.PrePRReview; log one Info line "Pre-PR review" with those fields, or "Pre-PR review skipped" with the reason.
5. Guard G1 (the review runs only when all hold): mode not off; task.CreatePR; not task.DirectCommit; task.Branch non-empty; quality gates passed or no checker factory; complexity not in skip_complexity; diff non-empty; at least 2 m left in the finalize window. A verdict of unknown or exhausted is recorded as no verdict.
6. Replace: when mode is not off, the self-review phase and the in-run intent judge do not run (gate both on EffectiveSkipSelfReview / the pre-PR mode); the self-review hygiene step still runs so the tree stays clean. Shadow mode never starts a fix round.
7. Stages: add pre_pr_review and pre_pr_fix stage constants in `internal/memory/store.go` beside the existing execution-event stages.
8. Docs: the key in `configs/pilot.example.yaml` next to intent_judge with the three modes explained, and a paragraph in `docs/content/getting-started/configuration.mdx`.
9. Tests, table-driven; fakes read their arguments (the mocks gate): the fake reviewer asserts PreMerge is true, MergeCommitSHA equals the worktree HEAD SHA, ProjectPath equals the execution path, CIResult names each gate; the recording backend (guardRecordingBackend idiom in backend_execute_guard_test.go) shows self-review still runs at mode off and does not run at mode shadow.

## Acceptance

- With mode off, a run behaves as today: the self-review backend call happens and no reviewer call happens.
- With mode shadow, after the gates pass, the reviewer is called once with PreMerge set, the worktree HEAD SHA and the execution path; the run continues to push and PR as today; no label or comment is written.
- A trivial task skips the review with reason recorded.
- `go test -race ./internal/executor/ -run TestRunPrePRReview` passes; paste the output into the PR body.
- `go test -race ./internal/executor/ -run TestPostMergeReviewerPrompt_PreMergeVariant` passes; paste the output into the PR body.
- `go test -race ./internal/config/ -run TestPrePRReviewConfig` passes; paste the output into the PR body.
- drop the PreMerge sentence swap -> TestPostMergeReviewerPrompt_PreMergeVariant fails
- pass task.ProjectPath to the reviewer instead of the execution path -> TestRunPrePRReview_Shadow_RecordsOutcomeNoFix fails
- let self-review run when mode is shadow -> TestRunPrePRReview_Shadow_SkipsSelfReview fails
- run the review when mode is off -> TestRunPrePRReview_OffIsNoop fails

## Out of scope

- Anything written to GitHub (PR body section, label) — L2.
- The fix round — L3. Retiring self-review code — L4. Autopilot hold — L5. Sampling — L6.

## Refs

- .agent/tasks/TASK-620-pre-pr-reviewer-replaces-self-review-and-judge.md
- .agent/system/bench/post_merge_reviewer_calibration_2026-10.md

### LEG L2 — repo: qf-studio/pilot — title: feat(executor): pre-PR verdict in the PR body, pilot-review-rc label on a REQUEST-CHANGES, pr_reviews row as pre-pr-reviewer, and counters
probe: present qf-studio/pilot func ghAddLabels\(
probe: present qf-studio/pilot func \(s \*Store\) UpsertPRReview\(
probe: absent  qf-studio/pilot func RenderPrePRReviewSection\(

## Context

Depends on #5949. The pre-PR review (shadow mode) records a verdict on the execution result but writes nothing anyone can see. This leg makes the verdict visible and comparable: a section in the PR body, a label when the verdict is REQUEST-CHANGES, a row in the pr_reviews ledger under a distinct reviewer id so shadow runs can be compared with the post-merge reviewer's rows on the same PR, and two counters. The PR body section must be machine-parseable, because autopilot (L5) and the post-merge sampler (L6) read it; the parser follows the LiveSmokeNotVerifiedBullets idiom in `internal/executor/acceptance_evidence.go`.

## Implementation

1. New file in prose: internal/executor/pre_pr_review_render.go with RenderPrePRReviewSection(outcome) producing an H2 "Pre-PR review" with Mode, Verdict, each defect on one line in the post-merge format with its severity tag, a Not-verified list, a Fix line (reserved; "none" until L3), and ParsePrePRReviewSection(prBody) returning mode, verdict, fix outcome and ok.
2. In `internal/executor/runner.go`, append the section at the three PR-body build sites after appendAcceptanceEvidence (direct GitHub, MR creator, gh CLI). After CreatePR returns a URL: when the verdict is REQUEST-CHANGES call ghAddLabels on the PR number with pilot-review-rc (extractPRNumberFromURL from `internal/executor/git.go`); upsert a pr_reviews row through r.logStore.UpsertPRReview with reviewer id pre-pr-reviewer (new const beside the post-merge id in `internal/executor/post_merge_reviewer.go`), state COMMENTED, review_id = the negated 62-bit FNV-64 of execution id plus ":pre-pr" (post-merge rows negate the comment id; pre-PR has none), verdict through a shared LedgerVerdict helper moved from autopilot (autopilot delegates to it). The learning string gains "Pre-PR review: verdict".
3. New file in prose: internal/executor/pre_pr_review_metrics.go with mutex-map counters exported from `internal/gateway/prometheus.go` as pilot_pre_pr_reviews_total by verdict and pilot_pre_pr_fixes_total by outcome (outcome labels reserved for L3).
4. Tests, table-driven: render and parse round trip; label added only on REQUEST-CHANGES (gh shim on PATH as the title-rejection tests do); ledger row with a real temp store asserting reviewer id, negative review_id and pr_number; the prometheus families appear.

## Acceptance

- A shadow run that ends REQUEST-CHANGES produces a PR whose body has the Pre-PR review section and the pilot-review-rc label, and a pr_reviews row with reviewer pre-pr-reviewer.
- A shadow run that ends APPROVE-WITH-NOTES produces the section and no label.
- `go test -race ./internal/executor/ -run TestRenderPrePRReviewSection` passes; paste the output into the PR body.
- `go test -race ./internal/executor/ -run TestPrePRReviewLedgerRow` passes; paste the output into the PR body.
- `go test -race ./internal/executor/ -run TestPrePRReviewLabel` passes; paste the output into the PR body.
- add the label on APPROVE-WITH-NOTES too -> TestPrePRReviewLabel_OnlyOnRC fails
- use a positive review_id -> TestPrePRReviewLedgerRow_ShadowWritesPrePRReviewerID fails
- change the section heading -> TestParsePrePRReviewSection_RoundTrip fails

## Out of scope

- The fix round, the autopilot hold, sampling.

## Refs

- #5949

### LEG L3 — repo: qf-studio/pilot — title: feat(executor): pre-PR review fix round in enforce mode — one session-resumed fix on blocking defects, one gate re-check, reset to the pre-fix SHA on failure
probe: present qf-studio/pilot ResumeSessionID
probe: present qf-studio/pilot func \(r \*Runner\) buildRetryPrompt\(
probe: absent  qf-studio/pilot func \(r \*Runner\) runReviewFix\(

## Context

Depends on #5956. In enforce mode a REQUEST-CHANGES with at least one blocking defect gets one fix attempt in the same conversation before the push. The executor already holds the session id from the stream init event (BackendResult.SessionID) and the claude-code backend supports --resume (`internal/executor/backend_claudecode.go`, with the GH-2377 fallback that retries without resume when the session is gone); the box runs with use_session_resume on. The gate-retry prompt builder in `internal/executor/prompt_builder.go` (buildRetryPrompt) tells the model to delete its approach and start over, so it must not be reused: the fix prompt is narrow. The fix call must go through r.backendExecute, which enforces ProjectPath equals the execution path (the GH-4702 phantom-reimplementation guard); never the reviewer's confined environment. Budget: the fix runs only when at least 12 minutes of task time remain, on a context that is the minimum of fix_timeout and the remaining time; the re-gate runs on a fresh 5-minute context like the GH-4876 retry pass; everything after already runs on detached windows, so push, evidence and PR creation cannot starve (the GH-5861/5867 class).

## Implementation

1. New file in prose: internal/executor/pre_pr_fix.go with runReviewFix(taskCtx, taskCtxWasDone, task, executionPath, git, state, outcome) returning ReviewFixOutcome (Status one of fixed, no_change, skipped_ctx, skipped_budget, failed, reverted, regate_error; PreFixSHA; PostFixSHA; Fixed list; Elapsed).
2. Guard G2: mode enforce, verdict REQUEST-CHANGES, at least one blocking defect, rounds below max_fix_rounds, not taskCtxWasDone, taskCtx not done, and (no deadline or at least 12 m remain). fixCtx = WithTimeout(WithoutCancel(taskCtx), min(fix_timeout, remaining)) with the cancel propagation idiom from finalizeCtx.
3. The fix call: r.backendExecute with a new buildReviewFixPrompt in `internal/executor/prompt_builder.go` listing the blocking defects as file:line, claim, evidence, then "fix only these; no refactor; no new files unless a defect needs one; do not delete tests; commit; end with REVIEW_FIXED: list or REVIEW_UNFIXABLE: reason"; ExecuteOptions with ResumeSessionID = state.sessionID when the backend config has use_session_resume, the executor's tool options, the liveness policy, the heartbeat and an event handler that extracts commit SHAs. Dead-man attempt, success and failure events around it; rename the self-review tracker in `internal/executor/alerts.go` to a review_fix tracker and update its registration in `cmd/pilot/poller_github.go`.
4. Tree hygiene: generalise the self-review hygiene in `internal/executor/self_review_hygiene.go` into a phase-named helper; the fix round's commit message is "chore(review-fix): apply pre-PR review fixes"; it runs in worktree mode too, because the worktree is what gets pushed. No commit after the fix means no_change: the tree is reset and the verdict stays REQUEST-CHANGES.
5. Guard G3 re-gate: only when HEAD moved; the quality checker's Check on a fresh 5-minute context. G4: on not passed or error, git ResetHardToCommit to PreFixSHA (the idiom around runner.go line 6327), status reverted, verdict stays REQUEST-CHANGES.
6. The PR body's Fix line (L2 renderer) carries the status; the pre-PR note that the self-review path used to add is now fed by the fix round. The execution event for stage pre_pr_fix carries status, both SHAs and elapsed; counters by outcome.
7. Tests, table-driven with the guardRecordingBackend idiom (fakes read their arguments): the fix call carries ResumeSessionID and the execution path and the prompt lists each blocking file:line; skipped when the task context is done; skipped under 12 minutes remaining; the fix context deadline is the minimum of fix_timeout and remaining; a failed re-gate resets HEAD to PreFixSHA in a real temp repo; no commit is no_change; the tracker name.

## Acceptance

- In enforce mode a REQUEST-CHANGES with a blocking defect leads to exactly one more backend call that resumes the session and runs in the execution path, followed by one gate check.
- When the re-gate fails, HEAD equals the pre-fix SHA and the verdict stays REQUEST-CHANGES.
- When fewer than 12 minutes of task time remain, no fix call happens and the outcome says skipped_budget.
- `go test -race ./internal/executor/ -run TestReviewFix` passes; paste the output into the PR body.
- drop the 12-minute floor -> TestReviewFix_SkippedUnder12mRemaining fails
- reset to HEAD instead of the pre-fix SHA -> TestReviewFix_RegateFailureResetsToPreFixSHA fails
- call the backend with task.ProjectPath -> TestReviewFix_UsesResumeSessionAndExecutionPath fails
- reuse buildRetryPrompt for the fix -> TestReviewFix_PromptListsBlockingDefectsOnly fails

## Out of scope

- A second review pass after the fix (max_fix_rounds stays 1). Retiring the old phases — L4.

## Refs

- #5956

### LEG L5 — repo: qf-studio/pilot — title: feat(autopilot): a PR labelled pilot-review-rc whose pre-PR review section says REQUEST-CHANGES escalates to awaiting_approval from handleCIPassed, escalate-only and fail-open
probe: present qf-studio/pilot func SizeFloorReason\(
probe: present qf-studio/pilot func LiveSmokeNotVerifiedBullets\(
probe: absent  qf-studio/pilot func PreReviewReason\(

## Context

Depends on #5956 (the section parser). When the pre-PR review ends REQUEST-CHANGES and the run pushes anyway, a human must decide before the merge. Autopilot's handleCIPassed in `internal/autopilot/controller.go` already runs an escalate-only chain (size floor, scope drift, test evidence, live smoke) that sets an escalation reason and moves the PR to awaiting_approval (`internal/autopilot/scope_guard.go` documents the pattern: gates only escalate, never relax). The PR body is already fetched there for the live-smoke hold. This leg adds one more reason from the Pre-PR review section. Per the founder decision, a section whose Fix line says fixed does not hold: a clean fix round merges on green and the post-merge audit samples it.

## Implementation

1. PreReviewReason(logger, prBody) in `internal/autopilot/scope_guard.go` beside SizeFloorReason: parse the section with the executor's ParsePrePRReviewSection; return a reason prefixed "Pre-PR review requested changes:" only when the verdict is REQUEST-CHANGES and the fix outcome is not fixed; empty for APPROVE, APPROVE-WITH-NOTES, a fixed outcome, a missing section or a parse failure (fail-open).
2. In handleCIPassed, after the live-smoke block and using the already fetched PR body, append the reason to the escalation chain; record an execution event from ci_passed to awaiting_approval with the reason; the approval release path is the existing one.
3. Tests, table-driven: the reason function over the verdict and fix variants; handleCIPassed with a fake GitHub client returning a body with the section ends in awaiting_approval with the reason set (model: the live-smoke test in gh5568_test.go); a GetPullRequest error does not escalate.

## Acceptance

- A PR whose body section says REQUEST-CHANGES with no fix goes to awaiting_approval with a reason that starts with "Pre-PR review requested changes".
- A PR whose section says REQUEST-CHANGES with Fix: fixed merges on green.
- `go test -race ./internal/autopilot/ -run TestPreReviewReason` passes; paste the output into the PR body.
- `go test -race ./internal/autopilot/ -run TestHandleCIPassed_PreReview` passes; paste the output into the PR body.
- return a reason for APPROVE-WITH-NOTES -> TestPreReviewReason_RCBlocksMergeOnlyWhenRC fails
- hold when the fix outcome is fixed -> TestPreReviewReason_FixedDoesNotHold fails

## Out of scope

- Any change to the approval channel or the size guard.

## Refs

- #5956

### LEG L4 — repo: qf-studio/pilot — title: refactor(executor): retire the self-review phase and the in-run intent judge now that the pre-PR review covers both; keep the pre-flight issue judge
probe: present qf-studio/pilot func \(r \*Runner\) runSelfReview\(
probe: present qf-studio/pilot func buildSelfReviewPrompt\(
probe: present qf-studio/pilot func \(j \*IntentJudge\) JudgeIssue\(

## Context

Depends on #5961 and on the enforce exit criteria in TASK-620 (do not dispatch before the founder marks them met). With the pre-PR review and its fix round live, the self-review phase (runSelfReview in `internal/executor/runner.go`, buildSelfReviewPrompt in `internal/executor/prompt_builder.go`) and the in-run intent judge (Judge in `internal/executor/intent_judge.go`, wired in the runner's parallel block) duplicate work: the self-review's build and lint checks are the quality gates, and the judge's text-only verdict reaches only a log string and a Telegram line. The pre-flight issue judge (JudgeIssue, used by `cmd/pilot/poller_github.go`) stays; IntentWarning stays because the pre-push lint path also writes it.

## Implementation

1. Delete runSelfReview and its caller block, buildSelfReviewPrompt, the in-run Judge call, the judge wiring, SetIntentJudge, HasIntentJudge and the intentRetried state; keep the pre-PR review call in their place; keep JudgeIssue and the diff payload helpers in `internal/executor/intent_judge.go`.
2. Config: skip_self_review and self_review_timeout become deprecated no-op keys with a one-line comment in `internal/executor/backend.go` and the example YAML; intent_judge stays for the pre-flight judge.
3. Rename the self-review hygiene and tracker names to the review-fix ones left by L3; update `cmd/pilot/handlers.go` where the warning line is built.
4. Tests: remove or port the self-review and in-run judge tests (the expired-context assertion from the GH-5342 test moves onto the fix round's skipped test); add a test that after the gates exactly one backend call can follow (the fix round) and that the pre-flight judge is still wired.

## Acceptance

- No backend call named self-review happens after the gates in any mode; the pre-flight issue judge still runs at admission.
- `go test -race ./internal/executor/ -run TestNoSelfReviewBackendCallAfterGates` passes; paste the output into the PR body.
- `go test -race ./cmd/pilot/ -run TestIntentJudgePreflightStillWired` passes; paste the output into the PR body.
- re-add a self-review backend call after the gates -> TestNoSelfReviewBackendCallAfterGates fails

## Out of scope

- Sampling (L6). Any change to the pre-flight judge.

## Refs

- #5961

### LEG L6 — repo: qf-studio/pilot — title: feat(autopilot): post-merge review sampling — post_merge_review.sample_rate reviews one PR in N, always reviews a PR whose pre-PR review was not an enforce-mode approve
probe: present qf-studio/pilot type PostMergeReviewConfig struct
probe: present qf-studio/pilot func \(c \*Controller\) maybeStartPostMergeReview\(
probe: absent  qf-studio/pilot EffectiveSampleRate\(

## Context

Depends on #5962 and on the enforce exit criteria. Once the pre-PR review carries the load, the post-merge reviewer becomes an audit: it samples one merged PR in N to measure the pre-PR reviewer, and always reviews the PRs the pre-PR review did not clear — section missing, mode not enforce, verdict not an approve, fix outcome not clean, or the PR body could not be fetched. PostMergeReviewConfig lives in `internal/autopilot/types.go`; the start decision is maybeStartPostMergeReview in `internal/autopilot/post_merge_review.go`, where the daily budget is acquired. Sampling by PR number modulo N is restart-safe; a counter is not.

## Implementation

1. SampleRate on PostMergeReviewConfig with EffectiveSampleRate (0 or 1 = every PR); validate non-negative in `internal/config/config.go` beside daily_cap.
2. In maybeStartPostMergeReview, after the PostMergeReviewPosted check and before the budget is acquired: fetch the PR body once, parse the Pre-PR review section with the executor parser, compute forced as described; when not forced and PR number modulo N is not zero, record the metric label sampled_out, log one Info line and return without consuming the budget.
3. Metric help text in `internal/gateway/prometheus.go`; the key in `configs/pilot.example.yaml` under post_merge_review.
4. Tests, table-driven with a fake reviewer that reads the PR number: one in N over ten PR numbers; forced on each section variant; forced on a fetch error; a sampled-out PR leaves the budget unchanged.

## Acceptance

- With sample_rate 5, PRs 5 and 10 of 1..10 are reviewed and the others are sampled out, unless their pre-PR section forces a review.
- A sampled-out PR does not consume the daily budget.
- `go test -race ./internal/autopilot/ -run TestPostMergeReviewSampling` passes; paste the output into the PR body.
- move the sample check after the budget acquire -> TestPostMergeReviewSampling_SampledOutDoesNotConsumeBudget fails
- drop the forced path -> TestPostMergeReviewSampling_ForcedOnRCOrShadow fails

## Out of scope

- Changing the audit's prompt, cap or timeout.

## Refs

- #5962

### LEG L7 — repo: qf-studio/pilot — title: fix(autopilot): keep the live-smoke segment last in EscalationReason so the pre-PR hold reason is not parsed as a live-smoke bullet; test the combined hold and the pre_pr_review_hold event
probe: present qf-studio/pilot func liveSmokeBulletsFromReason\(
probe: present qf-studio/pilot const preReviewReasonPrefix
probe: present qf-studio/pilot func newLiveSmokeController\(
probe: absent  qf-studio/pilot func TestHandleCIPassed_PreReviewAndLiveSmoke\(

## Context

Found by both the pre-PR review and the post-merge review of PR#5963 (#5962, TASK-620 L5); neither filed it because the verdicts were APPROVE-WITH-NOTES. In handleCIPassed in `internal/autopilot/controller.go` the live-smoke segment is appended to escalateReason first (the block at the liveSmokeReasonPrefix join, around line 3830) and the pre-PR review reason is appended after it (the block commented TASK-620 L5, around line 3842). liveSmokeBulletsFromReason (around line 3928) cuts the reason at liveSmokeReasonPrefix and splits everything after it on "; ", and its comment states that the live-smoke segment is always appended last. With both holds on one PR, the string "Pre-PR review requested changes: …" becomes a fake live-smoke bullet in two places: metadata["live_smoke_bullets"] in the approval request (around line 5301) and the Telegram hold list in `internal/autopilot/telegram_notifier.go` (around line 74). The prefix is the constant preReviewReasonPrefix in `internal/autopilot/scope_guard.go`. Second gap: the pre_pr_review_hold execution event recorded around line 3877 has no test, because newLiveSmokeController in `internal/autopilot/gh5568_test.go` builds the controller with a nil memory store and recordExecutionEvent returns early on nil (around line 2697). TestHandleCIPassed_PreReview in `internal/autopilot/pre_review_gate_test.go` has no case with both holds.

## Implementation

1. In handleCIPassed move the pre-PR review append above the live-smoke append so the live-smoke segment stays last; keep the escalate-only semantics and the fail-open parse.
2. In liveSmokeBulletsFromReason, after the cut at liveSmokeReasonPrefix, also cut the remainder at "; " followed by preReviewReasonPrefix when present, so a future segment appended after the live-smoke one cannot leak into the bullets either. Update the function comment.
3. Add TestHandleCIPassed_PreReviewAndLiveSmoke in `internal/autopilot/pre_review_gate_test.go`: a PR body with a Not verified live-smoke bullet and a Pre-PR review section whose verdict is REQUEST-CHANGES and Fix is none; assert the PR ends in awaiting_approval, EscalationReason contains both segments, liveSmokeBulletsFromReason returns exactly the one smoke bullet, and the pre-PR text is absent from the bullets.
4. Add TestHandleCIPassed_PreReviewRecordsHoldEvent: build the controller with a real store from memory.NewStore in t.TempDir() (the idiom in `internal/autopilot/controller_test.go` around line 976) instead of nil; after handleCIPassed, ListExecutionEvents for the task id returns one event whose detail starts with "pre_pr_review_hold:" and whose stage is awaiting_approval.
5. Add a unit case for liveSmokeBulletsFromReason with the pre-PR segment after the live-smoke segment (the pre-fix order) returning only the smoke bullets.

## Acceptance

- `go test -race ./internal/autopilot/ -run 'TestHandleCIPassed_PreReview|TestLiveSmokeBulletsFromReason'` passes; paste the output into the PR body.
- `go test -race ./internal/autopilot/ -run TestHandleCIPassed_LiveSmoke` still passes; paste the output into the PR body.
- restore the pre-PR append after the live-smoke append and drop the step-2 cut -> TestHandleCIPassed_PreReviewAndLiveSmoke fails
- skip the recordExecutionEvent call for the pre-PR hold -> TestHandleCIPassed_PreReviewRecordsHoldEvent fails

## Out of scope

- Whether an RC-only hold should fail closed on approval timeout (founder decision; today it follows default_action).
- The gate reading the pilot-review-rc label (the body section is the contract).

## Refs

- #5962
- .agent/tasks/TASK-620-pre-pr-reviewer-replaces-self-review-and-judge.md

## Budget arithmetic (complex task, 60 m)

| Case | Impl | Gates | Review | Left | Fix | Re-gate | Added wall-clock |
|---|---|---|---|---|---|---|---|
| fast impl, RC | 35 | 5 (t=40, 20 m left) | 6 (t=46) | 14 ≥ 12 → fix | min(10, 14) = 10 (t=56) | fresh 5 m | ≤ 21 m; finalize/evidence/PR on detached windows as today |
| slow impl, RC | 50 | 5 (t=55, 5 m left) | 6 on the detached window | < 12 → fix skipped | — | — | +6 m, RC pushed, approval hold |
| APPROVE | any | | +6 | | — | — | +3 m net (self-review 2 m + judge 1 m retired) |

## Rollout and exit criteria

_Decision log: 10-30 17:20Z founder "keep shadow" — sample restarts with two passes at 16:21Z; next read ≈ 2026-11-03 or at 20 two-pass runs. Numbers come from `pr_reviews` (pairs by pr_number) and the daemon's `Pre-PR review` log lines._
1. Shadow on the box after L1+L2 (`executor.pre_pr_review.mode: shadow`; post-merge unchanged): compare `pr_reviews` rows by (project_path, pr_number) across reviewer ids pre-pr-reviewer and post-merge-reviewer.
2. Shadow → enforce (≥ 20 PRs, ≥ 5 working days): error+unknown+exhausted ≤ 10 %; verdict-class agreement ≥ 70 %; pre-PR RC rate 10–40 %; median added minutes ≤ 7; zero new deadline failures at CreatePR.
3. Enforce (L3+L5; `mode: enforce`), next 20 merges: fixed ≥ 50 % of RC runs; reverted ≤ 20 %; post-merge RC on pre-PR-approved PRs ≤ 1 per 10; pilot-review-rc holds ≤ 2 per 10; median added minutes ≤ 12; no ErrProjectPathNotIsolated in logs.
4. Then L4 and L6 (`sample_rate: 5`).

## Reviewer gaps observed on PR#6003 (2026-11-01, TASK-628)

Both Opus 5.5 passes (pre-PR shadow in the PR body, post-merge comment 18:45:48Z) returned APPROVE-WITH-NOTES with 7 `[minor]` defects and found 3 of the 4 Navigator review notes plus 2 the human skipped. Three gaps, none a model miss:

1. **Minor defects have no sink.** `postPostMergeReview` files an issue only on REQUEST-CHANGES (`internal/autopilot/post_merge_review.go:330`), and the verdict rule makes REQUEST-CHANGES need one `[blocking]`. Seven minors were posted as a comment and dropped; the fast-follow had to be hand-filed (#6005). Candidate: one `pilot`+`no-decompose` fast-follow issue per PR when ≥ 2 minors cite a `file:line` and are not tagged as a known residual / out-of-scope item; or a weekly digest from `pr_reviews`. Founder call: noise vs. loss.
2. **Caller tracing stops at the direct consumer.** The reviewer followed `Result.RunnerMissing()` to `executor/runner.go:9444/9464` and stopped; the stale exit-127 headline sits one hop further (`outcomeRunnerMissing` 9596 → 6574, a file outside the diff). Candidate: for a new or widened bool helper, trace one hop past the cited consumers (TASK-619 follow-on).
3. **The reviewer cannot execute.** "cannot run tests in this read-only checkout" — the pasted `go test` output and both spec mutations landed in Not-verified. The human pass added only that. Candidate: allow `go test -run <acceptance selection>` and a mutation run inside the review checkout, budgeted; then the Navigator re-review step retires (memory `navigator-pr-review-executes-reviewer-not-verified-list-not-a-reread`).

## Box log lines to watch
`Pre-PR review` (task_id verdict defects blocking memories checkout files_read duration mode) · `Pre-PR review skipped` (reason) · `Pre-PR review fix` (status pre_fix_sha post_fix_sha elapsed) · `Pre-PR review: labelled pilot-review-rc` · autopilot `merge gate escalated … Pre-PR review requested changes:` · `post-merge review sampled out`.

### LEG L8 — repo: qf-studio/pilot — title: feat(autopilot): post-merge review files one batched Navigator-template follow-up issue for filtered minor defects — post_merge_review.file_minors (default off), per-project daily cap, review-minor label

## Context

Today the post-merge reviewer files a follow-up issue only on REQUEST-CHANGES: `postPostMergeReview` in `internal/autopilot/post_merge_review.go` (lines 319-340) returns at line 330 when the verdict is anything else, and `CreatePostMergeReviewIssue` in `internal/autopilot/feedback_loop.go` (line 490) formats `BlockingDefects()` only, under a `## Defects` header, with a hardcoded "returned REQUEST-CHANGES" sentence and two checkbox acceptance lines. Minor defects stay in the PR comment and are lost. On 2026-11-01 PR #6003 got APPROVE-WITH-NOTES with seven `[minor]` defects; two were real (a stale headline one caller hop outside the diff, a make flag value word parsed as the target), three were tagged as known residuals or out of scope, two were about the PR body. A human read the comment and hand-filed #6005. Nothing else sinks a minor.

Facts the leg rests on (verified at main 4cd9725e8): `PostMergeDefect` holds `Severity`, `Location`, `Claim`, `Evidence`, with `Location` a single string — `splitPostMergeLocation` (post_merge_reviewer.go line 784) takes the first whitespace-free token, so a body-level defect the model writes as "PR — body" has Location "PR"; the comment renderer adds the word "at" (post_merge_review.go lines 414 and 440). `findExistingFixIssue` (feedback_loop.go line 553) dedups by exact title among open issues that carry every label in `f.issueLabels`; the default `IssueLabels` is pilot plus autopilot-fix (internal/autopilot/types.go line 764). Issue labels today are `f.issueLabels` plus `postMergeReviewLabel` ("post-merge-review") and `postMergeReviewNoDecomposeLabel` ("no-decompose"), added once each. Issues are created through `ghissue.CreatePilotIssue` (internal/ghissue/create.go line 60). The config struct is `PostMergeReviewConfig` (types.go lines 313-340: enabled, model, daily_cap, timeout, max_diff_chars, passes), validated in `internal/config/config.go` lines 1436-1449, documented in `configs/pilot.example.yaml` lines 619-638 under orchestrator.autopilot.post_merge_review. The review budget `postMergeReviewBudget` (post_merge_review.go lines 78-85) is a UTC-day counter with a `now` seam. Metrics: `RecordPostMergeReview` in `internal/autopilot/metrics.go` line 440; no counter for filed issues. A `Controller` is per repo, so a counter on it is per project by construction.

Issue-body rules the generated issue must obey (SOP .agent/sops/onboarding/new-project-issue-authoring.md): H2 headers from the spec validator's set (Context, Implementation, Acceptance, Refs); a conventional-commit title; the `no-decompose` label, since the body has H2 headers and may exceed 200 words; no backticked span of the form path:line, because the dispatcher's referenced-path gate (`ExtractReferencedPaths` in `internal/executor/dependency_detector.go`) treats it as a missing prerequisite file and holds the task; acceptance bullets in the evidence gate's dialect (a backticked allowlisted command as the only inline code span, plus "paste the output into the PR body"), no shell operators, no make build/test/lint bullets.

## Implementation

1. Config. Add to `PostMergeReviewConfig`: a bool `FileMinors` (yaml key file_minors, default false) and an int `MinorIssueDailyCap` (yaml key minor_issue_daily_cap; 0 means the default of 1; negative is a validation error next to the existing daily_cap check in `internal/config/config.go`). Add an `EffectiveMinorIssueDailyCap()` helper beside `EffectiveDailyCap()`. Document both keys in `configs/pilot.example.yaml` under the post_merge_review block with one comment each: minors are filed only when file_minors is true; one issue per project per UTC day.
2. Executor. Add `MinorDefects()` next to `BlockingDefects()` in `internal/executor/post_merge_reviewer.go`, returning the defects whose severity is minor, in order.
3. Filter. New `filedMinorDefects(review *executor.PostMergeReview) (kept, dropped []executor.PostMergeDefect)` in `internal/autopilot/post_merge_review.go`. Keep a defect only when all hold: Location matches a path shape (letters, digits, dot, dash, underscore, slash; contains a slash; ends in a file extension; an optional colon and digits), so "PR" and bare words drop; neither Claim nor Evidence contains, case-insensitively, "out of scope", "known residual", "residual risk", "not a regression", "not verified", "cannot run", or "PR body". Collapse duplicates by Location plus Claim (two passes can report the same item). Everything else goes to dropped.
4. Cap. New `postMergeMinorBudget` on the `Controller`, same shape as `postMergeReviewBudget` (UTC-day rollover, `now` seam), built from `EffectiveMinorIssueDailyCap()` where the controller is wired (cmd/pilot/post_merge_reviewer.go, `wirePostMergeReviewer`), one per controller. When it is exhausted, log once per day: "post-merge review: minor follow-up skipped, daily cap reached" with pr and cap.
5. Controller. In `postPostMergeReview`, keep the REQUEST-CHANGES path exactly as it is. After it, when the verdict is APPROVE-WITH-NOTES, `FileMinors` is true, kept is non-empty, and the budget acquires: call the new feedback-loop method and log "post-merge review: minor follow-up filed" with pr, issue, kept, dropped. On error log "post-merge review: failed to file minor follow-up" with pr and error. Increment a new metrics counter `PostMergeMinorIssuesFiled` through `RecordPostMergeMinorIssueFiled()` (field, constructor init, method under the mutex, same as the existing pattern).
6. Feedback loop. New `CreatePostMergeMinorsIssue(ctx, prNumber int, prURL string, issueNumber int, kept, dropped []executor.PostMergeDefect) (int, error)` in `internal/autopilot/feedback_loop.go`, reusing `findExistingFixIssue` for dedup and `ghissue.CreatePilotIssue` for creation. Title: "fix(review): post-merge minors from PR #<pr> for issue #<issue>" — unique per PR, so the exact-title dedup suppresses a second filing for the same PR. Labels: `f.issueLabels` plus post-merge-review, no-decompose and a new constant `postMergeReviewMinorLabel` = "review-minor", each added once. Every model-derived string passes through `plainPostMergeField` as today; the whole body is cut with `executor.CutAtBoundary` at `maxPostMergeIssueHardChars`, and the Implementation list is cut first so the trailing sections survive (same idiom as `cutPostMergeDefects`).
7. Body template. Exactly these H2 sections in this order; paths in plain text (never in backticks), line numbers written as "line N":

   "## Context" — one paragraph: the post-merge review of PR #N (merge of #M) returned APPROVE-WITH-NOTES with K minor defects; J pass the filing filter and are listed under Implementation; fix only the listed items; each names the file and line as the reviewer saw them at the merge commit; link to the review comment URL when known, else the PR URL.

   "## Implementation" — a numbered list, one item per kept defect: "internal/quality/runner.go line 356 — <claim>. Evidence: <evidence>." A final sentence: "Add one test per item that fails without the change, and name each test in the PR body as the item's mutation pin (revert the item -> the test fails)."

   "## Acceptance" — one bullet per distinct Go package directory among the kept locations, at most four, in the evidence dialect with the command as the only inline code span: "`go test -race ./internal/quality/` passes; paste the output into the PR body." Skip non-.go locations. Then one plain bullet: "Each listed item has a test, and the PR body names the mutation pin for each item."

   "## Out of scope" — one plain line per dropped defect ("<location> — <claim> (dropped: <first matched tag or 'no file location'>)"), then the sentence "No change to any file not named above."

   "## Refs" — the PR URL, the original issue as #M, the review comment URL when known.
8. Example yaml and the docs page for autopilot config (if one lists post_merge_review keys) gain the two keys.

## Acceptance

- `TestPostMergeMinorFilter` in internal/autopilot (table): Location "PR" dropped; "internal/quality/runner.go:356" kept; "internal/executor/quality.go" kept; Evidence "the issue marks this as a known residual risk" dropped; Claim "out of scope per the issue" dropped; Evidence "I cannot run tests in this checkout" dropped; two defects with the same Location and Claim across passes yield one kept.
- `TestPostMergeMinorsIssue_Template` in internal/autopilot: the generated body contains the five H2 headers in order and no "## Defects"; the title starts with "fix(review): post-merge minors from PR #" and ends with the issue number; no backticked span matches a path followed by a colon and digits; every backticked span starts with "go test -race ./" and ends with "/"; at most four command bullets; labels contain pilot, autopilot-fix, post-merge-review, no-decompose and review-minor exactly once each; the body length stays at or under `maxPostMergeIssueHardChars` with 40 kept defects, and the "## Refs" header is still present.
- `TestPostMergeMinor_FilesOneIssueOnApproveWithNotes` through `newPMRGitHub` and `newPMRController`: APPROVE-WITH-NOTES with two kept and one dropped defect, file_minors true → exactly one issue created, labels as above, log line "post-merge review: minor follow-up filed" present; the same review with file_minors false → zero issues; REQUEST-CHANGES with one blocking defect → exactly one issue with today's title and no review-minor label; APPROVE with no defects → zero issues.
- `TestPostMergeMinor_DailyCapOnePerDay`: two APPROVE-WITH-NOTES reviews on different PRs within one UTC day with the default cap → one issue and one "daily cap reached" log line; advance the `now` seam a day → the next review files again.
- `TestPostMergeMinor_DedupsByTitle`: a second review of the same PR number the same day with the cap raised to 2 → no second issue (exact-title dedup hit).
- `TestPostMergeReview_MinorDefects` in internal/executor: a review with blocking, minor and untagged defects returns the minor and untagged ones in order.
- `TestConfig_PostMergeReviewMinorFields` in internal/config: negative minor_issue_daily_cap fails validation; 0 gives an effective cap of 1; file_minors defaults to false.
- `go test -race ./internal/autopilot/ -run TestPostMergeMinor` passes; paste the output into the PR body.
- `go test -race ./internal/executor/ -run TestPostMergeReview_MinorDefects` passes; paste the output into the PR body.
- `go test -race ./internal/config/ -run TestConfig_PostMergeReviewMinorFields` passes; paste the output into the PR body.
- Mutation: remove the path-shape check from the filter -> TestPostMergeMinorFilter fails.
- Mutation: remove the budget acquire before the minors call -> TestPostMergeMinor_DailyCapOnePerDay fails.
- Mutation: write the kept location in backticks with its line suffix -> TestPostMergeMinorsIssue_Template fails.
- Mutation: drop the review-minor label -> TestPostMergeMinorsIssue_Template fails.

## Out of scope

Pre-PR review minors (they belong to the enforce-mode fix round); a `pr_reviews` column for the filed issue number; a Slack digest of minors; a label manifest or auto-creation of the review-minor label (GitHub creates a missing label on issue creation; the operator may also create it by hand); checking that a kept path exists on main (paths are plain text, so the dispatcher's path gate never fires); any change to the REQUEST-CHANGES path, its title or its body; the verdict rule; the exit read (operator, after 10 filed issues: at least 5 merge clean, at most 1 REQUEST-CHANGES or revert — recorded in this task doc).

probe: present qf-studio/pilot func \(c \*Controller\) postPostMergeReview\(
probe: present qf-studio/pilot func \(f \*FeedbackLoop\) CreatePostMergeReviewIssue\(
probe: present qf-studio/pilot func \(r \*PostMergeReview\) BlockingDefects\(\)
probe: present qf-studio/pilot func \(f \*FeedbackLoop\) findExistingFixIssue\(
probe: present qf-studio/pilot yaml:"daily_cap"
probe: present qf-studio/pilot IssueLabels:[[:space:]]+\[\]string\{"pilot", "autopilot-fix"\}
probe: present qf-studio/pilot func splitPostMergeLocation\(
probe: absent qf-studio/pilot func \(r \*PostMergeReview\) MinorDefects
probe: absent qf-studio/pilot yaml:"file_minor[s]"
probe: absent qf-studio/pilot postMergeReviewMinorLabel = "

## Refs

- L1 → https://github.com/qf-studio/pilot/issues/5949
- L2 → https://github.com/qf-studio/pilot/issues/5956
- L3 → https://github.com/qf-studio/pilot/issues/5961
- L5 → https://github.com/qf-studio/pilot/issues/5962
- L7 → https://github.com/qf-studio/pilot/issues/5967
- L8 → https://github.com/qf-studio/pilot/issues/6007
