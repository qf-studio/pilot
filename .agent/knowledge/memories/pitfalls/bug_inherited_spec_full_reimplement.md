---
name: inherited-spec sub-issues — each child re-implements the full parent spec
description: Decomposer children carrying "Parent: GH-N" + inherited-spec:true fetch the parent issue as their authoritative spec and implement ALL of it, producing N redundant colliding full-feature PRs instead of N slices.
type: pitfall
originTask: TASK-361
---

The decomposer writes each sub-issue body as
`<!--autopilot-meta parent: GH-N inherited-spec: true-->` + `Parent: GH-N` +
a 1–3 sentence slice description (`internal/executor/epic.go` `subIssueBody`).
`inherited-spec: true` exists for the spec_validator bailout
(`spec_validator.go` ~:49) — validation delegates to the parent.

Side effect: the **executor** Claude Code, seeing `Parent: GH-N`, fetches the
parent issue and treats its full spec as the task. Nothing constrained it to
the subtask's slice. In GH-3513, children #3515 and #3517 each produced a
complete 12-file implementation of the entire feature (+365 and +433 lines,
byte-identical outside one file) — colliding PRs, wasted runs, and the
double-merge hazard.

Secondary symptom from the same incident: child PR #3520 was based on a
**sibling's branch** (`pilot/GH-3515`), not `main`, and the release tagged its
merge commit → phantom `v2.181.0` containing code main never had.

**Fix (PR #3527):** `subIssueBody()` appends a scope fence:
"Implement ONLY the slice described above… consult the parent for context, but
do NOT implement parts outside this subtask." Prompt-level fence — verify it
holds on the next decomposed epic. The base-branch routing bug is NOT fixed
yet (tracked in TASK-361 follow-ups).

Related: [[bug_premature_parent_close_partial_links]], [[pattern_decomposer_thin_subissue_oom]]
