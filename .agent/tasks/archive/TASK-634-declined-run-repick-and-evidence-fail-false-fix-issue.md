# TASK-634: A declined run is re-picked, its reason is lost, and a FAIL in pasted evidence files a fix issue that green CI already refutes

**Status**: 📦 **ARCHIVED 11-08 — A1/B1/C1 + minors PR#6047 released in v2.312.0.** ✅ **ALL THREE LEGS MERGED 2026-11-03** — A1 → PR#6039 (`f3a2c0a6d` 17:30Z, post-merge APPROVE-WITH-NOTES; its minors → #6042 → PR#6047 `f81111fc7`) · B1 → PR#6043 (`8ab175c6b` 17:55Z) · C1 → PR#6044 (`9bef167f9` 18:21Z). All unreleased (next tag after v2.311.0). A false ci_post_merge issue #6041 (go-proxy stream error on the docs-sync commit, main green twice after) closed by hand. Held: D (one re-run on FAIL), E (parallel -race runs). Was: DISPATCHED 16:30Z — A1 [pilot#6033](https://github.com/qf-studio/pilot/issues/6033) · B1 [pilot#6034](https://github.com/qf-studio/pilot/issues/6034) · C1 [pilot#6035](https://github.com/qf-studio/pilot/issues/6035) (`pilot` + `no-decompose`, one PR each). Incident: GH-6031 (closed not-planned 16:22Z).
**Created**: 2026-11-03
**Assignee**: Pilot

---

## Context

**Incident (box log + recording TG-1793717982550, 2026-11-03):** GH-6028 (a `review-minor` fix issue) ran 14:59Z–15:30Z and merged as PR#6030. The runner's own evidence command `go test -race ./internal/executor/` ended non-zero at 585 s while Claude ran a second full `-race` pass of the same package in the background. The paste was trimmed to 20 head + 80 tail lines; the `--- FAIL:` line fell in the omitted middle, so no artifact names the failing test. The box gates (`-short`) and CI (`-timeout 20m`) were green. The post-merge reviewer applied the TASK-614 contract literally (a failing test in a changed package is blocking) and the feedback loop filed GH-6031 as `autopilot-fix`.

Pilot's run 1 on GH-6031 reproduced on main (`ok 552s`), checked both mutation pins, and returned `DECLINED:` with the reason "Full go test -race ./internal/executor/ passes on main (ok 552s) and both mutation pins fail as designed, so the PR #6030 defects are an unreproduced run and missing PR-body evidence, not code defects." The reason went to the log and `reportProgress` only. The dispatcher counted the declined row as no-progress, re-picked it 54 s later, run 2 ended `no_op` mid-test, and the cap of 2 stalled the task with `pilot-blocked` and the generic "needs a rescope" comment. The founder had to read the daemon log to learn why. Laptop confirmation on main `12c753c6a`: `ok github.com/qf-studio/pilot/internal/executor 511.456s`.

**Four gaps, verified on main (navigator-research 2026-11-03):**

| # | Where | What |
|---|---|---|
| 1 | `internal/executor/dispatcher.go` near line 1787 (`isNoProgressOutcome`), cap at line 1601 | A `declined` row is no-progress like `no_op`; the dispatcher re-picks it. A decline is a judgement on the issue body; the same body yields the same judgement. |
| 2 | `internal/executor/dispatcher.go` near line 2810 (`surfaceStalledIssue`); `internal/executor/runner.go` near line 2314 (`finishDeclined`) | The decline reason is never written to the execution row and never posted to the issue. Only `escalateRefusal` (near line 2612) posts a reason, and only for model refusals. |
| 3 | `internal/executor/post_merge_reviewer.go` near line 393 (severity contract) and the downgrade guards near lines 560–590 | A FAIL line in pasted evidence for a changed package is blocking even when CI at merge was green. The reviewer cannot re-run and sees CI only as the "CI status at merge time" line. |
| 4 | `internal/executor/acceptance_evidence.go` near lines 319–345 (`trimOutputForEvidence`) | The head+tail trim drops every `--- FAIL:` / `panic:` / `DATA RACE` line in the middle; nothing logs the failing test names. The heading flips to "FAILS" (GH-5568) but the paste is not diagnosable. |

**Not in scope here (held, see § Held):** a one-time re-run of a failed evidence command; the runner and Claude both running the full `-race` package in parallel on the box.

---

## Known Pitfalls & Patterns

- **PITFALL** (95%, `terminal-status-without-rearm-probe`): a new terminal status without a re-arm probe kills operator recovery → Leg A adds no new status; it reuses `stalled` + `pilot-blocked`, which the existing relabel re-arm (GH-5139) already covers.
- **PITFALL** (95%, mem-142): a path that skips the normal failure branch leaves the card wrong → Leg A goes through `escalateStalledTask` exactly as the cap path does.
- **PITFALL** (90%, `post-merge-fix-blames-merged-pr-for-preexisting-flake`): a post-merge fix issue blamed a merged PR for a pre-existing race → Leg B is the deterministic guard for the evidence-only variant.
- **PATTERN** (GH-5568): the evidence heading must never claim "passes" when a FAIL line is present → Leg C keeps that and adds the failure lines themselves.

---

## Legs

### LEG A1 — repo: qf-studio/pilot — title: fix(dispatcher): a declined run stalls the task on the first decline and the stall comment quotes the decline reason

## Context

A Pilot run ends `declined` when Claude's final text carries `DECLINED:<reason>` (parsed near line 2291 of `internal/executor/runner.go`, finished by finishDeclined near line 2314, which only logs and calls reportProgress). The dispatcher treats the `declined` row as a no-progress outcome in isNoProgressOutcome near line 1787 of `internal/executor/dispatcher.go`, so the next poll re-picks it for a retry; only after dispatcherNoProgressRepickCap (2) consecutive no-progress rows does stallTaskAfterNoProgressCap stall it and surfaceStalledIssue post "N consecutive runs ended without a change … needs a rescope". The decline reason is not on the execution row and not in that comment. Observed 2026-11-03 on GH-6031: run 1 declined with a reason that fully answered the issue, run 2 re-ran the same body and ended no_op, the comment said nothing about why.

A decline is a judgement on the issue body. Re-running the same body cannot change it. The refusal path (escalateRefusal near line 2612) already stops on the first occurrence and posts the reason; declines need the same shape without a new status.

probe: present qf-studio/pilot func[[:space:]]isNoProgressOutcome\(
probe: present qf-studio/pilot dispatcherNoProgressRepickCap[[:space:]]*=[[:space:]]*2
probe: present qf-studio/pilot func[[:space:]]\(d[[:space:]]\*Dispatcher\)[[:space:]]surfaceStalledIssue\(
probe: present qf-studio/pilot func[[:space:]]\(r[[:space:]]\*Runner\)[[:space:]]finishDeclined\(
probe: absent qf-studio/pilot func[[:space:]]declinedStallReason\(

## Scope

- Persist the reason: when the worker writes the terminal row for a declined result, the row's error text is `declined: <DeclinedReason>` (the existing "no new commit produced" text moves after it or is dropped for this status). Refusal declines keep their current error text so IsRefusalFailure still matches.
- Stall on the first decline: in the repick path, before the generic no-progress cap check, a newest terminal row with status `declined` that is not a refusal stalls the task at once through escalateStalledTask with reason key `declined_stalled`, metadata `decline_reason`, and no further generation. Add a helper `declinedStallReason(exec) (string, bool)` in `internal/executor/dispatcher.go` that returns the quoted reason for such a row.
- The stall comment: stalledIssueComment for this path reads "Pilot declined this issue after one run: <reason verbatim>. Rescope the issue body or close it; re-add the `pilot` label to re-arm." The `pilot-blocked` label and the pilot-failed / pilot-in-progress removal stay as today.
- isNoProgressOutcome keeps returning true for `declined` so the generic cap still counts it when the new check does not fire (for example a decline without a parsed reason).
- No new execution status, no new label, no config key.

Tests to add in `internal/executor/dispatcher_test.go` or a new file, plain text: internal/executor/declined_stall_test.go — named exactly: TestDispatcher_DeclinedRunStallsWithoutRepick (one declined row with a reason → stalled + no generation 2 claimed), TestDispatcher_StallCommentCarriesDeclineReason (the posted comment contains the reason verbatim), TestWorker_DeclinedRowPersistsReason (the terminal row's error starts with `declined: `), TestDispatcher_RefusalDeclineStillEscalatesAsRefusal (a refusal-classed decline takes escalateRefusal, not the new path).

## Acceptance

- [ ] `go test -race ./internal/executor/ -run TestDispatcher_Declined` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/executor/ -run TestWorker_DeclinedRowPersistsReason` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/executor/ -run TestDispatcher_StallCommentCarriesDeclineReason` passes; paste the output into the PR body.
- [ ] remove the new declined check so the generic cap handles it -> TestDispatcher_DeclinedRunStallsWithoutRepick fails
- [ ] drop the reason from the stall comment -> TestDispatcher_StallCommentCarriesDeclineReason fails
- [ ] write the old "no new commit produced" error for a declined row -> TestWorker_DeclinedRowPersistsReason fails
- [ ] Do not re-run make build, make test or make lint as evidence: the daemon's quality gates already run them on this repo.

## Out of scope

Changing what counts as a decline in the runner prompt, the refusal path, the no-progress cap value, the re-arm rules (GH-5139), and any Telegram or Slack message text.

## Refs

- GH-6031 (the incident issue, closed not-planned) · GH-5139 (re-arm) · Navigator TASK-634 in this repo

### LEG B1 — repo: qf-studio/pilot — title: fix(review): a FAIL in pasted PR-body evidence is minor when CI at merge was green — deterministic downgrade guard plus the contract line

## Context

The post-merge reviewer's severity contract (prompt text near line 393 of `internal/executor/post_merge_reviewer.go`) says a pasted-output bullet is blocking when "the pasted output shows a failing test or build for code the diff changed". The reviewer is read-only (GH-5916), cannot re-run the command, and sees CI only as the context line "CI status at merge time: <status>" (near line 482). On PR#6030 the pasted `go test -race ./internal/executor/` ended `FAIL … 585.572s` from a loaded run, CI at merge was success, main passes (`ok 552s` on the box, `ok 511.456s` on a laptop), and the reviewer still returned REQUEST-CHANGES with two blocking defects whose only location was the PR body. The feedback loop filed GH-6031, which Pilot then declined as not a code defect.

The downgrade guards near lines 560–590 of the same file already demote a REQUEST-CHANGES verdict in defined cases (citation rules, zero files read, no blocking defect). They have no case for an evidence-only defect that green CI refutes.

probe: present qf-studio/pilot unless the pasted output shows a failing test or build
probe: present qf-studio/pilot CI status at merge time
probe: absent qf-studio/pilot func[[:space:]]downgradeEvidenceFailWithGreenCI\(

## Scope

- Contract line: extend the severity sentence in the prompt: a failing test or build in pasted output is [minor] when the CI status at merge time is success, naming the package and the CI status; it is [blocking] only when CI at merge was not success, or when the pasted output names a test the diff added or changed and CI did not run that package.
- Deterministic guard in `internal/executor/post_merge_reviewer.go`: add `downgradeEvidenceFailWithGreenCI(defects, ciStatus)` next to the existing downgrade guards. A [blocking] defect whose location is the PR body (no file path) and whose text matches the evidence FAIL pattern (`FAIL`, `--- FAIL:`, "failing test", "FAIL lines in output") is rewritten to [minor] with the suffix "(CI at merge: success)" when ciStatus is success. The guard runs before BlockingDefects() is computed, so a verdict that had only such defects becomes APPROVE-WITH-NOTES and files no `autopilot-fix` issue (the minors sink still applies under file_minors).
- Log one INFO line per downgrade with pr, defect index and ci status.
- Document the rule in the reviewer section of `docs/content/` only if a reviewer page exists there; otherwise no docs change.

Tests to add in `internal/executor/post_merge_reviewer_test.go`, named exactly: TestPostMergeReviewer_EvidenceFailWithGreenCIDowngradedToMinor, TestPostMergeReviewer_EvidenceFailWithFailedCIStaysBlocking, TestPostMergeReviewer_FileLocatedDefectNotDowngraded (a FAIL-worded defect at a real file path stays blocking), TestPostMergeReviewer_PromptCarriesGreenCIRule (the built prompt contains the new contract sentence).

## Acceptance

- [ ] `go test -race ./internal/executor/ -run TestPostMergeReviewer_EvidenceFail` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/executor/ -run TestPostMergeReviewer_FileLocatedDefectNotDowngraded` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/executor/ -run TestPostMergeReviewer_PromptCarriesGreenCIRule` passes; paste the output into the PR body.
- [ ] skip the guard when ciStatus is success -> TestPostMergeReviewer_EvidenceFailWithGreenCIDowngradedToMinor fails
- [ ] apply the guard regardless of ciStatus -> TestPostMergeReviewer_EvidenceFailWithFailedCIStaysBlocking fails
- [ ] apply the guard to file-located defects too -> TestPostMergeReviewer_FileLocatedDefectNotDowngraded fails
- [ ] Do not re-run make build, make test or make lint as evidence: the daemon's quality gates already run them on this repo.

## Out of scope

The pre-PR reviewer (`internal/executor/pre_pr_review.go`), the two-pass mechanics, the minors sink and its cap, any change to how CI status is fetched, and the evidence trim (Leg C1).

## Refs

- GH-6031 · PR#6030 · TASK-614 severity contract · GH-5916 read-only checkout · Navigator TASK-634 in this repo

### LEG C1 — repo: qf-studio/pilot — title: fix(executor): the evidence trim keeps the failure lines from the omitted middle and the runner logs the failing test names

## Context

trimOutputForEvidence near line 329 of `internal/executor/acceptance_evidence.go` keeps evidenceHeadLines (20) and evidenceTailLines (80) of a long command output and replaces the middle with "... N lines omitted ...". A Go test run with log-heavy tests prints hundreds of lines; the `--- FAIL: TestX` line, any `panic:` and any `WARNING: DATA RACE` block can sit in the omitted middle. The heading already flips to "FAILS (FAIL lines in output)" (GH-5568, evidenceHeading in the same file), so the reader sees a failure with no test name. PR#6030 (2026-11-03) shipped exactly that paste: head = test log noise, tail = test log noise plus `FAIL github.com/qf-studio/pilot/internal/executor 585.572s`. The runner (`internal/executor/acceptance_evidence_run.go`, runPasteOutputItemReusing near line 274) pastes a non-zero exit as a legitimate result and logs nothing about which tests failed, so the daemon log is blind too.

probe: present qf-studio/pilot evidenceHeadLines[[:space:]]*=[[:space:]]*20
probe: present qf-studio/pilot func[[:space:]]trimOutputForEvidence\(
probe: present qf-studio/pilot func[[:space:]]evidenceHeading\(
probe: absent qf-studio/pilot evidenceFailureLineBudget

## Scope

- In `internal/executor/acceptance_evidence.go`: when the output exceeds the cap, collect from the omitted middle every line that matches a failure pattern (`^--- FAIL:`, `^panic:`, `^WARNING: DATA RACE`, `^FAIL\b`, `^\s+\S+_test\.go:\d+:` immediately after a `--- FAIL:` line) up to a new constant evidenceFailureLineBudget (30). Emit them between the head and the tail as "... N lines omitted; M failure lines kept: ..." followed by the kept lines, then the tail. When no failure line is in the middle, the output is byte-identical to today.
- The total still respects a hard cap of maxEvidenceOutputLines + evidenceFailureLineBudget lines.
- In `internal/executor/acceptance_evidence_run.go`: after a paste-output command exits non-zero, log one WARN line "acceptance evidence command failed" with task_id, the command, the exit code, the duration and the first ten `--- FAIL:` test names, so the daemon log names the tests even when the PR body is trimmed.
- evidenceHeading is unchanged.

Tests to add, named exactly. Trim tests in a new file, plain text: internal/executor/acceptance_evidence_trim_test.go — TestTrimOutputForEvidence_KeepsFailLinesFromOmittedMiddle, TestTrimOutputForEvidence_NoFailureLinesUnchanged (long output with no failure lines → identical to the current head/marker/tail form), TestTrimOutputForEvidence_FailureLineBudgetCapped. Runner test in `internal/executor/acceptance_evidence_run_test.go` — TestEvidenceRun_LogsFailingTestNames (a fake command that prints two `--- FAIL:` lines and exits 1 → the WARN log carries both names). The existing TestFinalizeEvidenceOutput tests in `internal/executor/acceptance_evidence_gh5568_test.go` must keep passing unchanged.

## Acceptance

- [ ] `go test -race ./internal/executor/ -run TestTrimOutputForEvidence` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/executor/ -run TestEvidenceRun_LogsFailingTestNames` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/executor/ -run TestFinalizeEvidenceOutput` passes; paste the output into the PR body.
- [ ] drop the middle scan -> TestTrimOutputForEvidence_KeepsFailLinesFromOmittedMiddle fails
- [ ] remove the budget cap -> TestTrimOutputForEvidence_FailureLineBudgetCapped fails
- [ ] remove the WARN log -> TestEvidenceRun_LogsFailingTestNames fails
- [ ] Do not re-run make build, make test or make lint as evidence: the daemon's quality gates already run them on this repo.

## Out of scope

The head and tail budgets, the per-command timeout (`defaultAcceptanceEvidenceCommandTimeout` in `internal/executor/backend.go`), any re-run of a failed command, the allowlist, and the heading rule (GH-5568).

## Refs

- PR#6030 (the trimmed paste) · GH-5568 (honest heading) · GH-5437 (timeout reason) · Navigator TASK-634 in this repo

---

## Held

- **D — one re-run on FAIL.** When a paste-output command for a package the diff changed exits non-zero and the remaining execution budget allows, run it once more and paste both results; a pass on the re-run marks the item "flaky: passed on re-run" instead of "FAILS". Hold until C1 lands and the log shows how often the first run fails.
- **E — parallel `-race` runs on the box.** Claude runs the full package test in the background while the runner runs the same command for evidence (recording TG-1793717982550, 30 min, 14 min in Testing). Two race runs on a t3.xlarge is the likely cause of the 585 s FAIL. Candidate: the prompt tells Claude not to run the paste-output commands itself, or the evidence runner reuses Claude's result when the command text matches. Needs a design read first.

## Refs

- GH-6031 (closed not-planned 2026-11-03 16:22Z, comment carries the decline reason and the laptop `ok` line)
- PR#6030 · GH-6028 · TASK-633 F3 (#6026 / PR#6027, the tests involved)
- TASK-614 (severity contract) · TASK-620 (reviewer program) · GH-5568 (evidence heading) · GH-5139 (re-arm)

## Refs

- A1 → https://github.com/qf-studio/pilot/issues/6033
- B1 → https://github.com/qf-studio/pilot/issues/6034
- C1 → https://github.com/qf-studio/pilot/issues/6035
