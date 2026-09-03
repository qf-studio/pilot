---
name: go-time-cutoff-vs-sqlite-current-timestamp-timezone
description: Binding a Go time.Now()-derived cutoff against a SQLite CURRENT_TIMESTAMP column compares LOCAL-time text to UTC text — grace windows silently break on any non-UTC host (reaper reaped live claims on UTC+2; invisible on the UTC box and UTC CI)
type: pitfall
---

# Go time cutoff vs SQLite CURRENT_TIMESTAMP: timezone mismatch

**What happened (2026-09-03, found reviewing #5297/PR#5306):**
`ReapOrphanedClaims` (`internal/memory/store.go:~3500`) does
`cutoff := time.Now().Add(-grace)` and binds it to `WHERE created_at < ?`.
`execution_claims.created_at` is `DATETIME DEFAULT CURRENT_TIMESTAMP` — SQLite
writes that as **UTC** text. The DSN's `_time_format=sqlite` makes the driver
write the bound `time.Time` in the same text layout but in the value's own
location — i.e. **local** time. On a CEST laptop a 1-second-old claim reads as
2 hours old and is reaped inside the 10-minute grace window:
`TestDispatcher_ReapOrphanedClaims_LeavesFreshClaimWedgedForDuplicatePickup`
fails deterministically, passes under `TZ=UTC`. Filed as pilot#5308.

**Why it hid:** founder box and CI both run UTC, so local == UTC and the
comparison is accidentally correct. Every self-hoster east of UTC has live
dispatch claims deleted (duplicate-execution risk); west of UTC nothing is
reaped until offset+grace elapses.

**The repo already knew:** `store.go:~3159` moved a stale-sweep comparison
into Go for exactly this reason and says so in a comment. The reaper repeated
the pattern anyway — the pitfall is not discoverable from the call site.

**Rule:**
- Never bind a raw `time.Now()`-derived value against a `CURRENT_TIMESTAMP`
  column. Use `cutoff.UTC()`, or select candidates and compare in Go.
- Any test that touches a `CURRENT_TIMESTAMP` grace/cutoff must run once under
  a fixed non-UTC `time.Local` so UTC CI reproduces the self-hoster case.
- When reviewing: grep `store.go` for `_at\s*[<>]=?\s*\?` and check each
  param's construction. Related: [[absolute-state-paths-bypass-cutover-shim]]
  (another "correct on the box, wrong elsewhere" class).
