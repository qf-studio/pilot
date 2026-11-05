# TASK-641: Pre-PR review enforce — a reverted fix round still opens the PR; the re-gate cap cannot pass; the close handler lies on a closed issue

**Status**: 📋 PLANNED 2026-11-05 — three legs drafted (A1 re-gate budget + status · A2 `on_unfixed: hold` · B1 close handler). Founder decision D1 taken 11-05: a PR with unfixed blocking defects is the primary defect, so `hold` becomes the default. Dispatch order A1 → B1 (independent) → A2 (after A1, same files).
**Created**: 2026-11-05
**Assignee**: Pilot
**Origin**: GH-6109 / PR#6117 (execution `2c437730`, 11-05 17:17–20:50Z), the first REQUEST-CHANGES under `pre_pr_review.mode: enforce` on the founder box.

---

## What happened (box log, daemon.log)

| Time (Z) | Event |
|---|---|
| 17:17:17 | GH-6109 admitted (post-merge minors for PR#6105, `review-minor`). Claude run 48 s → afc0bdd. |
| 17:18–17:35 | Gates attempt 1 fail after 17m06s → Claude retry → gates pass 17:41 (5m30s). |
| 17:42:30 | Pre-PR review `mode=enforce verdict=REQUEST-CHANGES blocking=2 minor=1` (1m10s, 2 passes). |
| 17:43–17:48 | Fix round: fix commit d1e1a8d; re-gate `all_passed=false total_time=5m0.04s` → **"Pre-PR review fix failed the quality gates, reverting"** → `status=reverted pre_fix_sha=afc0bdd fixed=0`. |
| 17:48:32 | **"Pull request created" PR#6117** at afc0bdd with body `## Pre-PR review / Mode: enforce / Verdict: REQUEST-CHANGES / Fix: reverted` + the two blocking defects. "Task completed successfully". |
| 18:02 | Autopilot: CI green → `awaiting_approval` (PreReviewReason) → Slack approval request, 24 h timeout. |
| 20:49:46 | Human review agrees with both blocking defects; PR closed by hand ("GitHub blocks a REQUEST-CHANGES on a same-account PR, so the review-feedback loop cannot run"); corrected spec refiled as #6122; issue #6109 closed. |
| 20:50:00 | `notifyExternalClose`: execution → `failed`, error `closed without merging (no reason recorded)`; comment on the **closed** issue: "The issue has been marked pilot-retry-ready and will be retried automatically"; log `skipping issue label correction: issue already closed` → `pilot-in-progress` left on the closed issue. |
| 20:50:21 | The Slack approval for the closed PR is still live and gets "approved". |

## Findings (navigator-research on main, 11-05)

1. **The re-gate cannot pass on this repo.** `reviewFixRegateTimeout = 5 * time.Minute` is a hard-coded const (`internal/executor/pre_pr_fix.go:27`) wrapping the whole `checker.Check()` (`:206`). The `test` gate alone takes 5m30s–17m here. `executeCommand` returns `ErrGateTimeout` per gate (`internal/quality/runner.go:538-570`), but when the parent ctx expires first the runner records `"context cancelled"` with `StatusFailed` (`runner.go:280-286`), and `runReviewFix` only looks at `gateErr != nil` and `!gate.Passed` (`pre_pr_fix.go:207-220`). A timeout is indistinguishable from a red test; both end `reverted`.
2. **The run pushes on every fix outcome except `fixed`.** `runner.go:6790` checks `fix.Status == ReviewFixFixed` only to re-stamp gates and evidence; everything else falls through to the intent judge and PR creation. `runReviewFix`'s doc says "It never fails the run" (`pre_pr_fix.go:97-100`). `PrePRReviewConfig` (`internal/executor/backend.go:873-903`) has Mode, Model, Timeout, FixTimeout, MaxFixRounds, MaxDiffChars, Passes, SkipComplexity — no knob to hold or fail.
3. **The only brake is a human-approval hold after CI with no automated exit.** `publishPrePRReview` adds `pilot-review-rc` (`pre_pr_review.go:263, 287-298`); autopilot's `PreReviewReason` (`scope_guard.go:223-252`) parks the PR `awaiting_approval`. TASK-620 L5 recorded this as the founder decision ("when the pre-PR review ends REQUEST-CHANGES and the run pushes anyway, a human must decide before the merge"). The review-feedback loop cannot run: own-login reviews are dropped (`isTrustedReviewer`, GH-5326) and GitHub refuses REQUEST-CHANGES from the PR author's account. So every enforce RC costs a human close + a refile.
4. **`notifyExternalClose` tells a closed issue it will be retried.** `nextSteps` is set to the retry text at `controller.go:11474` before the closed check at `:11537`; the comment posts unconditionally at `:11635-11639`. On a closed issue the handler skips all label writes (`:11552-11553`), including the removal of `pilot-in-progress`. The default reason "closed without merging (no reason recorded)" (`:11331`) has no producer for an RC-held PR. The pending approval request is not cancelled on close.

## Founder decisions

| # | Question | Decision |
|---|---|---|
| D1 | After a reverted/errored fix round with blocking defects: open the PR (TASK-620 L5) or hold? | **Hold (11-05).** Push the branch, open no PR, hand the defects to the human on the issue. `on_unfixed: open_pr` keeps the old behaviour per project. |
| D2 | Re-gate budget | Config `regate_timeout`, default 20m (this repo's worst gate run 17m), per-gate timeouts still apply inside. |
| D3 | A timed-out re-gate keeps or reverts the fix commit? | **Revert** (unverified code never ships), but with its own status `regate_timeout` so the log, the PR body and the hold comment say "not verified" rather than "failed". |

## Legs (dispatch order)

| Leg | Scope | Depends on |
|---|---|---|
| **A1 — re-gate budget + timeout status** | `pre_pr_review.regate_timeout`, `ReviewFixRegateTimeout` status, log + body fields. | — |
| **B1 — close handler** | closed-issue message, label removal, RC-held reason, approval cancel. | — |
| **A2 — `on_unfixed: hold`** | push, no PR, `pilot-needs-human` + defects comment, terminal `failed` reason `review_blocking_unfixed`. | A1 |

## Dispatched legs

### LEG A1 — repo: qf-studio/pilot — title: fix(executor): pre-PR review re-gate gets a configurable budget (default 20m) and a timed-out re-gate reports regate_timeout instead of a red-test revert

## Context

Under `pre_pr_review.mode: enforce`, a REQUEST-CHANGES with a blocking defect gets one fix round: runReviewFix in `internal/executor/pre_pr_fix.go` resumes the session, commits the fix, then re-runs the quality gates through r.qualityCheckerFactory under `context.WithTimeout(context.Background(), reviewFixRegateTimeout)` (near line 206) with `reviewFixRegateTimeout = 5 * time.Minute` hard-coded (near line 27). The switch after checker.Check (near lines 207-220) handles gateErr != nil (ReviewFixRegateError) and !gate.Passed (ReviewFixReverted); both reset HEAD to PreFixSHA through resetReviewFix. The quality runner (`internal/quality/runner.go`) gives each gate its own gate.DefaultTimeout() (test 10m, build 5m, lint 2m; `internal/quality/types.go` near line 76) and returns ErrGateTimeout per gate (GH-6080), but when the parent context expires first, runGate records "context cancelled" with StatusFailed (near line 280). PrePRReviewConfig (`internal/executor/backend.go` near line 873) has Timeout (6m) and FixTimeout (10m) but no re-gate field. RenderPrePRReviewSection (`internal/executor/pre_pr_review_render.go` near line 46) writes the `Fix:` line of the PR body from the fix status. The "Pre-PR review fix round" log line (pre_pr_fix.go, finish) reports status, pre_fix_sha, post_fix_sha, fixed, elapsed.

On 2026-11-05 (GH-6109, execution 2c437730) the re-gate ended `all_passed=false total_time=5m0.04s` on a repo whose `test` gate takes 5m30s–17m; the fix was reverted as a red test and the PR body said `Fix: reverted`. The 5m cap can never pass here, and a timeout is reported as a failure.

probe: present qf-studio/pilot reviewFixRegateTimeout[[:space:]]*=[[:space:]]*5
probe: present qf-studio/pilot ReviewFixReverted[[:space:]]+ReviewFixStatus
probe: present qf-studio/pilot func[[:space:]]RenderPrePRReviewSection\(
probe: absent qf-studio/pilot yaml:"regate_timeout,omitempty"
probe: absent qf-studio/pilot ReviewFixRegateTimeout

## Scope

- Config (`internal/executor/backend.go`, PrePRReviewConfig): RegateTimeout time.Duration with the yaml tag regate_timeout,omitempty and EffectiveRegateTimeout() returning 20m for zero. Validation in `internal/config/config.go` next to the other pre_pr_review checks: a negative value or one under 1m is rejected with the default in the message. `configs/pilot.example.yaml`: one commented line under pre_pr_review, `regate_timeout: 20m`, with a comment that it must exceed the project's slowest gate run and that the per-gate timeouts still apply inside it.
- Re-gate (`internal/executor/pre_pr_fix.go`, runReviewFix): the context budget comes from the config's EffectiveRegateTimeout(); delete the reviewFixRegateTimeout const. New status ReviewFixRegateTimeout = "regate_timeout" with a doc comment like its siblings. After checker.Check: when regateCtx.Err() is context.DeadlineExceeded, or gateErr wraps quality.ErrGateTimeout, or any gate result's error is "context cancelled" / wraps ErrGateTimeout, revert exactly as ReviewFixReverted does (resetReviewFix, fix.Fixed = nil) and finish with ReviewFixRegateTimeout; log "Pre-PR review fix re-gate timed out, reverting" with regate_budget and regate_elapsed. The existing reverted and regate_error branches are unchanged. The "Pre-PR review fix round" log line gains regate_budget and regate_elapsed on every outcome.
- Body (`internal/executor/pre_pr_review_render.go`): the `Fix:` line for the new status reads `re-gate timed out after <elapsed> (budget <budget>); fix reverted, not verified`. ReviewFixOutcome (or the struct the renderer reads) carries RegateBudget and RegateElapsed.
- Everything downstream treats regate_timeout like reverted (the verdict stays REQUEST-CHANGES; autopilot's PreReviewReason text includes the status string as today).

Tests to add, named exactly, in `internal/executor/pre_pr_fix_test.go` (reuse its fake checker factory and git fixtures): TestReviewFix_RegateTimeoutRevertsWithOwnStatus (a checker that blocks until ctx.Done and returns a result with error "context cancelled" → status regate_timeout, HEAD == PreFixSHA, fix.Fixed nil, log fields present), TestReviewFix_RegateBudgetFromConfig (regate_timeout 1s → the checker observes a deadline within 1s±200ms of its start; unset → within 20m±1s), TestReviewFix_RedTestStillReverted (checker returns Passed=false with no timeout → status reverted, unchanged). Plus TestPrePRReviewConfig_RegateTimeoutValidation (`internal/config/config_test.go`: unset ok, 20m ok, 30s rejected with the default in the message) and TestRenderPrePRReviewSection_RegateTimeout (`internal/executor/pre_pr_review_render_test.go`: the Fix line text).

## Acceptance

- [ ] `go test -race ./internal/executor/ -run 'TestReviewFix_Regate|TestReviewFix_RedTest|TestRenderPrePRReviewSection_RegateTimeout'` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/config/ -run TestPrePRReviewConfig_RegateTimeoutValidation` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/executor/ -run 'TestReviewFix'` still passes; paste the output into the PR body.
- [ ] keep the 5m const instead of the config value -> TestReviewFix_RegateBudgetFromConfig fails
- [ ] report a timed-out re-gate as reverted -> TestReviewFix_RegateTimeoutRevertsWithOwnStatus fails
- [ ] keep the fix commit on a re-gate timeout -> TestReviewFix_RegateTimeoutRevertsWithOwnStatus fails (HEAD check)
- [ ] Do not re-run make build, make test or make lint as evidence: the daemon's quality gates already run them on this repo.

## Out of scope

Whether the run opens a PR after an unfixed fix round (leg A2, `on_unfixed`); the autopilot close handler (leg B1); the reviewer passes, prompts or the fix session itself; the quality runner's own per-gate timeouts and the GH-6080/GH-6081 gate semantics.

## Refs

- GH-6109 / PR#6117 (first enforce RC, re-gate `5m0.04s`) · TASK-620 L3 (fix round spec, "fresh 5-minute context") · GH-6080 / PR#6085 (ErrGateTimeout) · Navigator TASK-641 in this repo

### LEG B1 — repo: qf-studio/pilot — title: fix(autopilot): a PR closed without merge on an already-closed issue gets a truthful comment, loses pilot-in-progress, carries the RC-held reason, and cancels its pending approval

## Context

notifyExternalClose in `internal/autopilot/controller.go` (near line 11326) handles a PR that was closed on GitHub without a merge: it reclassifies the execution to failed with the reason "closed without merging (no reason recorded)" (near line 11331) unless a reason is known, fetches the issue (near line 11396), sets nextSteps to "The issue has been marked pilot-retry-ready and will be retried automatically." (near line 11474) before the closed check, computes issueAlreadyClosed (near line 11537), skips every label write when the issue is closed (near line 11552, including the removal of pilot-in-progress that lives in the else branch), and posts the comment with nextSteps unconditionally (near line 11635). mutateIssueLabels (near line 8562) is the add/remove helper. The approval subsystem (`internal/approval`, request ids like pr-<n>-<ts>, Slack channel) opens a request when autopilot parks a PR awaiting_approval with PreReviewReason (`internal/autopilot/scope_guard.go` near line 223, TASK-620 L5); nothing cancels it when the PR closes.

On 2026-11-05 PR#6117 (GH-6109, held awaiting_approval for a pre-PR review REQUEST-CHANGES) was closed by hand at 20:49:46Z after the issue was closed. The daemon logged "skipping issue label correction: issue already closed", posted "will be retried automatically" on the closed issue, left pilot-in-progress on it, recorded "no reason recorded", and the Slack approval was still live and got "approved" at 20:50:21Z.

probe: present qf-studio/pilot func[[:space:]]\(c[[:space:]]\*Controller\)[[:space:]]notifyExternalClose\(
probe: present qf-studio/pilot issueAlreadyClosed[[:space:]]*:=
probe: present qf-studio/pilot closed without merging \(no reason recorded\)
probe: absent qf-studio/pilot closed by hand while held

## Scope

- Closed issue (`notifyExternalClose`): when issueAlreadyClosed, nextSteps becomes "The issue is already closed; no retry will run. Reopen it or refile to run again." and the retry-ready marking is skipped (no label, no retry state). pilot-in-progress (and pilot-review-rc when present) are still removed through mutateIssueLabels with an empty add list; no label is added to a closed issue. The INFO log "skipping issue label correction: issue already closed" becomes "closed issue: removed status labels only" with the removed names.
- Reason: when the PR state was awaiting_approval with a PreReviewReason (the pre-PR review RC hold), the recorded reason is "closed by hand while held: pre-PR review REQUEST-CHANGES" instead of the no-reason default; the execution row, the comment and the alert carry it.
- Approval: on external close, cancel the pending approval request for that PR (the approval store/engine's cancel path; if none exists, add a Cancel(requestID) that marks the request cancelled so a later Slack click is answered "this PR is closed" and recorded as ignored). Log one INFO line.

Tests to add, named exactly, in a new file, plain text: internal/autopilot/controller_external_close_test.go (use the existing fake GitHub client and state fixtures from the notifyExternalClose tests): TestNotifyExternalClose_ClosedIssue_NoRetryTextAndLabelsRemoved (issue closed → comment contains "already closed", does not contain "retried automatically", removed labels == [pilot-in-progress pilot-review-rc], no add), TestNotifyExternalClose_OpenIssue_Unchanged (issue open → today's retry text and labels), TestNotifyExternalClose_RCHeldReason (PR state awaiting_approval with PreReviewReason → execution error and comment carry "closed by hand while held"), TestNotifyExternalClose_CancelsPendingApproval (a pending request for the PR → cancelled; a later decision on it is ignored).

## Acceptance

- [ ] `go test -race ./internal/autopilot/ -run 'TestNotifyExternalClose'` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/approval/...` passes; paste the output into the PR body.
- [ ] post the retry text on a closed issue -> TestNotifyExternalClose_ClosedIssue_NoRetryTextAndLabelsRemoved fails
- [ ] skip the label removal on a closed issue -> TestNotifyExternalClose_ClosedIssue_NoRetryTextAndLabelsRemoved fails
- [ ] keep "no reason recorded" for an RC-held PR -> TestNotifyExternalClose_RCHeldReason fails
- [ ] Do not re-run make build, make test or make lint as evidence: the daemon's quality gates already run them on this repo.

## Out of scope

The executor's fix round and PR decision (legs A1, A2); the GH-5351 delivery-mismatch guard (TASK-640); the approval UI text beyond the cancel answer; the retry-ready path for open issues.

## Refs

- GH-6109 / PR#6117 (close at 20:49:46Z, comment at 20:50:02Z, approval click 20:50:21Z) · GH-5849 (live-issue re-check at admission, the same idea on the dispatch side) · GH-4701 (informational close comment) · Navigator TASK-641 in this repo

### LEG A2 — repo: qf-studio/pilot — title: feat(executor): pre-PR review on_unfixed hold — blocking defects that survive the fix round push the branch, open no PR, hand the defects to the issue with pilot-needs-human, and end the run failed with reason review_blocking_unfixed

## Context

After the fix round, `internal/executor/runner.go` (near line 6790) checks `fix.Status == ReviewFixFixed` only to re-stamp the gates and refresh the evidence; every other outcome (reverted, regate_error, regate_timeout from leg A1, failed, none) falls through to the intent judge, the push and PR creation, with the review rendered into the PR body by RenderPrePRReviewSection and the label pilot-review-rc added by publishPrePRReview (`internal/executor/pre_pr_review.go` near line 263). Autopilot then parks the PR awaiting_approval (PreReviewReason, `internal/autopilot/scope_guard.go` near line 223). That hold has no automated exit: own-login reviews are dropped (isTrustedReviewer, GH-5326) and GitHub refuses a REQUEST-CHANGES review from the PR author's account, so on 2026-11-05 PR#6117 (GH-6109) sat two hours and was closed by hand with a refile. PrePRReviewConfig (`internal/executor/backend.go` near line 873) has no field for this decision. The existing needs-human path (label pilot-needs-human, the dispatcher's admission skip for it, the operator SOP) is the model for how a run hands work back.

Founder decision D1 (TASK-641, 11-05): the PR with unfixed blocking defects is the defect; the default becomes hold.

Depends on: leg A1 (same files; it adds ReviewFixRegateTimeout and the regate fields the hold comment renders).

probe: present qf-studio/pilot fix\.Status[[:space:]]*==[[:space:]]*ReviewFixFixed
probe: present qf-studio/pilot ReviewFixRegateTimeout
probe: present qf-studio/pilot const[[:space:]]PrePRReviewRCLabel
probe: absent qf-studio/pilot yaml:"on_unfixed,omitempty"
probe: absent qf-studio/pilot review_blocking_unfixed

## Scope

- Config (`internal/executor/backend.go`, PrePRReviewConfig): OnUnfixed string with the yaml tag on_unfixed,omitempty; values hold (default for empty) and open_pr (today's behaviour). Validation in `internal/config/config.go` rejects other values with both accepted values in the message. `configs/pilot.example.yaml`: one commented line `on_unfixed: hold` with a two-line comment (hold = push the branch, no PR, defects on the issue, human decides; open_pr = open the PR and park it awaiting approval).
- Hold path (`internal/executor/runner.go`, right after the runReviewFix call): when mode is enforce, the verdict is REQUEST-CHANGES, at least one blocking defect remains (fix.Status != ReviewFixFixed, or the post-fix review still lists a blocking defect) and OnUnfixed is hold: push the branch as today (the work is kept on origin), skip the intent judge and PR creation, post one issue comment made of the rendered "## Pre-PR review" section (verdict, fix outcome incl. the A1 regate fields, defects) plus the branch name and the commit SHA, add pilot-needs-human and remove pilot-in-progress on the issue through the existing label mutation path, and end the execution failed with reason review_blocking_unfixed (a terminal reason: admission never auto-retries it; the human edits the issue or the branch and removes pilot-needs-human to re-run, as for any needs-human hold). The task-failed alert carries the reason and the branch. One INFO log line "Pre-PR review hold" with task_id, branch, sha, blocking count, fix status.
- open_pr: unchanged (PR opened, pilot-review-rc, awaiting_approval).
- Docs: `docs/content/features/` page that documents pre_pr_review (find it by grep) gets a short "on_unfixed" paragraph; `docs/content/cli/commands.mdx` is unchanged.

Tests to add, named exactly, in a new file, plain text: internal/executor/pre_pr_hold_test.go (use the runner fixtures of pre_pr_fix_test.go with a fake PR creator, fake GitHub label client and the alert sink): TestPrePRHold_NoPRWhenBlockingUnfixed (enforce, RC with one blocking defect, fix status reverted, on_unfixed unset → PR creator not called, branch pushed, issue comment contains "## Pre-PR review" and the branch, labels add [pilot-needs-human] remove [pilot-in-progress], execution failed with reason review_blocking_unfixed, one alert), TestPrePRHold_RegateTimeoutRendersNotVerified (fix status regate_timeout → the comment says "not verified"), TestPrePRHold_FixedProceedsToPR (fix status fixed → PR created, no hold), TestPrePRHold_OpenPRKeepsToday (on_unfixed open_pr → PR created with pilot-review-rc), TestPrePRHold_MinorOnlyProceeds (RC with minors only → PR created). Plus TestPrePRReviewConfig_OnUnfixedValidation (`internal/config/config_test.go`: empty, hold, open_pr accepted; "fail" rejected with the accepted values) and TestAdmission_ReviewBlockingUnfixedIsTerminal (`internal/executor/`: a failed row with that reason is not re-picked by the retry sweep).

## Acceptance

- [ ] `go test -race ./internal/executor/ -run 'TestPrePRHold|TestAdmission_ReviewBlockingUnfixedIsTerminal'` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/config/ -run TestPrePRReviewConfig_OnUnfixedValidation` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/executor/ -run 'TestReviewFix|TestPrePRReview'` still passes; paste the output into the PR body.
- [ ] open the PR on hold -> TestPrePRHold_NoPRWhenBlockingUnfixed fails
- [ ] skip the branch push on hold -> TestPrePRHold_NoPRWhenBlockingUnfixed fails
- [ ] mark the failed row retry-able -> TestAdmission_ReviewBlockingUnfixedIsTerminal fails
- [ ] hold on minors only -> TestPrePRHold_MinorOnlyProceeds fails
- [ ] Do not re-run make build, make test or make lint as evidence: the daemon's quality gates already run them on this repo.

## Out of scope

The re-gate budget and the regate_timeout status (leg A1, already merged when this runs); the autopilot close handler (leg B1); a second fix round (max_fix_rounds > 1); the reviewer prompts; shadow mode; the hosted console surfaces for needs-human.

## Refs

- GH-6109 / PR#6117 (the first enforce RC: fix reverted, PR opened, closed by hand after 2 h) · TASK-620 L3/L5 (fix round, awaiting_approval hold; L5 decision superseded by TASK-641 D1) · GH-5326 (own-login reviews dropped) · Navigator TASK-641 in this repo

## Refs

- A1 → (pending)
- B1 → (pending)
- A2 → (pending)
