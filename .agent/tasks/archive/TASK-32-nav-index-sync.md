# TASK-32: Navigator Index Auto-Sync

**Status**: 📋 Planned
**Created**: 2026-01-27
**Priority**: High
**Category**: Workflow

---

## Problem

When Pilot completes tasks, it updates individual task files (`.agent/tasks/TASK-XX.md`) but does NOT update `DEVELOPMENT-README.md`. This causes drift:

- Task files show "✅ Complete"
- Index shows "📋 Planned"
- Manual sync required (error-prone, often forgotten)

**Evidence**: TASK-05, TASK-12 through TASK-16 were all implemented but index was stale.

---

## Root Cause

Pilot's workflow:
1. ✅ Execute task
2. ✅ Update task file status
3. ✅ Commit code
4. ❌ **Missing**: Update DEVELOPMENT-README.md index

---

## Solutions

### Option A: Post-Execution Hook (Recommended)
Add to `internal/executor/runner.go`:
- After successful task completion, call `syncNavigatorIndex()`
- Parse all `.agent/tasks/TASK-*.md` files
- Update `DEVELOPMENT-README.md` status entries

**Pros**: Automatic, no human intervention
**Cons**: Requires parsing markdown, fragile

### Option B: Navigator Prompt Enhancement
Update Navigator's task completion instructions to explicitly require index update.

**Pros**: Simple, no code changes
**Cons**: Relies on LLM compliance

### Option C: Makefile Script
Add `make sync-nav-index` that:
```bash
# Scan task files, extract status, update DEVELOPMENT-README.md
```

**Pros**: Can run anytime, CI/CD hook
**Cons**: Manual trigger needed

### Option D: Pre-Commit Hook
Git hook that validates index matches task files.

**Pros**: Catches drift before commit
**Cons**: Can block commits, annoying

---

## Recommendation

**Implement A + C**:
1. Auto-sync after task execution (Option A)
2. `make sync-nav-index` for manual recovery (Option C)
3. CI check that warns on drift (soft enforcement)

---

## Implementation Plan

### Phase 1: Sync Script
- [ ] Create `scripts/sync-nav-index.sh`
- [ ] Parse task file headers (Status, Completed fields)
- [ ] Update DEVELOPMENT-README.md programmatically
- [ ] Add `make sync-nav-index` target

### Phase 2: Post-Execution Hook
- [ ] Add `syncNavigatorIndex()` to runner.go
- [ ] Call after successful task with Navigator project
- [ ] Log sync result

### Phase 3: CI Warning
- [ ] Add GitHub Action to check index freshness
- [ ] Warn (don't fail) on PR if drift detected

---

## Files to Modify

```
scripts/sync-nav-index.sh          NEW
Makefile                           +1 target
internal/executor/runner.go        +syncNavigatorIndex()
.github/workflows/ci.yml           +index check step
```

---

## Acceptance Criteria

- [ ] `make sync-nav-index` syncs task statuses to index
- [ ] Pilot auto-syncs after task completion
- [ ] CI warns on index drift
- [ ] No more manual index updates needed

---

**Last Updated**: 2026-01-27
