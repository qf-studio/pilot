# GH-46: Task Queue with Per-Project Coordination

**Status**: ✅ Completed
**Created**: 2026-01-28
**Completed**: 2026-01-28

---

## What Was Built

Added a task queue system with per-project serialization to prevent concurrent executions on the same project. The system ensures that tasks from multiple sources (GitHub poller, Linear webhooks) are queued and executed one at a time per project, while allowing parallel execution across different projects.

Also added OpenCode as an alternative executor backend with pluggable architecture.

---

## Implementation

### Phase 1: OpenCode Backend (Committed)

**Changes**:
- Created `internal/executor/backend.go` - Backend interface definition
- Created `internal/executor/backend_claudecode.go` - Extracted Claude Code logic
- Created `internal/executor/backend_opencode.go` - OpenCode with SSE streaming
- Created `internal/executor/backend_factory.go` - Factory pattern for backends
- Created `internal/executor/quality.go` - QualityChecker interface
- Modified `internal/executor/runner.go` - Pluggable backend architecture
- Modified `internal/config/config.go` - Executor config with backend type

**Key Design**:
```go
type Backend interface {
    Name() string
    Execute(ctx context.Context, task *Task, callbacks Callbacks) (*ExecutionResult, error)
}
```

### Phase 2: Task Queue Dispatcher

**Changes**:
- Created `internal/executor/dispatcher.go` - Core dispatcher + ProjectWorker
- Created `internal/executor/dispatcher_test.go` - Comprehensive tests
- Modified `internal/memory/store.go` - Queue query methods
- Modified `cmd/pilot/main.go` - Wired GitHub poller to dispatcher

**Key Components**:

1. **Dispatcher** - Manages per-project workers
   - `QueueTask()` - Add task to SQLite queue
   - `WaitForExecution()` - Poll for completion
   - `GetWorkerStatus()` - Monitoring
   - Stale task recovery on startup

2. **ProjectWorker** - Serialized execution per project
   - One worker per project path
   - FIFO task processing
   - Automatic signaling when tasks queued

3. **Store Methods**:
   - `GetQueuedTasksForProject()` - Project-filtered queue query
   - `UpdateExecutionStatus()` - Status updates with error
   - `IsTaskQueued()` - Duplicate prevention
   - `GetStaleRunningExecutions()` - Crash recovery

---

## Technical Decisions

| Decision | Options | Chosen | Reasoning |
|----------|---------|--------|-----------|
| Queue storage | In-memory, Redis, SQLite | SQLite | Already used, persistent across restarts |
| Worker model | Global pool, per-project | Per-project | Prevents git conflicts, natural isolation |
| Signaling | Polling, channels | Buffered channel | Non-blocking, no missed signals |
| CLI behavior | Queue all, direct for CLI | Direct for CLI | User expects synchronous feedback |
| Telegram | Queue, direct | Direct | Already has per-chat mutex, sync feedback |

---

## Files Modified

**New Files**:
- `internal/executor/dispatcher.go` (429 lines)
- `internal/executor/dispatcher_test.go` (516 lines)
- `internal/executor/backend.go` (183 lines)
- `internal/executor/backend_claudecode.go` (226 lines)
- `internal/executor/backend_opencode.go` (429 lines)
- `internal/executor/backend_factory.go` (31 lines)
- `internal/executor/quality.go` (27 lines)
- Tests for all new files (~1,000 lines)

**Modified Files**:
- `internal/memory/store.go` (+136 lines) - Queue methods, task detail columns
- `internal/executor/runner.go` (refactored) - Pluggable backends
- `internal/config/config.go` (+35 lines) - Executor config
- `cmd/pilot/main.go` (+64 lines) - Dispatcher integration

---

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                     TASK SOURCES                             │
│   Telegram │ GitHub Poller │ CLI │ Linear Webhook           │
└──────┬─────┴───────┬───────┴──┬──┴────────┬─────────────────┘
       │             │          │           │
       │             ▼          │           │
       │      ┌──────────────┐  │           │
       │      │  Dispatcher  │  │           │
       │      │ (queued in   │  │           │
       │      │  SQLite)     │  │           │
       │      └──────┬───────┘  │           │
       │             │          │           │
       ▼             ▼          ▼           ▼
┌─────────────────────────────────────────────────────────────┐
│                   EXECUTION                                  │
│  • Direct (CLI, Telegram - synchronous)                     │
│  • Queued (GitHub poller - per-project serialization)       │
└─────────────────────────────────────────────────────────────┘
```

---

## Testing

- ✅ Unit tests: `internal/executor/dispatcher_test.go` (10 tests)
- ✅ Store tests: Queue methods, status updates, stale detection
- ✅ Backend tests: All backend implementations
- ✅ Lint: Clean (`make lint` passes)
- ✅ Build: `go build ./...` passes

---

## Verify

```bash
# Run tests
go test ./internal/executor/... -v

# Run linter
make lint

# Build
go build ./...

# Check queue status (after running)
sqlite3 ~/.pilot/pilot.db "SELECT task_id, project_path, status FROM executions ORDER BY created_at DESC LIMIT 10"
```

---

## Done

- [x] `Dispatcher` manages per-project workers
- [x] `ProjectWorker` executes tasks serially per project
- [x] SQLite queue persists across restarts
- [x] Stale task recovery on startup
- [x] Duplicate task prevention
- [x] GitHub poller uses dispatcher
- [x] CLI/Telegram remain direct (synchronous)
- [x] All tests pass
- [x] Lint clean

---

## Config Example

```yaml
executor:
  type: claude-code  # or "opencode"
  opencode:
    model: "anthropic/claude-sonnet-4"
    server_url: "http://127.0.0.1:4096"
```

---

**Completed**: 2026-01-28
**Implementation Time**: ~4 hours
