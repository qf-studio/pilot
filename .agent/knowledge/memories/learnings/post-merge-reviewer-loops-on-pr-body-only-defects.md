# Learning: The post-merge reviewer (TASK-620) filed a fix issue whose only defect was a mis

## Summary
The post-merge reviewer (TASK-620) filed a fix issue whose only defect was a missing note in an already-merged PR body (console#468 after #462 → #466 → #467). Pre-flight declined it as reject_vague ('no change to code or docs can satisfy it'). Rule for the reviewer: a defect that only the merged PR body could satisfy must be dropped or downgraded to a note, never filed as a fix issue; humans close such loops as not-planned.

## Context
Discovered during development.

## Details
The post-merge reviewer (TASK-620) filed a fix issue whose only defect was a missing note in an already-merged PR body (console#468 after #462 → #466 → #467). Pre-flight declined it as reject_vague ('no change to code or docs can satisfy it'). Rule for the reviewer: a defect that only the merged PR body could satisfy must be dropped or downgraded to a note, never filed as a fix issue; humans close such loops as not-planned.

## Recommended Approach
Apply this knowledge when working on related topics.

## Related
- None documented

---
**Captured**: 2026-11-03
**Confidence**: 85%
**Concepts**: review, autopilot, navigator
