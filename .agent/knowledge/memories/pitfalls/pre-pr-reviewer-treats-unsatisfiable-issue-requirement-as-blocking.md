# Pitfall: An issue requirement the code cannot satisfy becomes a [blocking] pre-PR defect:

## Summary
An issue requirement the code cannot satisfy becomes a [blocking] pre-PR defect: TASK-624 D4 said 'the ledger row carries the full NOT-VERIFIED set' but pr_reviews has no such field, so the pre-PR reviewer returned REQUEST-CHANGES, L5 held PR#5990 with pilot-review-rc, and the founder had to approve by hand (merge 2 h later, 2026-10-31). The reviewer reads every requirement bullet as ground truth. Before dispatch, check each 'X carries / records / persists Y' bullet against the struct or table it names; if the field does not exist, either add the field in the leg or drop the claim.

## Context
Discovered during development.

## Details
An issue requirement the code cannot satisfy becomes a [blocking] pre-PR defect: TASK-624 D4 said 'the ledger row carries the full NOT-VERIFIED set' but pr_reviews has no such field, so the pre-PR reviewer returned REQUEST-CHANGES, L5 held PR#5990 with pilot-review-rc, and the founder had to approve by hand (merge 2 h later, 2026-10-31). The reviewer reads every requirement bullet as ground truth. Before dispatch, check each 'X carries / records / persists Y' bullet against the struct or table it names; if the field does not exist, either add the field in the leg or drop the claim.

## Recommended Approach
Apply this knowledge when working on related topics.

## Related
- None documented

---
**Captured**: 2026-11-01
**Confidence**: 100%
**Concepts**: review, issue-authoring, acceptance-evidence, dispatch
