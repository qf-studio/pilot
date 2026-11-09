# TASK-620: the reviewer moves before the PR — one in-run review after the gates with a resumed fix round; self-review and the in-run intent judge retire; post-merge becomes a sampled audit

**Status**: 📦 **ARCHIVED 11-09 — all six legs released (L1–L3, L5 by v2.313.x · L4 v2.315.2 · L6 v2.316.0).** Box: `pre_pr_review.mode: enforce` since 11-05; `post_merge_review.sample_rate: 5` live at the v2.316.0 hot upgrade (11-09 09:47Z). Founder call 11-09: a REQUEST-CHANGES pre-PR verdict with a clean fix round is always reviewed post-merge (not sampled). Follow-up: [pilot#6238](https://github.com/qf-studio/pilot/issues/6238) (comment and doc drift from PR#6235, one PR fetch). Calibration continues in TASK-612.
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
probe: present qf-studio/pilot func \(r \*Runner\) buildSelfReviewPrompt\(
probe: present qf-studio/pilot func \(j \*IntentJudge\) Judge\(
probe: present qf-studio/pilot func \(j \*IntentJudge\) JudgeIssue\(

## Context

Depends on #5961 and on the enforce exit criteria in TASK-620 (do not dispatch before the founder marks them met). Verified at main 38a945b10 (2026-11-06): the in-run judge is already skipped whenever pre_pr_review.mode is not off (runner.go, runIntentJudge gate near line 6684, log "Intent judge skipped: replaced by the pre-PR review"), so its removal is dead-code deletion; the self-review is NOT gated on the pre-PR mode (runner.go, runSelfReview := qualityGatesPassed || ... near line 6734) and runs on every gated run alongside the pre-PR review today — that is the live duplication this leg removes. buildSelfReviewPrompt is a Runner method (prompt_builder.go line 640); self_review_hygiene.go holds afterSelfReview and selfReviewTreeHygiene. The founder marked the stage-3 criteria read on 2026-11-06 13:50Z and chose L4 now, L6 after a re-read. With the pre-PR review and its fix round live, the self-review phase (runSelfReview in `internal/executor/runner.go`, buildSelfReviewPrompt in `internal/executor/prompt_builder.go`) and the in-run intent judge (Judge in `internal/executor/intent_judge.go`, wired in the runner's parallel block) duplicate work: the self-review's build and lint checks are the quality gates, and the judge's text-only verdict reaches only a log string and a Telegram line. The pre-flight issue judge (JudgeIssue, used by `cmd/pilot/poller_github.go`) stays; IntentWarning stays because the pre-push lint path also writes it.

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

_Decision log: **11-06 13:50Z stage-3 read** — 30 enforce runs / 28 merges: fixed 60 % ✓ · reverted 40 % ✗ (n=5, both gate failures on v2.313.1, now held by TASK-641 A2) · post-merge RC on approved 4 % ✓ · holds 2/30 ✓ · median added 2.3 min ✓ · isolation errors 0 ✓. Founder call pending on step 4 (L4 + L6)._

_Decision log: **11-05 15:5xZ read (box ledger `pr_reviews` + `daemon.log`, window from 10-30 16:21Z)** — 98 distinct PRs with a two-pass pre-PR review (104 log lines; 70 APPROVE-w-notes, 28 RC → RC rate 28.6 %), 88 paired with a post-merge row: agreement 77/88 = 87.5 % (59 A/A, 18 RC/RC); post-merge RC on a pre-PR-approved PR 8/88 = 9.1 % (console 461/464/486, ui 303/317/330/333, pilot#6030 = the TASK-634 false evidence FAIL); pre-PR RC on a post-merge-approved PR 3/88 (console 463, pilot#6061, ui 356); 0 `Pre-PR review skipped`, 0 error/unknown/exhausted; duration median 109 s, p90 ≈ 180 s, max 269 s; 0 CreatePR deadline failures; working days 5 of 5 (10-30 pm, 10-31, 11-03, 11-04, 11-05); minors sink 11 `review-minor` issues (pilot 6, ui 4, console 1). **All seven criterion-2 numbers pass.** Qualitative counter-point unchanged: the severity miss on PR#6072 (contract breach tagged minor by both passes, then dropped by the minors daily cap) — in enforce the fix round fires only on `[blocking]`, so this class still passes through; candidate leg L11 = severity rule "an issue requirement not met or a documented invariant broken ⇒ blocking". Founder call pending. · **11-03 16:4xZ read** — 42 two-pass pre-PR runs (32 APPROVE-WITH-NOTES, 10 RC), 35 paired with a post-merge row, verdict-class agreement 31/35 = 88.6 %, disagreements console PR 461/463/464 + pilot PR#6030 (the TASK-634 false evidence FAIL), post-merge RC on a pre-PR-approved PR 3/35, pre-PR RC rate 23.8 %, 0 `Pre-PR review skipped`, median duration 109 s / max 258 s, working days 3 of 5; minors sink: 2 filed, daily cap hit once. Criterion 2 is met except the day count; next read 11-05. · 10-30 17:20Z founder "keep shadow" — sample restarts with two passes at 16:21Z; next read ≈ 2026-11-03 or at 20 two-pass runs. Numbers come from `pr_reviews` (pairs by pr_number) and the daemon's `Pre-PR review` log lines._
1. Shadow on the box after L1+L2 (`executor.pre_pr_review.mode: shadow`; post-merge unchanged): compare `pr_reviews` rows by (project_path, pr_number) across reviewer ids pre-pr-reviewer and post-merge-reviewer.
2. Shadow → enforce (≥ 20 PRs, ≥ 5 working days): error+unknown+exhausted ≤ 10 %; verdict-class agreement ≥ 70 %; pre-PR RC rate 10–40 %; median added minutes ≤ 7; zero new deadline failures at CreatePR.
3. Enforce (L3+L5; `mode: enforce`), next 20 merges: fixed ≥ 50 % of RC runs (measured under the A1 budget); reverted ≤ 20 %; post-merge RC on pre-PR-approved PRs ≤ 1 per 10; pre-PR holds (needs_human, pre-PR reason) ≤ 2 per 10; median added minutes ≤ 12; no ErrProjectPathNotIsolated in logs.
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

### LEG L9 — repo: qf-studio/pilot — title: feat(executor): the pre-PR review runs after the acceptance evidence and reads it, so a failing evidence command is a blocking defect the fix round can act on

## Context

The runner's finalize order is gates, then the pre-PR review (runPrePRReview near line 6759 of `internal/executor/runner.go`), then the fix round, and only inside the CreatePR block the acceptance evidence (appendAcceptanceEvidence near line 7368, which runs the paste-output commands) followed by RenderPrePRReviewSection. The review input (`internal/executor/pre_pr_review.go` near line 184) carries IssueTitle, IssueBody, Diff, CIResult from the gates and Memories; the PRBody field of PostMergeReviewInput stays empty because no PR body exists yet. So the in-run reviewer never sees the evidence result in any mode. On GH-6028 (2026-11-03) the pre-PR review ended at 15:20:00Z with APPROVE-WITH-NOTES, the evidence run started at 15:20:14Z and ended FAIL, the PR merged on green CI, and only the post-merge reviewer could flag it, as a new issue (GH-6031, declined; TASK-634). The post-merge reviewer reads the pasted evidence as part of PRBody; the pre-PR reviewer must get the same text.

probe: present qf-studio/pilot func[[:space:]]\(r[[:space:]]\*Runner\)[[:space:]]runPrePRReview\(
probe: present qf-studio/pilot func[[:space:]]\(r[[:space:]]\*Runner\)[[:space:]]appendAcceptanceEvidence\(
probe: present qf-studio/pilot PRBody[[:space:]]+string
probe: absent qf-studio/pilot func[[:space:]]\(r[[:space:]]\*Runner\)[[:space:]]collectAcceptanceEvidenceForReview\(

## Implementation

1. Split evidence collection from PR-body assembly: a new method on Runner, collectAcceptanceEvidenceForReview, runs the acceptance classification and the paste-output commands once, after the gates and before runPrePRReview, and returns the rendered evidence section plus the per-command results. appendAcceptanceEvidence reuses that result when present instead of running the commands again; its signature gains the cached result as an optional argument so the three CreatePR call sites stay one-line changes.
2. runPrePRReview passes the rendered evidence section as PRBody in PostMergeReviewInput, so the existing contract sentence about pasted build, test or lint output applies in-run. In the pre-merge prompt variant the sentence says the output was produced by this run on this branch, not by CI, so a FAIL for code the diff changed is [blocking] without a CI caveat.
3. After a fix round that ends ReviewFixFixed, the paste-output commands run again on the fixed SHA inside the existing re-gate window, and the PR body carries the second result; the first result is discarded. When the remaining budget is below the evidence cost plus the review budget the evidence step runs on the detached finalize window as today and the review proceeds without it, logging `Pre-PR review: evidence not ready`.
4. Budget: the paste-output commands are already capped per command (defaultAcceptanceEvidenceCommandTimeout) and per item; the review's 6 m budget and the 12 m fix floor are unchanged. Record the measured evidence time in the `Pre-PR review` log line as evidence_s.
5. No config key, no new status, no change to the post-merge reviewer.

## Acceptance

- [ ] `go test -race ./internal/executor/ -run TestPrePRReview_ReadsAcceptanceEvidence` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/executor/ -run TestAcceptanceEvidence_RunsOnceAcrossReviewAndPR` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/executor/ -run TestPrePRReview_FixRoundRerunsEvidence` passes; paste the output into the PR body.
- [ ] pass an empty PRBody to the pre-PR reviewer -> TestPrePRReview_ReadsAcceptanceEvidence fails
- [ ] run the paste-output commands a second time at CreatePR without a fix round -> TestAcceptanceEvidence_RunsOnceAcrossReviewAndPR fails
- [ ] keep the first evidence result after a ReviewFixFixed round -> TestPrePRReview_FixRoundRerunsEvidence fails
- [ ] A test with a fake paste-output command that exits 1 asserts the pre-PR review input's PRBody contains the FAIL line and the heading reads FAILS.
- [ ] Do not re-run make build, make test or make lint as evidence: the daemon's quality gates already run them on this repo.

## Out of scope

The severity contract wording for the post-merge reviewer (TASK-634 B1), the evidence trim (TASK-634 C1), the fix-round gate on [blocking] only, enforce mode itself, and any change to the quality gates or their order.

### LEG L10 — repo: qf-studio/pilot — title: fix(autopilot): a post-merge defect located at the PR body never blocks or files a fix issue, and the minors follow-up asks for the pin in the test file, not in a PR body the runner writes

## Context

Chain seen three times on 2026-11-05 on a UI project (issues 301/313/327 → PRs 303/317/330 → fix issues 304/319/331) and once on this repo (PR#6072): a review-minor issue built by buildPostMergeMinorsIssue in `internal/autopilot/feedback_loop.go` (near lines 626 and 637) tells Pilot to "name each test in the PR body as the item's mutation pin" and its acceptance says "the PR body names the mutation pin for each item". The PR body is not written by the model: the runner assembles it from the issue text, the acceptance-evidence sections (RenderAcceptanceEvidenceSections in `internal/executor/acceptance_evidence.go` near line 595, which pastes only allowlisted commands) and the pre-PR review block. Test names never reach the body, so the bullet is unsatisfiable by construction. The post-merge reviewer then returns REQUEST-CHANGES with one `[blocking]` defect whose Location is "PR — body" (PostMergeDefect in `internal/executor/post_merge_reviewer.go` near line 128; BlockingDefects near line 201), postPostMergeReview in `internal/autopilot/post_merge_review.go` (near line 350) files a fix issue for it, and the pre-flight judge declines that issue as reject_vague (`internal/executor/intent_judge.go` near line 253) because a body defect on a merged PR has no code change to make. The issue parks as pilot-needs-clarification and a human closes it. The code was correct every time; the pins existed in the test files.

Depends on: #6073

probe: present qf-studio/pilot name each test in the PR body as the item's mutation pin
probe: present qf-studio/pilot the PR body names the mutation pin for each item
probe: present qf-studio/pilot func[[:space:]]\(r[[:space:]]\*PostMergeReview\)[[:space:]]BlockingDefects\(
probe: present qf-studio/pilot func[[:space:]]\(c[[:space:]]\*Controller\)[[:space:]]postPostMergeReview\(
probe: absent qf-studio/pilot func[[:space:]]isPRBodyLocation\(

## Scope

- `internal/executor/post_merge_reviewer.go`: a new isPRBodyLocation(location string) bool that is true for a Location that names the PR body rather than a file: it starts with "PR" and carries no "/" and no ".go"/".ts"/file extension (covers "PR — body", "PR - body", "PR body", "PR — body Evidence", "PR — body Not verified"). parsePostMergeReview downgrades every defect with such a Location to minor (keep the text, set Severity minor) before the REQUEST-CHANGES → APPROVE-WITH-NOTES downgrade rule runs, so a review whose only blocking defects are body defects becomes APPROVE-WITH-NOTES with Downgraded set and is logged with the reason "pr-body-only". BlockingDefects therefore never returns a body defect and postPostMergeReview never files a fix issue for one. The posted comment keeps the defect under Defects tagged [minor] with a trailing "(PR body, not code)".
- `internal/autopilot/feedback_loop.go`, buildPostMergeMinorsIssue: the instruction becomes "Add one test per item that fails without the change; put a comment `// mutation pin: <item summary> -> this test fails` directly above each test" and the acceptance bullet becomes "Each listed item has a test with a `mutation pin:` comment above it in the test file". filedMinorDefects drops defects whose Location is a PR-body location (isPRBodyLocation), so a body defect never becomes a minors item either.
- The pre-PR reviewer prompt (`internal/executor/pre_pr_review.go` if that is where the checklist lives; otherwise the file that holds the post-merge checklist) gets one line: "A defect about the PR body, the Evidence block or the Not verified section is never [blocking]; the body is assembled by the runner, not by the author."
- No change to the pre-flight judge, the minors cap or the ledger.

Tests to add, named exactly: TestParsePostMergeReview_PRBodyDefectNeverBlocks (`internal/executor/post_merge_reviewer_test.go`: a REQUEST-CHANGES text whose only [blocking] defect is "at PR — body (## PR Body section)" parses as APPROVE-WITH-NOTES, Downgraded true, the defect kept as minor; a review with a [blocking] file defect plus a [blocking] body defect stays REQUEST-CHANGES with exactly one blocking defect), TestIsPRBodyLocation (table: the five body forms true; "internal/x.go:12", "PR_handler.go:3", "src/views/PR.vue:1" false), TestPostPostMergeReview_PRBodyOnlyFilesNoIssue (`internal/autopilot/post_merge_review_test.go`: the controller with a fake feedback loop; a body-only REQUEST-CHANGES review posts the comment and files nothing), TestBuildPostMergeMinorsIssue_PinInTestFile (`internal/autopilot/feedback_loop_test.go`: the body no longer contains "in the PR body" and contains "mutation pin:"; a body-located defect is not among the items).

## Acceptance

- [ ] `go test -race ./internal/executor/ -run 'TestParsePostMergeReview_PRBodyDefectNeverBlocks|TestIsPRBodyLocation'` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/autopilot/ -run 'TestPostPostMergeReview_PRBodyOnlyFilesNoIssue|TestBuildPostMergeMinorsIssue'` passes; paste the output into the PR body.
- [ ] keep a body defect blocking -> TestParsePostMergeReview_PRBodyDefectNeverBlocks fails
- [ ] file the fix issue for a body-only review -> TestPostPostMergeReview_PRBodyOnlyFilesNoIssue fails
- [ ] keep "in the PR body" in the minors template -> TestBuildPostMergeMinorsIssue_PinInTestFile fails
- [ ] Do not re-run make build, make test or make lint as evidence: the daemon's quality gates already run them on this repo.

## Out of scope

The reviewer severity calibration for contract breaches and the minors daily cap (TASK-620 enforce call); any change to how the runner writes the PR body; the UI project's three closed issues.

## Refs

- PR#6072 (this repo, body-only minors) · GH-6007 / L8 (minors sink) · TASK-612 (post-merge calibration) · Navigator TASK-620 in this repo


### LEG L11 — repo: qf-studio/pilot — title: feat(executor): reviewer severity contract — a change that breaks a documented invariant or contract sentence is [blocking], and a test pins the severity paragraph of the review prompt

## Context

The review prompt is the constant postMergeReviewerSystemPrompt in `internal/executor/post_merge_reviewer.go` (near line 420); the pre-PR review reuses it with two sentences swapped (preMergeAboutToPushSentence, preMergePRBodyParagraph, preMergeEvidenceRule). Its severity paragraph starts "Severity. A defect about the PR body" and lists what [blocking] means: "exactly one of: a requirement of the issue is not met; a wire field has no producer; a test proves nothing about the contract it guards; a migration loses data; an out-of-scope change alters behaviour. Everything else is [minor]." No test in `internal/executor/post_merge_reviewer_test.go` pins that paragraph.

Severity miss seen on 2026-11-04 (PR 6072, both passes): the change moved the usage-limit episode onto a goroutine, which broke the documented worker contract "hold admission before the next pick" in a code comment; the pre-PR shadow review and the post-merge review both tagged it [minor], so no fix round ran and no fix issue was filed (hand-filed as issue 6073). The list above has no entry for a breach of a documented invariant, so the reviewer had no rule to apply. Enforce mode is live since 2026-11-05, so a [minor] tag now means the fix round never runs.

probe: present qf-studio/pilot const[[:space:]]postMergeReviewerSystemPrompt
probe: present qf-studio/pilot Blocking means exactly one of
probe: absent qf-studio/pilot TestReviewPrompt_SeverityNamesInvariantBreach

## Scope

- `internal/executor/post_merge_reviewer.go`, postMergeReviewerSystemPrompt: extend the "Blocking means exactly one of" list with one more item, worded exactly: "a change breaks an invariant or contract sentence that is written down in a code comment, a doc string, a config field description, the task doc or the issue, naming that sentence and the line that breaks it". Keep every other sentence of the paragraph unchanged, so the pre-PR and post-merge prompts both carry the new item.
- Move the severity paragraph into its own constant postMergeSeverityParagraph, concatenated into postMergeReviewerSystemPrompt where the paragraph sits today, so a test can reference it without a copy of the text.
- Tests in `internal/executor/post_merge_reviewer_test.go`, named exactly: TestReviewPrompt_SeverityNamesInvariantBreach (the built pre-PR prompt and the built post-merge prompt both contain postMergeSeverityParagraph, and the paragraph contains the exact new item sentence); TestReviewPrompt_SeverityListUnchangedOtherwise (the paragraph still contains the five existing items and the "Everything else is [minor]." sentence, and "never [blocking]" for the PR body).
- No change to the parser, the verdict rule, the fix round, the minors sink or any config key.

## Acceptance

- [ ] `go test -race ./internal/executor/ -run 'TestReviewPrompt_SeverityNamesInvariantBreach|TestReviewPrompt_SeverityListUnchangedOtherwise'` passes; paste the output into the PR body.
- [ ] remove the invariant item from postMergeSeverityParagraph -> TestReviewPrompt_SeverityNamesInvariantBreach fails
- [ ] drop the "Everything else is [minor]." sentence -> TestReviewPrompt_SeverityListUnchangedOtherwise fails
- [ ] Do not re-run make build, make test or make lint as evidence: the daemon's quality gates already run them on this repo.

## Out of scope

Reviewer recall calibration (TASK-612); the minors daily cap; L4 (retire self-review and judge) and L6 (post-merge sampling), which wait for the stage-3 read; any change to how the pre-PR fix round acts on a [blocking] defect.

## Refs

- https://github.com/qf-studio/pilot/pull/6072
- https://github.com/qf-studio/pilot/issues/6073
- https://github.com/qf-studio/pilot/issues/6079


## Refs

- L1 → https://github.com/qf-studio/pilot/issues/5949
- L2 → https://github.com/qf-studio/pilot/issues/5956
- L3 → https://github.com/qf-studio/pilot/issues/5961
- L5 → https://github.com/qf-studio/pilot/issues/5962
- L7 → https://github.com/qf-studio/pilot/issues/5967
- L9 → https://github.com/qf-studio/pilot/issues/6037
- L10 → https://github.com/qf-studio/pilot/issues/6079
- L11 → https://github.com/qf-studio/pilot/issues/6126
- L4 → https://github.com/qf-studio/pilot/issues/6157
- L6 → https://github.com/qf-studio/pilot/issues/6232
- L8 → https://github.com/qf-studio/pilot/issues/6007 → PR#6008 merged 2026-11-01 20:11Z (v2.308.0)
