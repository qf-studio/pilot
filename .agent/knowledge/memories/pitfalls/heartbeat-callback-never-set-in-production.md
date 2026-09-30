# Pitfall: HeartbeatCallback is never set in production, so heartbeat_timeout has never been emitted

## Summary
ExecuteOptions.HeartbeatCallback is defined (backend.go) and invoked by the Claude Code backend's heartbeat monitor right before it SIGKILLs a hung process group, but no production code sets it: the main backendExecute call in runner.go sets only WatchdogCallback. Result: the documented heartbeat_timeout alert has never been emitted (found 2026-09-30 reviewing PR#5465; fix spec in #5498). A killed task still surfaces as task_failed with error type shutdown_terminated.

## Context
2026-09-30, reviewing PR#5465 (idle deadlock re-fire). The PR body claimed `heartbeat_timeout` covers a hung executor; nav-research showed the type is declared and documented but has no emitter.

## Details
ExecuteOptions.HeartbeatCallback is defined (backend.go) and invoked by the Claude Code backend's heartbeat monitor right before it SIGKILLs a hung process group, but no production code sets it: the main backendExecute call in runner.go sets only WatchdogCallback. Result: the documented heartbeat_timeout alert has never been emitted (found 2026-09-30 reviewing PR#5465; fix spec in #5498). A killed task still surfaces as task_failed with error type shutdown_terminated.

## Recommended Approach
Set `HeartbeatCallback` at the main `backendExecute` call in `runner.go` next to `WatchdogCallback` (and the retry / self-review sites), emit `AlertEventTypeHeartbeatTimeout`, and add the engine case. Spec: #5498. Do not string-match "after Pilot heartbeat timeout" from the failure result; the callback is the clean seam. Callback fires before the kill, so a failed kill still alerts.

## Related
- [[alerts-engine-drops-events-without-handleevent-case]]
- [[executor-alert-event-strings-must-equal-alerts-eventtype]]
- #5498
- `internal/executor/backend.go`, `internal/executor/backend_claudecode.go`, `internal/executor/runner.go`

---
**Captured**: 2026-09-30
**Confidence**: 90%
**Concepts**: alerts, executor
