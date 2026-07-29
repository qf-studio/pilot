# Pitfall: core.bare=true in shared repo config silently breaks git status and go VCS stamping everywhere

## Summary
A stray core.bare=true in .git/config made 'git status' fatal ('must be run in a work tree') in the repo root AND all linked worktrees, which cascaded into go build/golangci-lint/go test failures via 'error obtaining VCS status: exit status 128' (go -buildvcs runs git status). The pre-push gate failed 4/6 checks with misleading per-check errors; none named the real cause.

## Context
2026-07-28, while manually rebasing pilot/GH-4597 (PR #4603) after a needs-manual-rebase escalation. First gate run failed with VCS-status errors that reproduced on untouched main, proving it environmental. git log/rebase/push still worked — only work-tree-requiring commands (status, submodule) failed, so the repo looked half-healthy.

## Details
Diagnosis chain: gate check output 'error obtaining VCS status: exit status 128' -> reproduce with 'go build -o /dev/null ./cmd/pilot' -> 'git status --porcelain' says 'fatal: this operation must be run in a work tree' -> 'git config --show-origin --get-all core.bare' shows file:.git/config true. Fix: git config core.bare false (the only valid value for a repo with a work tree). Worktrees share the main repo's .git/config, so one bad flag poisons every worktree. Unknown which tool/session set it; session-start gitStatus snapshot worked, so it flipped mid-day.

## Recommended Approach
When git or go builds fail oddly in a worktree, run 'git status' first; if it errors, check 'git config --show-origin --get-all core.bare' before debugging the individual tools. Separately: two pre-existing local gate failures on macOS are known noise — TestResolveMemoryDBPath (/var vs /private/var EvalSymlinks) and the integration check's newTestStartCmd orphan-command false positive (test helper). Both reproduce on clean main; CI (Linux) is the arbiter.

## Related
- `.git/config`
- `scripts/pre-push-gate.sh`
- `scripts/check-integration.sh`

---
**Captured**: 2026-07-28
**Confidence**: 80%
**Concepts**: git, worktree, core.bare, vcs-stamping, pre-push-gate, go-build
