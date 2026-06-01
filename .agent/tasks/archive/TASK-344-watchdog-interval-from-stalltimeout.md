# TASK-344: stall watchdog interval ignores configured stall_timeout (A3)

## Context

`runStallWatchdog` (`internal/executor/watchdog.go:24`) builds its ticker from the package
constant `defaultStallWatchdogInterval` (30s), ignoring the `stallTimeout` it is passed.
`BackendConfig.EffectiveStallTimeout()` (`backend.go:384`) applies NO lower bound — any positive
`StallTimeoutMs` is returned verbatim (only 0 → 3m default, negative → disabled). So an operator who
sets `stall_timeout_ms: 10000` (10s) to fail fast gets detection latency of at least 30s (one tick)
plus slack on top. The function's doc comment is also stale — it claims the watchdog "ticks every
watchdogInterval" but there is no such parameter; the tick interval is not derived from the timeout
at all. This couples correct stall detection to an undocumented constant and makes small configured
timeouts behave unexpectedly.

## Approach

Derive the tick interval from `stallTimeout`:
`interval := min(defaultStallWatchdogInterval, stallTimeout/3)` with a small floor (e.g. 1s) so a
sub-30s timeout is actually honored. Either clamp `EffectiveStallTimeout` to a sane minimum or document
that sub-30s timeouts are rounded up — pick the derive-interval path (less surprising). Fix the
doc comment to match the actual parameter list.

## Acceptance

- [ ] Tick interval is derived from `stallTimeout` (min with the 30s default, floored ≥ 1s).
- [ ] Test: a 9s `stallTimeout` produces a tick interval ≤ 3s (detection latency bounded well under 30s).
- [ ] Test: a large `stallTimeout` (≥90s) still caps the tick at the 30s default (no busy-tick regression).
- [ ] Doc comment on `runStallWatchdog` updated to describe the real interval derivation.
- [ ] `make test` green for `internal/executor`; `make lint` clean.

## Refs

- Findings ledger: `.agent/tasks/TASK-322-security-audit-findings.md` (A3, medium)
- Kickoff: `.agent/tasks/TASK-342-wave3-kickoff.md`
- File: `internal/executor/watchdog.go:24`, `internal/executor/backend.go:384`
