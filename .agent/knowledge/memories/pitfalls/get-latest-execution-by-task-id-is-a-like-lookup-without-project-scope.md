# Pitfall: Store.GetLatestExecutionByTaskID(taskID, "") matches task_id = ? OR task_id LIKE

## Summary
Store.GetLatestExecutionByTaskID(taskID, "") matches task_id = ? OR task_id LIKE '%taskID%' with no project scope, so GH-15 with no row of its own returns GH-150's row, GH-1500's, or another repo's GH-15. Any done/terminal check built on it inherits the borrow (the startup label guard and the stale-label gate did, 2026-10-31). TASK-626 (#5996) adds Store.GetLatestExecutionByExactTaskID for those two callers; every other caller of the fuzzy lookup still borrows. Use the exact variant, with a project path, whenever the answer gates re-dispatch or a label.

## Context
Discovered during development.

## Details
Store.GetLatestExecutionByTaskID(taskID, "") matches task_id = ? OR task_id LIKE '%taskID%' with no project scope, so GH-15 with no row of its own returns GH-150's row, GH-1500's, or another repo's GH-15. Any done/terminal check built on it inherits the borrow (the startup label guard and the stale-label gate did, 2026-10-31). TASK-626 (#5996) adds Store.GetLatestExecutionByExactTaskID for those two callers; every other caller of the fuzzy lookup still borrows. Use the exact variant, with a project path, whenever the answer gates re-dispatch or a label.

## Recommended Approach
Apply this knowledge when working on related topics.

## Related
- None documented

---
**Captured**: 2026-11-01
**Confidence**: 100%
**Concepts**: state-store, ledger, dispatcher, labels, repick
