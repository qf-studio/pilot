# Pitfall: A multi-part leg filed without no-decompose gets epic-split into children that t

## Summary
A multi-part leg filed without no-decompose gets epic-split into children that touch the same files; children cut from the same base re-implement each other and conflict once the first merges. 2026-11-02: C4 (console#469 → #470/#471: both re-implemented Decide(reason)+PostLearning; PR#473 needs-manual-rebase) and U10 (ui#287 → #288/#289/#290: three PRs on types.ts/httpAdapter.ts/mockAdapter.ts; PR#292 also bound a route the console never had). Cost: three hand-filed revisions (console#474, ui#296, ui#297). Rule: every leg whose scope spans more than one file group goes out with the no-decompose token; split by hand into legs with disjoint files if parallelism is wanted.

## Context
Discovered during development.

## Details
A multi-part leg filed without no-decompose gets epic-split into children that touch the same files; children cut from the same base re-implement each other and conflict once the first merges. 2026-11-02: C4 (console#469 → #470/#471: both re-implemented Decide(reason)+PostLearning; PR#473 needs-manual-rebase) and U10 (ui#287 → #288/#289/#290: three PRs on types.ts/httpAdapter.ts/mockAdapter.ts; PR#292 also bound a route the console never had). Cost: three hand-filed revisions (console#474, ui#296, ui#297). Rule: every leg whose scope spans more than one file group goes out with the no-decompose token; split by hand into legs with disjoint files if parallelism is wanted.

## Recommended Approach
Apply this knowledge when working on related topics.

## Related
- None documented

---
**Captured**: 2026-11-03
**Confidence**: 95%
**Concepts**: issue-authoring, decomposition, epic, dispatch
