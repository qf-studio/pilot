# Pitfall: modernc.org/sqlite binds time.Time as Go String() format — date() returns NULL, ordering breaks

## Summary
modernc.org/sqlite writes bound `time.Time` params using Go's `time.Time.String()` format by default, which SQLite's `date()`/`datetime()` can't parse (returns NULL) and sorts inconsistently against `DEFAULT CURRENT_TIMESTAMP` columns. Fix: open the DB with `?_time_format=sqlite`.

## Context
Root-causing internal/briefs's flaky `TestGeneratorGenerate`, 2026-07-15.

## Details
Two compounding issues: (1) the driver's default time binding produces `2026-07-15 10:00:00.123456789 +0200 CEST m=+0.001` style strings — SQLite date functions return NULL on them, and lexicographic ordering against `CURRENT_TIMESTAMP`-defaulted columns (format `2026-07-15 08:00:00`) is inconsistent, so time-window queries silently drop rows. (2) `SaveExecution`'s INSERT omitted `created_at`, letting the DB default silently override caller-supplied timestamps — tests that constructed executions with explicit timestamps got CURRENT_TIMESTAMP instead, making the assertion window flaky.

## Recommended Approach
Open modernc.org/sqlite DBs with `?_time_format=sqlite` in the DSN. When an INSERT is meant to honor caller-supplied timestamps, bind the column explicitly — never rely on a DB default to "usually match". For flaky time-window tests, check binding format vs column default format first.

## Related
- `internal/memory/store.go`
- `internal/briefs`

---
**Captured**: 2026-07-15
**Confidence**: 90%
**Concepts**: sqlite, database, testing, flaky-test

> Note: file reconstructed 2026-07-16 from its graph.json entry — the original
> was indexed but never committed (drift-gate FAIL on main); summary preserved verbatim.
