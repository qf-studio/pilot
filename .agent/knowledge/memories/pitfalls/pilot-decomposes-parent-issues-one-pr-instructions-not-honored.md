# Pitfall: Pilot decomposes multi-phase issues into sequential children — 'ship in ONE PR' instructions are not honored

## Summary
A task doc that says 'the workflow edit and the file deletions ship in ONE PR' is decomposed by Pilot into sibling sub-issues executed sequentially (GH-5315 → #5316..#5319). The ordering-sensitive coupling was lost: the sync rewrite (#5316/PR#5320) merged and ran alone, re-syncing the not-yet-deleted GitLab files into pilot-docs; the deletion child (#5317/PR#5321) then failed on a stale CI step (ci.yml 'Check docs/.gitlab-ci.yml script YAML validity' → FileNotFoundError) that the task doc never mentioned because it was only relevant once the deletion ran in isolation.

## Context
2026-09-06, TASK-494 (#5315) post-AWS-cutover docs sync fix. Observed via #5315 progress comments and PR#5321 close-without-merge → autopilot fix #5322.

## Details
Two lessons. (1) Atomicity across phases cannot be expressed in prose for Pilot; if two changes must land together, write them as ONE phase with one file list, or accept decomposition and make each child safe on its own (order-independent). (2) Deleting a file that CI validates by name needs the CI step removed in the SAME child; grep .github/workflows and scripts/ for the filename before authoring a deletion task (here scripts/check-gitlab-ci-yaml.py + its test).

## Recommended Approach
Before dispatching a multi-phase task doc: (a) ask 'is any phase unsafe alone?' — if yes, merge those phases into one; (b) for every file deletion, grep CI/Makefile/scripts for the path and include the cleanup in the same phase; (c) when a child fails, fix via the autopilot fix issue BODY (Pilot never reads comments) with the exact root cause.

## Related
- TASK-494
- TASK-492
- `.github/workflows/ci.yml`
- `scripts/check-gitlab-ci-yaml.py`
- `.agent/tasks/TASK-494-docs-sync-github-only-post-aws-cutover.md`

---
**Captured**: 2026-09-06
**Confidence**: 95%
**Concepts**: pilot-dispatch, decomposition, ci, docs-sync, workflow-discipline
