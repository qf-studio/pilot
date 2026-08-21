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

**Update 2026-08-08 — the same incident also poisoned the commit identity.**
The event that wrote core.bare=true also wrote `[user] name = Test User /
email = test@example.com` into the same `.git/config`. Unlike core.bare it
broke nothing, so it survived 10 days: 69 commits on origin/main
(2026-07-29 → 2026-08-07) are authored `Test User <test@example.com>` —
unlinked to any GitHub account, no avatar/contribution credit, discovered
only when the operator noticed "Test User" in their GitHub history. Fixed
by `git config --unset user.name / user.email` (falls back to the correct
global identity; worktrees share the repo config, so one unset fixes all).
History NOT rewritten — 69 commits interleaved with squash merges on a
pushed, autopilot-driven main; author metadata is cosmetic, the push auth
was always the operator's. Lesson: **after any .git/config poisoning
incident, audit the whole file** (`git config --list --show-origin | grep
'file:.git/config'`), not just the key that broke — and note the in-tree
executor tests are properly `-C`-scoped and use `test@test.com`, so the
`test@example.com` signature rules them out as the writer.

**Update 2026-08-21 — ROOT CAUSE SOLVED after 3rd occurrence (GH-5063).**
The writer is the repo's own test suite, run by the pre-push gate. git
exports `GIT_DIR` when invoking hooks; `pre-push-gate.sh` never scrubs it;
and `GIT_DIR` env OVERRIDES cwd/`-C` repo discovery — so the earlier
"-C-scoped, therefore ruled out" reasoning was wrong. Under the hook,
every scratch-repo git call targets the real `.git`: `git init --bare`
tests (runner_git_test.go:28, worktree_test.go:332) write core.bare=true;
`git config user.*` tests write their fake identities (07-28 poison
`Test User`/`test@example.com` = the runGit family; 08-21 poison
`Pilot Test`/`test@pilot.local` = runner_test.go:4059 +
handler_common_test.go:1416 — exact string matches). SIGTERM asymmetry:
a completed suite mostly self-repairs core.bare (later plain `git init`
overwrites false) but nothing overwrites identity keys — hence bare shows
up only on killed pushes while identity survived 10 days. Fix filed in
GH-5063: `unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_OBJECT_DIRECTORY
GIT_COMMON_DIR` at the top of the gate + scrub `GIT_*` in test git
helpers. Recovery recipe unchanged (config repair + full-file audit).
