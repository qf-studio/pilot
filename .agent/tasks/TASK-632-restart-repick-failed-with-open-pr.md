# TASK-632: A failed issue with an open PR is re-picked after autopilot's hold redrive strips `pilot-needs-human` — admission must refuse an issue whose pilot branch has an open PR

**Status**: 🚀 A1 DISPATCHED 2026-11-03 → [pilot#6020](https://github.com/qf-studio/pilot/issues/6020) (`pilot` + `no-decompose`).
**Created**: 2026-11-03
**Assignee**: Pilot

---

## Context

**Incident** (founder box, 2026-11-03, daemon v2.308.0 → v2.309.0): [pilot#6016](https://github.com/qf-studio/pilot/issues/6016) (TASK-631 A1) ran 10:19Z, opened PR#6017 at 11:20Z, CI failed 11:33Z, the CI-fix size guard declined (759 production additions over 200) and autopilot parked it: `pilot-needs-human` added 11:34:20Z, `pilot-in-progress` stripped 11:41:59Z, execution row `failed`. The PR stayed OPEN. A hand-filed fix issue #6019 pushed `3270a6b` to `pilot/GH-6016` at 11:53:37Z. At 11:53:50Z `pilot-needs-human` was removed from #6016. At 11:54:09Z the daemon re-picked #6016 itself (comment #2 at 11:54:12Z), cut a fresh worktree off `origin/main` and re-implemented the work PR#6017 already held. PR#6017 merged 12:14Z; the duplicate run continued past 12:23Z.

**Hypothesis in the request was partly wrong.** Evidence from the issue timeline and the code:

1. The hot-upgrade restart (11:46–11:52Z) is incidental. The re-pick came 19 s after a label removal, not after boot. `StartupRecover` (`internal/adapters/github/cleanup.go`, near line 602) only lists issues carrying `pilot-in-progress`; #6016 had lost it at 11:41:59Z, so the boot sweep never saw the issue.
2. `pilot-needs-human` IS a pickup gate. `githubLiveAdmissionSkipReason` and the GH-5056 backstop in `cmd/pilot/handlers.go` (near lines 1056 and 1384) skip any issue that carries it. The gate worked from 11:34Z to 11:53:50Z.
3. **The label was removed by autopilot.** The 11:53:51Z PR comment on #6017 reads "Re-adopted: branch updated (new head `3270a6b`) while held (CI fix size guard fired)". That is `Controller.redriveHeldPR` in `internal/autopilot/controller.go` (near line 8642): a new head on a held PR makes it call `releaseHoldLabels` (call near line 8680, definition near line 8706), whose first entry in `removeLabels` is `labelNeedsHuman`. The hold labels are removed from the linked issue, which is #6016. The helper `redriveSizeGuardHoldOnGreen` (call near line 8769) does the same on a green re-run.
4. After that removal the issue is `pilot` + no marker label, and the latest row is `failed`. A `failed` row is not terminal (`HasTerminalCompletion` is true only for completed-with-deliverable, clean no_op, canceled, superseded, needs_human), so `terminalCompletionChecker.HasCompletedExecutionReason` in `cmd/pilot/main.go` (near line 4565) returns "not done". The SDK poller's candidate filter has no PR-awareness. The issue was a clean candidate; the label-lifecycle write at 11:54:09Z is the normal pre-claim `pilot-in-progress`.
5. The TASK-624 D2 open-PR heal does not apply. `Dispatcher.healOpenPRExecution` in `internal/executor/dispatcher.go` runs from `reconcileOrphanedExecutions` on claimed `running` rows at boot; the row here was already `failed`. `startupKeepReason` in cleanup.go does check `FindOpenPRByBranch`, but only for issues that still carry `pilot-in-progress`. No admission-time code checks for an open PR on `pilot/GH-<n>`.

**Class**: any path that strips every marker label (`pilot-in-progress`, `pilot-needs-human`, `pilot-failed`…) from an issue whose PR is still open re-opens the issue for a second run on the same work. Known paths: the redrive above, a human clearing `pilot-needs-human` to unblock the PR, a label cycle. The invariant "an open PR on `pilot/GH-<n>` means the issue is owned by autopilot, not by the poller" is enforced nowhere at admission.

---

## Fix shape (decision)

Chosen: **an admission-time open-PR gate**, option (b) moved from the boot heal to the admission chokepoint. Not (a): making `pilot-needs-human` gate harder changes nothing, it already gates and autopilot removes it on purpose. Not "stop the redrive from removing the label": the redrive must clear the human-hold so the PR can proceed; the issue label is not what keeps the PR moving, and keeping it would leave a stuck label after the PR merges.

- In `cmd/pilot/handlers.go`, right after the GH-5056 needs-human backstop and before any side effect, skip admission when `FindOpenPRByBranch(owner, repo, "pilot/GH-<n>")` is true. Use the per-event `specClient` that the live-issue fetch already built. Skip reason `open_pr`. Arm the repick backoff with `recordClaimLostDrop` exactly as the needs-human branch does (the SDK bridge drops `Skipped`, so the poller unmarks and re-picks).
- Fail open: a nil client or a lookup error admits, with a Warn log. A transient GitHub error must not strand a queue.
- GH-5139 reopen/relabel re-admission keeps working: that path re-arms an issue whose PR was closed or merged, so there is no open PR and the gate passes. The cost: a human who wants a fresh run while the PR is still open must close the PR first. This is the intended trade.
- Autopilot-fix issues are unaffected: a fix issue's own task branch is `pilot/GH-<fix>` with no PR; it works on the borrowed source branch but admission looks up its own branch.

---

## Known pitfalls

- The redrive removes `pilot-needs-human` on purpose (GH-5042 delta log). Do not "fix" it there.
- Place the check after the needs-human backstop and before the spec-guard / `pilot-in-progress` comment, so a refused admission leaves no comment and no label (the duplicate comment #2 on #6016 is the symptom).
- `skipReason` values are host-only; add a `skipReasonOpenPR` const beside the others, and make `githubLiveAdmissionSkipReason` stay pure (it takes no client). The open-PR check is a separate step.

---

## Legs — pilot (daemon)

### LEG A1 — repo: qf-studio/pilot — title: fix(daemon): refuse SDK admission of an issue whose pilot branch has an open PR, so a hold redrive that strips pilot-needs-human cannot start a duplicate run
probe: present qf-studio/pilot ^func githubEventHasNeedsHumanLabel\(
probe: present qf-studio/pilot ^func githubLiveAdmissionSkipReason\(
probe: present qf-studio/pilot ^func \(c \*Controller\) releaseHoldLabels\(
probe: absent  qf-studio/pilot skipReasonOpenPR[[:space:]]+=

## Context

On 2026-11-03 a daemon on the founder box started a second run for issue 6016 while its pull request 6017 was still open, and the run re-implemented work the pull request already held. The issue had been parked by autopilot: the execution row was failed, pilot-in-progress was stripped, and pilot-needs-human was added after the CI-fix size guard declined. A new commit landed on the pull request branch, and Controller.redriveHeldPR in `internal/autopilot/controller.go` re-adopted the pull request and called releaseHoldLabels, which removed pilot-needs-human from the linked issue. A failed row is not terminal, the SDK poller's candidate filter does not look at pull requests, and the GH-5056 backstop in handleGithubIssueEventSDK (`cmd/pilot/handlers.go`) only checks the pilot-needs-human label. The issue therefore passed admission 19 seconds after the label was removed, and a fresh worktree was cut from origin/main. Nothing at admission checks whether the pilot branch for the issue already has an open pull request. The SDK client built for the event (specClient) already exposes FindOpenPRByBranch(ctx, owner, repo, branch) (bool, error).

## Scope

1. In `cmd/pilot/handlers.go` add a host-only skip reason constant skipReasonOpenPR with value open_pr beside skipReasonNeedsHuman.
2. In handleGithubIssueEventSDK, directly after the GH-5056 needs-human backstop block and before the spec guard, the pilot-in-progress label and the task construction, run the open-PR gate: when specClient is not nil and repoOwner and repoName are set, call FindOpenPRByBranch with the branch name pilot/ plus taskID (the same branch name the handler builds a few lines later). When it returns true, log at Info with the task id, the branch and reason open_pr, arm the backoff with repickBackoff.recordClaimLostDrop(backoffKey) when dispatcher is not nil (same as the needs-human branch), and return an IssueResult with Success false, Skipped true and SkipReason skipReasonOpenPR.
3. A lookup error or a nil client admits the issue and logs one Warn line with the task id and the error. Do not change githubLiveAdmissionSkipReason; it stays a pure function of state and labels.
4. Do not change releaseHoldLabels or redriveHeldPR in `internal/autopilot/controller.go`: removing the human-hold label on a redrive is intended.
5. Tests, table-driven, in a new file, plain text: cmd/pilot/gh6016_open_pr_admission_test.go, modelled on `cmd/pilot/gh5056_needs_human_admission_test.go` and the httptest server wiring through githubTaskClientOpts in `cmd/pilot/gh5072_needs_human_repick_backoff_test.go`. Cases: TestHandleGithubIssueEventSDK_OpenPRBlocksAdmission (issue open, labelled pilot only, the pulls endpoint returns an open PR with head ref pilot/GH-N: result Skipped with reason open_pr, no pilot-in-progress label added, no comment posted, dispatcher not called); TestHandleGithubIssueEventSDK_NoOpenPRAdmits (pulls endpoint returns an empty list: admission proceeds); TestHandleGithubIssueEventSDK_OpenPRLookupErrorAdmits (pulls endpoint returns 500: admission proceeds); TestHandleGithubIssueEventSDK_OpenPRArmsRepickBackoff (a second event inside the cooldown is dropped by the allow check before any API call).

## Acceptance

- [ ] `go test -race ./cmd/pilot/ -run TestHandleGithubIssueEventSDK_OpenPR` passes; paste the output into the PR body.
- [ ] `go test -race ./cmd/pilot/ -run TestHandleGithubIssueEventSDK_NeedsHuman` passes; paste the output into the PR body.
- [ ] remove the open-PR gate from handleGithubIssueEventSDK -> TestHandleGithubIssueEventSDK_OpenPRBlocksAdmission fails
- [ ] make the gate return Skipped on a lookup error -> TestHandleGithubIssueEventSDK_OpenPRLookupErrorAdmits fails
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

Changing the autopilot redrive or the label set it releases, the TASK-624 boot heal in the dispatcher, the SDK poller's candidate filter (it lives in the studio-sdk module), and a PR-aware check inside the terminal-completion checker.

## Refs

- Incident 2026-11-03: pilot#6016 / PR#6017 / pilot#6019 · Navigator TASK-632 in this repo
- Related: TASK-624 D2 (boot open-PR heal), TASK-625, GH-5056 (needs-human backstop), GH-5072 (repick backoff), GH-5139 (reopen/relabel re-arm), GH-5042 (hold label release)

---

## Refs

- Operator check after release: on the next hold-redrive of a PR whose source issue is `failed`, the daemon log shows `skipping admission` with reason `open_pr` and `pilot-board` shows no second `GH-<n>` row.
- Incident evidence: issue timeline of pilot#6016 (labeled 11:34:20Z, unlabeled 11:53:50Z, re-picked 11:54:09Z), PR#6017 comment 11:53:51Z "Re-adopted".
- Filed legs - A1 → https://github.com/qf-studio/pilot/issues/6020:

---

**Last Updated**: 2026-11-03
