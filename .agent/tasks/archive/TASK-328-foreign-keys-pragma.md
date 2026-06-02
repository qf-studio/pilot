# TASK-328: Enable `PRAGMA foreign_keys=ON` so `ON DELETE CASCADE` actually runs

**Wave:** 1 (XS) · **Pilot** · **Severity:** HIGH · **Audit ref:** TASK-322 §high
"foreign_keys PRAGMA never enabled — DeleteCrossPattern silently orphans child rows"

---

## Problem

`NewStore` sets only `PRAGMA journal_mode=WAL` and `PRAGMA busy_timeout` at open (`store.go:45`);
`PRAGMA foreign_keys=ON` is **never** issued. SQLite (incl. the `modernc.org/sqlite` driver) defaults
FK enforcement to **OFF per connection** (probing the project's exact open sequence reports
`foreign_keys="0"`). Consequently the `FOREIGN KEY ... ON DELETE CASCADE` clauses on `pattern_projects`
and `pattern_feedback` are inert.

`DeleteCrossPattern`'s own doc comment ("Related pattern_projects and pattern_feedback records are
deleted via foreign key cascade") is therefore false — deleting a cross-pattern leaves orphaned child
rows that accumulate forever and can resurface (e.g. via `GetProjectsForPattern`) referencing a
non-existent pattern. Unbounded growth + dangling references masquerading as working cascades.

## Approach

### Step 1 — Enable FKs (XS, ~10 min)
- Add `PRAGMA foreign_keys=ON;` to the pragma `Exec` in `NewStore`:
  `PRAGMA journal_mode=WAL; PRAGMA busy_timeout=10000; PRAGMA foreign_keys=ON;`
- With `MaxOpenConns(1)` the single connection enforces cascades.

### Step 2 — Verify + correct the comment (XS, ~20 min)
- Add a test: insert a cross-pattern with child `pattern_projects`/`pattern_feedback` rows, call
  `DeleteCrossPattern`, assert the children are gone (`COUNT(*) == 0`).
- If enabling FKs project-wide is risky for any legacy rows, the fallback is to make `DeleteCrossPattern`
  explicitly `DELETE` the child rows in the same `withRetry` block — but prefer the pragma. Fix the
  `DeleteCrossPattern` comment to match whichever path is taken.

## Files to modify
- `internal/memory/store.go` (`NewStore` pragmas; `DeleteCrossPattern` comment)
- `internal/memory/store_test.go` — cascade-delete test

## Test Strategy
- Unit: cascade test above. Run the full `internal/memory` suite to confirm enabling FKs doesn't break
  existing inserts (watch for any insert that relied on FK enforcement being off).

## Effort
XS (~30 min). One PR.

## Out of Scope
- Adding new FK constraints elsewhere — only enable enforcement of the existing ones.
- Retention/pruning of `execution_logs` — separate Wave 3 task (D5).
