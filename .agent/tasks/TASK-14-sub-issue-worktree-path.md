# TASK-14: Fix sub-issue worktree path bug (GH-2177)

## Status: COMPLETE

## Problem

When an epic task runs with `use_worktree: true`, sub-issues inherited the parent's
worktree path (`/tmp/pilot-worktree-GH-xxx-...`) as their `ProjectPath`. This caused
three failure modes:

1. **"src refspec does not match any"** — sub-issue branch had no commits because
   `SwitchToDefaultBranchAndPull` fails inside a worktree (can't checkout `main` when
   it's already checked out in the primary repo)
2. **"Claude completed but made no code changes"** — false positive when `CountNewCommits`
   couldn't resolve the base branch ref in the worktree context
3. **"must first push current branch to remote"** — `gh pr create` couldn't resolve the
   GitHub repo from a `/tmp/` worktree path

## Root Cause

`epic.go:621` set `projectPath := executionPath` where `executionPath` was the parent's
worktree. Sub-tasks constructed at line 677 used this worktree path as `ProjectPath`.
When `executeWithOptions(ctx, subTask, false)` ran, `allowWorktree=false` prevented new
worktree creation but the sub-task still operated inside the parent's worktree.

## Fix

Added `repoPath` parameter to `ExecuteSubIssues()`:
- `executionPath` — still used for `gh` CLI commands (issue comments) that need worktree context
- `repoPath` — used for `subTask.ProjectPath` so sub-issues branch from the real repo

### Files Changed

| File | Change |
|------|--------|
| `internal/executor/epic.go:617` | Added `repoPath string` param, sub-tasks use `subTaskRepoPath` |
| `internal/executor/runner.go:1074` | Pass `task.ProjectPath` as `repoPath` |
| `internal/executor/epic_worktree_integration_test.go` | Updated all call sites, fixed assertions |
| `internal/executor/worktree_epic_test.go` | Updated call site, assertions verify real repo path |
| `internal/executor/epic_sequential_test.go` | Updated all call sites with `""` fallback |
| `internal/executor/sub_issue_callback_test.go` | Updated all call sites with `""` fallback |
| `docs/content/features/epic-decomposition.mdx` | Updated function signature |

## Testing

All 20 epic/worktree/sub-issue tests pass. `TestWorktreeEpicIntegration` now confirms
sub-issues receive the real repo path instead of the worktree path.
