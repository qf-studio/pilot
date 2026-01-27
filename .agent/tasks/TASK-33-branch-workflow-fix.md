# TASK-33: Fix Branch Workflow - Tasks Commit to Main

## Status: COMPLETE

## Problem

Pilot commits directly to `main` instead of creating a branch and PR.

**Root cause:** `Branch` field not set when creating `executor.Task` in Telegram handler.

**Location:** `internal/adapters/telegram/handler.go` lines 472-478

```go
// Current (broken)
task := &executor.Task{
    ID:          taskID,
    Title:       truncateDescription(description, 50),
    Description: description,
    ProjectPath: h.projectPath,
    Verbose:     false,
    // Branch: NOT SET ← bug
}
```

## Solution

Set `Branch` and `BaseBranch` fields when creating tasks:

```go
task := &executor.Task{
    ID:          taskID,
    Title:       truncateDescription(description, 50),
    Description: description,
    ProjectPath: h.projectPath,
    Verbose:     false,
    Branch:      fmt.Sprintf("pilot/%s", taskID),
    BaseBranch:  "main",
}
```

## Files to Modify

1. `internal/adapters/telegram/handler.go`
   - Line ~472: Add Branch field to task creation
   - Line ~923: Check if there's another task creation that needs fixing

## Acceptance Criteria

- [x] All task creations in handler.go set Branch field
- [x] Branch format: `pilot/{taskID}`
- [x] BaseBranch defaults to "main"
- [x] Existing tests pass
- [x] No direct commits to main from Pilot tasks

## Testing

```bash
make test
```

## Notes

- Question tasks (Q-*) should NOT have branches (read-only)
- Only execution tasks need branches
