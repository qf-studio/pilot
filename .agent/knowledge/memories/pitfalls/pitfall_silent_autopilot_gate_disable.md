# Pitfall: Silent autopilot gate disable

## Summary
Autopilot can be silently disabled at startup when a gate check fails without emitting a log line, leaving operators unaware that automated execution is off.

## Context
Surfaced during workshop dry-run when autopilot didn't fire despite a valid queue. Patched in v2.149.1 (commit f032689b).

## Details
Startup gates evaluated during autopilot init were short-circuiting to "disabled" without writing a corresponding log entry. Operators saw no error — just no execution. The fix added explicit warning log lines whenever a startup gate trips the disable path so the failure mode is observable.

## Recommended Approach
When user reports "autopilot didn't fire" or "pilot stuck at startup":
1. Grep startup logs for "autopilot disabled" first.
2. If absent, inspect the autopilot init path for gate-evaluation branches that exit without logging.
3. Do NOT pivot to queue/DB state investigation until the startup-gate path is ruled out.

## Related
- v2.149.1 commit f032689b
- mem-pilot-002, mem-pilot-003, mem-pilot-004 (other v2.149.x patterns)

---
**Captured**: 2026-05-26
**Confidence**: 95%
**Concepts**: pilot, executor, debugging, autopilot, gates, silent-failure
