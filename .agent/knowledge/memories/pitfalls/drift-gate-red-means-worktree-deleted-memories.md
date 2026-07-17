---
name: drift-gate-red-means-worktree-deleted-memories
description: Knowledge Graph Drift Gate failing on every PR usually means executor worktrees carried DELETIONS of memory files merged after their branch cut — check git log --diff-filter=D, restore from adding commits, verify scripts/check-graph.py
type: pitfall
---

# Drift gate red across PRs = executor worktrees deleting merged memory files

**What happened (2026-07-17):** The Knowledge Graph Drift Gate went red on every
memory-writing PR. Forensics: 4 Pilot PRs (#4408, #4410, #4424, #4429) each
**deleted** graph-indexed memory files that had merged to main after their
worktree branched — squash merges then carried the deletions in. Two graph
edges also used a wrong key schema (`source/target/relation` instead of
`from/to/type`), reading as dangling `None -> None` edges. Net effect: gate
structurally red → contributed to autopilot destroying PR #4419 (#4422 path).

## Diagnosis recipe
1. `python3 scripts/check-graph.py` locally — FAIL classes: broken file links,
   unindexed files, dangling edges.
2. For each missing file: `git log --oneline --diff-filter=A -1 -- <path>` and
   `--diff-filter=D` → identifies the adding commit and the deleting PR.
3. Restore: `git checkout <adding-sha> -- <path>`; fix edge keys with a small
   json rewrite (`ensure_ascii=False`!); re-run gate; commit to main.
4. If an OPEN PR carries a deletion: restore the file on its branch via the
   GitHub contents API (base64 PUT with `branch=`) — no local checkout needed.

## Why it recurs
Every Pilot worktree branches from main-at-dispatch-time; any memory merged
during its run looks like an untracked local artifact to session cleanup.
Fixed by the TASK-410 guard series: detection (#4430 wires
`RestoreDeletedIndexedMemoryDocs` at all three finalize points) + restore
(#4421). Until proven in prod, run the diagnosis recipe whenever the gate
reds. Related: [[bare-exit-137-mislabeled-oom]], #4422, TASK-410.
