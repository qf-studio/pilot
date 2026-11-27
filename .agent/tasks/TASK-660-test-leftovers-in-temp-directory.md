# TASK-660: Test leftovers in the temporary directory — the Pilot tests remove what they create, and the box is cleaned one time

**Status**: 🚀 **State 2026-11-27 18:21Z: both code tickets are merged and reviewed. The second one ([pilot#6747](https://github.com/qf-studio/pilot/issues/6747) → PR#6753, 18:07Z) removes the askpass helper directory at the end of the test binary; proof with a fresh temporary directory: the parent commit leaves 1 entry, the merge commit leaves 0. Open: the release, then the count on the box one day later.** **State 2026-11-27 17:25Z: the review of PR#6740 is done (comment on the PR). The whole executor package with a fresh, empty temporary directory (macOS, 842 s) leaves no directory of the three fixed kinds. It leaves ONE directory with the prefix pilot-askpass-: the function gitAskpassHelperPath makes it one time for each process and nothing removes it. A second ticket (L2) removes it at the end of the test binary.** One test of that run failed on the laptop and fails on the parent commit too (TestMemoryGraphBudgetCountsOnlyFailedReads, the case with the cancelled context); the CI is green; not from this change. **State 2026-11-27 17:15Z: the test fix merged ([pilot#6733](https://github.com/qf-studio/pilot/issues/6733) → PR#6740, 16:55Z). Review after the merge: the three tests of the ticket pass, a mutation of the helper clean-up fails the right test; a run of the whole executor package with a fresh temporary directory is in work (the first try stopped at the 10-minute default limit of Go and gave no result).** Earlier state: **L1 DISPATCHED 2026-11-27 16:10Z → [pilot#6733](https://github.com/qf-studio/pilot/issues/6733) (`pilot` + `no-decompose`): the executor tests remove the worktree and the repository directories that they create.** The box clean-up is a step of the session (the 1,279 old test worktrees and the other test leftovers older than 24 hours). A temporary directory for each run is held until the count one day after the release.
**Created**: 2026-11-27
**Assignee**: Pilot (one ticket) + the session (box clean-up, a count one day later)

---

## Context

**Problem**:
The temporary directory of the founder box is memory (`tmpfs`, 7.8G). The quality gates of each ticket run the Go tests of this repository there, and some tests leave directories. On 2026-11-27 the session removed 5,661 leftover directories by hand. A read of the box at 16:03Z found 2,345 entries again, of them 1,279 with the name prefix pilot-worktree-GH-5399.

Read on 2026-11-27 (code at release v2.328.0, box read-only through SSM):

- The 1,279 directories with the prefix pilot-worktree-GH-5399 are test output, not daemon worktrees. The test TestBackendTimeoutSalvage_PushFailure_PreservesWorktreeAndBranch in `internal/executor/runner_gh5399_test.go` uses the task ID GH-5399, proves that the worktree stays after a failed push, and never removes it. Each directory points to a git directory of that test, and that git directory is gone in all 1,279 cases. No repository of the daemon names them, so the clean-up of the daemon at its start skips them. 1,238 belong to the daemon user, 41 to the agent user; the oldest is of 09-09.
- The helper setupTestRepoWithRemote in `internal/executor/worktree_test.go` makes two directories (prefixes worktree-remote- and worktree-local-) and gives both paths back with no clean-up of its own. Callers that drop a path leak it. Known callers: three in `internal/executor/gh_repo_arg_test.go`, two in `internal/executor/handover_test.go`, one in `internal/executor/handover_delivery_test.go`. The box held 33 of each prefix, all of the agent user.
- Two tests leave a file: `internal/tunnel/service_test.go` (prefix plist-test-) and `internal/teams/teams_test.go` (prefix pilot-teams-test-).
- Other prefixes on the box come from tests whose clean-up does not run when the test process is killed (a gate timeout): pilot-dispatcher-test (82), tmp. (80), pilot-empty-hooks (50, from releases before the hook path moved to a fixed place). No code fix removes this class; a temporary directory for each run does.
- The daemon process has no TMPDIR. The agent environment lets TMPDIR through (allow list in `internal/executor/agent_command.go`) but sets none for a run.
- The memory mem-356 says that the GH-5399 directories are daemon worktrees. That is wrong and is corrected with this task.

**Goal**:
The tests of Pilot leave nothing in the temporary directory after a normal run. The box is cleaned one time. A count one day later proves the result.

---

## Known Pitfalls & Patterns

- **PITFALL** (98%, mem-177): a Go test that runs git from a hook can act on the real repository, because git exports GIT_DIR. The clean-up code of this task runs git only with an explicit directory of the test, and removes only paths that the test made.
- **PITFALL** (mem-356, corrected here): do not name a directory set "daemon worktrees" before a read of its git pointer on the box.

---

## Acceptance Criteria

- [ ] After a full run of the tests of the executor package, no new entry with the prefixes worktree-remote-, worktree-local- or pilot-worktree-GH-5399 is in the temporary directory.
- [ ] The box has no directory with the prefix pilot-worktree-GH-5399 that is older than one day.
- [ ] One day after the fix is on the box, the count of leftover test directories of that day is under 10.

---

## Route

### Phase 1: The tests clean up
- [x] 1. The executor tests remove the worktree and the two repository directories that they create; two more tests stop leaving a file (L1) — pilot#6733, merged 11-27 16:55Z; not released yet
- [x] 1a. The test binary of the executor package removes the askpass helper directory that the process made (L2) — pilot#6747, merged 11-27 18:07Z

### Phase 2: The box
- [x] 2. The session removes the 1,279 old test worktrees and the other test leftovers older than 24 hours from the box (operator) — done 11-27 16:12Z: 1,431 entries removed (1,276 GH-5399 test worktrees, each with a dead git pointer; 82 pilot-dispatcher-test; 50 pilot-empty-hooks; 23 of four other test prefixes); the entries of the temporary directory went from 2,348 to 917; 3 GH-5399 worktrees and 33 pairs of the helper directories are younger than 24 hours and stay; 80 entries with the prefix tmp. have no known source and stay
- [ ] 3. The session counts the leftovers one day after the release with step 1 is on the box (operator, needs 1)

### Side tracks
- [ ] A temporary directory for each run, which the daemon removes after the run; it covers killed tests too (ticket written after step 3, only when the count is not under 10)
- [x] The memory mem-356 is corrected: the GH-5399 directories are test output

### Waits for you
- Nothing.

---

## Implementation

### Phase 1 — L1
**Goal**: no leak in a normal test run.

**Files**:
- `internal/executor/worktree_test.go` - the helper registers its own clean-up
- `internal/executor/runner_gh5399_test.go` - the test removes the preserved worktree after its assertions
- `internal/tunnel/service_test.go`, `internal/teams/teams_test.go` - the file is made in the directory of the test

### Phase 2 — box
The removal runs through SSM as the user that owns the entries (the daemon user and the agent user), only for entries older than 24 hours and only by the name prefixes of this document. The daemon stays up: no repository names the directories.

---

## Out of Scope

- A lint that forbids a direct temporary directory in tests (317 call sites are correct today).
- The production code that makes temporary directories; each caller removes its own in the normal path.
- Other boxes.

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|----------|-------------------|--------|-----------|
| Where the helper leak is fixed | Each of the six callers; the helper itself | The helper registers the clean-up | One change covers each caller of today and of later |
| The GH-5399 test | Remove the assertion that the worktree stays; remove the worktree after the assertions | Remove after the assertions | The test keeps its proof |
| Killed tests | A temporary directory for each run now; measure first | Measure first | The count after step 1 says if the larger change is necessary |
| Box clean-up with the daemon up | Stop the daemon; keep it up | Keep it up | All 1,279 git pointers are dead; entries younger than 24 hours stay |

---

## Verify

```bash
go test -race -count=1 ./internal/executor/ -run 'TestBackendTimeoutSalvage_PushFailure|TestTempLeftovers'
go test -race -count=1 ./internal/tunnel/ ./internal/teams/
```

---

## Done

- [ ] L1 is merged, released, and on the box.
- [ ] The box count of step 3 is recorded here.

---

## Legs

### LEG L1 — repo: qf-studio/pilot — title: test(executor): the tests remove the worktree and the repository directories that they create — no leftover in the temporary directory after a run

probe: present qf-studio/pilot func[[:space:]]setupTestRepoWithRemote\(t[[:space:]]\*testing\.T\)
probe: present qf-studio/pilot func[[:space:]]TestBackendTimeoutSalvage_PushFailure_PreservesWorktreeAndBranch\(
probe: absent  qf-studio/pilot func[[:space:]]TestTempLeftovers_

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-660. The quality gates of each ticket run the Go tests on a box whose temporary directory is memory. Three test places leave entries there after a normal run, and the box collected thousands of them:

1. The helper setupTestRepoWithRemote in `internal/executor/worktree_test.go` makes two directories with os.MkdirTemp (name prefixes worktree-remote- and worktree-local-) and gives both paths back. It registers no clean-up. Each caller must remove both, and some callers drop a path with the blank identifier: three in `internal/executor/gh_repo_arg_test.go`, two in `internal/executor/handover_test.go`, one in `internal/executor/handover_delivery_test.go`. This list comes from a read of the code and can be incomplete.
2. The test TestBackendTimeoutSalvage_PushFailure_PreservesWorktreeAndBranch in `internal/executor/runner_gh5399_test.go` uses the task ID GH-5399 and proves that the worktree stays after a failed push. The worktree is a directory with the name prefix pilot-worktree-GH-5399- directly in the temporary directory of the system. The test never removes it. The box held 1,279 of them.
3. `internal/tunnel/service_test.go` makes a file with os.CreateTemp (prefix plist-test-) and `internal/teams/teams_test.go` makes one (prefix pilot-teams-test-). Check each for a removal; when it is absent or can be skipped by an early return, fix it.

## Scope

1. `internal/executor/worktree_test.go`: setupTestRepoWithRemote registers a t.Cleanup that removes both directories with os.RemoveAll. The signature and the two return values stay. A caller that removes a directory itself keeps its code (a second removal of an absent path is no error). Do not change the directory names or their place.
2. `internal/executor/runner_gh5399_test.go`: after the last assertion of the test (register it early with t.Cleanup, so that a failed assertion does not skip it), remove the preserved worktree directory with os.RemoveAll. The assertions that the worktree and the branch stay after the failed push do not change. Run no git command for the removal against a repository that the test did not make.
3. `internal/tunnel/service_test.go` and `internal/teams/teams_test.go`: make the file under t.TempDir() in place of os.CreateTemp with an empty directory argument, or make sure that a t.Cleanup removes it.
4. A new test file in the directory of `internal/executor/worktree_test.go` (name it temp_leftovers_test.go) with two tests. They must not run in parallel with other tests (no t.Parallel) and they read the directory that os.TempDir() gives:
   - TestTempLeftovers_SetupTestRepoWithRemote: a subtest calls setupTestRepoWithRemote and keeps both paths. After the subtest ends, the parent asserts that both paths are absent.
   - TestTempLeftovers_GH5399WorktreeIsRemoved: list the entries with the prefix pilot-worktree-GH-5399- before; run the body of the GH-5399 test as a subtest (move the body into a helper function that both tests call, or call the test function with the subtest's testing.T); assert after it that no new entry with that prefix exists. Entries that were there before the test are not a failure.
5. Search the executor tests for other callers of setupTestRepoWithRemote that drop a return value, and for other os.MkdirTemp calls with an empty first argument that have no removal in the same function and no t.Cleanup. Fix what you find and list each place in the PR body, also when it is nothing.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ -run 'TestTempLeftovers|TestBackendTimeoutSalvage_PushFailure'` passes; paste the output into the PR body.
- [ ] `go test -race -count=1 ./internal/tunnel/ ./internal/teams/` passes; paste the output into the PR body.
- [ ] Remove the t.Cleanup from setupTestRepoWithRemote -> TestTempLeftovers_SetupTestRepoWithRemote fails
- [ ] Remove the removal of the preserved worktree from the GH-5399 test -> TestTempLeftovers_GH5399WorktreeIsRemoved fails
- [ ] The PR body lists each other leak place that step 5 found, or says that there was none.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

Production code. A temporary directory for each run. A lint for os.MkdirTemp. Tests of other packages than the four files that this issue names, except the places that step 5 finds in the executor package.

## Refs

- Navigator TASK-660 in this repo

### LEG L2 — repo: qf-studio/pilot — title: test(executor): the test binary removes the askpass helper directory of its process — one more leftover of each test run

probe: present qf-studio/pilot func[[:space:]]gitAskpassHelperPath\(\)
probe: present qf-studio/pilot func[[:space:]]TestTempLeftovers_SetupTestRepoWithRemote\(
probe: absent  qf-studio/pilot func[[:space:]]removeGitAskpassHelperDir\(

## Context

This issue is for this repository (qf-studio/pilot); the code is in the working tree.

Navigator TASK-660. The quality gates run the Go tests on a host whose temporary directory is memory, and test leftovers collect there. Issue 6733 fixed three sources. A run of the whole executor test package with a fresh, empty TMPDIR then left exactly one entry: a directory with the name prefix pilot-askpass-.

The source is production code that is correct for the daemon: gitAskpassHelperPath in `internal/executor/git_credentials.go` writes the askpass helper script one time for each process into a private directory made by os.MkdirTemp (decision GH-6694: a private directory, never a fixed name). The daemon is one long process, so one directory is right. A test binary is a new process for each run, so each run of the tests leaves one directory. The test entry point of the package is TestMain in `internal/executor/gitenv_testmain_test.go`; it ends with os.Exit, so a deferred call there does not run.

## Scope

1. `internal/executor/git_credentials.go`: a small unexported function removeGitAskpassHelperDir that removes the directory of the helper script when this process made one, and does nothing in the other case. It must remove only the directory that gitAskpassHelperPath made in this process (take the path from the package state; no search by name prefix). No production code calls it. Do not change gitAskpassHelperPath, except what the new function needs to read the path safely.
2. `internal/executor/gitenv_testmain_test.go`: TestMain keeps the exit code of the run, calls removeGitAskpassHelperDir, and then exits with that code.
3. A test in the directory of `internal/executor/git_credentials.go` (put it into the existing test file of the credentials code): TestRemoveGitAskpassHelperDir. It must not break other tests that use the helper after it in the same process: the test runs the two functions against a temporary state of its own (for example a helper that takes the state as a parameter, with the package functions as thin wrappers), makes the directory, asserts that it exists, removes it, and asserts that it is gone and that a second removal is no error.
4. Check the other test packages whose code reaches gitAskpassHelperPath (search for callers; the daemon command package is a candidate). For each such package with its own TestMain, add the same removal. For a package with no TestMain, do NOT add one in this issue; list the package in the PR body.

## Acceptance

- [ ] `go test -race -count=1 ./internal/executor/ -run 'TestRemoveGitAskpassHelperDir|TestTempLeftovers'` passes; paste the output into the PR body.
- [ ] `go build ./...` passes; paste the output into the PR body.
- [ ] Make removeGitAskpassHelperDir do nothing -> TestRemoveGitAskpassHelperDir fails
- [ ] The PR body shows the entries of a fresh, empty TMPDIR after a run of the git credentials tests of the package (the test names that match GitAskpass or Credential), before and after the change.
- [ ] Do not re-run make build, make test or make lint as evidence.

## Out of scope

The daemon and each other production caller. A fixed path for the helper script. A temporary directory for each run. Other leftover kinds.

## Refs

- Navigator TASK-660 in this repo

## Refs

- L1 → https://github.com/qf-studio/pilot/issues/6733
- L2 → https://github.com/qf-studio/pilot/issues/6747

- Research of 2026-11-27 (session): a scan of the 317 test call sites of os.MkdirTemp and os.CreateTemp; a read of the box at 16:03Z (entries by owner and prefix, the git pointer of each GH-5399 directory).
- Memory mem-356 (the incident of 11-27, corrected by this task).
- TASK-657 (the box work with the infrastructure owner; the leftovers were item 5 of the owner's list).

---

**Last Updated**: 2026-11-27
