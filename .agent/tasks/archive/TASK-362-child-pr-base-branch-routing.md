# TASK-362: Decomposed-child PRs created against sibling branches instead of main

**Status**: ✅ **FULLY SHIPPED 2026-06-10** — both halves on main, both released.
- ✅ **#3540 base-branch pin SHIPPED + artifact-verified on main** via PR [#3548](https://github.com/qf-studio/pilot/pull/3548) (base=main, CI green, released as v2.185.1): `runner.go:1429-1436` resolves the repo default branch from `task.ProjectPath` (the real repo, not the worktree) before `CreatePR`; root cause documented in-line — old code called `GetDefaultBranch` *inside the worktree*, susceptible to concurrent execution / stale worktree state (TASK-362 hypothesis 1+2 confirmed). Test: `runner_base_branch_test.go`.
- ✅ **#3541 tag-reachability guard RECOVERED + SHIPPED in v2.186.1** — after being falsely superseded 12:14:29Z (TASK-364 Hole 2 live exposure; no `pilot/GH-3541` branch ever pushed), it was folded into the TASK-363 issue (#3557→#3558, user-approved) and landed via PR [#3559](https://github.com/qf-studio/pilot/pull/3559) (merge `09dcb16e`, artifact-verified): `guardReleaseSHAReachable` in `internal/autopilot/controller.go` refuses to tag a SHA not reachable from the default branch (compare status must be `ahead`/`identical`; fail-open on transient errors; diverged/behind → `StageFailed`, no tag). Tests: `TestHandleReleasing_DivergedSHARefused` (diverged + behind). The phantom-v2.181.0 vector is closed at BOTH ends (base pin + tag guard). ⚠️ Takes effect when the daemon upgrades to ≥v2.186.1.
- Noise: stale autopilot fix-request #3551 (`pilot-needs-clarification`) for dead duplicate PR #3550 (SHA `84b76b4`, never merged) — close as obsolete; main is green at the merged SHA `429e98e4`.
**Priority**: P1 (corrupts releases)
**Origin**: GH-3513 incident follow-up (see TASK-361); discovered 2026-06-10

## Problem

During the GH-3513 epic decomposition, child PR [#3520](https://github.com/qf-studio/pilot/pull/3520) was created with `baseRefName: pilot/GH-3515` — a **sibling child's branch** — instead of `main`. The autopilot then merged it into that sibling branch and tagged the merge commit (`aa3b1e82`) as `v2.181.0`, producing a **phantom release** whose code was never on main (`compare/main...aa3b1e82` → `diverged`, ahead 3 / behind 1).

Verified facts:
- `gh pr view 3520 --json baseRefName` → `pilot/GH-3515` (not main)
- Tag `v2.181.0` → `aa3b1e82`, diverged from main
- The other child PRs (#3519 → `pilot/GH-3515` head, #3523 → `pilot/GH-3517` head) had `main` as base — only #3520 mis-based

## Hypotheses (unverified — investigate first)

1. The executor worktree for child #3516 was created while the repo/worktree state pointed at a sibling's branch, and the PR-creation path derived `base` from the worktree's tracking ref instead of pinning `main` (or the repo's default branch).
2. A race: children executed concurrently; `gh pr create` without an explicit `--base` falls back to the default *or* to the branch the local HEAD forked from.
3. Decomposition dependency wiring (`DependsOn` → branch stacking?) intentionally stacks child branches — if so it's working as designed but the **release tagger must never tag commits not on main**.

## Where to look

- `internal/executor/` PR-creation path — grep `pr create`, `--base`, `baseRefName`, `CreatePullRequest`
- Worktree setup for decomposed children (`pilot-worktree-GH-*` creation) — what ref is checked out / branched from
- `internal/autopilot/releaser.go` + `controller.go` `handleReleasing` — guard: refuse to tag a SHA not reachable from main (cheap check via compare API; this guard is worth adding even if the base bug is fixed)

## Acceptance criteria

- [ ] Root cause identified with file:line
- [ ] Child PRs always base on the repo default branch (explicit `--base`/API field, never inferred)
- [ ] Release path refuses to tag commits not reachable from main (belt-and-suspenders)
- [ ] Test covering the decomposed-children PR-creation path asserts base == default branch

## Cross-refs

- TASK-361 (incident record) · PR #3527 (decomposition guards, shipped v2.183.0)
- Memory: `mem-033` (verify artifact not status — includes the baseRefName check recipe)
