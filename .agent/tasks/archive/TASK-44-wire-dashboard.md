# TASK-44: Wire Up Dashboard TUI

**Status**: ✅ Complete
**Created**: 2026-01-28
**Priority**: P3 Medium
**Category**: Integration

---

## Context

**Problem**:
`internal/dashboard/` is a complete Bubbletea TUI (~300 lines) but it's **never imported anywhere**. The dashboard was built but never integrated into `pilot start`.

**Goal**:
Wire dashboard into `pilot start` daemon mode for real-time task monitoring.

---

## Current State

### What Exists
- `internal/dashboard/tui.go` - Complete TUI with:
  - Task list with status icons and progress bars
  - Log viewer (toggleable)
  - Keyboard controls (q, l, ↑/↓)
  - Real-time refresh via tick messages
  - `UpdateTasks()` and `AddLog()` message functions

### What's Missing
- Zero imports of `github.com/anthropics/pilot/internal/dashboard`
- No integration with `cmd/pilot/start.go`
- No task state bridging from executor

---

## Implementation Plan

### Phase 1: Add Dashboard Flag to Start Command
```go
// cmd/pilot/start.go
var dashboardFlag bool

func init() {
    startCmd.Flags().BoolVar(&dashboardFlag, "dashboard", false, "Show TUI dashboard")
}
```

### Phase 2: Run Dashboard in Daemon Mode
```go
// cmd/pilot/start.go
if dashboardFlag {
    // Run with TUI
    p := tea.NewProgram(dashboard.NewModel(), tea.WithAltScreen())

    // Bridge executor events to dashboard
    go func() {
        for event := range executor.Events() {
            p.Send(dashboard.UpdateTasks(convertToDisplay(event)))
        }
    }()

    p.Run()
} else {
    // Existing headless mode
    server.Start()
}
```

### Phase 3: Event Bridge
Create channel from executor to dashboard:
```go
// internal/executor/events.go
type ExecutorEvent struct {
    TaskID   string
    Status   string
    Phase    string
    Progress int
    Log      string
}

func (r *Runner) Events() <-chan ExecutorEvent
```

---

## Files to Modify

| File | Change |
|------|--------|
| `cmd/pilot/start.go` | Add --dashboard flag, TUI mode |
| `internal/executor/runner.go` | Add event channel for TUI updates |
| `internal/dashboard/tui.go` | Minor tweaks if needed |

---

## UX

```
$ pilot start --dashboard

🚀 Pilot Dashboard

╭─────────────────────────────────────────────────────────────╮
│ 📋 Tasks                                                     │
│ ────────────────────────────────────────────────────────────│
│ ▶ ● TASK-123 Add user auth       [████████░░░░] 65%  2m    │
│   ○ TASK-124 Fix login bug       [░░░░░░░░░░░░]  0%        │
╰─────────────────────────────────────────────────────────────╯

q: quit • l: toggle logs • ↑/↓: select task
```

---

## Acceptance Criteria

- [x] `pilot start --dashboard` launches TUI
- [x] Tasks appear in real-time as webhooks arrive
- [x] Progress updates live during execution
- [x] Logs show Claude Code output
- [x] Keyboard controls work (q, l, arrows)

## Implementation Notes

### Changes Made
1. **cmd/pilot/main.go**: Added `--dashboard` flag to `newStartCmd()` and `runDashboardMode()` function
2. **internal/pilot/pilot.go**: Added `OnProgress()` and `GetTaskStates()` methods
3. **internal/orchestrator/orchestrator.go**: Added `progressCallback` field and `OnProgress()` method

### Event Bridge
- Progress events from executor → orchestrator → pilot → dashboard TUI
- Periodic refresh (2s) ensures missed updates are caught
- Logs display progress messages with task ID, phase, message, and percentage
