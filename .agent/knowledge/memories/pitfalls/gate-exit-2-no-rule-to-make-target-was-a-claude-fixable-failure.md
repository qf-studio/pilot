# Pitfall: A make gate exit 2 'No rule to make target' was treated as a Claude-fixable code failure and Claude rewrote the Makefile

## Summary
Until #6002 (merged 2026-11-01, v2.308 train) only exit 127 was a runner failure; a global gate 'make test-changed' on a repo without that target exited 2, the retry prompt told Claude the previous approach FAILED, and Claude rewrote the SDK Makefile (test recipe dropped, test-changed defined twice) — shipped in sdk PR#153, fixed by sdk#154/PR#155. Fix: resolveCommand swaps the absent target for the gate type's default (test/build) or the native command; exit 2 + 'No rule to make target' / BSD 'don't know how to make' without 'needed by' -> ErrGateTargetMissing, RunnerMissing() true, no gate retry, no Claude retry.

## Context
TASK-627 S1 run (sdk#152) on the founder box; box workaround was a per-project quality: block for studio-sdk (config.yaml.bak-sdkgates-20261101). Same gap existed for auth-service, Navigator, ai-coding-summit and every customer repo.

## Details
Residuals after #6002: a flag with a separate value word (-f ci.mk, -C sub, -j 4) is parsed as the target and can rewrite a valid command; the executor task-failure headline at internal/executor/runner.go:6574 still says exit 127 — both in fast-follow #6005. Multi-target lines (test test-changed:) still read as absent (out of scope). BSD make prints no 'needed by', so a missing prerequisite on BSD classifies as target-missing (box and tenants run GNU make).

## Recommended Approach
Any new 'config problem, not code' failure class must set a sentinel that Result.RunnerMissing() covers, so RunnerMissingOnly, ShouldRetry and the executor headline need no new plumbing. Check every headline string that branches on outcomeRunnerMissing, not only the per-gate line in internal/executor/quality.go.

## Related
- TASK-628
- TASK-627
- TASK-616
- `internal/quality/runner.go`
- `internal/quality/types.go`
- `internal/executor/quality.go`
- `internal/executor/runner.go`

---
**Captured**: 2026-11-01
**Confidence**: 95%
**Concepts**: quality-gate, executor, retry
