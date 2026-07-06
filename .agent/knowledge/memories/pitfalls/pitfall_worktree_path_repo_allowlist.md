# Pitfall: Worktree path equality vs repo allowlist

## Summary
Pilot's ephemeral `/tmp/pilot-worktree-*` checkouts are rejected by a strict repo-allowlist path check because the worktree path does not equal the configured project path; use `git rev-parse --git-common-dir` to resolve the common git dir before comparing.

## Context
Surfaced during workshop dry-run as the second Pilot failure of the session. Patched in v2.149.2 (commit aac6de5f, TASK-286).

## Details
The repo allowlist in `cmd/pilot/repo_allowlist.go:53` compared the runtime working directory against configured project paths via direct equality. Pilot's executor creates worktrees in `/tmp/pilot-worktree-*` (`executor/worktree.go:86`), which never match the project root path string. The fix uses `git rev-parse --git-common-dir` to resolve the worktree back to its common git dir before equality comparison, so worktrees of allowed projects pass the gate.

## Recommended Approach
When a Pilot run fails at allowlist/permission check:
1. Check whether the failing path matches `/tmp/pilot-worktree-*` pattern.
2. If yes, verify `repo_allowlist.go` is using `git-common-dir` resolution, not raw string equality.
3. Do NOT chase config-file or env-var issues until the worktree resolution path is verified.

## Related
- v2.149.2 commit aac6de5f
- TASK-286
- `executor/worktree.go:86`, `cmd/pilot/repo_allowlist.go:53`
- mem-pilot-001, mem-pilot-003, mem-pilot-004 (other v2.149.x patterns)

---
**Captured**: 2026-05-26
**Confidence**: 95%
**Concepts**: pilot, executor, debugging, worktree, allowlist, git
