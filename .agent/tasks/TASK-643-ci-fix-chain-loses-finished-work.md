# TASK-643: Autopilot CI-fix chain loses finished work — sweep deletes the fix branch, pre-flight rejects the fix issue, re-arm restarts from scratch, the upgrade drain pauses admission behind a long task

**Status**: ✅ **B1 RELEASED IN v2.315.2 (11-08 15:04Z; PR#6202 + PR#6204/#6206). All four legs released. Open: E1 candidate (retry after a pre-PR hold continues the held branch) — not filed 11-08.** Was: ✅ **A1 + C1 + D1 SHIPPED IN v2.315.1 (tag 11-07 14:14Z, published 15:19Z) — A1 [PR#6184](https://github.com/qf-studio/pilot/pull/6184) 08:50Z (post-merge RC → #6185 fail-open on a store error → PR#6192 10:40Z) · C1 [PR#6188](https://github.com/qf-studio/pilot/pull/6188) 09:50Z (minors #6191 → PR#6197, its RC → #6198 → PR#6199) · D1 [PR#6194](https://github.com/qf-studio/pilot/pull/6194) 11:06Z (passive wait).** 🔁 **B1 [#6182](https://github.com/qf-studio/pilot/issues/6182) re-armed twice:** 09:12Z pre-PR hold (3 blocking: failing-test list read the capped excerpt; test fed a raw log; bypass skipped the label check) → re-armed with the three fixes as instructions; 10:12Z the retry started from origin/main and could not fast-forward the held branch (`non-fast-forward against task branch pilot/GH-6182`) → `pilot-blocked` + `needs-human`; 15:35Z held branch moved to `pilot-hold/GH-6182-e1a27dc`, re-armed `pilot-retry-ready`. **Follow-up candidate E1 (not filed):** a retry after a pre-PR hold must continue the held branch (same mechanism as C1's footer) instead of starting from main and failing the push — the hold comment carries `branch=… sha=…` but no `autopilot-meta` footer, so the dispatcher's branch reuse never fires. Was: 🚀 **A1/B1/C1 DISPATCHED 2026-11-07 → [#6181](https://github.com/qf-studio/pilot/issues/6181) · [#6182](https://github.com/qf-studio/pilot/issues/6182) · [#6183](https://github.com/qf-studio/pilot/issues/6183)** (`pilot` + `no-decompose`; serial pilot queue runs them in that order; C1 rebases after #6163). **D1 DISPATCHED 2026-11-07 → [#6187](https://github.com/qf-studio/pilot/issues/6187) (passive-wait design, founder call).** Was: 📋 PLANNED 2026-11-06 17:20Z — four legs drafted (A sweep guard · B pre-flight for autopilot-fix · C re-arm keeps the branch · D drain does not re-pause admission). Founder review before dispatch; D is a founder call.** Incident salvage done the same evening: branch `pilot/GH-6157` restored from `refs/pull/6165/head`, hand-filed fix [#6168](https://github.com/qf-studio/pilot/issues/6168) (autopilot-fix shape, three tests named) picked up by Pilot; #6167 closed; #6157 unlabelled so its from-scratch re-run drops at admission.
**Created**: 2026-11-06 · **Owner**: Navigator plans, Pilot executes · **Related**: TASK-508 (CI-fix attribution), TASK-641/TASK-642 (owner-death family; #6163 in flight fixes the `pilot-in-progress` strip in the same re-arm path), TASK-620 (L4 is the run that was lost)

---

## Context

**Incident 2026-11-06 (box v2.314.0 → v2.315.0):**

1. 15:12Z the 15:00 release train cut v2.315.0. The self-upgrade paused dispatcher admission three times (15:15, 15:45, 16:15) for 30 min each while GH-6157 (TASK-620 L4, 2 h 05 m) kept the drain from completing. Queued work waited ~90 min.
2. 16:19Z L4 finished → PR#6165 (28 files, +582 −2515). 16:30Z CI failed on three stale self-review tests in `internal/executor/quality_integration_test.go` (`TestQualityGatesHappyPath` 182, `TestQualityGatesRetrySuccess` 266, `TestQualityGatesRetryFeedbackPropagation` 567). 16:32Z autopilot closed the PR, filed CI-fix #6167 to continue `pilot/GH-6157`, and queued it — behind the paused admission.
3. 16:44:55Z the hot upgrade restarted the daemon. Boot reconciliation marked four queued executions dead-owner (GH-6152, GH-6163, GH-6167, console-ui GH-390). 16:46:30Z the startup `sweepStaleBranches` **deleted `pilot/GH-6157`**: closed PR, no open PR, no running task with that id — the pending fix issue that referenced the branch in its `autopilot-meta` footer is not consulted.
4. 16:47:21Z the pre-flight judge rejected #6167 `reject_vague` ("does not explain … whether the tests need updating or the implementation needs fixing"). 16:47:26Z `rearmDeadOwnerSource` re-armed #6157 for a **from-scratch** retry; `reactToDeadFixIssue` had already forgotten the borrowed branch. Same shape the same day: #6155 (12:42Z, fix for PR#6154) and console-ui #392 (16:50Z). On 11-04 and 11-05 all four CI-fix issues passed pre-flight.

**Verified at main c7461d04c (v2.314.0) — none of the four behaviours changed in v2.313.1..v2.314.0; this is latent design, not a regression:**

- `internal/autopilot/branch_sweeper.go`: for a head ref with a closed PR the branch is deleted on the next tick unless it is in `openHeads`, `staleBranchTracked()` (activePRs) or `staleBranchLiveTasks()` (running task ids, matched by the `GH-<n>` suffix — the fix issue's id is `GH-6167`, so it never matches, and queued tasks are not covered). `DefaultStaleBranchMinAge` (24 h, line 16) applies only to branches with no PR. `BorrowedBranchLookup` (`controller.go` 1441–1458) is not consulted. Interval `max(MergedPRScanWindow, 1h)` plus one sweep at startup (`controller.go` ~10331).
- `cmd/pilot/poller_github.go` 288–307 `sdkPreFlightJudge.JudgeIssue` forwards title + body + empty repoContext to `IntentJudge.JudgeIssue` (`intent_judge.go` 660); `preflightJudgeSystemPrompt` (338–352) has no label or title awareness; `IsRejection` is `Decision != accept` with no threshold. The CI-fix body's Task section (`feedback_loop.go` ~850, `FailureCIPreMerge`) is one sentence: "Fix the CI failures listed above. Run tests locally before committing." The judge's complaint matches that sentence exactly.
- `internal/autopilot/owner_death.go`: `reactToDeadFixIssue` (477) calls `forgetBorrowedBranchForIssue`; `rearmDeadOwnerSource` (537–582) adds `pilot-retry-ready`, strips `pilot-failed`/`pilot-superseded`, and posts a comment with only the reason — no branch, PR or sha. The retry starts from the default branch.
- `internal/upgrade/hot.go` `PerformHotUpgrade` (109): pauses admission (`Dispatcher.PauseAdmission`, `dispatcher.go` 3345), waits for running tasks with a timeout, fails with "timeout waiting for tasks", and the version checker retries ~30 min later — pausing again. One long task therefore turns the drain into a 30-min-on/30-min-off admission pause until it ends.

**Goal**: a CI failure on a Pilot PR costs one short fix run on the same branch, never the original run; a release never costs the queue more than one drain window.

---

## Known Pitfalls & Patterns

- **PITFALL** (`ci-fix-continuation-rebuilds-branch-from-main-drops-original-commits`): the fix continuation must check out the existing branch by name with its remote commits, never `-B` from `origin/main`. Leg C's "continue the branch" relies on this fix staying in place; add a pin test.
- **PITFALL** (`autopilot-fix-issue-depends-on-open-original-deadlock`): a fix body that says "Depends on: #original" while the original stays open deadlocks admission. Leg B's richer body must keep `Closes #<source>` and must not add a dependency line.
- **PITFALL** (`hand-file-the-ci-fix-issue-in-autopilot-fix-shape-when-the-size-guard-declines`): the hand-filed shape (labels `pilot` + `autopilot-fix` + `no-decompose`, `autopilot-meta` footer with `branch: pr: iteration: source: sha:`) is the recovery path today and is what #6168 used; Leg C must keep the footer contract.
- **PITFALL** (`agent-opened-pr-adopted-and-merged-before-gates-reaper-prunes-live-worktree`): any new branch-protection logic must not protect branches of adopted agent PRs forever — scope the guard to open `autopilot-fix` issues and pending executions.
- **PITFALL** (mem-282, log trimming → false fix issue): the CI excerpt in the body is head-truncated; the third failing test in this incident was outside the excerpt. Leg B extracts `--- FAIL:` names from the full log, not from the excerpt.

---

## Acceptance Criteria

- [ ] A branch whose closed PR is referenced by an open `autopilot-fix` issue, or by a queued/pending execution, survives the stale-branch sweep; the sweep log line names the guard that held it.
- [ ] An autopilot-generated CI-fix issue passes pre-flight without a model call, or its body states which side (tests or implementation) is wrong and lists every `--- FAIL:` test from the full log.
- [ ] When a fix issue dies, the re-arm comment carries the closed PR's branch and sha, and the retry's worktree starts from that branch with its commits present.
- [ ] (D, founder call) While a task runs past the drain timeout, admission is not paused again until the running set is empty; the queue keeps draining other projects.

---

## Legs — pilot (daemon)

### LEG A1 — repo: qf-studio/pilot — title: fix(autopilot): the stale-branch sweep keeps a closed-PR branch that an open autopilot-fix issue or a pending execution still references, and applies the 24h age gate to closed-PR branches
probe: present qf-studio/pilot func \(c \*Controller\) sweepStaleBranches\(
probe: present qf-studio/pilot func \(c \*Controller\) staleBranchLiveTasks\(
probe: present qf-studio/pilot func \(c \*Controller\) forgetBorrowedBranchForIssue\(

## Context

`sweepStaleBranches` in `internal/autopilot/branch_sweeper.go` deletes the head branch of a closed PR on the next tick when the branch is not an open PR head, not in `activePRs`, and not the `GH-<n>` suffix of a running task. On 2026-11-06 16:46:30Z the startup sweep deleted `pilot/GH-6157` 14 minutes after autopilot closed PR#6165 and filed CI-fix #6167 whose `autopilot-meta` footer named that branch (`branch:pilot/GH-6157 pr:6165`). The fix issue was queued, not running, under its own id GH-6167, so no guard matched. The 24-hour `DefaultStaleBranchMinAge` applies only to branches without a PR.

## Implementation

1. Add a third protection set, `staleBranchReferencedByFixIssues(ctx, repo)`, built from the open issues labelled `autopilot-fix` in the repo: parse each body's `<!-- autopilot-meta branch:… -->` footer with the existing footer parser used by the dispatcher's branch reuse ("Reusing branch from autopilot-meta footer") and collect the branch names. Also include every branch recorded by the borrowed-branch registry (`BorrowedBranchLookup`) for issues that are still open.
2. Extend `staleBranchLiveTasks` to include queued and pending executions, not only running ones, using the dispatcher's queue view the TUI already reads.
3. Apply `DefaultStaleBranchMinAge` to closed-PR branches as well: a branch whose PR closed less than 24 h ago is `skipped_young`, independent of the guards above.
4. Log one line per held branch: `stale branch sweep: kept branch` with `branch`, `pr`, `reason` ∈ {`fix_issue`, `pending_task`, `young`}. Add the three counters to the summary line.
5. Tests in `branch_sweeper_test.go`: closed PR + open autopilot-fix issue with the footer → kept with `reason=fix_issue`; closed PR + queued task `GH-<other>` whose footer names the branch → kept; closed PR 25 h old, no references → deleted; closed PR 1 h old, no references → `skipped_young`.

## Acceptance

- `go test -race ./internal/autopilot/ -run 'TestSweepStaleBranches'` passes; paste the output into the PR body.
- The 2026-11-06 sequence replayed in a test (PR closed at T, fix issue open with the footer, sweep at T+14 min) leaves the branch in place.
- Mutation: drop the fix-issue guard → the replay test fails.

## Out of scope

- Changing when autopilot closes the failed PR (it stays closed; the fix issue continues the branch).
- The pre-flight judge (Leg B) and the re-arm (Leg C).

## Refs

- Incident log 2026-11-06 16:46:30Z `stale branch sweep: deleted orphaned branch … branch=pilot/GH-6157 pr=6165`
- `internal/autopilot/branch_sweeper.go` lines 16, 57–70, 86–170 · `controller.go` 1441–1458 (borrowed-branch registry)

### LEG B1 — repo: qf-studio/pilot — title: fix(autopilot): autopilot-generated CI-fix issues pass pre-flight — the judge accepts autopilot-fix issues with a valid autopilot-meta footer, and the generated Task section names every failing test from the full log and states that the tests or the implementation must change
probe: present qf-studio/pilot func \(s \*sdkPreFlightJudge\) JudgeIssue\(
probe: present qf-studio/pilot func \(f \*FeedbackLoop\) generateBody\(
probe: present qf-studio/pilot const preflightJudgeSystemPrompt

## Context

Three autopilot CI-fix issues were rejected `reject_vague` by the pre-flight judge on 2026-11-06 (#6155 12:42Z, #6167 16:47Z, console-ui #392 16:50Z) after four passed on 11-04/11-05. `sdkPreFlightJudge.JudgeIssue` (`cmd/pilot/poller_github.go` 288–307) sees title and body only — no labels — and `preflightJudgeSystemPrompt` (`internal/executor/intent_judge.go` 338–352) has no clause for machine-generated fix requests. The generated body (`internal/autopilot/feedback_loop.go` `generateBody`, Task text for `FailureCIPreMerge` near line 850) says only "Fix the CI failures listed above. Run tests locally before committing." The CI excerpt is head-truncated, so the third failing test of PR#6165 was not in the body at all. The judge's stated reason ("whether the tests need updating or the implementation needs fixing") is a fair reading of that body.

## Implementation

1. Deterministic bypass. In the poller's pre-flight path, before the model call: when the issue carries the `autopilot-fix` label **and** its body ends with a well-formed `autopilot-meta` footer (branch, pr, source, sha present), return `accept` with reason `autopilot-fix footer` and log `pre-flight: accepted autopilot-fix issue without a judge call` (issue, branch, pr). Issues with the label but no footer still go to the judge. Expose the labels to the judge call site (the SDK poller passes the issue; read `issue.Labels`).
2. Richer body. In `generateBody` for `FailureCIPreMerge` and `FailureCIPostMerge`: extract every `--- FAIL: <Name>` line and the assertion line that precedes it from the **full** failed-job log (the same source `ci_failure_excerpt.go` reads before truncation), de-duplicate, and render a `## Failing tests` list above the excerpt. Replace the one-line Task with: "The PR's change is the intended one. First decide per failing test whether the test is stale (asserts behaviour the PR removed) or the change is wrong; port stale tests, fix wrong code; say which in the PR body." Keep `Closes #<source>` and the footer unchanged; do not add a `Depends on` line.
3. Tests: `feedback_loop_test.go` — a log with three FAIL blocks, two outside the 4000-char excerpt → all three in `## Failing tests`; the Task text contains the decision sentence. `poller_github_test.go` (or the SDK-shim test) — an `autopilot-fix` issue with a valid footer never invokes the judge; one without a footer does.

## Acceptance

- `go test -race ./internal/autopilot/ -run 'TestGenerateBody'` and `go test -race ./cmd/pilot/ -run 'TestPreFlight'` pass; paste both outputs into the PR body.
- A replay of #6167's inputs (PR#6165 run 42251277383 log) yields a body listing `TestQualityGatesHappyPath`, `TestQualityGatesRetrySuccess`, `TestQualityGatesRetryFeedbackPropagation`.
- Mutation: remove the footer check from the bypass → the "no judge call" test fails.

## Out of scope

- Changing the judge prompt for human-written issues.
- The size guard (`ci-fix-size-guard…` pitfall) and the post-merge path's dedup (TASK-508).

## Refs

- #6155, #6167, console-ui #392 (declined 2026-11-06); #6107/#6108 (accepted 2026-11-05)
- `cmd/pilot/poller_github.go` 288–307 · `internal/executor/intent_judge.go` 267, 318–352, 660 · `internal/autopilot/feedback_loop.go` 55, 157, 777, ~850 · `internal/autopilot/ci_failure_excerpt.go` 23

### LEG C1 — repo: qf-studio/pilot — title: fix(autopilot): owner-death re-arm keeps the dead fix issue's branch and sha — the re-arm comment carries them and the retry continues that branch instead of starting from main
probe: present qf-studio/pilot func \(c \*Controller\) reactToDeadFixIssue\(
probe: present qf-studio/pilot func \(c \*Controller\) rearmDeadOwnerSource\(

## Context

When a designated fix issue dies (pre-flight decline, close, supersede), `reactToDeadFixIssue` (`internal/autopilot/owner_death.go` 477–479) calls `forgetBorrowedBranchForIssue` for the dead issue, and `rearmDeadOwnerSource` (537–582) re-labels the source `pilot-retry-ready` with a comment that carries only the reason. The retry claims the next generation and builds a fresh worktree from the default branch. On 2026-11-06 this discarded a 2 h 05 m run (PR#6165, 28 files) whose only defect was three stale test assertions; the salvage was manual (branch restored from `refs/pull/6165/head`, hand-filed #6168).

## Implementation

1. Before forgetting the dead issue's borrowed branch, transfer it: parse the dead issue's `autopilot-meta` footer (`branch`, `pr`, `sha`) and record the branch for the **source** issue in the borrowed-branch registry (`rememberBorrowedBranch` or the existing setter beside `forgetBorrowedBranchForIssue`), keyed by the source issue number.
2. `rearmDeadOwnerSource` appends to the re-arm comment a machine-readable footer in the same shape the dispatcher already parses: `<!-- autopilot-meta branch:<branch> pr:<pr> iteration:<n+1> source:<source> sha:<sha> -->`, plus one human line: "Retry continues branch `<branch>` at `<sha7>`; the closed PR #<pr> holds the review history."
3. Dispatcher side: the branch-reuse step ("Reusing branch from autopilot-meta footer") already reads a footer from the issue body for `autopilot-fix` issues; extend it to read the latest `autopilot-meta` footer from the issue's **comments** for a `pilot-retry-ready` source issue, verify the branch still exists on the remote (`git ls-remote`), and check it out by name with its commits (per the `ci-fix-continuation-rebuilds-branch-from-main` fix — never `-B` from `origin/main`). If the branch is gone, fall back to the current from-scratch path and log `re-arm branch missing, starting from main`.
4. Tests: `owner_death_test.go` — dead fix issue with footer → source registry entry present and comment footer rendered; dispatcher test — a source issue with a re-arm footer comment and an existing remote branch → worktree HEAD equals the footer sha; missing branch → fallback logged.

## Acceptance

- `go test -race ./internal/autopilot/ -run 'TestRearmDeadOwnerSource|TestReactToDeadFixIssue'` and `go test -race ./internal/executor/ -run 'TestReuseBranchFromRearmComment'` pass; paste both outputs into the PR body.
- Replay of 2026-11-06: fix issue declined → source re-armed → retry worktree HEAD = `b57d141…` with 28 changed files vs main.
- Mutation: skip the registry transfer → the HEAD assertion fails.

## Out of scope

- Reopening the closed PR (the retry opens a new PR from the same branch, as the CI-fix path does today).
- Leg A's sweep guard (prerequisite for the branch to still exist; land A first).

## Refs

- `internal/autopilot/owner_death.go` 477–479, 537–582 · `internal/autopilot/controller.go` 1441–1458
- #6157 re-arm comment 2026-11-06 16:47Z; TASK-642 (#6128 touched `verifyFixPRDeliversSourceScope` in the same file); #6163 (in flight, same function family — rebase after it merges)

### LEG D1 — repo: qf-studio/pilot — title: fix(upgrade): the hot-upgrade drain does not re-pause admission every cooldown while one long task runs — after a drain timeout the upgrader waits for the running set to empty before pausing again
probe: present qf-studio/pilot func \(h \*HotUpgrader\) PerformHotUpgrade\(
probe: present qf-studio/pilot func \(d \*Dispatcher\) PauseAdmission\(

## Context

**Founder decision 2026-11-07: design (1), the passive wait — admission stays open after the first drain timeout; the box may run the old binary until the longest task ends.** On 2026-11-06 the version checker found v2.315.0 at 15:15Z while GH-6157 ran (2 h 05 m). `PerformHotUpgrade` (`internal/upgrade/hot.go` 109) paused admission (`Dispatcher.PauseAdmission`, `internal/executor/dispatcher.go` 3345), waited, failed with "timeout waiting for tasks" after 30 min, and the checker retried on its cooldown: three windows (15:15, 15:45, 16:15) with admission paused for most of 90 minutes across every project on the box; the queue depth itself no longer blocks the drain (log: "queue depth no longer blocks the drain"), only the running task did. Two designs were possible: (1) after a drain timeout, do not pause admission again until the running set is empty — new tasks keep flowing and the upgrade waits for a natural gap; or (2) keep pausing but let admission continue for projects whose running set is empty. (1) is chosen; (2) needs per-project admission state that does not exist and is out of scope.

## Implementation

1. In the upgrader's retry path: after a drain timeout, record `drainBlockedBy=<task ids>` and switch to a passive wait — poll the task checker on the checker interval **without** calling `PauseAdmission`; when the running set is empty, pause, re-check (a task may have been admitted between the check and the pause), and proceed with the install. Log `self-upgrade: passive wait, admission open` once per transition.
2. Cap the passive wait at a configurable `upgrade.max_wait` (default 6 h); on expiry alert once (`self-upgrade waited N h; a long task blocks the drain`) and keep waiting — never kill a task for an upgrade.
3. Tests in `hot_test.go`: a fake task checker with one task that finishes at T+45 min → exactly one pause window, admission open between the timeout and T+45; two tasks ending 10 min apart → the second pause starts only after both end.

## Acceptance

- `go test -race ./internal/upgrade/ -run 'TestPerformHotUpgrade'` passes; paste the output into the PR body.
- Replay of 2026-11-06 timings (task 13:56→16:19, release at 15:12) yields one 30-min pause window, then open admission until 16:19, then the install.
- Mutation: re-enable the pause in the retry path → the "admission open between" assertion fails.

## Out of scope

- The release train's cut timing and what counts as a release-worthy merge.
- Tenant upgrades (hosted tenants do not self-upgrade — `tenants-do-not-self-upgrade-hosted-invariant`).

## Refs

- Log 2026-11-06 15:15:57Z → 16:44:55Z (`self-upgrade`, `dispatcher admission paused`, `upgrade failed: timeout waiting for tasks`, `drain timeout … [GH-6157]`)
- `internal/upgrade/hot.go` 109–140 · `internal/upgrade/drain.go` · `internal/executor/dispatcher.go` 3345–3360 · `internal/config/config.go` 808/852 (30-min cooldown)

---

## Out of Scope (task)

- Why the release train bundled a docs anchor PR (#6161) with code members — correct behaviour, the train cut on a code-carrying scope.
- TASK-620 L4 itself (tracked there; #6168 is its fix run).

---

## Technical Decisions

| Decision | Options | Chosen | Reasoning |
|---|---|---|---|
| Pre-flight for CI-fix issues | (a) label-only bypass · (b) footer-validated bypass · (c) richer body only | (b) + richer body | A label alone is forgeable from the UI; the footer is written by the daemon. The richer body still helps the executor, and covers hand-filed issues without a footer. |
| Sweep guard source | (a) parse open autopilot-fix footers · (b) borrowed-branch registry only | both | The registry is in-memory and empty after a restart — exactly when the startup sweep runs (16:46:30Z was a boot sweep). |
| Re-arm branch carry | (a) reopen the closed PR · (b) footer in the re-arm comment | (b) | The CI-fix path already continues a closed PR's branch via footer; a reopen would re-enter autopilot tracking with stale CI state. |
| Drain | (1) passive wait, admission open · (2) per-project pause | founder call, default (1) | (1) is the smaller change and removes the 30-min-on/off pattern; (2) needs per-project admission state that does not exist. |

---

## Verify

```bash
go test -race ./internal/autopilot/ -run 'TestSweepStaleBranches|TestGenerateBody|TestRearmDeadOwnerSource|TestReactToDeadFixIssue'
go test -race ./cmd/pilot/ -run 'TestPreFlight'
go test -race ./internal/upgrade/ -run 'TestPerformHotUpgrade'
# box, after release: a CI-fix issue created by autopilot shows "pre-flight: accepted autopilot-fix issue without a judge call" in daemon.log
```

---

## Done

- [ ] A, B, C merged and released; the next autopilot CI-fix on the box runs on the original branch within one tick, with the branch intact after a restart.
- [ ] D decided (founder); if go, merged — the next release during a long task shows one pause window in the log.
- [ ] Pitfall memory written for the chain; this doc archived.

---

## Refs

- Incident data: `daemon.log` 2026-11-06 15:12–16:51Z; PR#6165 (closed, head `b57d1413d`), #6167 (closed, superseded by #6168), #6157 (unlabelled), #6155, console-ui #392
- Research 2026-11-06 (navigator-research): no diff in `branch_sweeper.go`, `owner_death.go` re-arm, `intent_judge.go`, `poller_github.go`, `feedback_loop.go` body builder between v2.313.1 and v2.314.0 beyond #6132's post-merge dedup
- Marker `2026-11-06_pm-v2.314.0-stage3-read-l4-dispatched-task460-research-task629-m3m4-released.md`

**Last Updated**: 2026-11-06

## Refs

- A1 → https://github.com/qf-studio/pilot/issues/6181
- B1 → https://github.com/qf-studio/pilot/issues/6182
- C1 → https://github.com/qf-studio/pilot/issues/6183
- D1 → https://github.com/qf-studio/pilot/issues/6187
