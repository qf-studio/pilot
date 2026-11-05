# TASK-641: Pre-PR review enforce — a reverted fix round still opens the PR; the re-gate cap cannot pass; the close handler lies on a closed issue

**Status**: 🚀 **A1 + B1 DISPATCHED 11-05 → [pilot#6130](https://github.com/qf-studio/pilot/issues/6130) (re-gate budget `regate_timeout` default 20m + `regate_timeout` status) · [pilot#6131](https://github.com/qf-studio/pilot/issues/6131) (close handler: closed-issue comment, label removal, RC-held reason, approval cancel)** (`pilot` + `no-decompose`). A2 (`on_unfixed: hold` on `holdPushedBranch` / `needs_human`) dispatches after A1 merges; C1 (needs_human metrics + Run Story + SOP) after A2. Was: 📋 PLANNED 2026-11-05 — full picture read (nav-research + box data, § Full picture); legs A1 (re-gate budget) · B1 (close handler) · A2 (`on_unfixed: hold` on the existing `holdPushedBranch` / `needs_human` path) · C1 (needs_human metrics + Run Story + SOP). D1 taken 11-05: an opened PR with unfixed blocking defects is the primary defect → `hold` default (per daemon: `pre_pr_review` has no per-project override). Dispatch order A1 → B1 (independent) → A2 (after A1) → C1 (after A2). A1/B1 pass pre-flight; A2 and C1 probes need A1/A2 on main.
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

## Full picture (nav-research on main b07740130 + box data, 11-05 21:30Z)

**Where enforce stands.** `executor.pre_pr_review.mode: enforce` went live on the founder box 11-05 15:57Z (TASK-620 stage 3: next 20 merges; targets fixed ≥ 50 % of RC runs, reverted ≤ 20 %, `pilot-review-rc` holds ≤ 2/10). The setting is daemon-wide (`ExecutorConfig.PrePRReview`, `internal/executor/backend.go:425`); no per-project override exists; hosted tenants render no `pre_pr_review` block (`pilot-console/internal/fleet/configrender.go:188-205`) and run `off` with `require_approval: false`, so A2 reaches tenants only when a tenant knob is added. Shadow data (TASK-620 decision log, 98 PRs): RC rate 28.6 %, verdict agreement with the post-merge reviewer 87.5 %, no measured false-RC rate. L4 (retire self-review + judge) and L6 (post-merge sampling) wait for the stage-3 read; L11 (#6126, documented-invariant breach ⇒ blocking) raises the RC rate.

**Enforce so far (box log, 15:57Z–21:30Z).** 14 enforce reviews: 11 APPROVE-WITH-NOTES, 3 RC. Fix rounds: GH-6109 (pilot) `reverted` after the 5m re-gate cap on a 5m30s–17m test gate; GH-370 (pilot-console-ui) `fixed` with the re-gate at **4m58s**, two seconds under the cap; GH-376 (ui) RC at 21:22Z with its gates at 5m27s, so its re-gate will likely time out. **The 5m cap is a coin flip on every project served by the box, not a pilot-repo quirk.** Shadow RCs today parked 7 PRs `awaiting_approval` with PreReviewReason (ui 342/351/352/355/356, pilot 6089/6104).

**What a run that ends without a PR loses (A2 blast radius).** No `pr_reviews` row and no `pilot-review-rc` (`runner.go:7503-7512`) → the stage-3 comparison and L6 sampling lose the held cases. No autopilot registration (`controller.go:3062`), no `RecordTimeToPR` sample. The success branch alone sends the task-completed alert, the PR webhook, the Navigator index sync, the context marker, `ArchiveTaskDoc`, the completion `AddMemory` and `recorder.Finish` (`runner.go:7533-7567`); `recordLearning` / `recordOutcome` / `recordGraphLearning` count any non-success as a failure, so holds push the model-routing escalation tracker. Acceptance evidence is pasted only into the PR body (`runner.go:~6755`). Run Story maps `failed` → "failed" and shows diff stats and the PR link only with a `pr_url` (`internal/gateway/run_story.go:129-243`); `briefs.ClassifyFailure` (`internal/briefs/structured.go:159-176`) and `outcomeClassifiers` (`runner.go:458-533`) substring-classify the error text, so a reason containing "timeout", "timed out", "conflict", "test failed", "push failed", "rate limit" or "usage limit" is misfiled. Ticket ledger (TASK-546 D8): a billable ticket is an execution that opened a PR; `succeeded_tasks` counts `status='completed'` (`store.go:4975`), so a hold is free.

**The hold path that already exists.** `holdPushedBranch` (`runner.go:3326`, GH-5399) pushes the branch, posts a comment, adds `pilot-needs-human`, removes `pilot-retry-ready`, sets `result.Outcome = "needs_human"` and `StageFailed`. `needs_human` is a terminal status (`runner.go:562`, `lifecycle.go:70-80` "must NOT stack a pilot-failed label", `dispatcher.go:55/87`), `HasTerminalCompletion` counts it done so `nextRetryGeneration` grants no retry (`store.go:1545`, `dispatcher.go:2055`), `handler_common.go:~672-717` sends no duplicate TaskFailed alert, `cmd/pilot/rearm_needs_human.go:22-57` re-arms when the human removes `pilot-needs-human` with the trigger label present, and the GH-5849 live-issue check skips admission while the label is on (`cmd/pilot/handlers.go:1056, 1421`). Gaps: `GetLifetimeTaskCounts` / `GetWindowedStats` have no `needs_human` bucket (`store.go:4942-5065`: in Total, in neither Failed nor NonFailure), there is no Prometheus counter for holds, and no operator SOP for resolving a needs-human hold.

**Why the review loop cannot close itself.** `isTrustedReviewer` drops own-login and `[bot]` reviews (`controller.go:5233-5243`, GH-5326); GitHub refuses REQUEST_CHANGES from the PR author's account; founder decision 2026-08-30 "Pilot never reads GH comments" (memory `pilot-never-reads-gh-comments-by-design`): review feedback enters as issues only. No design exists yet for an App-authored or second-account review that could drive the loop automatically — a separate research item, not a leg here.

**Risks the first draft of A2 missed (now in the leg).** A hold on a borrowed branch (a fix issue that continues an open PR, `runner.go:1190-1236`) would push unfixed defects onto that PR, and `PreReviewReason` reads the original PR body so autopilot would not park it. The hold point "right after runReviewFix" sits before the already-merged, no-commits and existing-PR guards (`runner.go:7088-7137`); the push is at `:7256`. `outcome.Fix` is nil when the round does not apply (`pre_pr_fix.go:~108`), and the fix round never re-reviews, so "post-fix review still lists a blocking defect" is a dead clause. A `failed` row with the trigger label and no `pilot-needs-human` gets generation+1 from the dispatcher and the `pilot-failed` retry ladder — the reason A2 now uses `needs_human`, not `failed`.

**Effect on TASK-620's rollout.** Stage-3 criterion "`pilot-review-rc` holds ≤ 2/10" measures PRs parked for a human; under A2 those become needs-human holds without a PR. Re-state the criterion as "pre-PR holds (needs_human with reason pre-PR review) ≤ 2/10" when A2 ships, and count `fixed` against the A1 budget, not the 5m cap. L4 and L6 stay gated on the stage-3 read.

## Founder decisions

| # | Question | Decision |
|---|---|---|
| D1 | After a reverted/errored fix round with blocking defects: open the PR (TASK-620 L5) or hold? | **Hold (11-05).** Push the branch, open no PR, hand the defects to the human on the issue. `on_unfixed: open_pr` keeps the old behaviour (per daemon; no per-project override exists). |
| D2 | Re-gate budget | Config `regate_timeout`, default 20m (this repo's worst gate run 17m), per-gate timeouts still apply inside. |
| D3 | A timed-out re-gate keeps or reverts the fix commit? | **Revert** (unverified code never ships), but with its own status `regate_timeout` so the log, the PR body and the hold comment say "not verified" rather than "failed". |

## Legs (dispatch order)

| Leg | Scope | Depends on |
|---|---|---|
| **A1 — re-gate budget + timeout status** | `pre_pr_review.regate_timeout`, `ReviewFixRegateTimeout` status, log + body fields. | — |
| **B1 — close handler** | closed-issue message, label removal, RC-held reason, approval cancel. | — |
| **A2 — `on_unfixed: hold`** | reuse `holdPushedBranch`: push (never on a borrowed branch), no PR, defects + evidence comment, `pilot-needs-human`, terminal `needs_human`, `recorder.Finish`. | A1 |
| **C1 — needs_human visibility** | `needs_human` bucket in lifetime/windowed counts, `pilot_pre_pr_holds_total`, Run Story outcome `needs_human`, operator SOP, TASK-620 stage-3 criterion re-stated. | A2 |

## Dispatched legs

### LEG A1 — repo: qf-studio/pilot — title: fix(executor): pre-PR review re-gate gets a configurable budget (default 20m) and a timed-out re-gate reports regate_timeout instead of a red-test revert

## Context

Under `pre_pr_review.mode: enforce`, a REQUEST-CHANGES with a blocking defect gets one fix round: runReviewFix in `internal/executor/pre_pr_fix.go` resumes the session, commits the fix, then re-runs the quality gates through r.qualityCheckerFactory under `context.WithTimeout(context.Background(), reviewFixRegateTimeout)` (near line 206) with `reviewFixRegateTimeout = 5 * time.Minute` hard-coded (near line 27). The switch after checker.Check (near lines 207-220) handles gateErr != nil (ReviewFixRegateError) and !gate.Passed (ReviewFixReverted); both reset HEAD to PreFixSHA through resetReviewFix. The quality runner (`internal/quality/runner.go`) gives each gate its own gate.DefaultTimeout() (test 10m, build 5m, lint 2m; `internal/quality/types.go` near line 76) and returns ErrGateTimeout per gate (GH-6080), but when the parent context expires first, runGate records "context cancelled" with StatusFailed (near line 280). PrePRReviewConfig (`internal/executor/backend.go` near line 873) has Timeout (6m) and FixTimeout (10m) but no re-gate field. RenderPrePRReviewSection (`internal/executor/pre_pr_review_render.go` near line 46) writes the `Fix:` line of the PR body from the fix status. The "Pre-PR review fix round" log line (pre_pr_fix.go, finish) reports status, pre_fix_sha, post_fix_sha, fixed, elapsed.

On 2026-11-05 (GH-6109, execution 2c437730) the re-gate ended `all_passed=false total_time=5m0.04s` on a repo whose `test` gate takes 5m30s–17m; the fix was reverted as a red test and the PR body said `Fix: reverted`. The 5m cap can never pass here, and a timeout is reported as a failure.

probe: present qf-studio/pilot reviewFixRegateTimeout[[:space:]]*=[[:space:]]*5
probe: present qf-studio/pilot ReviewFixReverted[[:space:]]+ReviewFixStatus
probe: present qf-studio/pilot func[[:space:]]RenderPrePRReviewSection\(
probe: absent qf-studio/pilot func[[:space:]]TestReviewFix_RegateTimeoutRevertsWithOwnStatu[s]
probe: absent qf-studio/pilot ReviewFixRegateTimeou[t][[:space:]]+ReviewFixStatus

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
probe: absent qf-studio/pilot func[[:space:]]TestNotifyExternalClose_RCHeldReaso[n]

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

### LEG A2 — repo: qf-studio/pilot — title: feat(executor): pre-PR review on_unfixed hold — blocking defects that survive the fix round end the run needs_human through holdPushedBranch: branch pushed, no PR, defects and evidence on the issue with pilot-needs-human

## Context

After the fix round, `internal/executor/runner.go` (near line 6790) checks `fix.Status == ReviewFixFixed` only to re-stamp the gates and refresh the evidence; every other outcome (reverted, regate_error, regate_timeout from leg A1, failed, and a nil fix when the round does not apply: `internal/executor/pre_pr_fix.go` near line 108) falls through to the intent judge, the already-merged / no-commits / existing-PR guards (near lines 7088-7137), the push (near line 7256) and PR creation (near line 7503), where publishPrePRReview adds pilot-review-rc and writes the pr_reviews row. Autopilot then parks the PR awaiting_approval (PreReviewReason, `internal/autopilot/scope_guard.go` near line 223); that hold has no automated exit (own-login reviews dropped by isTrustedReviewer, GH-5326; GitHub refuses REQUEST_CHANGES from the PR author's account), so on 2026-11-05 PR#6117 (GH-6109) sat two hours and was closed by hand with a refile.

The executor already has the hand-off this leg needs: holdPushedBranch (`internal/executor/runner.go` near line 3326, GH-5399) pushes the branch under holdPushBudget, posts an issue comment, adds pilot-needs-human, removes pilot-retry-ready, sets result.Outcome = "needs_human" and StageFailed. needs_human is terminal (TerminalStatus near line 562; ExecStatusNeedsHuman in `internal/executor/lifecycle.go` near line 70 "must NOT stack a pilot-failed label"; `internal/executor/dispatcher.go` near lines 55 and 87), HasTerminalCompletion counts it done (`internal/memory/store.go` near line 1545) so nextRetryGeneration grants no retry, handler_common.go sends no duplicate TaskFailed alert, `cmd/pilot/rearm_needs_human.go` re-arms it when the human removes the label, and the GH-5849 live-issue admission check skips the issue while pilot-needs-human is on. The fix round never re-reviews: after a fix the verdict stays REQUEST-CHANGES. Borrowed branches (a fix issue that continues an open PR, runner.go near line 1190) push onto that PR's head; PreReviewReason reads the original PR body, so a hold there would ship unfixed defects. The error text is substring-classified by briefs.ClassifyFailure (`internal/briefs/structured.go` near line 159) and outcomeClassifiers (runner.go near line 458). PrePRReviewConfig (`internal/executor/backend.go` near line 873) is daemon-wide; there is no per-project override. recorder.Finish and the completion bookkeeping run only in the success branch (runner.go near line 7567).

Founder decision D1 (TASK-641, 11-05): the PR with unfixed blocking defects is the defect; the default becomes hold.

Depends on: leg A1 (same files; it adds ReviewFixRegateTimeout and the regate fields the hold comment renders).

probe: present qf-studio/pilot fix\.Status[[:space:]]*==[[:space:]]*ReviewFixFixed
probe: present qf-studio/pilot ReviewFixRegateTimeout
probe: present qf-studio/pilot func[[:space:]]\(r[[:space:]]\*Runner\)[[:space:]]holdPushedBranch\(
probe: present qf-studio/pilot ExecStatusNeedsHuman
probe: absent qf-studio/pilot func[[:space:]]prePRHoldReaso[n]\(
probe: absent qf-studio/pilot func[[:space:]]TestPrePRHold_NoPRWhenBlockingUnfixe[d]

## Scope

- Config (`internal/executor/backend.go`, PrePRReviewConfig): OnUnfixed string with the yaml tag on_unfixed,omitempty; values hold (default for empty) and open_pr (today's behaviour). Validation in `internal/config/config.go` rejects other values with both accepted values in the message. `configs/pilot.example.yaml`: one commented line `on_unfixed: hold` with a two-line comment (hold = branch pushed, no PR, defects on the issue, pilot-needs-human, the run ends needs_human; open_pr = PR opened and parked awaiting approval). The setting is daemon-wide; say so in the comment.
- Hold decision (`internal/executor/runner.go`): a helper `prePRHoldReason(review *PrePRReviewOutcome, fix *ReviewFixOutcome, cfg PrePRReviewConfig) (string, bool)` returns true when mode is enforce, OnUnfixed resolves to hold, the verdict is REQUEST-CHANGES with at least one [blocking] defect, and the fix is nil or its Status != ReviewFixFixed. The reason string is fixed text that never changes with the fix status: "pre-PR review: blocking defects not fixed" (no "timeout", "timed out", "conflict", "test failed", "push failed", "rate limit" or "usage limit" substrings; the fix status goes into the comment, not the reason). Never hold on a borrowed branch or when an existing PR is adopted for the branch (the existing-PR guard near line 7120): in those cases log one WARN "Pre-PR review hold skipped: borrowed branch" and continue as today (open_pr path).
- Hold execution: at the point where the run would push and create the PR (after the already-merged and no-commits guards and the existing-PR adoption, before CreatePR), when prePRHoldReason is true, call holdPushedBranch with that reason, then: post the hold comment through the same issue-comment path holdPushedBranch uses, made of the rendered "## Pre-PR review" section (verdict, fix outcome incl. the A1 regate fields, the defects), the acceptance evidence block the PR body would have carried, the branch name and HEAD SHA, and the re-arm instruction (edit the issue or the branch, then remove pilot-needs-human); remove pilot-in-progress on the issue through the same label mutation (pilot-needs-human is added by holdPushedBranch; add no pilot-failed); call recorder.Finish (the recording must not stay unfinished) and record the "Pre-PR review hold" INFO log line with task_id, branch, sha, blocking count, fix status, regate status; skip the intent judge, CreatePR, publishPrePRReview, the PR webhook and the completion alert. The run ends with result.Outcome = "needs_human" (status needs_human), never failed; recordLearning / recordOutcome see it as they see any needs_human run today. Increment the existing pre-PR fix counter with outcome "hold" (RecordPrePRFix) so Prometheus shows holds until C1 adds its own counter.
- open_pr: unchanged (PR opened, pilot-review-rc, awaiting_approval).
- Docs: `docs/content/getting-started/configuration.mdx` pre_pr_review block (near line 296): add on_unfixed with one sentence each for hold and open_pr, and replace the stale "fix_timeout … used by a later release" note.

Tests to add, named exactly, in a new file, plain text: internal/executor/pre_pr_hold_test.go (use the runner fixtures of pre_pr_fix_test.go and the holdPushedBranch tests: fake PR creator, fake GitHub issue/label client, recorder spy, alert sink): TestPrePRHold_NoPRWhenBlockingUnfixed (enforce, RC with one blocking defect, fix status reverted, on_unfixed unset → PR creator not called, branch pushed, issue comment contains "## Pre-PR review", the evidence block and the branch, labels add [pilot-needs-human] remove [pilot-in-progress] and no pilot-failed, result.Outcome needs_human, recorder finished, no TaskFailed alert), TestPrePRHold_NilFixWithBlockingHolds (max_fix_rounds 0 → fix nil → hold), TestPrePRHold_RegateTimeoutRendersNotVerified (fix status regate_timeout → comment says "not verified"; reason text unchanged), TestPrePRHold_FixedProceedsToPR (fix status fixed → PR created, no hold), TestPrePRHold_OpenPRKeepsToday (on_unfixed open_pr → PR created with pilot-review-rc), TestPrePRHold_MinorOnlyProceeds (RC with minors only → PR created), TestPrePRHold_BorrowedBranchNeverHolds (task on a borrowed branch with an open PR → no hold, WARN logged, today's path), TestPrePRHold_ReasonTextIsClassifierSafe (briefs.ClassifyFailure and the outcomeClassifiers table leave the reason text unclassified / unknown). Plus TestPrePRReviewConfig_OnUnfixedValidation (`internal/config/config_test.go`: empty, hold, open_pr accepted; "fail" rejected with the accepted values) and TestNeedsHumanHold_NotRetriedByDispatcher (`internal/executor/`: a needs_human row with the trigger label and pilot-needs-human present gets no generation+1; after the label is removed and rearm runs, it does).

## Acceptance

- [ ] `go test -race ./internal/executor/ -run 'TestPrePRHold|TestNeedsHumanHold'` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/config/ -run TestPrePRReviewConfig_OnUnfixedValidation` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/executor/ -run 'TestReviewFix|TestPrePRReview|TestHoldPushedBranch'` still passes; paste the output into the PR body.
- [ ] open the PR on hold -> TestPrePRHold_NoPRWhenBlockingUnfixed fails
- [ ] end the run failed instead of needs_human -> TestPrePRHold_NoPRWhenBlockingUnfixed fails
- [ ] hold on a borrowed branch -> TestPrePRHold_BorrowedBranchNeverHolds fails
- [ ] treat a nil fix as "no blocking defects" -> TestPrePRHold_NilFixWithBlockingHolds fails
- [ ] put the fix status into the reason text -> TestPrePRHold_ReasonTextIsClassifierSafe fails
- [ ] hold on minors only -> TestPrePRHold_MinorOnlyProceeds fails
- [ ] Do not re-run make build, make test or make lint as evidence: the daemon's quality gates already run them on this repo.

## Out of scope

The re-gate budget and the regate_timeout status (leg A1, already merged when this runs); the autopilot close handler (leg B1); needs_human metrics buckets, the holds counter, the Run Story outcome and the operator SOP (leg C1); a second fix round (max_fix_rounds > 1); the reviewer prompts; shadow mode; a per-project or per-tenant on_unfixed override; the hosted console surfaces for needs-human; non-GitHub adapters (holdPushedBranch's SourceAdapter branching stays as it is).

## Refs

- GH-6109 / PR#6117 (the first enforce RC: fix reverted, PR opened, closed by hand after 2 h) · GH-5399 (holdPushedBranch) · GH-5849 (live-issue admission skip on pilot-needs-human) · TASK-620 L3/L5 (fix round, awaiting_approval hold; L5 decision superseded by TASK-641 D1) · GH-5326 (own-login reviews dropped) · Navigator TASK-641 in this repo

### LEG C1 — repo: qf-studio/pilot — title: feat(metrics): needs_human runs get their own bucket in the lifetime and windowed task counts, a pilot_pre_pr_holds_total counter, a Run Story outcome, and an operator SOP for resolving a pre-PR review hold

## Context

A needs_human execution (ExecStatusNeedsHuman, `internal/executor/lifecycle.go` near line 70; set by holdPushedBranch, `internal/executor/runner.go` near line 3326, and by the pre-PR review hold of leg A2) is terminal but invisible to the counts: GetLifetimeTaskCounts and GetWindowedStats (`internal/memory/store.go` near lines 4942-5065) put it in Total and in neither Failed nor NonFailure, the attempts denominator is `status IN ('completed','failed')`, and `internal/gateway/dashboard.go` (near line 256) derives SucceededTasks / FailedTasks / DeliveryRate / AttemptSuccessRate from them. Prometheus has pilot_pre_pr_fixes_total{outcome} (`internal/gateway/prometheus.go` near line 623) but no counter for holds. Run Story (`internal/gateway/run_story.go` near lines 188-243) maps statuses to outcomes (failed, review_needed, …) with no needs_human outcome, so a hold renders as a failure with no branch and no next step. `.agent/sops/` has no procedure for resolving a needs-human hold (the re-arm path is `cmd/pilot/rearm_needs_human.go`: remove pilot-needs-human with the trigger label present).

Depends on: leg A2.

probe: present qf-studio/pilot func[[:space:]]\(s[[:space:]]\*Store\)[[:space:]]GetLifetimeTaskCounts\(
probe: present qf-studio/pilot pilot_pre_pr_fixes_total
probe: present qf-studio/pilot ExecStatusNeedsHuman
probe: present qf-studio/pilot func[[:space:]]prePRHoldReason\(
probe: absent qf-studio/pilot func[[:space:]]TestRunStory_NeedsHumanOutcom[e]
probe: absent qf-studio/pilot func[[:space:]]TestLifetimeTaskCounts_NeedsHumanBucke[t]

## Scope

- Counts (`internal/memory/store.go`): LifetimeTaskCounts and the windowed stats struct gain NeedsHuman int, filled from `status = 'needs_human'`; NonFailure() includes it; the attempts denominator stays `completed + failed` (a hold is neither an attempt success nor a failure). `internal/gateway/dashboard.go` exposes needs_human_tasks next to succeeded_tasks / failed_tasks on the same endpoints; `pilot metrics summary` prints a "needs human" line.
- Prometheus (`internal/gateway/prometheus.go`): pilot_pre_pr_holds_total{project} incremented from the A2 hold path (replace the temporary RecordPrePRFix outcome "hold" with this counter).
- Run Story (`internal/gateway/run_story.go`): outcome needs_human for status needs_human, with the hold reason, the branch name and HEAD SHA (from the execution row's branch / commit_sha), and next_step "Edit the issue or the branch, then remove pilot-needs-human". classifyRunStoryFailure is not called for this outcome.
- SOP: new file, plain text: .agent/sops/operations/resolve-pre-pr-review-hold.md — when a run ends needs_human with reason "pre-PR review: blocking defects not fixed": read the defects on the issue comment, choose (a) fix the branch by hand and remove pilot-needs-human (the re-arm runs the task again on the existing branch), (b) edit the issue and remove the label, or (c) close the issue; what the Run Story card shows; what `pilot billing-mode status` / `pilot status` show nothing about it. Link it from `.agent/DEVELOPMENT-README.md` § SOPs. Also update TASK-620 § Rollout stage 3: "pilot-review-rc holds ≤ 2/10" becomes "pre-PR holds (needs_human, pre-PR reason) ≤ 2/10", and "fixed ≥ 50 % of RC runs" is measured under the A1 budget.

Tests to add, named exactly: TestLifetimeTaskCounts_NeedsHumanBucket (`internal/memory/store_test.go`: one completed, one failed, one needs_human row → NeedsHuman 1, NonFailure 2, attempts 2), TestWindowedStats_NeedsHumanBucket (same on the windowed query), TestDashboardStats_NeedsHumanField (`internal/gateway/dashboard_test.go`: the field is present and equals the store count), TestRunStory_NeedsHumanOutcome (`internal/gateway/run_story_test.go`: outcome needs_human, branch and sha present, next_step text), TestPrometheus_PrePRHoldsCounter (`internal/gateway/prometheus_test.go`: counter increments once per hold with the project label).

## Acceptance

- [ ] `go test -race ./internal/memory/ -run 'NeedsHumanBucket'` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/gateway/ -run 'TestDashboardStats_NeedsHumanField|TestRunStory_NeedsHumanOutcome|TestPrometheus_PrePRHoldsCounter'` passes; paste the output into the PR body.
- [ ] count needs_human as failed -> TestLifetimeTaskCounts_NeedsHumanBucket fails
- [ ] map needs_human to the failed outcome in Run Story -> TestRunStory_NeedsHumanOutcome fails
- [ ] Do not re-run make build, make test or make lint as evidence: the daemon's quality gates already run them on this repo.

## Out of scope

The hold itself (leg A2); the console UI card for needs_human (a pilot-console-ui leg once the gateway field ships); the ticket ledger (a hold is already unbilled: no completed status, no PR); hosted tenant config.

## Refs

- TASK-641 A2 · GH-5399 (holdPushedBranch, needs_human) · TASK-583 (Run Story) · TASK-620 § Rollout (stage-3 criteria) · Navigator TASK-641 in this repo

## Refs

- A1 → https://github.com/qf-studio/pilot/issues/6130
- B1 → https://github.com/qf-studio/pilot/issues/6131
- A2 → (pending)
- C1 → (pending)
