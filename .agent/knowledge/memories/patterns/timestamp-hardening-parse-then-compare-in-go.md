# Pattern: parse-then-compare in Go, never raw SQL string ranges, for mixed-format timestamp columns

## Summary
When a SQLite TEXT column can contain timestamps written under two different
on-disk layouts (e.g. before/after a driver DSN fix), do not filter or order
by that column with a raw `WHERE created_at < ?` / `ORDER BY created_at` SQL
predicate. Select the rows unfiltered, `Scan` each `created_at` into a
`time.Time` (the modernc.org/sqlite driver's read path already parses both
the `time.Time.String()` legacy layout and any `_time_format`-written
layout — see `conn.go`'s `parseTime`/`parseTimeString`), then filter/sort by
comparing the already-parsed `time.Time` values in Go.

## Context
Fixing `GetStaleRunningExecutions`/`GetStaleQueuedExecutions` and adding
`GetClaimedNonTerminalExecutions` for GH-4392 (2026-07-17): the pre-fix
`WHERE created_at < ?` predicate silently dropped legacy-format rows from
stale-recovery results (lexicographic comparison across two different text
layouts is not guaranteed to match chronological order), which is why the
stale-recovery sweep logged "reset 0 tasks" straight through an incident
where 5 dead-owner rows needed recovering.

## Details
The driver's *read* path is not the problem — `conn.parseTime` already
handles both `t.String()`-style text (via the "m=" monotonic-marker split)
and the `_time_format=sqlite` layout, so `Scan(&time.Time)` reliably
produces a correct absolute instant regardless of which layout wrote the
row. The bug is entirely on the *SQL comparison* side: two syntactically
different but individually-parseable text layouts do not sort against each
other lexicographically the way their underlying instants do.

## Recommended Approach
```go
rows, _ := db.Query(`SELECT ..., created_at, ... FROM t WHERE status = ?`, status)
// scan created_at into time.Time per row, unfiltered
...
// then, in Go:
cutoff := time.Now().Add(-staleDuration)
for _, r := range rows {
    if r.CreatedAt.Before(cutoff) { ... }
}
sort.Slice(rows, func(i, j int) bool { return rows[i].CreatedAt.Before(rows[j].CreatedAt) })
```
Applies to any column that predates a timestamp-format fix in this codebase
(see #4345/GH-4332) — grep for other `WHERE created_at <`/`ORDER BY
created_at` raw-SQL predicates before assuming a "just add `_time_format=
sqlite`" fix alone is sufficient; existing rows written before the fix still
need the Go-side comparison treatment on every future read.

## Related
- `internal/memory/store.go` — `filterAndSortStale`, `staleReference`, `GetClaimedNonTerminalExecutions`
- `.agent/knowledge/memories/pitfalls/pitfall_sqlite_time_bind_default_format.md` (the original DSN-fix pitfall this pattern hardens against on the read side)
- `.agent/knowledge/memories/pitfalls/nextretrygeneration-blind-to-dead-owner-nonterminal-claims.md`

---
**Captured**: 2026-07-17
**Confidence**: 90%
**Concepts**: sqlite, database, timestamp, time-parsing, modernc-sqlite
