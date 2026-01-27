# TASK-31: Pilot PR Workflow Improvements

**Status**: 📋 Planned
**Created**: 2026-01-27
**Priority**: High
**Category**: Core Workflow

---

## Context

**Problem**:
The `--create-pr` flag in `pilot task` doesn't complete the full PR workflow. After executing TASK-23, Pilot:
- ✅ Created branch
- ✅ Implemented feature
- ✅ Ran tests and lint
- ✅ Committed changes
- ❌ Did NOT push to origin
- ❌ Did NOT create PR

User had to manually run `git push` and `gh pr create`.

**Root Cause Analysis**:
The `--create-pr` flag is parsed but the post-commit workflow is incomplete or broken.

**Goal**:
Make `pilot task --create-pr` fully autonomous - from branch creation to PR URL output.

---

## Workflow Issues Identified

### Issue 1: Missing Git Push
**Current**: Commit stays on local branch
**Expected**: Push branch to origin with `-u` flag
**Location**: `internal/executor/runner.go` or `internal/executor/git.go`

### Issue 2: Missing PR Creation
**Current**: No PR created despite `--create-pr` flag
**Expected**: Call `gh pr create` with proper title/body
**Location**: `cmd/pilot/main.go` (task command handler)

### Issue 3: No PR URL in Output
**Current**: Task result doesn't include PR URL
**Expected**: Return PR URL in success message for easy access
**Location**: Task result formatting

### Issue 4: Progress Display Issues
**Current**: Raw JSON streaming, manual output file monitoring
**Expected**: Clean progress bar with real-time updates
**Location**: `internal/executor/progress.go`

---

## Implementation Plan

### Phase 1: Fix Git Push After Commit
**Goal**: Ensure branch is pushed to origin after commit

**Tasks**:
- [ ] Add `PushBranch(ctx, branch)` method to `internal/executor/git.go`
- [ ] Call push after successful commit in runner
- [ ] Handle push failures gracefully (auth, network, etc.)
- [ ] Add `--no-push` flag to skip push if needed

**Files**:
- `internal/executor/git.go` - Add PushBranch method
- `internal/executor/runner.go` - Call push after commit

### Phase 2: Implement PR Creation
**Goal**: Create PR using gh CLI after push

**Tasks**:
- [ ] Add `CreatePR(ctx, title, body, base)` function
- [ ] Generate PR title from task/commit message
- [ ] Generate PR body from implementation summary
- [ ] Parse PR URL from `gh pr create` output
- [ ] Store PR URL in task result

**Files**:
- `internal/executor/git.go` - Add CreatePR method
- `internal/executor/runner.go` - Integrate PR creation
- `cmd/pilot/main.go` - Wire --create-pr flag properly

### Phase 3: Improve Result Output
**Goal**: Clean task result with PR URL

**Tasks**:
- [ ] Add `PRURL` field to task result struct
- [ ] Display PR URL prominently in success message
- [ ] Include PR URL in Telegram/Slack notifications
- [ ] Log PR creation to execution history

**Files**:
- `internal/executor/runner.go` - Result struct
- `internal/adapters/telegram/notifier.go` - PR notification
- `internal/adapters/slack/notifier.go` - PR notification

### Phase 4: Progress Display Cleanup (Optional)
**Goal**: Better real-time progress display

**Tasks**:
- [ ] Filter raw JSON from verbose output
- [ ] Show only progress bar and key events
- [ ] Add `--raw` flag for full JSON output
- [ ] Improve phase detection accuracy

**Files**:
- `internal/executor/progress.go` - Display filtering
- `cmd/pilot/main.go` - Add --raw flag

---

## Technical Decisions

| Decision | Options | Recommendation | Reasoning |
|----------|---------|----------------|-----------|
| PR creation tool | gh CLI, GitHub API | gh CLI | Already available, handles auth, simpler |
| Push timing | Before PR, after PR | Before PR | PR creation needs remote branch |
| PR body source | Commit msg, task doc, AI summary | Commit msg + files changed | Balanced detail without AI cost |
| Error handling | Fail task, warn and continue | Warn and continue | Don't lose work due to PR failure |

---

## Test Plan

**Unit Tests**:
- [ ] `TestPushBranch` - Verify git push execution
- [ ] `TestCreatePR` - Verify gh pr create execution
- [ ] `TestPRURLParsing` - Extract URL from gh output

**Integration Tests**:
- [ ] Full workflow: branch → implement → commit → push → PR
- [ ] Failure scenarios: no gh CLI, no auth, network error

**Manual Tests**:
- [ ] `pilot task "Add hello world" --create-pr` creates PR
- [ ] PR URL appears in output
- [ ] Telegram notification includes PR link

---

## Acceptance Criteria

- [ ] `pilot task --create-pr` pushes branch to origin
- [ ] `pilot task --create-pr` creates GitHub PR
- [ ] PR URL displayed in task completion message
- [ ] PR URL included in Telegram/Slack notifications
- [ ] Graceful handling if gh CLI not available
- [ ] Tests pass for new git operations

---

## Related

**Depends On**:
- TASK-23: GitHub App Integration (provides PR types) ✅

**Enables**:
- TASK-12: Pilot Cloud (needs reliable PR creation)
- Better demo flow for new users

---

## Notes

**Quick Win**: Just fixing push + PR creation covers 80% of the value. Progress display improvements can be Phase 2.

**Risk**: gh CLI requires authentication. Need to handle case where user hasn't run `gh auth login`.

---

**Last Updated**: 2026-01-27
