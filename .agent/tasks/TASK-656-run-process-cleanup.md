# TASK-656: Run process clean-up — the daemon stops every process that a run started; a gate that its time limit killed says so

**Status**: 📋 Planned 2026-11-19. Founder go for the task: 11-19 ("yes"). No leg is dispatched. One decision round is open (see "Waits for you").
**Created**: 2026-11-19
**Assignee**: Pilot

---

## Context

**Problem**:
When a run ends, the daemon stops the agent process. It does not stop everything that the run started. An agent session starts background shell tasks (wait loops) and dev servers. These processes move into a session of their own, so the kill of the process group does not reach them. They stay on the host with parent 1.

Incident 2026-11-19 (mem-326): the daemon host held 133 such process trees (271 processes), up to 54 days old. The load average was 17 to 21 on 4 CPUs. The test gate needed more than its limit of 10 minutes and was killed. Each ticket failed for 16 hours. The failure text was `make test-changed exited with code -1`, with no word about a time limit, and the dispatcher parked one ticket for "consecutive identical failures". After a stop of the trees by hand the same gate passed in 4 minutes. Five hours later one new orphan tree was on the host.

**Facts from the host (read-only, 11-19)**:
- An orphan has a session and a process group of its own: `pgid` = `sid` = its pid. `setsid` is confirmed.
- Its working directory is the worktree of the run, also after the removal: `/tmp/pilot-worktree-GH-N-NANOS (deleted)`.
- Its environment holds `PILOT_EXECUTOR`, `PILOT_TASK_ISSUE`, `PILOT_TASK_BRANCH`, `CLAUDE_PID` and `CLAUDE_CODE_SESSION_ID`. No value is unique for one run of Pilot.
- The daemon runs in a tmux session scope. It has no cgroup of its own that it can manage.

**Facts from the code (research 11-19)**:
- The backend process gets a process group and a group kill on cancel (`internal/executor/backend_claudecode.go`, `backend_qwencode.go`, `parallel.go`, `acceptance_evidence_run.go`). After a normal exit (`cmd.Wait`) no group kill runs.
- agentCommand (`internal/executor/agent_command.go`) gives its seven callers no process group and no group kill: a cancel stops one pid.
- CancelAll (`internal/executor/runner.go`) sends SIGTERM and starts a timer of 10 seconds for SIGKILL. The daemon exits before the timer.
- CleanupOrphanedWorktrees (`internal/executor/worktree.go`) removes worktrees at the daemon start and stops no process. No code looks for processes of dead runs.
- A gate that its time limit killed returns exit code -1 with ErrGateTimeout (`internal/quality/runner.go`). qualityGateFailureSummary (`internal/executor/quality.go`) prints only "exited with code -1".

**Goal**:
At the end of a run the daemon stops each process that the run started. At its start and on a timer the daemon stops processes of runs that are no longer alive. A gate that its time limit killed is reported as a time limit and is not counted as a code failure.

---

## Known Pitfalls & Patterns

- **PITFALL** (mem-326): exit code -1 of a gate is a kill by the time limit; the load of orphan processes was the cause. This task is the fix.
- **PITFALL** (ci-infra-failure-misclassified-as-code): an infrastructure failure that is read as a code failure burns the retry budget and parks a correct ticket. The same class: leg R3 gives a gate time limit its own class.
- **LEARNING** (running-execution-cannot-be-cancelled-kill-the-claude-process-group-not-the-gate): the kill must reach the process group, not one pid. Legs R1 and R2 apply it to each spawn site; legs R4 to R7 cover what leaves the group.
- **SOP** `.agent/sops/executor-heartbeat-kills-live-local-tool.md`: work that the agent puts in the background with `nohup` or `setsid` leaves the tracked group. Known since then; no code followed.

---

## Acceptance Criteria

- [ ] After a run ends (success, failure, time limit, cancel), no process with the run marker or with a working directory below the worktree of the run is alive on a Linux host.
- [ ] After a daemon start, no process of an earlier daemon run with `PILOT_EXECUTOR` in its environment and a working directory below a pilot worktree path is alive.
- [ ] The sweep never stops the daemon, a process of a live run, or a process of another user.
- [ ] A gate that its time limit killed gives the text "timed out after N" in the issue comment and in the ledger, and two such failures in sequence do not park the ticket as "consecutive identical failures".
- [ ] A counter shows the number of processes that the sweep stopped. A config switch turns the sweep off.
- [ ] On macOS and other systems the sweep is a no-op and each test passes.

---

## Route

### Phase 1: Stop what the process group holds
- [ ] 1. After each exit of a backend process, the daemon kills its process group.
- [ ] 2. Helper sessions of the agent (planner, judge, classifiers, summary) get a process group and a group kill. (needs the firewall probe ticket #6530 of TASK-651: same file)

### Phase 2: Report a gate time limit as a time limit
- [ ] 3. The failure text says "timed out after N", and a time limit is not an identical code failure.

### Phase 3: Stop what left the process group
- [ ] 4. A sweep function finds the processes of a run by a marker or by the worktree path, and stops them (Linux).
- [ ] 5. Each run gets a marker in the environment of its agent process. (needs 1, 2)
- [ ] 6. The daemon runs the sweep at the end of each run; a config switch turns it off. (needs 4, 5, and the push records ticket #6534 of TASK-651: same file)
- [ ] 7. The daemon runs the sweep at its start and on a timer, and counts the stopped processes. (needs 6)

### Phase 4: Shutdown
- [ ] 8. On shutdown the daemon waits for the kill of the agent processes before it exits. (needs 6)

### Side tracks
- [ ] The sweep for processes of the agent user when `run_as_user` is set (a kill through sudo). A later leg, after TASK-651 turns the option on.

### Waits for you
- [ ] The decision round below (D1 to D5).

---

## Implementation

### Founder decisions (one round; each has a recommendation)

| # | Question | Recommendation |
|---|---|---|
| D1 | At the daemon start, also stop old orphans that have no run marker? Rule: `PILOT_EXECUTOR` in the environment AND a working directory below a pilot worktree path. | Yes. These are the 133 trees of the incident. |
| D2 | A sweep on a timer, next to the sweep at the end of a run and at the start? | Yes, each 10 minutes. It catches a process that starts after the end sweep. |
| D3 | A gate time limit: its own failure class, not counted in the "identical failures" streak, retried up to the usual retry limit? | Yes. |
| D4 | The sweep is on by default, with a config switch to turn it off? | Yes. |
| D5 | Dispatch order: the two legs with free files (R3, R4) now, the others in the order of the route as the TASK-651 tickets on the same files merge? | Yes. |

### Leg order and file conflicts

| Leg | Files | Waits for |
|---|---|---|
| R1 | `backend_claudecode.go`, `backend_qwencode.go`, `parallel.go`, `acceptance_evidence_run.go` | — |
| R2 | `agent_command.go` | #6530 (TASK-651 P8, same file), R1 |
| R3 | `quality.go`, `dispatcher.go` | — |
| R4 | `internal/procgroup/` (new files) | — |
| R5 | `backend_claudecode.go`, `backend_qwencode.go`, `model_env.go`, `agent_command.go` | R1, R2 |
| R6 | `runner.go`, `backend.go`, `configs/pilot.example.yaml` | R4, R5, #6534 (TASK-651 H18, same file) |
| R7 | `cmd/pilot/main.go`, `dispatcher.go`, `internal/gateway/prometheus.go` | R6, R3 |
| R8 | `runner.go`, `cmd/pilot/main.go` | R7 |

---

### LEG R1 — repo: qf-studio/pilot — title: fix(executor): a backend process that exits leaves no process in its group — a group kill after each exit of the claude, qwen, parallel and evidence processes

probe: present qf-studio/pilot func[[:space:]]killProcessGroup\(
probe: present qf-studio/pilot func[[:space:]]configureProcessGroup\(
probe: present qf-studio/pilot func[[:space:]]survivingChildCmdlines\(
probe: absent  qf-studio/pilot func[[:space:]]TestBackendExitKillsItsProcessGroup\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-656. The executor starts each backend process with a process group of its own (configureProcessGroup) and kills that group on a cancel, on a time limit and on a stall (killProcessGroup). When the backend process exits by itself, `cmd.Wait` returns and no kill runs: each child that is still in the group stays alive with parent 1. On the daemon host such processes collected for weeks and slowed the quality gates until each run failed. Keep the change small: only the items below.

- `internal/executor/backend_claudecode.go`: the group kill runs on the cancel, grace, heartbeat, stall and watchdog paths; none follows the normal `cmd.Wait`.
- `internal/executor/backend_qwencode.go`, `internal/executor/parallel.go`, `internal/executor/acceptance_evidence_run.go`: the same pattern.

## Scope

1. A helper in `internal/executor/process_group.go`: sweepProcessGroupAfterExit(cmd, log). It sends SIGKILL to the process group of cmd after the process was reaped, ignores the error "no such process", and writes one Info line with the count and the command lines of the processes that were alive in the group right before the kill (survivingChildCmdlines), only when there was one or more.
2. Call the helper right after `cmd.Wait` returns in the four files above, on each path (success and error).
3. With `run_as_user` set the helper uses the same kill route as killProcessGroup does today (through sudo); no new route.
4. Tests: a function TestBackendExitKillsItsProcessGroup — a fake backend script that starts `sleep 300` in the background (same process group) and exits 0: after the execute call returns, the sleep process is not alive; a second case where the script exits 1; the Info line holds the command line of the sleep process. One test for the helper with a process that is already gone: no error.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ -run 'BackendExitKillsItsProcessGroup|ProcessGroup'` passes; paste the output into the PR body.
- [ ] Remove the helper call after `cmd.Wait` in `backend_claudecode.go` -> TestBackendExitKillsItsProcessGroup fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

A process that left the group with `setsid` (other legs). agentCommand and its callers. The quality gates. The daemon shutdown. Windows.

## Refs

- Navigator TASK-656 in this repo · memory mem-326

### LEG R2 — repo: qf-studio/pilot — title: fix(executor): helper sessions of the agent get a process group and a group kill — agentCommand sets both for each caller

probe: present qf-studio/pilot func[[:space:]]agentCommand\(
probe: present qf-studio/pilot func[[:space:]]TestBackendExitKillsItsProcessGroup\(
probe: present qf-studio/pilot func[[:space:]]TestRunAsFirewallStatusAtTimeLimit\(
probe: absent  qf-studio/pilot func[[:space:]]TestAgentCommandCancelKillsTheGroup\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-656. agentCommand (`internal/executor/agent_command.go`) builds the command of each helper session of the agent: the epic planner, the post-execution summary, the contract evidence, the subtask parser, the intent judge, the complexity classifier and the effort classifier. With the option `run_as_user` empty it gives the command no process group, and a cancel of the context kills one pid. A child of that session stays alive. The backend process itself already has a group and a group kill. Keep the change small: only the items below, only in `agent_command.go`, `process_group.go` and their test files.

## Scope

1. With the option `run_as_user` empty, agentCommand calls configureProcessGroup on the command and sets `cmd.Cancel` to a SIGKILL of the process group (killProcessGroup). The wait delay of configureProcessGroup stays.
2. With the option set, the behaviour of today stays (the session of the agent user and the kill through sudo).
3. A caller that sets `cmd.Cancel` itself after agentCommand keeps its own value; do not change the callers.
4. Tests: a function TestAgentCommandCancelKillsTheGroup — a command from agentCommand that runs a script with `sleep 300` in the background and then waits: after the cancel of the context, `cmd.Wait` returns within the wait delay and the sleep process is not alive; the command has Setpgid set; with the test state of `run_as_user` set, the command has no Setpgid of the daemon side (the state of today).

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ -run 'AgentCommand'` passes; paste the output into the PR body.
- [ ] Remove the `cmd.Cancel` line -> TestAgentCommandCancelKillsTheGroup fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The backend process. The gate scripts, the lint fix and the workflow hooks (`internal/executor/gate.go`, `internal/executor/lint.go`, `internal/executor/workflow/hooks.go`). The opencode server. The firewall probe.

## Refs

- Navigator TASK-656 in this repo

### LEG R3 — repo: qf-studio/pilot — title: fix(executor): a quality gate that its time limit killed says "timed out" and is not an identical code failure

probe: present qf-studio/pilot func[[:space:]]qualityGateFailureSummary\(
probe: present qf-studio/pilot \)[[:space:]]priorClaimsHadIdenticalFailureStreak\(
probe: present qf-studio/pilot func[[:space:]]regateTimedOut\(
probe: absent  qf-studio/pilot func[[:space:]]TestQualityGateTimeoutSummarySaysTimedOut\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-656. A quality gate that runs longer than its time limit is killed. The gate runner (`internal/quality/runner.go`) returns the exit code -1 with ErrGateTimeout and records "quality gate timed out: after N" in the error of the gate result. qualityGateFailureSummary (`internal/executor/quality.go`) prints only the exit code: "gate `test`: `make test-changed` exited with code -1". The issue comment and the ledger then show no word about a time limit. The dispatcher (`internal/executor/dispatcher.go`, priorClaimsHadIdenticalFailureStreak) compares the failure texts of the last runs; two equal texts park the ticket with "consecutive identical failures (will not retry)". On 2026-11-19 a loaded host made the test gate exceed its limit for 16 hours: each ticket failed with this text and one was parked, though no test failed. Keep the change small: only the items below, only in `quality.go`, `dispatcher.go` and their test files.

## Scope

1. qualityGateFailureSummary: for a gate whose result is a time limit (the error of the gate holds the text of quality.ErrGateTimeout, as regateTimedOut reads it) the line is "- gate `NAME`: `COMMAND` timed out after LIMIT (time limit of the gate: not a code failure)". LIMIT is the duration from the gate error when it is there; with no duration the line ends after "timed out". Each other line stays.
2. A function isGateTimeoutFailure(text string) bool next to the streak check: true when a failure text holds "timed out" in a gate line of the summary. priorClaimsHadIdenticalFailureStreak does not count a failure for which it is true: such a ticket is retried up to the usual retry limit and is never parked as "consecutive identical failures".
3. The retry prompt for the agent after a gate time limit keeps its text of today.
4. Tests: a function TestQualityGateTimeoutSummarySaysTimedOut — an outcome with one gate killed by its limit of 10 minutes gives the new line and no "exited with code -1"; an outcome with a failed test keeps "exited with code 1"; a gate with a missing runner keeps its note. A dispatcher test: two prior failures with the same time limit text give no streak; two prior failures with the same "exited with code 1" text still give the streak.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ -run 'QualityGateTimeout|IdenticalFailure'` passes; paste the output into the PR body.
- [ ] Remove the time limit branch of qualityGateFailureSummary -> TestQualityGateTimeoutSummarySaysTimedOut fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The time limits themselves. The gate runner in `internal/quality`. A new status in the ledger. Alerts. The CI failure path of the autopilot.

## Refs

- Navigator TASK-656 in this repo · memory mem-326

### LEG R4 — repo: qf-studio/pilot — title: feat(procgroup): a sweep that finds the processes of a run by a marker or by a directory and stops them (Linux; a no-op on other systems)

probe: present qf-studio/pilot func[[:space:]]Configure\(
probe: present qf-studio/pilot func[[:space:]]Alive\(
probe: absent  qf-studio/pilot func[[:space:]]SweepProcesses\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-656. The package `internal/procgroup` holds the helpers for process groups (Configure, Kill, Alive). A process that calls `setsid` leaves its group: background shell tasks and dev servers of an agent session do that, and they stay alive with parent 1 after the run. On the daemon host 133 such trees collected. Facts from that host: such a process keeps the environment of the run, and its working directory stays the worktree of the run, shown by the kernel with the suffix " (deleted)" after the removal. This leg adds the function that finds and stops such processes. No caller is added in this leg. Keep the change small: only new files in `internal/procgroup`.

## Scope

1. A new file `sweep_linux.go` in `internal/procgroup` with the function SweepProcesses(m SweepMatch, grace time.Duration) (SweepResult, error) and the types:
   - SweepMatch: EnvAll []string (each entry `NAME=VALUE` or `NAME=`: the environment must hold each one; an entry that ends in `=` matches any value), DirPrefixes []string (the working directory must be at or below one of them; the suffix " (deleted)" of the link text is cut before the compare; a prefix matches only at a path separator), MatchEnvOrDir bool (false: environment AND directory; true: environment OR directory; an empty list never matches by itself), KeepPIDs map[int]bool, Keep func(env []string, dir string) bool (a process for which it returns true is never stopped; nil means none).
   - SweepResult: Stopped int, Killed int (needed SIGKILL), Cmdlines []string (at most 20, each cut to 200 bytes).
2. The scan reads the proc file system: only processes of the effective user id of the caller; never pid 1, the caller itself, or a parent of the caller up to pid 1; never a pid in KeepPIDs. A process whose environment or working directory cannot be read is skipped. A SweepMatch with no EnvAll and no DirPrefixes is an error and stops nothing.
3. It sends SIGTERM to each match, waits up to grace for them to go, then sends SIGKILL to each one that is still alive and has the same start time as at the scan (field 22 of the stat file of the process), so that a reused pid is never killed.
4. A new file `sweep_other.go` in the same package (each other system): the same types, and a SweepProcesses that returns an empty result and no error.
5. Tests in a new file `sweep_linux_test.go` (they run on Linux; CI is Linux): a child started with `setsid` and a marker `PILOT_SWEEP_TEST=RANDOM` in its environment is stopped by a match on that entry, and a second child with another value is not; a child whose working directory is below a temporary directory is stopped by the directory match, also after the directory was removed; a prefix that ends in `a` does not match a directory that ends in `ab`; a child that ignores SIGTERM is gone after the grace and counts in Killed; a pid in KeepPIDs stays; Keep returns true -> stays; the test process and its parent stay with a match that would hold them; an empty match is an error. A test in a new file `sweep_other_test.go` with a build tag for the other systems: the no-op result.

## Acceptance

- [ ] `go test -race -count=1 ./internal/procgroup/` passes on Linux (CI); paste the output of the CI job or of a Linux run into the PR body.
- [ ] `GOOS=darwin go vet ./internal/procgroup/` and `GOOS=windows go vet ./internal/procgroup/` pass.
- [ ] Remove the start time check before SIGKILL -> a test fails (a test seam that changes the recorded start time is allowed).
- [ ] Remove the skip of the caller and its parents -> a test fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

A caller in the executor or in `cmd/pilot`. Processes of another user (a kill through sudo). cgroups. macOS and Windows scans. A metric.

## Refs

- Navigator TASK-656 in this repo · memory mem-326

### LEG R5 — repo: qf-studio/pilot — title: feat(executor): each run puts a marker PILOT_RUN_ID into the environment of its agent process, also with run_as_user

probe: present qf-studio/pilot func[[:space:]]modelSubprocessEnvFor\(
probe: present qf-studio/pilot func[[:space:]]TestBackendExitKillsItsProcessGroup\(
probe: present qf-studio/pilot func[[:space:]]TestAgentCommandCancelKillsTheGroup\(
probe: absent  qf-studio/pilot func[[:space:]]TestRunIDReachesTheAgentProcess\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-656. A process that an agent session starts keeps the environment of the session, also after it left the process group with `setsid`. Today that environment holds `PILOT_EXECUTOR=1` (the same for each run) and the issue number, but no value that is unique for one run. A later leg stops the left processes of a run by such a value. This leg adds it. Keep the change small: only the items below.

## Scope

1. A function newRunID(taskID string) string in `internal/executor/process_group.go`: the task id, a dash, and the start time in nanoseconds as a decimal number. Only the characters A to Z, a to z, 0 to 9, dot, dash and underscore of the task id are kept; each other character becomes an underscore.
2. The backend process of a run gets `PILOT_RUN_ID=VALUE` in its environment, next to `PILOT_EXECUTOR=1`, in `internal/executor/backend_claudecode.go` and `internal/executor/backend_qwencode.go`. One run uses one value for each backend process it starts (the first execute call and each retry call of the same run): keep the value on the context of the run (a context key with a helper runIDFrom(ctx)) and create it where the run starts its first backend call when the context has none.
3. With the option `run_as_user` set, the name `PILOT_RUN_ID` is in the allow list of the agent environment (the list that holds `PILOT_EXECUTOR` in `internal/executor/agent_command.go`), so the value reaches the processes of the agent user.
4. The value is logged once for a run in the "Starting task execution" line (field `run_id`).
5. Tests: a function TestRunIDReachesTheAgentProcess — a fake backend script that prints its environment: the output holds `PILOT_RUN_ID=` with the task id and a number; two execute calls with one run context give the same value; two run contexts give two values; with the test state of `run_as_user` set, the preserved name list of the sudo arguments holds `PILOT_RUN_ID`; newRunID for a task id with a slash and a space gives only allowed characters.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ -run 'RunID'` passes; paste the output into the PR body.
- [ ] Remove `PILOT_RUN_ID` from the allow list -> the `run_as_user` case of the test fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The sweep and its callers. The quality gates, the workflow hooks and the lint fix (the daemon starts them, and their group kill holds). The helper sessions of agentCommand. A column in the ledger.

## Refs

- Navigator TASK-656 in this repo

### LEG R6 — repo: qf-studio/pilot — title: feat(executor): at the end of each run the daemon stops the processes that the run left — a sweep by the run marker and the worktree path, with a config switch

probe: present qf-studio/pilot func[[:space:]]SweepProcesses\(
probe: present qf-studio/pilot func[[:space:]]TestRunIDReachesTheAgentProcess\(
probe: present qf-studio/pilot func[[:space:]]TestHandOverPushForgivenIsRecorded\(
probe: absent  qf-studio/pilot func[[:space:]]TestRunEndSweepsLeftProcesses\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-656. procgroup.SweepProcesses stops processes by an environment entry or by a working directory. Each run has a marker `PILOT_RUN_ID` in the environment of its agent process, and its work happens in a worktree of its own (a directory with the name prefix `pilot-worktree-` in the temporary directory). Background tasks and dev servers of the agent leave the process group and stay alive after the run. This leg calls the sweep when a run ends. Keep the change small: only the items below.

## Scope

1. Config: a block `process_sweep` in the executor config (`internal/executor/backend.go`, next to `subprocess_limits`): `enabled` (bool, default true) and `grace` (duration, default 5s). Document it in `configs/pilot.example.yaml` with one comment line for each field.
2. A method of Runner, sweepRunProcesses(runID, worktreePath string): with the switch on, it calls procgroup.SweepProcesses with EnvAll `PILOT_RUN_ID=RUNID`, DirPrefixes with the worktree path (only when the path is a pilot worktree path, not the project directory itself), MatchEnvOrDir true, and the grace of the config. An empty run id gives no environment entry; with no entry and no directory it does nothing. It writes one Info line with the task id, the count and the command lines when one or more processes were stopped, and a Warn line on an error. It never fails the run.
3. The run calls it once, when the run is over on each path (success, failure, time limit, cancel), after the last backend call and after the quality gates and the push of that run, and before the worktree of the run is removed. Use a context that is not the expired context of the run.
4. With the option `run_as_user` set the call still runs; processes of the agent user are not visible to it and stay (a later leg).
5. Tests: a function TestRunEndSweepsLeftProcesses (Linux only, skipped with a reason on other systems) — a fake backend script that starts `setsid sleep 300` in the worktree and exits: after the run, that process is not alive; with `process_sweep.enabled: false` it is still alive (the test stops it); a process with the run id of another run and another directory stays; a run in the project directory itself (no worktree) passes no directory to the sweep. A config test: the default is on with 5s.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ -run 'RunEndSweeps|ProcessSweepConfig'` passes on Linux (CI); paste the output into the PR body.
- [ ] Remove the call at the end of the run -> TestRunEndSweepsLeftProcesses fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The sweep at the daemon start and on a timer. A metric. Processes of the agent user. The shutdown of the daemon.

## Refs

- Navigator TASK-656 in this repo

### LEG R7 — repo: qf-studio/pilot — title: feat(executor): the daemon stops processes of dead runs at its start and on a timer, and counts them

probe: present qf-studio/pilot \)[[:space:]]sweepRunProcesses\(
probe: present qf-studio/pilot func[[:space:]]isGateTimeoutFailure\(
probe: present qf-studio/pilot func[[:space:]]CleanupOrphanedWorktrees\(
probe: absent  qf-studio/pilot func[[:space:]]SweepDeadRunProcesses\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-656. The daemon stops the left processes of a run when the run ends. Two cases stay: processes of runs that ended before that code existed or while the daemon was down, and a process that starts after the end sweep. Facts from the daemon host: an orphan of a run keeps `PILOT_EXECUTOR=1` in its environment, and its working directory is a pilot worktree path (a directory with the name prefix `pilot-worktree-` in the temporary directory, with the suffix " (deleted)" after the removal). At the daemon start CleanupOrphanedWorktrees removes left worktrees and stops no process. Keep the change small: only the items below.

## Scope

1. A function SweepDeadRunProcesses(live func(runID string) bool, grace time.Duration, log) in `internal/executor/process_group.go`: it calls procgroup.SweepProcesses with EnvAll `PILOT_EXECUTOR=1`, DirPrefixes with the prefix of pilot worktree paths (the constant that the worktree code uses for its names), MatchEnvOrDir false (environment AND directory), and a Keep function that returns true when the environment of the process holds a `PILOT_RUN_ID` for which live returns true. A process with no `PILOT_RUN_ID` is not kept. It returns the count and logs as sweepRunProcesses does.
2. The Runner knows its live runs: a set of run ids, filled when a run starts and cleared when it ends (after its end sweep). A method liveRun(runID) reads it.
3. At the daemon start, before each call of CleanupOrphanedWorktrees in `cmd/pilot/main.go`, the daemon calls SweepDeadRunProcesses with a live function that is always false (no run is alive yet), when `process_sweep.enabled` is on. Only the daemon start does it, not each subcommand.
4. The loop that runs the stale recovery of the dispatcher (runStaleRecoveryLoop) also calls SweepDeadRunProcesses each 10 minutes with the live function of the Runner.
5. A counter `pilot_executor_process_sweep_stopped_total` with a label `phase` (`run_end`, `start`, `timer`) in the Prometheus text of `internal/gateway/prometheus.go`, in the pattern of `pilot_executor_result_exit_grace_kills_total`; sweepRunProcesses adds to it too.
6. Tests (Linux only, skipped with a reason on other systems): a function TestSweepDeadRunProcessesAtStart — a process started with `setsid`, `PILOT_EXECUTOR=1` and a working directory below a temporary pilot worktree path is stopped, also after the directory was removed; the same process with a `PILOT_RUN_ID` of a live run stays; a process with `PILOT_EXECUTOR=1` and a working directory outside a pilot worktree path stays; a process in a pilot worktree path with no `PILOT_EXECUTOR` stays; with the switch off nothing is stopped; the counter grows by the count with the correct label.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ ./internal/gateway/ -run 'SweepDeadRun|ProcessSweep'` passes on Linux (CI); paste the output into the PR body.
- [ ] Remove the Keep function -> the test with the live run fails.
- [ ] Change the match to environment OR directory -> a "stays" case fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

Processes of the agent user. A dashboard panel. A change of CleanupOrphanedWorktrees. The shutdown of the daemon.

## Refs

- Navigator TASK-656 in this repo · memory mem-326

### LEG R8 — repo: qf-studio/pilot — title: fix(executor): on shutdown the daemon waits for the kill of the agent processes before it exits

probe: present qf-studio/pilot \)[[:space:]]CancelAll\(
probe: present qf-studio/pilot func[[:space:]]SweepDeadRunProcesses\(
probe: absent  qf-studio/pilot func[[:space:]]TestCancelAllWaitsForTheKill\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-656. CancelAll of Runner (`internal/executor/runner.go`) sends SIGTERM to the process group of each running task and starts a timer of 10 seconds that sends SIGKILL to what is left. The shutdown path of the daemon (`cmd/pilot/main.go`) calls CancelAll and then goes on to its exit; the process ends before the timer fires. An agent process that ignores SIGTERM, and each process in its group, then outlives the daemon. Keep the change small: only the items below, only in `runner.go`, `cmd/pilot/main.go` and their test files.

## Scope

1. A method CancelAllAndWait(ctx context.Context) of Runner: it sends SIGTERM as CancelAll does, then waits until no tracked process group is alive or 10 seconds passed or ctx ended, then sends SIGKILL to each group that is still alive, and returns the count of groups that needed SIGKILL. Then, with `process_sweep.enabled` on, it calls sweepRunProcesses for each run that was live.
2. CancelAll keeps its behaviour for its other callers.
3. The shutdown path of the daemon start command calls CancelAllAndWait in place of CancelAll, with a context of 15 seconds, and logs the count.
4. Tests: a function TestCancelAllWaitsForTheKill — a tracked process that ignores SIGTERM is not alive when CancelAllAndWait returns, and the call returns after the wait and reports 1; a tracked process that exits on SIGTERM makes the call return in under 2 seconds with 0; a context that ends early still sends SIGKILL before the return.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ -run 'CancelAll'` passes; paste the output into the PR body.
- [ ] Return from CancelAllAndWait right after the SIGTERM -> TestCancelAllWaitsForTheKill fails.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The hot restart of the self-upgrade (the process image is replaced, the runs stay). A change of the 10 seconds. The gateway shutdown. Windows.

## Refs

- Navigator TASK-656 in this repo

---

## Out of Scope

- cgroups and systemd scopes: the daemon host runs the daemon in a tmux session scope with no managed cgroup. A later decision.
- Processes of the agent user with `run_as_user` set: a later leg, after TASK-651 turns the option on (a kill through sudo; a kill by user is safe only when no run is alive).
- A scan on macOS or Windows.
- The gate scripts, the lint fix and the workflow hooks: the daemon starts them, and a process that leaves their group is not marked.
- A change of gate time limits.

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|----------|-------------------|--------|-----------|
| How to find what left the process group | cgroup with `cgroup.kill`; a subreaper in the daemon; a kill by user; a scan of the proc file system by environment and working directory | The scan by environment and working directory | The host has no managed cgroup. A subreaper needs a wait loop that can take exit statuses of other children. A kill by user stops live runs. The environment and the working directory stay after `setsid` (seen on the host). |
| Match rule for old orphans | name patterns (`vite`, `bash`); `PILOT_EXECUTOR` alone; `PILOT_EXECUTOR` AND a pilot worktree directory | `PILOT_EXECUTOR` AND a pilot worktree directory | A name pattern can stop a process of a developer. `PILOT_EXECUTOR` alone can hold a tool that a person started from an agent shell. Both together name a process that an agent session started in a task worktree. |
| Run marker | the execution id of the ledger; the task id; the task id plus the start time | The task id plus the start time in nanoseconds | It needs no ledger read at the spawn site, and a retry of a task gets a new value. |
| Gate time limit class | a new ledger status; a text marker that the streak check reads | A text marker | The smallest change that stops the wrong park. A status can follow. |

---

## Verify

```bash
go test -race -count=1 ./internal/procgroup/
go test -race -count=1 ./internal/executor/ -run 'ProcessGroup|AgentCommand|RunID|RunEndSweeps|SweepDeadRun|CancelAll|QualityGateTimeout'
```

On the daemon host after the release with R7 (read-only): the count of processes of the daemon user with parent 1 that hold `PILOT_EXECUTOR` in their environment is 0 one hour after a run, and `pilot_executor_process_sweep_stopped_total` is in the metrics.

---

## Done

- [ ] Legs R1 to R8 are merged and each has a run-it review comment on its PR.
- [ ] One week after the release: no orphan process tree of a dead run on the daemon host (read through SSM).
- [ ] A gate time limit in the ledger reads "timed out after N".

---

## Refs

- Memory mem-326 (the incident of 2026-11-19).
- `.agent/sops/executor-heartbeat-kills-live-local-tool.md` (the `setsid` escape, known).
- Archive: TASK-416 (single pid kill leaks children), TASK-636 (gate group kill).
- TASK-651 (`run_as_user`): tickets #6530 and #6534 change files of legs R2 and R6.

---

**Last Updated**: 2026-11-19
