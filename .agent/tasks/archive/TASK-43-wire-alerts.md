# TASK-43: Wire Up Alerts Package

**Status**: ✅ Complete
**Created**: 2026-01-28
**Completed**: 2026-01-28
**Priority**: P2 High
**Category**: Integration

---

## Context

**Problem**:
`internal/alerts/` is a complete alerting engine (400+ lines) with tests passing, but it's **never imported anywhere**. TASK-14 built the package but integration was skipped.

**Goal**:
Wire alerts into executor and gateway so alerts actually fire.

---

## Current State

### What Exists
- `internal/alerts/engine.go` - Event processing, rule evaluation
- `internal/alerts/dispatcher.go` - Multi-channel dispatch (Slack, Telegram, Email, Webhook, PagerDuty)
- `internal/alerts/channels.go` - Channel implementations
- `internal/alerts/config.go` - Config adapter
- `internal/alerts/engine_test.go` - Tests (all passing)
- `internal/config/config.go` - AlertsConfig already defined

### What's Missing
- Zero imports of `github.com/anthropics/pilot/internal/alerts`
- No event emission from executor
- No engine initialization in gateway/server

---

## Implementation Plan

### Phase 1: Initialize Engine in Gateway
```go
// internal/gateway/server.go
import "github.com/anthropics/pilot/internal/alerts"

func NewServer(cfg *config.Config) *Server {
    // ...existing code...

    // Initialize alerts engine
    if cfg.Alerts != nil && cfg.Alerts.Enabled {
        alertEngine := alerts.NewEngine(cfg.Alerts,
            alerts.WithLogger(s.logger),
        )
        s.alertEngine = alertEngine
        go alertEngine.Start(ctx)
    }
}
```

### Phase 2: Emit Events from Executor
```go
// internal/executor/runner.go
func (r *Runner) Execute(ctx context.Context, task *Task) error {
    // On task start
    r.alertEngine.ProcessEvent(alerts.Event{
        Type:      alerts.EventTypeTaskStarted,
        TaskID:    task.ID,
        Timestamp: time.Now(),
    })

    // On task complete
    r.alertEngine.ProcessEvent(alerts.Event{
        Type:      alerts.EventTypeTaskCompleted,
        TaskID:    task.ID,
        Duration:  duration,
        Timestamp: time.Now(),
    })

    // On task failed
    r.alertEngine.ProcessEvent(alerts.Event{
        Type:      alerts.EventTypeTaskFailed,
        TaskID:    task.ID,
        Error:     err.Error(),
        Timestamp: time.Now(),
    })
}
```

### Phase 3: Wire Up Channels
- Initialize Slack channel from existing `adapters/slack`
- Initialize Telegram channel from existing `adapters/telegram`
- Register channels with dispatcher

---

## Files to Modify

| File | Change |
|------|--------|
| `internal/gateway/server.go` | Add alerts engine initialization |
| `internal/executor/runner.go` | Emit events on task lifecycle |
| `internal/executor/types.go` | Add alertEngine field to Runner |

---

## Testing

1. Configure alerts in `~/.pilot/config.yaml`
2. Run `pilot task` with a failing task
3. Verify alert received in Slack/Telegram

---

## Acceptance Criteria

- [x] Alerts engine starts with `pilot start`
- [x] Task failures trigger alerts
- [x] Stuck task detection works
- [x] Consecutive failures escalate to critical
- [x] Cooldowns prevent alert spam

---

## Implementation Summary

### Files Created
- `internal/executor/alerts.go` - AlertEventProcessor interface and event types for executor
- `internal/alerts/adapter.go` - EngineAdapter to bridge executor events to alerts engine

### Files Modified
- `internal/executor/runner.go` - Emit task lifecycle events (started, completed, failed)
- `internal/orchestrator/orchestrator.go` - SetAlertProcessor method to wire alerts to runner
- `internal/pilot/pilot.go` - Initialize alerts engine, register channels (Slack/Telegram/Webhook)

### Architecture
```
Pilot.initAlerts()
  ├─ Creates alerts.AlertConfig from config.AlertsConfig
  ├─ Creates alerts.Dispatcher
  ├─ Registers channels (Slack, Telegram, Webhook)
  ├─ Creates alerts.Engine with dispatcher
  └─ Wires to orchestrator via EngineAdapter

Executor.Runner
  ├─ SetAlertProcessor(AlertEventProcessor)
  └─ emitAlertEvent() on task lifecycle:
       ├─ task_started
       ├─ task_completed
       └─ task_failed
```

### Import Cycle Resolution
To avoid import cycles (alerts → adapters → executor → alerts), we:
1. Define `AlertEventProcessor` interface in executor package
2. Define `AlertEvent` type in executor package (mirrors alerts.Event)
3. Create `EngineAdapter` in alerts package that implements executor.AlertEventProcessor
