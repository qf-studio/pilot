---
name: Decomposer label evaporation cascade
description: Sub-issue creation in epic.go hardcodes ["pilot"] — no parent labels propagate, including no-decompose. Root cause of multi-level re-decomposition.
type: project
originSessionId: 89fe3897-6bc2-4725-a1f2-8635b79860b3
---
Both decomposer sub-issue creation paths now use `filterPropagatableLabels` (TASK-43 / GH-2792, shipped):

- `internal/executor/epic.go:1138` — adapter path: `subLabels := append([]string{"pilot"}, filterPropagatableLabels(plan.ParentTask.Labels)...)`
- `internal/executor/epic.go:1258` — GitHub CLI path: same pattern

`filterPropagatableLabels` (`epic.go:86-90`) allow-lists `no-decompose`, `no-plan`, `area:*`, `priority:*`, `scope:*`, blocking lifecycle labels (`pilot-done`, `-failed`, `-in-progress`, `-superseded`, `-needs-clarification`).

**Why the original bug existed:** Sub-issues hardcoded `["pilot"]` — no parent labels propagated. The runner's opt-out at `runner.go` checked `task.Labels`; sub-issues filed without `no-decompose` got re-classified as fresh epics by the poller. Cascades were the result.

**How to apply:** If cascade decomposition is observed, verify `filterPropagatableLabels` is being called at both create paths. Check sub-issue labels with `gh issue view N --json labels`. If only `pilot` is present, the call was missed.

**Related:** Decomposer prose-hint heuristic (TASK-42 / GH-2783, shipped v2.131.1) only protects originally-filed issues — sub-issue bodies are subtask descriptions, not parent prose, so prose-hints don't carry through. Label propagation is the structural fix.
