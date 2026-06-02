# TASK-323: Retry executions must run in the worktree, not `task.ProjectPath`

**Wave:** 0 (S) · **MANUAL — do NOT hand to Pilot** (changes the executor's own run/commit path) ·
**Severity:** CRITICAL · **Audit ref:** TASK-322 §critical #1 (`executor-core`)

---

## Problem

The primary execution runs the backend with `ProjectPath: executionPath` — the isolated git worktree
created at `runner.go:1253` — and all post-execution verification (`git.CountNewCommits`, ghost-SHA
guard, push) runs via `NewGitOperations(executionPath)` at `runner.go:1612`, i.e. against the worktree.

But **both retry paths pass `ProjectPath: task.ProjectPath`** instead:
- smart retry — `runner.go:2195-2197`
- no-commit retry — `runner.go:2559-2561`

`task.ProjectPath` is never reassigned to the worktree (confirmed: no `task.ProjectPath = ` anywhere).
When worktrees are enabled (default for any task with `Branch != "" && !DirectCommit` — the same
condition that gates the no-commit retry), this means:

1. **Dangerous:** Claude on retry runs in the original repo working directory; its commits land on
   whatever branch the real repo HEAD is on (or pollute the user's uncommitted tree), defeating the
   GH-936 worktree-isolation guarantee.
2. **Ineffective:** the subsequent `commitCount = git.CountNewCommits(ctx, baseBranch)` (line 2594)
   still inspects the worktree the retry never touched, so it always reports 0 — the no-commit retry
   is structurally incapable of clearing the `no_changes` failure.

So the retries are simultaneously ineffective (never recover) and dangerous (write to the wrong tree).

## Approach

### Step 1 — Point both retries at the worktree (S, ~30 min)
- `runner.go:2197` (smart retry): `ProjectPath: task.ProjectPath` → `ProjectPath: executionPath`
- `runner.go:2561` (no-commit retry): same change
- Confirm `executionPath` is in scope at both call sites (it is — set at line 1253). If a retry path
  runs before `executionPath` is assigned for a non-worktree task, fall back to `task.ProjectPath`
  explicitly so direct-commit mode is unaffected.

### Step 2 — Regression test (S, ~60 min)
- Add a test asserting that when `UseWorktree` is enabled, the retry backend invocation receives the
  worktree path, not `task.ProjectPath`. Use the existing backend-mock seam in the executor tests.
- Negative case: direct-commit (no worktree) retry still uses `task.ProjectPath`.

## Files to modify
- `internal/executor/runner.go` (lines 2197, 2561)
- `internal/executor/runner_test.go` (or the existing retry test file) — new regression test

## Test Strategy
- Unit: retry-uses-worktree-path assertion (both retry branches) + direct-commit negative case.
- Manual smoke: force a `no_changes` retry on a worktree task; confirm the retry now runs in the
  worktree and the commit-count check can see its commits.

## Effort
S (~1.5h). One PR. **Implement by hand via `/nav-loop`.**

## Out of Scope
- The `*PRState` race (TASK-324) — different file, separate manual task.
- Broader retry-policy redesign — only the ProjectPath target changes here.
