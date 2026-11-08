# Pitfall: A README row that cites a context marker is not proof the marker exists; three console sessions left no checkpoint

## Summary
DEVELOPMENT-README line 170 cited 2026-11-06-0740_console-session-board-landed-tenant-credentials-dead.md; the file was never written (not on disk, not in git history). The 11-05→11-07 console sessions (TASK-639 Board, TASK-637 mobile, TASK-640 credential health, TASK-645 ticket page) had no recoverable marker; the 11-08 session reconstructed the state from gh and task docs and wrote 2026-11-08_am-console-loose-ends-inventory-ticket-page-landed-5810-next.md to close the gap.

## Context
2026-11-08 console-session start: nav-start found only daemon-scope markers after 11-04; README row named a marker that does not exist.

## Details
DEVELOPMENT-README line 170 cited 2026-11-06-0740_console-session-board-landed-tenant-credentials-dead.md; the file was never written (not on disk, not in git history). The 11-05→11-07 console sessions (TASK-639 Board, TASK-637 mobile, TASK-640 credential health, TASK-645 ticket page) had no recoverable marker; the 11-08 session reconstructed the state from gh and task docs and wrote 2026-11-08_am-console-loose-ends-inventory-ticket-page-landed-5810-next.md to close the gap.

## Recommended Approach
Run nav-marker before leaving any session that dispatched or merged, console sessions included (CLAUDE.md session checklist item 5). When a README row names a marker, verify the file exists in the same commit (ls .agent/.context-markers/<name>) — the stop hook's marker_created flag is the requirement, not the README text.

## Related
- TASK-645
- TASK-639
- TASK-637
- TASK-640
- `.agent/DEVELOPMENT-README.md`

---
**Captured**: 2026-11-08
**Confidence**: 90%
**Concepts**: markers, workflow-discipline, multi-session, console-ui
