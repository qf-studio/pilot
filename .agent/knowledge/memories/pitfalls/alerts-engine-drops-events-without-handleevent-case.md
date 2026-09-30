# Pitfall: alerts.Engine.handleEvent silently drops any event type without a case

## Summary
alerts.Engine.handleEvent is a fixed switch on EventType; executor events with no case are silently dropped, not logged. As of 2026-09-30 task_timeout, watchdog_kill and heartbeat_timeout are emitted (or declared) by the executor but have no engine case, so any rule configured for them never fires. Also declared-but-never-emitted: StagnationWarn/Pause/Abort. When adding an alert type, check BOTH ends: an executor emit site AND a handleEvent case + AlertType rule match. Tracked in #5498 (heartbeat) and #5500 (the other two).

## Context
2026-09-30, mapping the heartbeat_timeout seam for #5498. task_timeout and watchdog_kill are emitted by runner.go and reach the engine, then vanish: no case, no log.

## Details
alerts.Engine.handleEvent is a fixed switch on EventType; executor events with no case are silently dropped, not logged. As of 2026-09-30 task_timeout, watchdog_kill and heartbeat_timeout are emitted (or declared) by the executor but have no engine case, so any rule configured for them never fires. Also declared-but-never-emitted: StagnationWarn/Pause/Abort. When adding an alert type, check BOTH ends: an executor emit site AND a handleEvent case + AlertType rule match. Tracked in #5498 (heartbeat) and #5500 (the other two).

## Recommended Approach
Adding an alert type is a two-ended change: executor emit site AND `handleEvent` case (+ `AlertType` constant, rule match, default rule, docs row). Model event-shaped handlers on `handleEnvClassFailureStreak` (loops rules, Warns when none match), not on the metrics-threshold `AlertTypeDeadlock` case. Filed #5500 for task_timeout / watchdog_kill.

## Related
- [[heartbeat-callback-never-set-in-production]]
- [[executor-alert-event-strings-must-equal-alerts-eventtype]]
- #5498, #5500
- `internal/alerts/engine.go`

---
**Captured**: 2026-09-30
**Confidence**: 90%
**Concepts**: alerts, executor
