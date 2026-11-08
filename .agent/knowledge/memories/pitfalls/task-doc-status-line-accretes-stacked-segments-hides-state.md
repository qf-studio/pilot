# Pitfall: Task-doc status lines accrete as stacked segments and stop saying the state

## Summary
Status lines are prepended, never replaced: TASK-645 opened with seven **Status**: segments, TASK-583's line was about 6 KB of chronology, TASK-497/584 led with stale dates and unticked Acceptance boxes although every leg had merged. The 11-08 console inventory had to re-derive the real state of 12 docs from gh instead of reading it. Replace the status line (one state, one date, one next step); move chronology to a dated section or the archive.

## Context
2026-11-08 console-session loose-ends inventory (navigator-research over 12 task docs + gh verification).

## Details
Status lines are prepended, never replaced: TASK-645 opened with seven **Status**: segments, TASK-583's line was about 6 KB of chronology, TASK-497/584 led with stale dates and unticked Acceptance boxes although every leg had merged. The 11-08 console inventory had to re-derive the real state of 12 docs from gh instead of reading it. Replace the status line (one state, one date, one next step); move chronology to a dated section or the archive.

## Recommended Approach
When a leg lands: rewrite the **Status** line as one sentence (state · date · next step), tick the Acceptance/Done boxes that the merge proves, and move the previous status text into a '## History' block or drop it. A status line over ~400 chars is a smell. Archive the doc when the last leg merges, in the same session.

## Related
- TASK-645
- TASK-583
- TASK-497
- TASK-584
- `.agent/DEVELOPMENT-README.md`

---
**Captured**: 2026-11-08
**Confidence**: 85%
**Concepts**: workflow-discipline, process, navigator, markers
