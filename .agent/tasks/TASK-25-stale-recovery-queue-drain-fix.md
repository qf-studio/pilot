# TASK-25: Fix Stale Recovery Killing Queued Tasks

**Status**: 📋 Planned
**Created**: 2026-04-17
**Assignee**: Pilot

---

## Context

**Problem**: The dispatcher's `recoverStaleTasks()` runs every 5 minutes and marks all queued tasks older than 5 minutes as failed. When `runner.Execute()` blocks for 10-20 minutes (GLM-5.1, complex tasks), tasks queued behind it age past the `StaleQueuedThreshold` and get nuked — even though the worker is actively processing the queue. The worker eventually finishes, loops back to `GetQueuedTasksForProject()`, finds nothing, and the queued tasks are lost.

**Timeline**:
1. Task A queued → worker starts, `processing=true`, `runner.Execute` blocks (10-20 min)
2. Tasks B, C queued → sit in `queued` status waiting for A to finish
3. 5 minutes pass → `recoverStaleTasks()` runs
4. `GetStaleQueuedExecutions(5min)` returns B and C (no project filter)
5. B and C marked failed — "stale queued task recovered (no worker picked up)"
6. Task A finishes → `processQueue` loops back, `GetQueuedTasksForProject` returns empty
7. B and C are lost

**Goal**: Before marking a stale queued task as failed, check if the project has an active worker currently processing (`processing == true`). If so, skip it — the worker will get to it.

**Success Criteria**:
- [ ] Queued tasks behind a busy worker are NOT marked failed by stale recovery
- [ ] Stale recovery still fails tasks for projects with no worker or idle worker (genuinely stuck)
- [ ] `isWorkerProcessing()` helper uses RLock (no blocking of queue ops)
- [ ] Tests cover active worker, idle worker, and no-worker scenarios
- [ ] Build succeeds, tests pass

---

## Implementation Plan

### Phase 1: Add `isWorkerProcessing` helper
**Goal**: Thread-safe check for active worker processing state.

**Tasks**:
- [ ] Add `isWorkerProcessing(projectPath string) bool` method on `Dispatcher`
- [ ] Uses `d.mu.RLock()` — safe from stale recovery loop
- [ ] Returns `worker.processing.Load()` for project, `false` if no worker

**Files**:
- `internal/executor/dispatcher.go` — Add method near `GetWorkerStatus()` (line ~374)

### Phase 2: Guard stale-queued loop
**Goal**: Skip tasks for projects with actively processing workers.

**Tasks**:
- [ ] In `recoverStaleTasks()` stale-queued loop, add guard after orphan-completed check
- [ ] Log at Debug level when skipping
- [ ] Continue to next task if `isWorkerProcessing()` returns `true`

**Files**:
- `internal/executor/dispatcher.go` — Add guard in loop (line ~211)

### Phase 3: Tests
**Goal**: Verify skip behavior for active/idle/no worker scenarios.

**Tasks**:
- [ ] `TestRecoverStaleTasks_SkipsActiveWorker` — active worker skips, no-worker fails
- [ ] `TestRecoverStaleTasks_IdleWorkerStillFails` — idle worker fails task

**Files**:
- `internal/executor/dispatcher_test.go` — Add 2 new test functions

---

## Technical Decisions

| Decision | Options Considered | Chosen | Reasoning |
|----------|-------------------|--------|-----------|
| Skip mechanism | Increase threshold, worker-aware check, hybrid | Worker-aware check | Targeted fix, keeps 5-min timeout for genuinely stuck tasks |
| Locking | Lock, RLock, atomic-only | RLock + atomic | Safe read without blocking `ensureWorker` |
| Log level | Info, Debug, Warn | Debug | Skip is expected behavior, not a concern |

---

## Edge Cases

| Scenario | `isWorkerProcessing` | Result |
|----------|---------------------|--------|
| Active worker, long task running | `true` | Skip (correct) |
| Worker idle, task stuck in queue | `false` | Fail (correct) |
| No worker for project | `false` | Fail (correct) |
| Worker crashes between check and next tick | `false` on next tick | Fail on next tick (correct) |
| Dispatcher just started, no workers yet | `false` | Fail (correct) |

---

## Files to Modify

| File | Change | Lines |
|------|--------|-------|
| `internal/executor/dispatcher.go` | Add `isWorkerProcessing()` | ~8 lines |
| `internal/executor/dispatcher.go` | Add guard in `recoverStaleTasks()` | ~8 lines |
| `internal/executor/dispatcher_test.go` | `TestRecoverStaleTasks_SkipsActiveWorker` | ~50 lines |
| `internal/executor/dispatcher_test.go` | `TestRecoverStaleTasks_IdleWorkerStillFails` | ~35 lines |

---

## Dependencies

**Requires**:
- None

**Blocks**:
- Reliable long-running task execution (GLM-5.1, complex tasks)

---

## Verify

```bash
go test ./internal/executor/ -run TestRecoverStale -v
go test ./internal/executor/ -v
make build
```

---

## Done

- [ ] `isWorkerProcessing()` method exists and uses RLock
- [ ] Guard added in `recoverStaleTasks()` stale-queued loop
- [ ] `TestRecoverStaleTasks_SkipsActiveWorker` passes
- [ ] `TestRecoverStaleTasks_IdleWorkerStillFails` passes
- [ ] `make build` succeeds
- [ ] All tests pass

---

**Last Updated**: 2026-04-17
