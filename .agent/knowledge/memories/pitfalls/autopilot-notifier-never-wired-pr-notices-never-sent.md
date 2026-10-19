# Pitfall: The autopilot PR lifecycle Notifier has no production caller: merged, CI failed, approval required, fix issue and released notices are never sent

## Summary
Controller.SetNotifier has no caller in cmd/ or internal/ and NewTelegramNotifier is never constructed (never wired since it was written). Every c.notifier call is a silent nil-skip; notify_on_release (default true) has no effect. The ops reference doc claimed Telegram receives these notices - false.

## Context
nav-research outbound mapping 2026-10-19; git log -S 'SetNotifier(' only finds the Discord handler wiring (PR #2137).

## Details
Alerts (alerts engine) and task lifecycle (orchestrator Slack notifier, gateway path) are separate and do send. Fixed by #5759 (cmd/pilot/autopilot_notifier.go wireLifecycleNotifier) behind an opt-in key orchestrator.autopilot.lifecycle_notifications (default off) - TASK-576 leg D.

## Recommended Approach
Before writing a notification claim into a reference doc, grep for the setter's production caller. An interface field that is nil-checked at every use site hides an unwired dependency.

## Related
- TASK-576
- `internal/autopilot/controller.go`
- `internal/autopilot/telegram_notifier.go`

---
**Captured**: 2026-10-19
**Confidence**: 90%
**Concepts**: autopilot, alerts
