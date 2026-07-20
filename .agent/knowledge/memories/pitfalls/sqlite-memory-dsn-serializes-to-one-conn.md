---
name: sqlite-memory-dsn-serializes-to-one-conn
description: NewStateStoreFromPath(":memory:") test stores need db.SetMaxOpenConns(1) — modernc.org/sqlite's in-memory database is private per-connection, so a second pooled connection sees an unmigrated "no such table" database under any concurrent access
type: pitfall
---

# `sql.Open("sqlite", ":memory:")` is NOT shared across pooled connections

**What happened (GH-4476, 2026-07-20):** a new test drove
`scheduleReleaseTickWithRetry`'s retry goroutine (which persists via
`StateStore` in a background goroutine) while the test's own goroutine
polled `StateStore.GetScopeRelease` concurrently. The poll intermittently
failed with `SQL logic error: no such table: autopilot_scope_release` even
though the exact same store had just successfully migrated and written rows
moments earlier from a different goroutine.

## Root cause

`internal/autopilot/state_store.go`'s `NewStateStoreFromPath` does
`sql.Open("sqlite", path)` and lets `database/sql`'s default connection pool
open more than one physical connection. For `modernc.org/sqlite`, a bare
`":memory:"` DSN (no `?cache=shared`) gives **every connection its own
private in-memory database** — `migrate()` runs on whichever connection the
pool handed it first, and a second concurrent connection opens a brand-new,
empty, unmigrated database. Every *sequential* test (open store → call →
call → assert, all on one goroutine) happened to reuse the same pooled
connection and never noticed; the first genuinely concurrent caller does.

## Fix

`NewStateStoreFromPath` now calls `db.SetMaxOpenConns(1)` right after
`sql.Open`, pinning the pool to a single connection so `":memory:"` behaves
as one logical database regardless of caller concurrency. This function is
test-only (`NewStateStore(db)` is the production entry point with an
externally-supplied `*sql.DB`), so this has zero production behavior change.

## Prevention

Any new test that exercises a `StateStore` (or other `":memory:"`-backed
store) from more than one goroutine must confirm the store's constructor
pins `SetMaxOpenConns(1)` — or use a shared-cache DSN
(`file::memory:?cache=shared`) — before trusting concurrent reads/writes
against it. A flaky "no such table" on a table you just successfully wrote
to, with no schema changes in sight, is the diagnostic signature.
