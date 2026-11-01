# TASK-628: Quality gate — an absent make target falls back to the gate's default target, and "No rule to make target" never reaches Claude as a code failure

**Status**: ✅ SHIPPED 2026-11-01 — #6002 → PR#6003 merged 18:43Z (v2.308 train); fast-follow #6005 → PR#6006 merged 19:08Z. Memories written (pitfall + pattern, see Refs).
**Origin**: TASK-627 S1 run (sdk#152, 2026-11-01): the global test gate `make test-changed` failed on a repo without that target; the Claude retry rewrote the SDK Makefile (dropped the `test` recipe, defined `test-changed` twice) and the damage shipped in sdk PR#153 → post-merge fix round sdk#154/PR#155. Box workaround in place since 16:01Z: a per-project `quality:` block for studio-sdk. Same gap today for auth-service, Navigator, ai-coding-summit and every Pilot Cloud customer repo.
**Research**: navigator-research 2026-11-01 (verified anchors below).

## Why it happens (file:line, origin/main `c6fad0050`)

1. `cmd/pilot/adapters.go:208-225` resolves gates per project (`project_override` log); `quality.ResolveConfig` (`internal/quality/types.go:285`) takes the project `quality:` block whole, else the global block. No per-gate merge.
2. `runGate` (`internal/quality/runner.go:198`) → `resolveCommand` (:314) only substitutes when `make` itself is missing from PATH (GH-5622); with `make` present the configured command runs as is. `executeCommand` (:359) runs `sh -c`. GNU make exits **2** with `No rule to make target 'test-changed'.  Stop.`
3. Only exit **127** is special (:285-291 → `ErrGateRunnerMissing`, `Result.RunnerMissing()` `types.go:110`). Exit 2 is "command exited with code 2": gate-level retries re-run the same missing target, `ShouldRetry` (:534) returns true, `FormatErrorFeedback` (:459) pastes the raw make line, and `buildRetryPrompt` (`internal/executor/prompt_builder.go:567`) tells Claude the previous approach FAILED and to implement a different solution → Claude edits the Makefile.
4. `hasMakefileTarget(path, target)` (`types.go:413`) already exists (line-prefix scan, no make execution); `DetectTestCommand` (:368) uses it to prefer `make test`. `nativeFallbackCommand` (:346) → `DetectBuildCommand` (:327) / `detectNativeTestCommand` (:378).

## Legs

### LEG Q1 — repo: qf-studio/pilot — title: fix(quality): a make gate whose target is absent from the Makefile falls back to the gate type's default target or the native command, and "No rule to make target" is a runner failure that never re-invokes Claude

## Context

On 2026-11-01 the founder box ran sdk#152 with the global test gate `make test-changed`. The studio-sdk Makefile has no `test-changed` target. `make` exited 2 with `No rule to make target 'test-changed'.  Stop.`; the runner treated it like a red test, the executor re-invoked Claude with the raw make line under "Your PREVIOUS approach FAILED", and Claude rewrote the Makefile (the `test` recipe vanished, `test-changed` was defined twice). The damage merged in sdk PR#153 and needed a post-merge fix round. Today only exit 127 (`ErrGateRunnerMissing`, `internal/quality/runner.go` around line 285) is treated as not Claude-fixable; `resolveCommand` in the same file substitutes a native command only when `make` is absent from PATH (GH-5622). `hasMakefileTarget` in `internal/quality/types.go` already scans a Makefile for a `target:` line and is used by `DetectTestCommand`.

## Scope

1. `resolveCommand`: when the command's first word is `make` and `make` is on PATH, extract the target — skip leading `-flag` words and `NAME=value` words, take the first remaining word; a command with no target word (bare `make`) is left unchanged. If `projectDir/Makefile` exists and `hasMakefileTarget` reports the target absent: use the gate type's default target when present (`test` for `GateTest`, `build` for `GateBuild`, via `hasMakefileTarget`), else `nativeFallbackCommand`, else keep the configured command. Keep flags and `NAME=value` words in the substituted make command. Log once per gate through the existing `fallbackWarned` map: WARN `configured make target absent; running fallback for quality gate` with `gate`, `configured_command`, `fallback_command`, `target`. A target that is present, a command that does not start with `make`, a missing Makefile, or a lint gate with no default leaves the command unchanged. Never run `make -n` or `make -q` to probe (both expand `$(shell …)` and recursive `$(MAKE)` lines).
2. `runGate`: next to the exit-127 branch, when the exit code is 2 and the output matches GNU `No rule to make target '<x>'` or BSD `don't know how to make <x>` **and** the line carries no `needed by` (a missing prerequisite file is a real code problem), set `result.Err` to a new sentinel `ErrGateTargetMissing` wrapped with the command and target, set `result.Error` and `StatusFailed`, report progress, and break out of the gate-level retry loop as the 127 branch does.
3. `Result.RunnerMissing()` returns true for `ErrGateTargetMissing` as well, so `RunnerMissingOnly`, `ShouldRetry`, `QualityGateDetail.RunnerMissing` and the failure headline need no new plumbing. Adjust the headline text in `internal/executor/quality.go` so it reads "command not found or make target absent" instead of claiming a tool is not installed.
4. `configs/pilot.example.yaml`: one sentence under the GH-5926 comment: a repo without `test-changed` falls back to `make test`, then the native test command.

## Acceptance

- Temp Makefile with only `test:`, gate `make test-changed` of type test, `make` on PATH -> `resolveCommand` returns `make test`; with `make -j4 test-changed` -> `make -j4 test`; with no `test:` and a `go.mod` -> `go test ./...`; target present -> unchanged; lint gate `make lint-changed` with no `lint:` -> unchanged; the WARN line appears once across two calls for the same gate.
- `runGate` with a stub command that prints `make: *** No rule to make target 'test-changed'.  Stop.` and exits 2 -> `Result.Err` wraps `ErrGateTargetMissing`, exactly one attempt runs with `MaxRetries: 2`, `ShouldRetry` returns false; the same with `make: don't know how to make test-changed. Stop in /x` -> same; with `No rule to make target 'foo.c', needed by 'test'` and exit 2 -> ordinary failure, retries run, `ShouldRetry` true.
- `go test -race ./internal/quality/ ./internal/executor/ -run 'TestResolveCommand|TestRunGate|TestShouldRetry|TestRunnerMissingOnly|TestQualityOutcomeFields'` passes; paste the output into the PR body.
- Mutation: restore the early `return gate.Command` when `make` is on PATH -> the `make test-changed` -> `make test` row fails.
- Mutation: drop the `needed by` exclusion -> the prerequisite-file row fails.
- Tests live in the existing tables: extend `TestResolveCommand_MakeFallback` and `TestShouldRetry_RunnerMissing` in `internal/quality/gh5622_test.go`, add `TestResolveCommand_MissingMakeTarget`, `TestRunGate_NoRuleToMakeTargetIsRunnerMissingAndNotRetried`, `TestRunGate_NoRuleNeededByStillRetries` next to `TestRunGate_Exit127IsRunnerMissingAndNotRetried`, using the `writeProjectFiles` and `newTestRunnerWithLog` helpers.

## Out of scope

Parsing `include`d Makefiles, pattern rules or multi-target lines (a false "absent" must not replace a valid command: when `hasMakefileTarget` cannot see the target but the Makefile has an `include` line, keep the configured command); commands that do not start with `make` (`cd x && make y`); changing `buildRetryPrompt`; per-gate merge of project and global config.

probe: present qf-studio/pilot func hasMakefileTarget\(path, target string\) bool
probe: present qf-studio/pilot ErrGateRunnerMissing = errors\.New
probe: present qf-studio/pilot func TestRunGate_Exit127IsRunnerMissingAndNotRetried
probe: absent qf-studio/pilot ErrGateTargetMissing

## Refs
- TASK-627 (incident, box workaround, S1/P1)
- Memories to write after merge: pitfall "gate exit 2 No rule to make target was a Claude-fixable failure" · pattern "hasMakefileTarget is the probe; never make -n/-q"
- GH-5622 (make-missing fallback), GH-5926 (changed-package scope), TASK-616

## Refs

- Q1 → https://github.com/qf-studio/pilot/issues/6002
- Q1 PR → https://github.com/qf-studio/pilot/pull/6003 (merged 2026-11-01 18:43Z, 4cd9725e8; Navigator review APPROVE-w-notes posted as COMMENTED, 4 minor)
- Fast-follow (notes 1+2: executor headline still says exit 127 · `-f`/`-C`/`-j N` value word parsed as target) → https://github.com/qf-studio/pilot/issues/6005 (`pilot`+`no-decompose`, filed 2026-11-01 18:45Z) → **PR#6006 merged 19:08:27Z** (pre-PR shadow APPROVE-WITH-NOTES; 23 min file→merge). **Task complete — archive with TASK-627.**
