# TASK-31: Pilot PR Workflow Improvements

**Status**: ✅ Phase 1-3 Complete
**Created**: 2026-01-27
**Completed**: 2026-01-27
**Priority**: High
**Category**: Core Workflow

---

## Context

**Problem** (SOLVED):
The `--create-pr` flag in `pilot task` didn't complete the full PR workflow. After executing TASK-23, Pilot:
- ✅ Created branch
- ✅ Implemented feature
- ✅ Ran tests and lint
- ✅ Committed changes
- ❌ Did NOT push to origin → **FIXED**: Was working, but errors were silent
- ❌ Did NOT create PR → **FIXED**: Was working, but errors were silent

**Root Cause** (FOUND):
`result.Success = true` was set before push/PR creation and never reset to `false` on failure. Errors were silently ignored.

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

### Phase 1: Fix Git Push After Commit ✅ COMPLETE
**Goal**: Ensure branch is pushed to origin after commit

**Actual Finding**: Push method already existed! Issue was silent error handling.

**Fix Applied**:
- [x] `Push()` method already in `internal/executor/git.go`
- [x] Push already called after commit in runner (line 294)
- [x] **FIX**: Set `result.Success = false` when push fails (PR #4)

### Phase 2: Implement PR Creation ✅ COMPLETE
**Goal**: Create PR using gh CLI after push

**Actual Finding**: CreatePR method already existed! Issue was silent error handling.

**Fix Applied**:
- [x] `CreatePR()` method already in `internal/executor/git.go`
- [x] PR creation already wired in runner (line 315)
- [x] **FIX**: Set `result.Success = false` when PR creation fails (PR #4)

### Phase 3: Improve Result Output ✅ COMPLETE
**Goal**: Clean task result with PR URL

**Already Working**:
- [x] `PRUrl` field exists in result struct
- [x] PR URL displayed in success message (line 386-388 in main.go)
- [x] **FIX**: Add warning when `--create-pr` fails silently (PR #4)

**Verified**: PR #5 created automatically via `pilot task --create-pr`

### Phase 4: Progress Display Cleanup (Optional) 📋 PLANNED
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
