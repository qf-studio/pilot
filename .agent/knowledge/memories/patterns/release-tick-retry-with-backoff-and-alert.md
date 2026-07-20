---
name: release-tick-retry-with-backoff-and-alert
description: scheduleReleaseTick (on_schedule release train) now retries a transient GitHub failure every 15-30m for up to 6h past the scheduled time before giving up and firing a release_tick_failed alert
type: pattern
---

# Release-train tick retry with bounded backoff + loud-failure alert (GH-4476)

## Problem

`startScheduleRelease`'s cron callback fired `scheduleReleaseTick` exactly
once at the scheduled time with no retry. The 2026-07-18 16:00 Europe/Berlin
tick hit a GitHub 403 (user-aggregate rate pool) on the very first API call
(`repoHasAnyTag` → `GetLatestRelease`) and the whole day's release train was
silently forfeited — the existing "missed tick" recovery
(`recoverMissedTrainTick`) only covers the daemon being *down* at tick time,
not the tick running and failing.

## Fix (internal/autopilot/scope_schedule.go)

1. `scheduleReleaseTick` now returns `(releaseTickOutcome, error)` instead of
   silently `return`-ing on every early-exit path. Three outcomes:
   - `releaseTickSucceeded` — enqueued a train.
   - `releaseTickSkipped` — a legitimate no-op (nothing merged yet, empty
     train, no resolvable member PRs). **Not retried** — retrying can't
     produce merged PRs that don't exist.
   - `releaseTickFailed` — a transient GitHub API failure (repoHasAnyTag,
     firstReleaseTrainMembers, GetCurrentVersionForRepo, CompareCommits all
     wrap their error into this outcome). **Retried.**
2. `scheduleReleaseTickWithRetry` wraps the cron callback and
   `recoverMissedTrainTick`'s call site. It runs the tick once synchronously;
   on `releaseTickFailed` it spawns `retryReleaseTick` in a goroutine so the
   caller returns immediately.
3. `retryReleaseTick` loops until `scheduledAt.Add(releaseTickRetryWindow)`
   (6h), waiting `releaseTickRetryMinInterval..releaseTickRetryMaxInterval`
   (15-30m) between attempts — preferring a `*github.RateLimitError`'s own
   `RetryAfter` (from `Retry-After`/`X-RateLimit-Reset`) over the default
   interval, clamped to the same bounds. `ctx.Done()` (daemon shutdown) exits
   the loop without alerting, since a restart re-attempts via
   `recoverMissedTrainTick` anyway.
4. Exhausted retries call `fireReleaseTickFailedAlert`, which logs `ERROR`
   and — mirroring `fireReleaseMissingAlert`'s existing pattern — fires a
   `release_tick_failed` event through `c.alertsEngine` (or logs a second
   `ERROR` if `SetAlertsEngine` was never called).

## Testability

`releaseTickRetryMinInterval`/`releaseTickRetryMaxInterval`/
`releaseTickRetryWindow` are **package-level `var`s, not `const`s**,
specifically so tests can shrink them to milliseconds instead of a real test
run waiting out real minutes/hours. See
`internal/autopilot/scope_schedule_retry_test.go`'s `withShortRetryTiming`
helper.

## Related pitfall

Writing the retry tests surfaced a pre-existing SQLite test-store gotcha —
see `sqlite-memory-dsn-serializes-to-one-conn` pitfall memory.
