# Pitfall: modernc.org/sqlite silently mis-formats bound time.Time params, breaking date()/created_at filters

## Summary
Binding a Go `time.Time` value directly as a `database/sql` query parameter against `modernc.org/sqlite` writes it using Go's `time.Time.String()` format (`"2006-01-02 15:04:05.999999999 -0700 MST"`) by default — a format SQLite's own `date()`/`datetime()`/`strftime()` don't recognize (they return NULL) and that sorts inconsistently against `DEFAULT CURRENT_TIMESTAMP`-populated columns in plain `>=`/`<` string comparisons. Fix: open the DB with `?_time_format=sqlite` in the DSN so the driver writes one of SQLite's own recognized date formats instead.

## Context
GH-4332: issue text claimed `internal/briefs` flaked with `FAIL ... sql: database is closed` (run 29378523581). Pulling the actual CI log (`gh run view <id> --log-failed`) showed that string came from an unrelated package's block in the same monorepo `go test ./...` run (a dispatcher test, output happens to land near the briefs block since `go test` flushes each package's buffered output as a unit, not chronologically); the real `internal/briefs` failure in that run was `TestGeneratorGenerate: generator_test.go:100: expected 1 blocked task, got 0`. **Always pull the actual CI log for the cited run before trusting a symptom string quoted in an issue** — see [[review-before-approval-ordering]] for the sibling "verify before acting on the stated premise" lesson.

## Details
`internal/memory/store.go`'s `SaveExecution` INSERT never listed the `created_at` column, so every row got `DEFAULT CURRENT_TIMESTAMP` (SQLite-native format, `date()`-parseable) regardless of what the caller set on `exec.CreatedAt` — silently discarding test-seeded timestamps. `GetExecutionsInPeriod`/`GetBriefMetrics` filter `WHERE created_at >= ? AND created_at < ?` against Go `time.Time` bounds (e.g. a test's `now`); under CI load, insertion could cross a wall-clock second after the test captured `now`, dropping the row from the window — a load-dependent flake, not a crash.

Fixing just the omitted column (bind `exec.CreatedAt` directly) traded one bug for a worse one: `modernc.org/sqlite`'s default write format for a bound `time.Time` is Go's own `.String()` output, not ISO-8601 — `date(created_at)` on those rows returns NULL, which broke `GetDailyMetrics`'s `GROUP BY date(created_at)` (`internal/memory/store_test.go` `TestGetDailyMetrics_Excludes*`). The driver supports a DSN param, `_time_format=sqlite`, that switches its write format to one of the 7 formats `https://www.sqlite.org/lang_datefunc.html` recognizes (`conn.go:118-131`, `parseTimeFormats[0]` in `sqlite.go:65`) — verified empirically: without it, `SELECT date(?)` on a bound `time.Time` param returns NULL; with it, a correct date string.

## Recommended Approach
When SQLite date/time query results look wrong (NULL groupings, off-by-load-dependent-amount row filtering) in a Go codebase using `modernc.org/sqlite` with hand-bound `time.Time` params:
1. Check the DSN for `_time_format=sqlite`. If absent, every bound `time.Time` param is written in Go's default `.String()` format, not SQLite's.
2. Don't assume `INSERT`ing a Go `time.Time` and letting a column's `DEFAULT CURRENT_TIMESTAMP` fire produce comparable formats — they don't, until the DSN param is set.
3. Fix at the `sql.Open()` DSN, not by hand-formatting individual bind calls — one fix covers every future INSERT/UPDATE in the store.

## Related
- Files: internal/memory/store.go (`NewStore`, `SaveExecution`)
- Issue: GH-4332
