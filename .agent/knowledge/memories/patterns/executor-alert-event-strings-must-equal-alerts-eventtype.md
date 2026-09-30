# Pattern: executor AlertEventType strings must equal alerts EventType strings (adapter casts blindly)

## Summary
Executor alert events cross into the alerts engine through alerts.NewEngineAdapter, which casts executor.AlertEvent.Type to alerts.EventType by string with no validation. The two constant sets live in different packages (internal/executor/alerts.go vs internal/alerts/engine.go) and drift silently. Pattern: when adding an event type, add it to both packages with the identical string and pin equality in a test (executor package already has TestAlertEventTypes for constant checks).

## Context
2026-09-30, #5498 research. `alerts.NewEngineAdapter` does `EventType(event.Type)` with no validation; the constant sets live in `internal/executor/alerts.go` and `internal/alerts/engine.go`.

## Details
Executor alert events cross into the alerts engine through alerts.NewEngineAdapter, which casts executor.AlertEvent.Type to alerts.EventType by string with no validation. The two constant sets live in different packages (internal/executor/alerts.go vs internal/alerts/engine.go) and drift silently. Pattern: when adding an event type, add it to both packages with the identical string and pin equality in a test (executor package already has TestAlertEventTypes for constant checks).

## Recommended Approach
Add the constant in both packages with the identical string and pin `string(executor.X) == string(alerts.Y)` in a test (mind the import direction; `alerts_test.go` `TestAlertEventTypes` in the executor package is the existing constant check). A typo on either side is a silent drop, not a compile error.

## Related
- [[alerts-engine-drops-events-without-handleevent-case]]
- `internal/alerts/adapter.go`

---
**Captured**: 2026-09-30
**Confidence**: 90%
**Concepts**: alerts, executor, testing
