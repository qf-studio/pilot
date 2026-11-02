# TASK-616: box quality gates from 23 min to under 5 — executor test hygiene, a changed-package test gate, and a box config step

**Status**: ✅ SCOPE LIVE 2026-10-29 15:46Z — first scoped run GH-5956 (executor diff): `package_count=27 full=false reason="affected 27 of 61 packages"`, gates 3m08 (executor diffs cannot beat the full -short suite by much: executor is the wall-bound package; leaf diffs are where the win is — sample one). Remaining: leaf-diff measurement, Go-cache disk watch (step 4). **Lint gap found 10-29 19:10Z:** GH-5961's box lint gate passed but CI `lint` failed (`pre_pr_fix_test.go:34:2: field started is unused`) → PR#5964 closed, a fix run + a founder approval later. Box lint gate = `make lint`, `required: false`, timeout 2m, golangci-lint **2.14.0** on the box vs **v2.8.0** in CI (ci.yml:119). The box cannot predict CI lint. **Operator step 5 DONE 10-29 20:09Z (founder "pin to v2.8.0"):** `go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.8.0` as ec2-user → `/home/ec2-user/go/bin/golangci-lint` reports 2.8.0 (backup `golangci-lint-2.14.0.bak` beside it); the daemon PATH lists `~/go/bin` first, no restart needed (the gate execs `make lint` per run). Still open: `required: true` for the lint gate once a few runs show it finishes inside the 2 m timeout; bump both sides together when CI moves. History: 🟡 G1 #5924 + G2 #5926 MERGED (v2.298.0); box on `make test-changed` since 10-29 11:49Z — but every scoped run computed `full`: `listPackages` trims the whole `go list` output before splitting lines, so the last package (tests/pentest/cmd/pentest, no tests, no test imports) loses its empty trailing columns and the parser rejects it. G3 #5952 → PR#5953 merged 10-29 14:47Z (Opus APPROVE-w-notes), in v2.299.0 on the box since 15:08Z; **re-measure the next run's scope line** (must be `full=false package_count>0`). Measured 10-29 (full suite under -short, -trimpath): GH-5946 3m08 · GH-5947 5m17 (failed, retried) → 4m21. Scoped timings still unmeasured.
**Spec refs**: `internal/quality/runner.go` (executeCommand ~293, RunAll retries ~147), `internal/quality/types.go` (ResolveConfig, IsParallel), `Makefile` (test, test-short), `internal/executor/git.go` (resolveMergeBaseSHA ~1579, ChangedFilesAgainstOrigin ~1764), `internal/executor/acceptance_evidence_run.go` (evidence reuse keyed by argv ~794), `scripts/pre-push-classify.sh` (docs-only classifier), TASK-393 Phase 2 "affected-tests-only" (never built).
**Origin**: founder 2026-10-28 "where is our 15 min per issue": GH-5915/5916/5917 took 47–48 min each; Claude 1–9 min, box gates 22–23 min, CI 12 min, merge 2 min.

## Why (measured)
- Box gate = `make test` = `go test -v -race ./...`, 61 packages, on 4 vCPUs. The gate itself drives load 9–11 (three test binaries at 70 % CPU each). 22–23 min wall on GH-5916/5917. CI then reruns the same suite in 10–12 min.
- CI package times: executor 389 s, memory 251 s, autopilot 114 s, cmd/pilot 102 s, wiring 61 s; suite sums to 1,204 s.
- Local: internal/memory 160 s with `-race`, 14 s without (277 NewStore calls on pure-Go SQLite, serial). internal/executor 499 s with `-race`, 343 s without, at 46 % CPU: wall-bound by waits. One test, TestIntentJudgeRetry_ExpiredTaskCtx_SkipsReinvocation in `internal/executor/runner_gh5342_test.go`, waits the production 60 s judge timeout. 99 executor tests take ≥ 1 s (265 s total); 30 of 207 executor test files use real temp git repos. `t.Parallel()` is used once in the repo; `testing.Short()` guards exist in 3 files.
- A changed-package scope helps leaf changes only: a change in internal/executor affects 26 of 60 packages (≈ 759 s of 1,204), internal/memory 30 of 60. Test imports must be one hop over the production closure; transitive test imports make executor and autopilot mutually reachable and the scope degenerates to the full suite.
- Scope blind spots: tests that read sibling sources by relative path (`internal/executor/terminal_status_inventory_test.go` reads ../memory/store.go and ../adapters/github/cleanup.go; `internal/executor/prompt_leak_test.go` reads ../autopilot), testdata, embed, go.mod/Makefile/scripts/.github changes.
- Evidence reuse is keyed by the gate's argv, so a scoped gate must use a distinct command (`make test-changed`), never a modified `make test`.
- A failed or timed-out test gate retries the whole suite (MaxRetries + 1 attempts). Box config: test gate `make test`, timeout 10m, max_retries 2 — per-gate wall is not logged, so whether the 10m is enforced is unknown.
- Box Go cache is 36 GB and GOFLAGS is unset; without -trimpath every pilot-worktree path may recompile first-party packages (unverified).
- CI stays the merge gate of record; autopilot blocks on any failed check-run outside the allowlist (GH-5468), so splitting CI is not a speed lever. Do not split CI.

## Legs — pilot (daemon)

### LEG G1 — repo: qf-studio/pilot — title: test(executor): remove the 60 s mock-timeout wait and put -short guards on the end-to-end executor tests so a box run with -short finishes in minutes
probe: present qf-studio/pilot func TestIntentJudgeRetry_ExpiredTaskCtx_SkipsReinvocation\(
probe: present qf-studio/pilot func TestRunner_AdoptAutoPreservedCommit\(
probe: present qf-studio/pilot ^test-short:

## Context

The internal/executor test suite takes 343 s locally without the race detector at 46 percent CPU: it is wall-bound by waits and serial real-git tests, not by compute. TestIntentJudgeRetry_ExpiredTaskCtx_SkipsReinvocation in `internal/executor/runner_gh5342_test.go` uses a mock judge that blocks on the judge context, and the production judge timeout in `internal/executor/intent_judge.go` is 60 seconds, so the test waits the full minute (61.5 s measured). The next largest are end-to-end Runner.Execute tests on real temp repos with a fake gh on PATH: TestRunner_AdoptAutoPreservedCommit in `internal/executor/gh5709_test.go` (18.7 s), TestExecute_PRCreateSurvivesDeadlineBlownDuringEvidence in `internal/executor/runner_gh5867_test.go` (7.8 s), TestPushBranchWithRetry_InheritedHeadLease and _OwnAutoPreservePush (5.2 s, 3.6 s), TestPreflightSelfHeal (5.0 s), TestConfigureProcessGroupWaitDelayBoundsCmdWait (5.0 s), TestStripAnthropicCoAuthor (4.8 s), TestFinalizeEpicBranchPR_AbortSweepsRunningChildren (3.8 s), TestExecuteSubIssues_ClaimLost_RunningSilentlyTimesOutFromStartedAt (3.7 s), TestDispatcher_AdoptedPreservedRun_NoBackoffNoBlockedLabel (3.6 s); `internal/executor/dispatcher_test.go` carries 19 time.Sleep calls across 118 tests. The repo uses testing.Short() in three files and t.Parallel() once. The Makefile has a test-short target (`go test -short -race ./...`) used only by the pre-push gate. The box gate will move to a -short run (TASK-616), so the guards must land first.

## Implementation

1. Make the judge timeout injectable for tests (a field or option on the intent judge, default 60 s) and set it to 200 ms in TestIntentJudgeRetry_ExpiredTaskCtx_SkipsReinvocation; the assertion stays the same (no re-invocation after the task context expired).
2. Add `if testing.Short() { t.Skip("end-to-end: real git/gh, skipped under -short") }` at the top of the ten tests listed above, and to any other executor test whose measured time exceeds 3 s (run `go test -json ./internal/executor/` once and list them in the PR body).
3. In dispatcher_test.go replace sleeps that wait for a goroutine with a channel or a polling wait bounded by the test context where the test logic allows it; leave the sleeps that model real backoff but cap them with -short (shorter durations under testing.Short()).
4. Add `t.Parallel()` to the pure-function test files in internal/executor that share no temp repo, env or global (start with the parser, classifier and prompt-builder tests); do not add it to anything that sets env, chdir or PATH.
5. Report before/after: `go test -short ./internal/executor/` and `go test -short ./internal/memory/` wall time without -race, pasted in the PR body.

## Acceptance

- TestIntentJudgeRetry_ExpiredTaskCtx_SkipsReinvocation completes in under 2 s.
- `go test -short ./internal/executor/` completes in under 120 s on the CI runner; paste the output into the PR body.
- `go test -race ./internal/executor/ -run TestIntentJudgeRetry_ExpiredTaskCtx` passes; paste the output into the PR body.
- The full suite (`go test -race ./...`, as CI runs it) still runs every guarded test when -short is absent.
- restore the hard-coded 60 s timeout in the mock judge test -> TestIntentJudgeRetry_ExpiredTaskCtx_SkipsReinvocation_FastTimeout fails

## Out of scope

- The scoped gate (G2) and the box config.
- Making internal/memory faster under -race (the box drops -race; CI keeps it).

## Refs

- .agent/tasks/TASK-616-box-quality-gate-speed.md

### LEG G2 — repo: qf-studio/pilot — title: feat(quality): changed-package test gate — the gate runner computes the Go packages a diff affects and exports them, make test-changed runs only those, full suite on any blind spot
probe: present qf-studio/pilot cmd.Dir = r.projectDir
probe: present qf-studio/pilot resolveMergeBaseSHA\(
probe: absent  qf-studio/pilot func ChangedGoPackages\(

## Context

Depends on #5924 (it lands the -short guards the scoped run relies on). The quality gate runner in `internal/quality/runner.go` executes each gate as sh -c in the task worktree and inherits the daemon environment; nothing tells a gate which files or packages the task changed, and no code in the repo computes a package set from a diff (the only diff classifier is `scripts/pre-push-classify.sh`, docs-only versus code; `internal/executor/git.go` has resolveMergeBaseSHA and ChangedFilesAgainstOrigin for file lists). The box therefore runs the whole 61-package suite on every task, 22–23 minutes, while CI reruns it in 10–12. A change in a leaf package affects itself and a handful of dependents; a change in internal/executor affects 26 of 60 packages, internal/memory 30 of 60, measured on the real import graph with go list. Evidence reuse in `internal/executor/acceptance_evidence_run.go` indexes gate output by the gate's argv, so a scoped run must have its own command. CI keeps the full `go test -v -race ./...` as the merge gate of record.

## Implementation

1. New file internal/quality/scope.go: ChangedGoPackages(ctx, dir, baseBranch) returns the affected import paths, a full flag and a reason. Diff = union of git diff --name-only merge-base...HEAD (merge-base from origin/base, then local base, as resolveMergeBaseSHA does), git diff --name-only HEAD, and untracked files from git ls-files -o --exclude-standard. Package graph from go list -f with ImportPath, Imports, TestImports and XTestImports over ./... in the worktree.
2. Affected set: a package is affected when it changed, or when the production-import closure of its Imports plus one hop of its TestImports and XTestImports contains a changed package. Test imports are one hop, never transitive. A non-Go file under a package directory marks that package changed. A test file that contains a relative-path literal into a sibling directory (a quoted string beginning with ../ followed by a directory that holds a package) adds an edge from that directory's package to the test's package.
3. Full-suite fallbacks (full = true with the reason logged): any change under go.mod, go.sum, Makefile, scripts, .github, configs, .golangci.yml or any path outside a Go package directory except .agent and docs; any git or go list error; no merge base; empty base branch. A diff whose every path is under .agent or docs yields an empty set with reason docs-only, and the test gate passes without running. When the set covers more than 70 percent of packages, run ./... instead of listing packages.
4. Plumb the base branch: add BaseBranch to the quality executor config (`internal/quality/executor.go`, `internal/quality/types.go`), set from the project's BranchFrom in `cmd/pilot/adapters.go` where the quality checker factory is built.
5. In RunAll compute the scope once and reuse it across the gate's retries; in executeCommand set cmd.Env to the daemon environment plus PILOT_CHANGED_PACKAGES (space-separated import paths, empty when full) and PILOT_CHANGED_FULL (0 or 1), and log one Info line per gate run with the package count, full flag and reason.
6. Makefile target test-changed: when PILOT_CHANGED_FULL is 1 or the package list is empty and the reason is not docs-only, run go test -short ./...; when docs-only, print "no Go changes" and exit 0; otherwise run go test -short on the listed packages. No -race on this target (CI keeps it).
7. Evidence reuse: a gate whose argv is make test-changed must never satisfy an evidence bullet that names make test; add the pin.
8. Document the gate in `configs/pilot.example.yaml` next to the quality block: how to switch the test gate's command to make test-changed, and that CI remains the full gate.
9. Tests, table-driven, with temp Go modules built in t.TempDir() (a tiny multi-package module with one changed leaf, one shared low-level package, one test-only import, one relative-path reader, one go.mod change, one docs-only diff).

## Acceptance

- A diff that changes only a leaf package yields that package plus its dependents, and the exported PILOT_CHANGED_PACKAGES lists exactly them.
- A diff that changes go.mod yields full = 1.
- A docs-only diff yields an empty set with reason docs-only, and make test-changed exits 0 without running go test.
- A test file that reads ../memory/store.go makes a memory change mark the executor package affected.
- `go test -race ./internal/quality/ -run TestChangedGoPackages` passes; paste the output into the PR body.
- `go test -race ./internal/quality/ -run TestRunner_ExecuteCommandExportsChangedPackagesEnv` passes; paste the output into the PR body.
- `go test -race ./internal/executor/ -run TestEvidenceGateOutputs_TestChangedDoesNotSatisfyMakeTestBullet` passes; paste the output into the PR body.
- treat TestImports as transitive edges -> TestChangedGoPackages_TestImportsAreOneHopNotTransitive fails
- drop the go.mod rule -> TestChangedGoPackages_GoModChangeForcesFull fails
- skip the relative-path scan -> TestChangedGoPackages_RelativePathReadInTestAddsEdge fails
- recompute the scope on a retry -> TestRunner_ChangedPackagesComputedOncePerRunAcrossRetries fails

## Out of scope

- Changing CI jobs or the autopilot required-checks logic.
- The box config switch itself (operator step below).
- Parallel gates (TASK-289 cache race) and the box size.

## Refs

- .agent/tasks/TASK-616-box-quality-gate-speed.md
- .agent/tasks/archive/TASK-393-throughput-acceleration.md

### LEG G3 — repo: qf-studio/pilot — title: fix(quality): changed-package scope accepts the last go list line — whole-output TrimSpace drops the trailing empty test columns and every box run falls back to the full suite
probe: present qf-studio/pilot func listPackages\(
probe: present qf-studio/pilot go list: unexpected line
probe: absent  qf-studio/pilot func TestListPackages_LastLineEmptyTrailingColumns\(

## Context

Since the changed-package gate landed (#5926, v2.298.0) the box test gate is make test-changed, but every run on the box has logged the scope as full. The daemon log for GH-5946 and GH-5947 shows, for each of the build, test and lint gates:

```
msg="Quality gate changed-package scope" gate=test package_count=0 full=true reason="go list: unexpected line \"github.com/qf-studio/pilot/tests/pentest/cmd/pentest\t/tmp/pilot-worktree-GH-5946-…/tests/pentest/cmd/pentest\tcontext flag fmt github.com/qf-studio/pilot/tests/pentest os os/signal path/filepath strings time\""
```

The cause is in listPackages in `internal/quality/scope.go`. The go list template prints five tab-separated columns per package: import path, dir, imports, test imports plus xtest imports, test files plus xtest files. For a package with no test files and no test imports the last two columns are empty, so the line ends in a tab, a space, a tab and a space. The function then calls strings.TrimSpace on the whole output before it splits lines. For the last package in go list order that trim removes the trailing empty columns, the line has three columns instead of five, and the function returns the unexpected-line error. ChangedGoPackages treats any go list error as a full-suite fallback, so the scope never applies. In this repo the last package is tests/pentest/cmd/pentest, which has no tests. The existing tests in `internal/quality/scope_test.go` build temp modules whose last package carries a test file, so the line always has content in the last column and the bug does not show.

## Implementation

1. In listPackages in `internal/quality/scope.go`, stop trimming the whole output. Split the raw output on newline, skip lines that are empty after a trailing carriage return is removed, and keep the five-column check on each line as it is. Do not trim individual lines either: an empty last column is a valid value.
2. Add a package to the test module fixture in `internal/quality/scope_test.go` that sorts last in go list order, has one Go file with no imports and no test file (for example a package named zz with one exported constant). Add TestListPackages_LastLineEmptyTrailingColumns: run listPackages on that module and assert the package is present with empty test imports and empty test files.
3. Run TestChangedGoPackages against the extended fixture and confirm the existing cases still pass with the extra package present (it is unaffected by every diff in the table).
4. In the PR body paste the scope log line from the box quoted above as the before state, and the test output as the after state.

## Acceptance

- `go test -race ./internal/quality/ -run TestListPackages_LastLineEmptyTrailingColumns` passes; paste the output into the PR body.
- `go test -race ./internal/quality/ -run TestChangedGoPackages` passes with the extended fixture; paste the output into the PR body.
- Running listPackages against this repository root returns a package set that includes github.com/qf-studio/pilot/tests/pentest/cmd/pentest with no error.
- restore strings.TrimSpace on the whole go list output -> TestListPackages_LastLineEmptyTrailingColumns fails

## Out of scope

- The scope rules themselves (test-import hops, blind-spot fallbacks) and the Makefile target.
- The box config; it already runs make test-changed.

## Refs

- .agent/tasks/TASK-616-box-quality-gate-speed.md
- G2 → https://github.com/qf-studio/pilot/issues/5926

## Operator steps (box, after the legs merge and the release lands)

1. ✅ Interim APPLIED 2026-10-28 20:02Z (founder "yes, after 5923"): test gate command `go test -short ./...` (no -race; CI keeps it), `export GOFLAGS=-trimpath` in the wrapper (verified in the daemon's /proc environ); backups `config.yaml.bak-gate-20261028`, `start-pilot.sh.bak-gate-20261028`; graceful restart, 1 proc, no 409. GH-5929 (1 min in) and GH-5924 (queued) re-pick from scratch. **Measured (daemon log, Quality gates passed total_time): GH-5924 4m19s · GH-5926 3m54s · GH-5932 2m59s · GH-5934 5m03s — vs 21m37s for GH-5927 (last run under `make test`). Gates 22 min → 3–5 min. Claude 7–15 min per run; CI ~12 min remains the long pole.**
2. ✅ After G2 (PR#5935 merged 10:40Z, in v2.298.0): test gate command `make test-changed`, timeout 10m, max_retries 1 — config edited 11:31Z (backup `config.yaml.bak-gate2-20261029`), loaded by the hot-upgrade config reload at 11:49:43Z. **Measured 10-29 13:55Z (daemon log):** every `Quality gate changed-package scope` line since the reload reads `package_count=0 full=true reason="go list: unexpected line …tests/pentest/cmd/pentest…"` (GH-5946 and GH-5947, all three gates). `make test-changed` therefore ran `go test -short ./...` each time: GH-5940 3m40 (pre-reload) · GH-5943 3m31 (pre-reload) · GH-5946 3m08 · GH-5947 5m17 (all_passed=false, gate-level retry) → 4m21. Parser bug → G3 → PR#5953 merged 14:47Z, box on v2.299.0 since 15:08Z. **Re-measured 15:46Z on GH-5956 (TASK-620 L2, executor + gateway diff): build/test/lint each `package_count=27 full=false "affected 27 of 61 packages"`, total_time 3m08.** Same wall as the full -short suite because internal/executor is in scope and is the long pole; a leaf-package diff is the case that should drop under 1 min — measure one when it comes. GH-5958 (executor + gateway fix): gates 2m47.
3. Record before/after gate wall times in this doc. Target: non-executor diffs under 3 min, executor diffs under 6 min.
4. Separately: the Go build cache is 36 GB on the data volume; add it to the disk watch (pitfall go-caches-on-root-volume-fill-disk).

## Refs

- G1 → https://github.com/qf-studio/pilot/issues/5924
- G2 → https://github.com/qf-studio/pilot/issues/5926
- G3 → https://github.com/qf-studio/pilot/issues/5952 → PR#5953 merged 2026-10-29 14:47Z
