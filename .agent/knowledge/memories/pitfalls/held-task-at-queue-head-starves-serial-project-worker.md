# Pitfall: A prerequisite-held task at the queue head re-occupies the serial project worker every cycle and starves everything behind it

## Summary
Dispatcher: a task held by the referenced-path gate ('Task held: prerequisite not on main') is returned to the queue head; the serial ProjectWorker picks it again next cycle, holds again, and never reaches the runnable tasks queued behind it. 2026-10-01: GH-5566 held (a backticked fake path in my own issue body) → GH-5567/5568/5570 sat queued 54 min with nothing running; only a daemon restart broke the loop. Filed as a pilot bug the same day. Operator unblock: fix the held issue's body (or unlabel it) so the head becomes runnable.

## Context
2026-10-01 12:19–13:13Z box log; found while wiring Linear Invoices.

## Details
Dispatcher: a task held by the referenced-path gate ('Task held: prerequisite not on main') is returned to the queue head; the serial ProjectWorker picks it again next cycle, holds again, and never reaches the runnable tasks queued behind it. 2026-10-01: GH-5566 held (a backticked fake path in my own issue body) → GH-5567/5568/5570 sat queued 54 min with nothing running; only a daemon restart broke the loop. Filed as a pilot bug the same day. Operator unblock: fix the held issue's body (or unlabel it) so the head becomes runnable.

## Recommended Approach
Pre-flight every issue body with ExtractReferencedPaths before labeling; if the board shows one task cycling 'Processing task' + 'Task held' while others stay queued, fix or unlabel the held one immediately.

## Related
- `internal/executor/dispatcher.go`
- `internal/executor/dependency_detector.go`

---
**Captured**: 2026-10-01
**Confidence**: 85%
**Concepts**: dispatcher, queue, starvation, base-presence
