# TASK-279: Fix Branch Creation to Fork from Default Branch

**Issue:** [GH-279](https://github.com/alekspetrov/pilot/issues/279)
**Priority:** P1 - Bug affecting all users
**Branch:** `pilot/GH-279`

## Problem

When Pilot creates a new branch, it forks from current HEAD instead of the default branch (main/master). This causes PRs to chain off each other:

```
GH-18 → parent: main ✓
GH-20 → parent: GH-18 tip ✗
GH-21 → parent: GH-20 tip ✗
```

## Root Cause

`runner.go` calls `git.CreateBranch()` without first switching to default branch and pulling latest.

## Solution

### 1. Add `Pull()` method to `git.go`

```go
// Pull pulls latest from origin for current branch
func (g *GitOperations) Pull(ctx context.Context) error {
    cmd := exec.CommandContext(ctx, "git", "pull", "--ff-only", "origin")
    cmd.Dir = g.projectPath
    output, err := cmd.CombinedOutput()
    if err != nil {
        return fmt.Errorf("failed to pull: %w: %s", err, output)
    }
    return nil
}
```

### 2. Add `CreateBranchFromDefault()` convenience method

```go
// CreateBranchFromDefault switches to default branch, pulls latest, then creates new branch
func (g *GitOperations) CreateBranchFromDefault(ctx context.Context, branchName string) error {
    // Get default branch
    defaultBranch, err := g.GetDefaultBranch(ctx)
    if err != nil {
        return fmt.Errorf("failed to get default branch: %w", err)
    }

    // Switch to default branch
    if err := g.SwitchBranch(ctx, defaultBranch); err != nil {
        return fmt.Errorf("failed to switch to %s: %w", defaultBranch, err)
    }

    // Pull latest
    if err := g.Pull(ctx); err != nil {
        // Non-fatal - may be offline or no upstream
        // Log warning but continue
    }

    // Create new branch
    return g.CreateBranch(ctx, branchName)
}
```

### 3. Update `runner.go` to use new method

Replace in `Execute()` (~line 501):
```go
if err := git.CreateBranch(ctx, task.Branch); err != nil {
```

With:
```go
if err := git.CreateBranchFromDefault(ctx, task.Branch); err != nil {
```

Also update `executeDecomposed()` (~line 1132).

### 4. Update tests

Add test for `CreateBranchFromDefault()` in `git_test.go`.

## Files to Modify

- `internal/executor/git.go` - Add `Pull()` and `CreateBranchFromDefault()`
- `internal/executor/runner.go` - Use `CreateBranchFromDefault()` (2 places)
- `internal/executor/git_test.go` - Add tests

## Done

- [ ] `Pull()` method added
- [ ] `CreateBranchFromDefault()` method added
- [ ] `runner.go` updated (both Execute and executeDecomposed)
- [ ] Tests pass
- [ ] PR created
