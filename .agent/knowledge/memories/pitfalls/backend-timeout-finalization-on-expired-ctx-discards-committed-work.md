# Pitfall: Backend timeout → finalization runs on the expired task ctx → committed work reported as no_op and never pushed (false no-op)

## Summary
When Claude Code uses the full task timeout (1h for complex), every post-backend step — commit count, quality gates, intent judge, self-review, push — reuses the expired task ctx and fails instantly with 'context deadline exceeded'. The commit-count error is ignored and the PR guard reads it as zero commits → status no_op, branch never pushed, task re-queued. Seen 3x on pilot-console GH-263 (2026-09-06) with a complete committed solution in the worktree each time. Mirror of the TASK-460 false-success class. Filed as pilot#5342.

## Context
Diagnosed from daemon.log (all failures stamped the same second as 'Task completed') and the recording's final assistant summary ('committed solution 297183d is correct and complete'); branch pilot/GH-263 absent on GitHub.

## Details
Signature: 'Failed to count commits for no-commit check … context deadline exceeded' + 'Quality gates failed' with empty output + 'could not resolve merge-base' + 'Task ended without success status=no_op error=no_changes'. Root: runner.go ~4296 ignores the count error; epic.go ~2045 PR guard treats unknown as zero; retry re-invocation and self-review start on the dead ctx. Salvage: the last run's worktree under /tmp/pilot-worktree-<task>-* on the box still holds the commits (only the latest survives cleanup); push it as ec2-user and open the PR on the pilot/GH-* branch so autopilot adopts it.

## Recommended Approach
Until #5342 ships: (1) for tasks likely to exceed ~45 min (docker builds, full suites), put 'Execution limits' in the issue body — no local docker, targeted tests only, commit + push early; (2) when a run ends no_op right at the timeout, check the worktree before re-queuing; (3) treat 'no_op at exactly timeout' as a bug signal, not a Claude failure.

## Related
- TASK-495
- `internal/executor/runner.go`
- `internal/executor/epic.go`
- `internal/executor/git.go`

---
**Captured**: 2026-09-06
**Confidence**: 95%
**Concepts**: executor, timeout, dispatch, false-success, debugging
