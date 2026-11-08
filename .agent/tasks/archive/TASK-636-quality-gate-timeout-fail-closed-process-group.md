# TASK-636: Quality gate timeouts — fail closed on the last attempt, kill the whole process group

**Status**: 📦 **ARCHIVED 11-08 — G1/G2 + minors released in v2.314.0.** ✅ **SHIPPED IN v2.314.0 (11-06) — archive pending.** BOTH MERGED 11-05 — G1 [PR#6085](https://github.com/qf-studio/pilot/pull/6085) 09:38Z (APPROVE-WITH-NOTES → minors #6086 → PR#6089 in CI) · G2 [PR#6088](https://github.com/qf-studio/pilot/pull/6088) 10:29Z (APPROVE-WITH-NOTES; `internal/procgroup` shared package, executor helpers delegate). Unreleased → next tag. Community PRs [#6077](https://github.com/qf-studio/pilot/pull/6077) and [#6078](https://github.com/qf-studio/pilot/pull/6078) by RyoKusnadi (opened 11-04 23:07Z, before the dispatch) implement the same two fixes; #6078 additionally treats `exec.ErrWaitDelay` after a clean exit as success (a background server holding the pipes), where G2 counts it as a timeout — kept G2's stricter rule. Both closed 11-05 with credit comments that link #6085 / #6088 and name the ErrWaitDelay difference; they are invited to open an issue with a gate command if a clean-exit background process is a real case.** Was: 🚀 G1 #6080 + G2 #6081 dispatched 11-05 (`pilot` + `no-decompose`; G2 depends on G1). Replies with the leg links posted on #6075 and #6076. Triage: two reports from scrappydog ([pilot#6075](https://github.com/qf-studio/pilot/issues/6075) false pass on a last-attempt timeout · [pilot#6076](https://github.com/qf-studio/pilot/issues/6076) timeout does not bound wall time), both verified on main, both with tested fixes and repro tests in the report. Box exposure: `test` gate `make test-changed` timeout 10m, max_retries 1 — a timeout on the retry would pass the gate; daemon.log has 0 `quality gate timed out` lines so far. Legs G1 (fail closed) and G2 (process group, shared helper) below; G2 depends on G1 (same file).
**Created**: 2026-11-05
**Assignee**: Pilot

---

## Triage

- **Author**: scrappydog (Eric Bowen), the #5955 requester; every prior report shipped. Both bodies cite exact lines on v2.308.0 and say "unchanged on main at v2.312.0" — confirmed: `runGate` error branch `continue` (`internal/quality/runner.go` near line 258) leaves `StatusRunning` on the last attempt; `RunAll` treats only `StatusFailed` as a failure (near line 166); `executeCommand` (near line 500) sets no process group, no `Cancel`, no `WaitDelay`, while `configureProcessGroup` / `killProcessGroup` exist in `internal/executor/process_group_unix.go` and `process_group_other.go` (GH-4503).
- **Class**: #6075 is TASK-460 false-success (green gate on a timeout). #6076 is a hang (worker held until orphans die; the author saw 50 min and 20 h).
- **Decision**: dispatch both as Navigator legs that reference the reports; reply on each issue with the leg link (the #5955 pattern). The author's suggested diffs are the implementation; the legs add the named tests and the Windows fallback.

## Dispatched legs

### LEG G1 — repo: qf-studio/pilot — title: fix(quality): a gate that times out or fails to run on its last attempt is a failed gate, and RunAll fails on any required gate that did not pass

## Context

Report [pilot#6075](https://github.com/qf-studio/pilot/issues/6075), verified on main. In `internal/quality/runner.go`, runGate (near line 199) retries up to maxAttempts; on an execution error from executeCommand (ErrGateTimeout, or any error that is not an *exec.ExitError) the branch near line 258 sets result.Error and `continue`s, so on the last attempt the loop ends with result.Status still StatusRunning. Only the non-zero-exit path (near line 311) sets StatusFailed on the last attempt. RunAll (near line 166) counts a required gate as failed only when `result.Status == StatusFailed`, so a StatusRunning result passes: `all_passed=true`, "Quality gates passed", self-review and PR creation proceed. With the default max_retries 0 every timeout passes. Reproduced by the reporter on v2.308.0 with a unit test (`runGate status = "running", want "failed"`). The box's own `test` gate (10m, max_retries 1) is exposed on its retry.

Depends on: none

probe: present qf-studio/pilot func[[:space:]]\(r[[:space:]]\*Runner\)[[:space:]]runGate\(
probe: present qf-studio/pilot result\.Status[[:space:]]==[[:space:]]StatusFailed[[:space:]]&&[[:space:]]gate\.Required
probe: present qf-studio/pilot ErrGateTimeout
probe: absent qf-studio/pilot func[[:space:]]TestRunGate_TimeoutOnLastAttemptFails\(

## Scope

- `internal/quality/runner.go`, runGate: after the retry loop, any result that did not reach StatusPassed becomes StatusFailed (keep result.Error; when empty set "gate did not pass"), with the same reportProgress call the exit-code path makes. A timeout gets its own wrapped error in result.Err: `fmt.Errorf("%w: after %s", ErrGateTimeout, gate.DefaultTimeout())`, and result.Error is its text, so the feedback to the model and the alerts can tell a timeout from a test failure.
- `internal/quality/runner.go`, RunAll: a required gate fails the run when `result.Status != StatusPassed` (not only StatusFailed); the "Required quality gate failed" warning carries the status.
- No change to the retry count, the timeout values, the parallel path or the feedback text beyond the timeout error.

Tests to add, named exactly, in `internal/quality/runner_test.go`: TestRunGate_TimeoutOnLastAttemptFails (a required gate `sleep 30` with Timeout 500ms and no retries → Status failed, Err wraps ErrGateTimeout, Error names the timeout), TestRunGate_ExecErrorOnLastAttemptFails (executeCommand returns a non-ExitError → Status failed), TestRunAll_RequiredGateNotPassedFailsRun (a required gate left non-passed → AllPassed false and the warning logged; an optional gate left non-passed → AllPassed true).

## Acceptance

- [ ] `go test -race ./internal/quality/ -run 'TestRunGate_TimeoutOnLastAttemptFails|TestRunGate_ExecErrorOnLastAttemptFails|TestRunAll_RequiredGateNotPassedFailsRun'` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/quality/` passes; paste the output into the PR body.
- [ ] keep the `continue` without the post-loop fail-closed -> TestRunGate_TimeoutOnLastAttemptFails fails
- [ ] keep `== StatusFailed` in RunAll -> TestRunAll_RequiredGateNotPassedFailsRun fails
- [ ] Do not re-run make build, make test or make lint as evidence: the daemon's quality gates already run them on this repo.

## Out of scope

The process group and WaitDelay (leg G2, #6076); retry policy; the executor's own subprocess handling.

## Refs

- #6075 (report, with the tested diff) · TASK-460 (false-success class) · TASK-616 (box gate timing) · Navigator TASK-636 in this repo

### LEG G2 — repo: qf-studio/pilot — title: fix(quality): a gate timeout kills the gate's whole process group and bounds the wait on inherited pipes — shared process-group helper for the executor and the quality runner

## Context

Report [pilot#6076](https://github.com/qf-studio/pilot/issues/6076), verified on main. executeCommand in `internal/quality/runner.go` (near line 500) runs `exec.CommandContext(cmdCtx, "sh", "-c", command)` with bytes.Buffer outputs and sets no SysProcAttr, no cmd.Cancel and no cmd.WaitDelay. On timeout Go kills only `sh`; descendants (npm test → node workers, or `sleep 600 & sleep 600`) are reparented, keep the stdout/stderr pipe open, and cmd.Run does not return until they exit, so the timeout bounds nothing and the task's worker is held (the reporter saw 50 min and 20 h). The executor solved this for its agent subprocesses in GH-4503: configureProcessGroup (Setpgid + WaitDelay 5 s) and killProcessGroup (negative-pid group signal, single-pid fallback) in `internal/executor/process_group_unix.go`, with a no-op fallback in `internal/executor/process_group_other.go` and a test in `internal/executor/process_group_unix_test.go`. The quality runner never got them. `.goreleaser.yaml` builds Windows too, so the fallback shape must stay.

Depends on: #{{G1}}

probe: present qf-studio/pilot func[[:space:]]\(r[[:space:]]\*Runner\)[[:space:]]executeCommand\(
probe: present qf-studio/pilot func[[:space:]]configureProcessGroup\(
probe: present qf-studio/pilot func[[:space:]]killProcessGroup\(
probe: absent qf-studio/pilot package[[:space:]]procgroup

## Scope

- New package, plain text: internal/procgroup, with Configure(cmd *exec.Cmd) (Setpgid + WaitDelay, the delay a parameter with the executor's 5 s as default) and Kill(cmd *exec.Cmd, sig syscall.Signal) error, moved from the two executor files with their unix and other build tags and their test; the executor's configureProcessGroup / killProcessGroup become thin calls into the package (or the call sites use the package directly) with no behaviour change.
- `internal/quality/runner.go`, executeCommand: procgroup.Configure(cmd) before Start with a WaitDelay of 10 s; cmd.Cancel kills the group with SIGKILL through procgroup.Kill; `errors.Is(err, exec.ErrWaitDelay)` counts as ErrGateTimeout like context.DeadlineExceeded does. Output captured so far is still returned on timeout.
- No change to the gate config, the retry loop or RunAll (leg G1 owns the status).

Tests to add, named exactly: TestExecuteCommand_TimeoutBoundsWallTime (`internal/quality/runner_test.go`: a gate with Timeout 500ms running `sleep 8 & sleep 8` returns ErrGateTimeout within 3 s, unix only), TestExecuteCommand_WaitDelayCountsAsTimeout (a child that leaves the group with `setsid` and sleeps holds the pipe → ErrGateTimeout after the WaitDelay, unix only), TestProcgroup_KillReachesGrandchildren (new file, plain text: internal/procgroup/procgroup_unix_test.go — moved from the executor test with the package rename).

## Acceptance

- [ ] `go test -race ./internal/quality/ -run 'TestExecuteCommand_TimeoutBoundsWallTime|TestExecuteCommand_WaitDelayCountsAsTimeout'` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/procgroup/` passes; paste the output into the PR body.
- [ ] `go test -race ./internal/executor/ -run 'TestProcessGroup|TestKillProcessGroup|TestConfigureProcessGroup'` passes; paste the output into the PR body.
- [ ] `GOOS=windows go build ./...` passes; paste the output into the PR body.
- [ ] drop cmd.Cancel (kill only sh) -> TestExecuteCommand_TimeoutBoundsWallTime fails
- [ ] drop WaitDelay -> TestExecuteCommand_WaitDelayCountsAsTimeout fails
- [ ] Do not re-run make build, make test or make lint as evidence: the daemon's quality gates already run them on this repo.

## Out of scope

The last-attempt status (leg G1); the executor's agent subprocess lifecycle beyond the helper move; the self-review session's own test run (the reporter's 20 h orphan tree is the executor path, already on process groups).

## Refs

- #6076 (report, with the tested inline diff) · GH-4503 / GH-4357 (executor process groups) · Navigator TASK-636 in this repo

## Refs

- G1 → (pending)
- G2 → (pending)

## Refs

- G1 → https://github.com/qf-studio/pilot/issues/6080
- G2 → https://github.com/qf-studio/pilot/issues/6081
