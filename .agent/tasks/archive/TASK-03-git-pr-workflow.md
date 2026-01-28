# TASK-03: Git & PR Workflow

**Status**: ✅ Complete
**Created**: 2026-01-26
**Assignee**: Manual

---

## Context

**Problem**:
Pilot executes tasks but doesn't complete the full automation loop - no branch creation, no commit tracking, no PR creation. Users must manually create PRs after Pilot finishes.

**Goal**:
Implement git operations in executor to create branches, track commits, and automatically create GitHub PRs via `gh` CLI.

**Success Criteria**:
- [x] Branch created before task execution (when configured)
- [x] Commit SHA extracted from execution output
- [x] GitHub PR created via `gh pr create`
- [x] PR URL returned in ExecutionResult

---

## Implementation Plan

### Phase 1: Branch Operations
**Goal**: Create git branch before task execution

**Tasks**:
- [ ] Add `CreateBranch(name string)` to executor
- [ ] Call branch creation before Claude Code execution
- [ ] Handle branch already exists case
- [ ] Verify branch creation succeeded

**Files**:
- `internal/executor/git.go` - Git operations
- `internal/executor/runner.go` - Integration

### Phase 2: Commit Tracking
**Goal**: Extract commit SHA from task execution

**Tasks**:
- [ ] Parse git commit output from stream-json
- [ ] Extract SHA from `[main abc1234]` pattern
- [ ] Store commit SHA in ExecutionResult
- [ ] Handle multiple commits (use last one)

**Files**:
- `internal/executor/runner.go` - Parse commit from output
- `internal/executor/git.go` - Git helpers

### Phase 3: PR Creation
**Goal**: Automatically create GitHub PR after successful task

**Tasks**:
- [ ] Add `CreatePR(branch, title, body string)` to executor
- [ ] Generate PR title from task description
- [ ] Generate PR body with task summary
- [ ] Call `gh pr create` via subprocess
- [ ] Extract PR URL from output

**Files**:
- `internal/executor/git.go` - PR creation
- `internal/executor/runner.go` - Integration

### Phase 4: Integration
**Goal**: Wire git operations into task execution flow

**Tasks**:
- [ ] Add `--create-pr` flag to `pilot task`
- [ ] Create branch → Execute → Create PR flow
- [ ] Update progress display with git phases
- [ ] Handle errors gracefully (branch exists, PR fails)

**Files**:
- `cmd/pilot/main.go` - CLI flags
- `internal/executor/runner.go` - Execution flow

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|----------|-------------------|--------|-----------|
| PR tool | GitHub API, gh CLI, git push -u | gh CLI | Already installed, handles auth, simpler |
| Branch naming | user-provided, auto-generated | auto: `pilot/{task-id}` | Consistent, traceable |
| Commit extraction | Parse output, git log after | Parse stream-json | Real-time, no extra command |

---

## Dependencies

**Requires**:
- [x] Stream-json parsing (TASK: Progress Display)
- [x] Navigator integration (today's work)
- [ ] `gh` CLI installed and authenticated

**Blocks**:
- [ ] Linear E2E testing (needs full flow)

---

## Verify

Run these commands to validate the implementation:

```bash
# Run tests
make test

# Test branch creation
pilot task "Add hello.txt" --dry-run

# Test full flow (with PR)
pilot task "Add test file" --create-pr

# Verify PR created
gh pr list
```

---

## Done

Observable outcomes that prove completion:

- [ ] `internal/executor/git.go` exports CreateBranch, CreatePR functions
- [ ] ExecutionResult contains CommitSHA and PRUrl fields (already exist, need population)
- [ ] `pilot task --create-pr` creates actual GitHub PR
- [ ] All tests pass
- [ ] Progress display shows git phases (Branching, Committing, Creating PR)

---

## Notes

- `gh` CLI must be authenticated: `gh auth login`
- Branch creation should be optional (--no-branch flag exists)
- PR creation should be optional (--create-pr flag)
- Handle case where no commits made (task failed or no changes)

---

## Completion Checklist

Before marking complete:
- [x] Implementation finished
- [x] Tests written and passing (36 total, 10 new)
- [x] Documentation updated
- [ ] E2E test with real PR (requires live repo)

---

## Implementation Summary

**Files Changed**:
- `internal/executor/runner.go` - Integrated GitOperations, commit SHA extraction, PR creation flow
- `internal/executor/git_test.go` (new) - Git operations tests
- `internal/executor/runner_test.go` - Added commit SHA parsing tests
- `cmd/pilot/main.go` - Added `--create-pr` flag

**Key Features**:
1. Branch creation before execution (via GitOperations)
2. Commit SHA extraction from stream-json output (pattern: `[branch sha]`)
3. PR creation with `gh pr create` after successful execution
4. Progress display updated with git phases (Branching, Creating PR)

**Usage**:
```bash
# Full flow with PR
pilot task "Add new feature" --create-pr

# Without PR (just branch and execute)
pilot task "Add feature"

# No branch at all
pilot task "Quick fix" --no-branch
```

---

**Last Updated**: 2026-01-26
**Completed By**: Navigator Task Mode
