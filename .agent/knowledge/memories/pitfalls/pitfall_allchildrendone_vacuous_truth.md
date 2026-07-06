# Pitfall: allChildrenDone vacuous-truth on empty set

## Summary
`allChildrenDone()` returned `true` for an empty child set, tripping the epic-complete branch and exiting the executor without doing any work; empty-set guard added to require at least one child before reporting complete.

## Context
Surfaced during workshop dry-run as the third Pilot failure of the session. Patched in v2.149.3 (commit fd0f7b69).

## Details
At `epic.go:923`, `allChildrenDone()` performed a logical AND over the child slice. With zero children the AND-over-empty returned `true` (vacuous truth), so an epic with no expanded sub-issues was treated as already complete. The executor exited the loop without picking up work or surfacing an error. Fix added an explicit empty-set guard returning `false` when the child slice is empty, plus regression test coverage in `epic_test.go` (7 subcases).

## Recommended Approach
When the executor exits cleanly but no work happened on an epic:
1. Check `epic.go:923` and surrounding code for empty-set behavior in completion predicates.
2. Verify the child-expansion step actually produced sub-issues before the predicate runs.
3. This pattern recurs anywhere `all(...)` / `every(...)` is applied to a potentially-empty collection — audit similar predicates across the codebase.

## Related
- v2.149.3 commit fd0f7b69
- `epic.go:923`, `epic_test.go`
- mem-pilot-001, mem-pilot-002, mem-pilot-004 (other v2.149.x patterns)

---
**Captured**: 2026-05-26
**Confidence**: 95%
**Concepts**: pilot, executor, debugging, epic, vacuous-truth, predicates
