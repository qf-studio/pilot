# GH-367: Dashboard State Persistence

**Status:** Planning
**Priority:** P1 - Core functionality broken
**Type:** Bug/Feature Gap

## Problem

Dashboard is **session-only** despite SQLite storage existing. On restart:
- Zero task history
- Zero token usage
- Zero execution data
- Clean slate every time

**SQLite has the data. Dashboard ignores it.**

## Current Architecture (Broken)

```
┌─────────────────┐     ┌─────────────────┐
│  memory/Store   │     │    Dashboard    │
│    (SQLite)     │     │   (In-memory)   │
├─────────────────┤     ├─────────────────┤
│ ✅ executions   │     │ ❌ doesn't load │
│ ✅ patterns     │  X  │ ❌ doesn't save │
│ ✅ projects     │     │ ❌ session only │
└─────────────────┘     └─────────────────┘
        │                       │
        └───── NO CONNECTION ───┘
```

## Target Architecture

```
┌─────────────────┐     ┌─────────────────┐
│  memory/Store   │◄───►│    Dashboard    │
│    (SQLite)     │     │   (Hydrated)    │
├─────────────────┤     ├─────────────────┤
│ executions      │────►│ TaskHistory     │
│ sessions (NEW)  │◄───►│ TokenUsage      │
│ metrics (NEW)   │────►│ SessionMetrics  │
└─────────────────┘     └─────────────────┘
```

## Implementation Plan

### Phase 1: Wire Dashboard to Store

**File:** `internal/dashboard/tui.go`

1. Add Store dependency to Model:
```go
type Model struct {
    store       *memory.Store  // NEW
    taskHistory []TaskDisplay
    // ...
}
```

2. Load historical executions on Init:
```go
func (m Model) Init() tea.Cmd {
    // Load last 20 executions from Store
    executions, _ := m.store.GetRecentExecutions(20)
    for _, e := range executions {
        m.taskHistory = append(m.taskHistory, toTaskDisplay(e))
    }
    return tick()
}
```

### Phase 2: Add Session Table to SQLite

**File:** `internal/memory/store.go`

Add migration:
```sql
CREATE TABLE IF NOT EXISTS sessions (
    id TEXT PRIMARY KEY,
    started_at DATETIME NOT NULL,
    ended_at DATETIME,
    total_input_tokens INTEGER DEFAULT 0,
    total_output_tokens INTEGER DEFAULT 0,
    total_cost_cents INTEGER DEFAULT 0,
    tasks_completed INTEGER DEFAULT 0,
    tasks_failed INTEGER DEFAULT 0
);
```

### Phase 3: Persist Token Usage

**File:** `internal/dashboard/tui.go` or new `internal/dashboard/persistence.go`

1. On task completion, update session record
2. On dashboard exit, flush final counts
3. On dashboard start, load current day's session or create new

### Phase 4: Dashboard Hydration on Startup

**File:** `internal/dashboard/tui.go`

```go
func NewModel(store *memory.Store, opts ...Option) Model {
    m := Model{store: store}

    // Hydrate from store
    if store != nil {
        // Load recent executions
        m.taskHistory = m.loadTaskHistory()

        // Load today's session metrics
        m.session = m.loadOrCreateSession()
        m.totalInputTokens = m.session.TotalInputTokens
        m.totalOutputTokens = m.session.TotalOutputTokens
    }

    return m
}
```

### Phase 5: Hot Upgrade State Transfer (Optional)

For seamless upgrades:
1. Before upgrade: serialize Model state to temp file
2. After upgrade: new binary loads state file
3. Delete temp file

## Files to Modify

| File | Changes |
|------|---------|
| `internal/memory/store.go` | Add sessions table, session CRUD |
| `internal/dashboard/tui.go` | Add Store field, hydration logic |
| `cmd/pilot/start.go` | Pass Store to dashboard |
| `internal/executor/runner.go` | Save execution to Store (verify) |

## Acceptance Criteria

- [ ] Dashboard shows last 20 executions on startup
- [ ] Token usage persists across restarts (daily session)
- [ ] `pilot start --dashboard` shows historical data immediately
- [ ] No data loss on `pilot upgrade`

## Testing

```bash
# Run some tasks
pilot task "test task 1" --project ./test
pilot task "test task 2" --project ./test

# Restart - should show history
pilot start --dashboard
# Expected: Task history shows test task 1, test task 2

# Check SQLite directly
sqlite3 ~/.pilot/data/pilot.db "SELECT * FROM executions ORDER BY created_at DESC LIMIT 5;"
sqlite3 ~/.pilot/data/pilot.db "SELECT * FROM sessions;"
```

## Notes

- Store already saves executions via `SaveExecution()` - verify this is called
- Dashboard Model is already passed around - adding Store is straightforward
- Session table enables future features: daily/weekly reports, cost tracking
