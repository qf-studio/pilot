---
name: Trust PR merge state, not Pilot labels
description: When reporting on task outcomes, always cross-check actual PR merge state — Pilot labels frequently lie
type: feedback
originSessionId: 3eb8d5b9-a522-4cc1-b1cb-d3d565061021
---
**Never trust `pilot-done` / `pilot-failed` labels as ground truth. Always cross-check against actual PR merge state.**

**Why:** Pilot's label lifecycle is not transactional. Observed today (2026-04-17):
- `pilot-done` applied to issues whose PR closed unmerged (GH-2324, 2341, 2355)
- `pilot-failed` + `pilot-in-progress` both set simultaneously (GH-2340, 2341, 2345)
- `pilot-done` + `pilot-retry-ready` both set (GH-2324, 2341, plus 10 historical)
- `pilot-in-progress` stuck on closed issues (GH-2348, 2351)
- "✅ Task completed successfully!" logged when PR push actually failed (GH-2341 dispatch)

MEMORY.md already tracks this pattern (`bug_pilot_ghost_closes.md`, GH-1302 stale pilot-failed). But the failure mode RECURS because new label paths are added faster than they are made transactional.

**How to apply:** When summarizing queue or task status:
1. For "did work land?" — `gh pr list --search "head:pilot/GH-N" --state all --json state,mergedAt`. If `mergedAt` is null, work did NOT land.
2. For "is issue in progress?" — cross-reference `state=OPEN` AND label AND `executions` table (daemon actually dispatched).
3. Do NOT report "✅ X merged" based on label alone. Verify `mergedAt != null`.
4. Closed issues carrying `pilot-in-progress` are dashboard noise, not active work.

This rule saved us twice today from falsely reporting successful merges that never happened.
