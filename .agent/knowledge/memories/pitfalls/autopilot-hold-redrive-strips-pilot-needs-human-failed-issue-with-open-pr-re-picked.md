# Pitfall: A failed issue whose PR is still open gets a second run when any path strips its

## Summary
A failed issue whose PR is still open gets a second run when any path strips its last marker label. 2026-11-03: pilot#6016 failed CI, the CI-fix size guard declined (759 production additions > 200), the daemon stripped pilot-in-progress and added pilot-needs-human. A hand-filed fix issue (#6019) pushed a commit to the PR branch; autopilot's redriveHeldPR re-adopted PR#6017 and releaseHoldLabels removed pilot-needs-human from #6016. A failed row is not terminal (HasTerminalCompletion), the SDK poller has no PR-awareness, so 19 s later #6016 was admitted again and a fresh worktree off origin/main re-implemented work the open PR already held. The 2.309.0 hot-upgrade restart was incidental. Invariant to enforce at admission: an open PR on pilot/GH-<n> means autopilot owns the issue, not the poller (TASK-632 → pilot#6020). Same class: a human clearing pilot-needs-human to unblock a PR, a label cycle.

## Context
Discovered during development.

## Details
A failed issue whose PR is still open gets a second run when any path strips its last marker label. 2026-11-03: pilot#6016 failed CI, the CI-fix size guard declined (759 production additions > 200), the daemon stripped pilot-in-progress and added pilot-needs-human. A hand-filed fix issue (#6019) pushed a commit to the PR branch; autopilot's redriveHeldPR re-adopted PR#6017 and releaseHoldLabels removed pilot-needs-human from #6016. A failed row is not terminal (HasTerminalCompletion), the SDK poller has no PR-awareness, so 19 s later #6016 was admitted again and a fresh worktree off origin/main re-implemented work the open PR already held. The 2.309.0 hot-upgrade restart was incidental. Invariant to enforce at admission: an open PR on pilot/GH-<n> means autopilot owns the issue, not the poller (TASK-632 → pilot#6020). Same class: a human clearing pilot-needs-human to unblock a PR, a label cycle.

## Recommended Approach
Apply this knowledge when working on related topics.

## Related
- None documented

---
**Captured**: 2026-11-03
**Confidence**: 90%
**Concepts**: autopilot, repick, labels, hold
