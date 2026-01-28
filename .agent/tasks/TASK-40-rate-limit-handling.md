# TASK-40: Rate Limit Handling for Claude Code

**Status**: ✅ Completed
**Priority**: High (P1)
**Created**: 2026-01-28
**Completed**: 2026-01-28

---

## Context

**Problem**:
Pilot executes tasks using Claude Code, which has rate limits. When limit is hit, task fails immediately with no recovery.

**Error example**:
```
You've hit your limit · resets 6am (Europe/Podgorica)
```

**Goal**:
Handle rate limits gracefully with wait-and-retry or queue-for-later.

---

## Design Options

### Option 1: Wait Until Reset (Simple)

Parse reset time from error, sleep until then, retry automatically.

```go
type RateLimitError struct {
    ResetTime time.Time
    Timezone  string
    Message   string
}

func parseRateLimitError(msg string) (*RateLimitError, bool) {
    // Pattern: "resets 6am (Europe/Podgorica)"
    re := regexp.MustCompile(`resets (\d+)(am|pm) \(([^)]+)\)`)
    // Parse and return
}
```

**Pros**: Simple, automatic
**Cons**: Process blocks, can't handle multiple tasks

### Option 2: Queue for Later (Better)

When rate limited:
1. Save task to pending queue with retry_after timestamp
2. Notify user task is queued
3. Background scheduler picks up when limit resets

```go
type PendingTask struct {
    Task       *Task
    RetryAfter time.Time
    Attempts   int
}

func (e *Executor) handleRateLimit(task *Task, err error) {
    if rl, ok := parseRateLimitError(err.Error()); ok {
        e.queue.Add(PendingTask{
            Task:       task,
            RetryAfter: rl.ResetTime,
        })
        e.notify("Task queued, will retry at " + rl.ResetTime.Format("15:04"))
    }
}
```

**Pros**: Non-blocking, handles multiple tasks
**Cons**: More complex, needs persistence

### Option 3: Multi-API Key Pool (Advanced)

Rotate between multiple API keys when one hits limit.

**Pros**: No waiting
**Cons**: Requires multiple accounts, cost management

---

## Recommended: Option 2 with Simple Start

### Phase 1: Parse & Notify
- Parse reset time from error message
- Send Telegram notification with ETA
- Mark task as `pending_retry` in memory

### Phase 2: Background Scheduler
- Check pending queue every minute
- Execute tasks when `RetryAfter` passes
- Max retry attempts (3)

### Phase 3: Persistence
- Save pending queue to SQLite
- Survive restarts

---

## Implementation

### Files to Modify

| File | Change |
|------|--------|
| `internal/executor/runner.go` | Add rate limit detection and handling |
| `internal/executor/queue.go` | New file - pending task queue |
| `internal/executor/scheduler.go` | New file - background retry scheduler |
| `cmd/pilot/main.go` | Start scheduler alongside poller |

### Code Changes

#### runner.go - Detect rate limit

```go
func (e *Executor) Execute(ctx context.Context, task *Task) (*Result, error) {
    result, err := e.run(ctx, task)

    if err != nil {
        if rl, ok := parseRateLimitError(err.Error()); ok {
            return e.handleRateLimit(ctx, task, rl)
        }
    }

    return result, err
}

func parseRateLimitError(msg string) (*RateLimitInfo, bool) {
    // Pattern: "resets 6am (Europe/Podgorica)" or "resets 2:30pm (UTC)"
    patterns := []string{
        `resets (\d{1,2})(am|pm) \(([^)]+)\)`,
        `resets (\d{1,2}):(\d{2})(am|pm) \(([^)]+)\)`,
    }
    // Try each pattern...
}
```

#### queue.go - Task queue

```go
type TaskQueue struct {
    pending []PendingTask
    mu      sync.RWMutex
}

type PendingTask struct {
    Task       *Task
    RetryAfter time.Time
    Attempts   int
    Reason     string
}

func (q *TaskQueue) Add(task *Task, retryAfter time.Time, reason string) {
    q.mu.Lock()
    defer q.mu.Unlock()
    q.pending = append(q.pending, PendingTask{
        Task:       task,
        RetryAfter: retryAfter,
        Attempts:   0,
        Reason:     reason,
    })
}

func (q *TaskQueue) GetReady() []*PendingTask {
    q.mu.Lock()
    defer q.mu.Unlock()

    var ready []*PendingTask
    var remaining []PendingTask

    now := time.Now()
    for _, p := range q.pending {
        if now.After(p.RetryAfter) {
            ready = append(ready, &p)
        } else {
            remaining = append(remaining, p)
        }
    }

    q.pending = remaining
    return ready
}
```

---

## Acceptance Criteria

- [x] Rate limit errors are detected from Claude Code output
- [x] Reset time is parsed correctly (handles am/pm, timezones)
- [x] User notified via Telegram when task is queued
- [x] Task retries automatically when limit resets
- [x] Max 3 retry attempts before giving up
- [ ] `pilot status` shows queued tasks (deferred - requires CLI integration)

---

## Testing

1. Trigger rate limit (or mock the error)
2. Verify notification sent
3. Wait for reset time
4. Verify task executes

---

## Notes

- Reset time format may vary - need to handle edge cases
- Consider timezone differences between server and API
- May need to add buffer (retry 5 min after stated reset)
