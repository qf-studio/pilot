# Pitfall: A hand-filed revision/fix issue in the autopilot-fix shape (autopilot-meta branc

## Summary
A hand-filed revision/fix issue in the autopilot-fix shape (autopilot-meta branch:pilot/GH-N) is NOT closed by the daemon when its PR merges — only the original issue closes via the lifted 'Closes #N'. The fix issue stays open with pilot-in-progress (ui#296, console#465, console#474 on 2026-11-02/03). A sibling issue gated with 'Blocked by: #<fix issue>' therefore never dispatches (ui#297 waited on ui#296 until a human closed it). Rule: close fix issues by hand after the PR merges, and never gate on a fix-issue number; gate on the original issue or dispatch after the merge.

## Context
Discovered during development.

## Details
A hand-filed revision/fix issue in the autopilot-fix shape (autopilot-meta branch:pilot/GH-N) is NOT closed by the daemon when its PR merges — only the original issue closes via the lifted 'Closes #N'. The fix issue stays open with pilot-in-progress (ui#296, console#465, console#474 on 2026-11-02/03). A sibling issue gated with 'Blocked by: #<fix issue>' therefore never dispatches (ui#297 waited on ui#296 until a human closed it). Rule: close fix issues by hand after the PR merges, and never gate on a fix-issue number; gate on the original issue or dispatch after the merge.

## Recommended Approach
Apply this knowledge when working on related topics.

## Related
- None documented

---
**Captured**: 2026-11-03
**Confidence**: 90%
**Concepts**: autopilot, dispatch, github, merge
