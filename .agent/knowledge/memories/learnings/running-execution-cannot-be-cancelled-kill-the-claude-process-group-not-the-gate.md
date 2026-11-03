# Learning: There is no cancel for a RUNNING execution: 'pilot task cancel' refuses running 

## Summary
There is no cancel for a RUNNING execution: 'pilot task cancel' refuses running rows, chat /cancel is chat-scoped, and closing the issue does not stop the run (only the PR-creation preflight checkIssueSupersededBeforePR refuses the PR later, outcome superseded). To stop a duplicate run on the box: (1) while only the quality gate runs (daemon child = make test-changed, Claude already exited), do nothing — a killed gate just triggers a Claude fix retry; (2) once a Claude process is the daemon's child (pgrep -P <daemon-pid> -f '^claude -p'), kill -KILL its process group — the runner classifies 'signal: killed'/exit 137 as infra, strips the label, and the queue moves on within a minute. Check the stray pushed pilot/GH-<n> branch afterwards; autopilot's sweep usually deletes it. 2026-11-03 GH-6016 duplicate: killed 12:37Z, GH-6020 started 12:38Z.

## Context
Discovered during development.

## Details
There is no cancel for a RUNNING execution: 'pilot task cancel' refuses running rows, chat /cancel is chat-scoped, and closing the issue does not stop the run (only the PR-creation preflight checkIssueSupersededBeforePR refuses the PR later, outcome superseded). To stop a duplicate run on the box: (1) while only the quality gate runs (daemon child = make test-changed, Claude already exited), do nothing — a killed gate just triggers a Claude fix retry; (2) once a Claude process is the daemon's child (pgrep -P <daemon-pid> -f '^claude -p'), kill -KILL its process group — the runner classifies 'signal: killed'/exit 137 as infra, strips the label, and the queue moves on within a minute. Check the stray pushed pilot/GH-<n> branch afterwards; autopilot's sweep usually deletes it. 2026-11-03 GH-6016 duplicate: killed 12:37Z, GH-6020 started 12:38Z.

## Recommended Approach
Apply this knowledge when working on related topics.

## Related
- None documented

---
**Captured**: 2026-11-03
**Confidence**: 85%
**Concepts**: process, quality-gate, dispatch
