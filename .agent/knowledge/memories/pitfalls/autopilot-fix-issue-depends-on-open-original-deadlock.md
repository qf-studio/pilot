# Pitfall: Autopilot CI-fix issues deadlock: 'Depends on: #original' while the original stays open (pilot-superseded)

## Summary
When a PR fails CI, autopilot closes the PR, creates a fix issue whose body says 'Depends on: #<original>', and leaves the original issue OPEN with only the pilot-superseded label. The SDK poller (studio-sdk v0.38.0 poller.go hasPendingDependencies) skips any candidate with an open dependency — at Debug level, so the daemon log shows nothing. Observed 2026-09-06: #5322 and #5325 sat undispatched for 2h+ with zero log lines; unblocked by closing #5317/#5319 by hand.

## Context
TASK-494 (#5315) deletion children failed CI; both auto-fix issues never ran. Diagnosed via daemon.log (no task_id=GH-5322 lines at all) + poller.go source.

## Details
Symptom signature: pilot+autopilot-fix issue open, no 'Dispatching issue' or 'Task held' log line ever, original issue open with pilot-superseded. Diagnose: gh issue view <orig> --json state. Unblock: close the original (its branch lives on under the fix issue). Durable fix filed as a pilot bug (feedback loop must not emit a dependency on an issue its own flow never closes, or the closed-PR flow must close the original).

## Recommended Approach
If an autopilot-fix issue is silent for more than one poll interval, check the state of every '#N' after 'Depends on:' in its body before anything else.

## Related
- TASK-494
- `internal/autopilot/feedback_loop.go`

---
**Captured**: 2026-09-06
**Confidence**: 95%
**Concepts**: autopilot, feedback-loop, dispatch, ci, debugging
