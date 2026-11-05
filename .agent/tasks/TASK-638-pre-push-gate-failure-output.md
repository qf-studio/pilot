# TASK-638: Pre-push gate prints the failing lines, not the first 20 lines of output

**Status**: ✅ **SHIPPED IN v2.313.1 (auto-cut 11-05 15:44Z)** — [pilot#6093](https://github.com/qf-studio/pilot/issues/6093) → PR#6095 merged (gate failure output shows the failing lines, full log kept under `/tmp/pilot-gate`); post-merge minors #6096 → PR#6097 merged 14:30Z. Post-merge CI on PR#6097 failed on two flakes (ShellCheck action download, `TestListRecordingsWithFilters` 10 ms window) → autopilot fix issue #6098 declined by pre-flight and closed as not planned; the test flake is [pilot#6101](https://github.com/qf-studio/pilot/issues/6101). Archive after PR#6095 is reviewed. Was: 🚀 A1 DISPATCHED 11-05 → pilot#6093 (`pilot` + `no-decompose`). Origin: v2.313.0 hand tag push failed the gate at `[3/8] Test (short)` after 674 s and printed only `ok` package lines; a clean rerun passed. The failing test was never visible.
**Created**: 2026-11-05
**Assignee**: Pilot

---

## Triage

`run_check()` in `scripts/pre-push-gate.sh` (near line 59) captures the command output in a shell variable and on failure prints `head -20` of it. For `go test -short -race ./...` the first 20 lines are passing packages, so the `--- FAIL` / `FAIL` / `DATA RACE` lines are cut off. The output is also lost after the push is rejected. Output-only defect: exit codes and the docs-only fast path are correct.

## Legs

### LEG A1 — repo: qf-studio/pilot — title: fix(gate): pre-push gate failure output shows the failing lines and keeps the full log in /tmp/pilot-gate

## Context

`run_check()` in `scripts/pre-push-gate.sh` (near line 59) runs the command with `output=$(eval "$cmd" 2>&1)` and on failure prints the captured text through `head -20`. With `go test -short -race ./...` the first 20 lines are `ok` lines, so the operator sees no failure. Seen on the v2.313.0 tag push (11-05, 674 s run, failure invisible, rerun green). The existing harness for this gate is `scripts/test-prepush-fastpath.sh` (Makefile target `test-prepush-fastpath`).

probe: present qf-studio/pilot head[[:space:]]+-20
probe: absent qf-studio/pilot pilot-gate/[$]\{name

## Scope

- `scripts/pre-push-gate.sh`, `run_check()`: output-only change. Keep the command execution, the exit-code capture, the `✓` fast path and the return values exactly as they are.
- On failure, write the full captured output to a log file at /tmp/pilot-gate/<check-name>-<timestamp>.log (plain text, a temp path, not a repo file) (create the directory with `mkdir -p`; slugify the check name so `go test -short` becomes `go-test-short`). Print the log path on failure, on its own line, after the summary.
- On failure, print a summary instead of `head -20`:
  - every line matching the extended regex `^(--- FAIL|FAIL|panic:|.*DATA RACE|ok .*\(timeout\))`;
  - plus up to 40 lines of context after the first `--- FAIL` line;
  - if no line matches, print the last 40 lines of the output (`tail -40`), never the head.
- Put the summary logic in a function that a test can source without running the gate. New file, plain text: scripts/pre-push-gate-summary.sh, sourced by the gate. Do not change `run_check_warn`.
- Add a shell test, new file, plain text: scripts/test-prepush-gate-output.sh, in the style of `scripts/test-prepush-fastpath.sh`, and a Makefile target `test-prepush-gate-output` next to `test-prepush-fastpath` (add it to `.PHONY`). Cases: (1) 30 `ok` lines then `--- FAIL: TestX` then `FAIL` → both lines printed, no `ok` filler beyond the context window; (2) a `DATA RACE` block → the line is printed; (3) a `panic:` line → printed; (4) 60 lines with no match → exactly the last 40 are printed, line 1 is not; (5) the log file exists under /tmp/pilot-gate/, holds the full output, and its path is printed; (6) a passing command prints no log path and returns 0.

## Acceptance

- [ ] `make test-prepush-gate-output` passes; paste the output into the PR body.
- [ ] `make test-prepush-fastpath` passes; paste the output into the PR body.
- [ ] revert the summary to `head -20` -> the first test case fails
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

Changing which checks the gate runs, the timeouts, retries, the docs-only fast path, `run_check_warn`, and the Go test suite itself (flaky test hunting is separate).

## Refs

- Origin: v2.313.0 hand tag push, 11-05 · SOP `.agent/sops/quality/pre-push-gate.md` · Navigator TASK-638 in this repo

## Refs

- A1 → (pending)

## Refs

- A1 → https://github.com/qf-studio/pilot/issues/6093
