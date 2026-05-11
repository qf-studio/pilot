---
name: Decomposer label evaporation cascade
description: Sub-issue creation in epic.go hardcodes ["pilot"] — no parent labels propagate, including no-decompose. Root cause of multi-level re-decomposition.
type: project
originSessionId: 89fe3897-6bc2-4725-a1f2-8635b79860b3
---
Both decomposer sub-issue creation paths hardcode the label set to `["pilot"]`:

- `internal/executor/epic.go:883` — adapter path: `r.subIssueCreator.CreateIssue(ctx, parentID, title, body, []string{"pilot"})`
- `internal/executor/epic.go:1003` — GitHub CLI path: `args := [..., "--label", "pilot"]`

**Why:** No parent label propagates to children — including `no-decompose`, `area:*`, `priority:*`, anything. The runner's opt-out at `runner.go:1214-1216` checks `task.Labels`; sub-issues filed without `no-decompose` get re-classified as fresh epics by the poller, and the decomposer fires again. Once cascading starts, every level loses the opt-out further.

**How to apply:** When debugging "Pilot cascade decomposed despite no-decompose label" or "fix-issue's children re-split", check sub-issue labels with `gh issue view N --json labels`. If only `pilot` is present, this is the bug. Empirically observed today (2026-05-07) as the GH-2753 → GH-2754 → GH-2777 chain — three levels deep before manual unstick (added `no-decompose` to GH-2777 directly).

**Fix:** TASK-43 / GH-2792 — adds `filterPropagatableLabels` helper, allow-listing `no-decompose`, `no-plan`, `area:*`, `priority:*`, `scope:*`, blocking lifecycle labels (`pilot-done`, `-failed`, `-in-progress`, `-superseded`, `-needs-clarification`).

**Related:** Decomposer prose-hint heuristic (TASK-42 / GH-2783, shipped v2.131.1) only protects originally-filed issues — sub-issue bodies are subtask descriptions, not parent prose, so prose-hints don't carry through. Label propagation is the structural fix.
