---
name: sequential-gates-on-execution-not-merge-fastfollow-misbase
description: --sequential starts the next issue on execution completion, NOT PR merge — a fast-follow issue targeting an unmerged (size-held) PR's files mis-bases; the executor's coping is nondeterministic (vendors the whole target ×2, or self-stacks on its branch ×1). File fast-follows UNLABELED with a dispatch-gate note; label only after the target merges.
type: pitfall
---

# --sequential gates on execution completion, not PR merge — fast-follows against unmerged PRs mis-base

**What happened (overnight run 2026-08-15, TASK-478, pilot-console-ui):** four
legs + two review fast-follows were queued while their predecessors' PRs sat
**size-held** (`awaiting_approval`). The daemon picked each next issue the
moment the previous *execution* completed — the unmerged PR never stalled the
queue. Result: any issue whose spec edits files that exist only on an unmerged
PR's branch executes against a base without them.

**The executor's coping is nondeterministic — observed both modes in one night:**

1. **Vendoring (×2, bad):** GH-69 (edits `OnboardingView.vue`, which only
   existed on unmerged PR#67) and GH-75 (edits PR#74's timeline files)
   re-created their entire target PR inside their own branch, hunk-identical,
   with the fix layered on top → scope fence broken by construction + add/add
   conflict with the target (PR#73, PR#77).
2. **Stacking (×1, good):** GH-71 branched from `pilot/GH-70` instead of main
   (PR#76) → clean fix-only diff; the PR just carries a merge-order constraint
   (target must merge first; base branch deletion auto-retargets it to main).

**Speed of the trap:** GH-75 was claimed **within minutes** of the issue being
filed — removing the `pilot` label a few minutes later was already too late.

**Rules:**
- File review fast-follows **without the `pilot` label** + a bold dispatch-gate
  note in the body ("label only after PR #N merges"). Labeling is the dispatch.
- **Resolution for a vendored PR:** merge the TARGET first, then **close** the
  vendored PR — the external close arms the retry (reclassify-to-failed +
  retry-ready), and the re-run lands a clean fix-only PR on the merged base.
  **Never the inverse** (merging the vendored superset and closing the target
  arms a retry on already-shipped work — the PR#4846 close-arms-retry trap).
- Candidate daemon fixes (not filed as of 2026-08-15): base-presence check
  before claiming a task whose issue references files absent from main, or a
  deterministic prefer-the-stack rule.

Related: [[pilot-issue-missing-no-decompose-fragments-single-fix]] ·
[[incidents-always-first]] (the 08-12 close-vs-merge lesson this generalizes).
