# TASK-337 (E1+E5): Alert engine — decouple dispatch from event loop + enforce SuppressDuplicates

**Wave:** 2 · **Pilot** · **Severity:** HIGH (reliability) · **Issue:** #3328 · **Audit ref:** TASK-322 findings E1 + E5

> Two findings combined because both edit `internal/alerts/engine.go` (same-file → one PR avoids self-collision).

---

## Problem A (E1) — one hung channel drops ALL alerts
`processEvents` runs in ONE goroutine. For each event it calls `handleEvent` synchronously → `fireAlert` →
`dispatcher.Dispatch`, which blocks until every channel returns (`wg.Wait`), each with a 30s timeout. A
single slow/hung channel blocks the entire event loop for up to 30s per fired alert. `eventCh` is buffered
to only 100; `ProcessEvent` does a non-blocking send and **silently drops on overflow** — including
`EventTypeEscalation` / `EventTypeOOMKilled` / consecutive-failure criticals. The moment alerting matters
most, alerts are silently lost.

## Problem B (E5) — SuppressDuplicates is a no-op
`AlertDefaults.SuppressDuplicates` is plumbed through types.go/config.go and **defaults to true**, but the
engine never reads it. The only flood control is `shouldFire()`, gating purely on per-RULE cooldown by
`rule.Name`. Several rules ship `Cooldown:0` — notably the default `task_failed` rule — so a repeatedly
failing task fires an alert on **every** failure event, flooding channels.

## Approach
- **E1:** Decouple firing from the event loop — `fireAlert` dispatches on a bounded worker pool (or
  `go dispatcher.Dispatch(...)` behind a semaphore) so `handleEvent` never blocks on channel I/O. On
  `eventCh` overflow, prefer dropping low-severity over critical/escalation; emit a drop counter (coordinate
  with alert-metrics #3317/TASK-332 if landed).
- **E5:** Implement `SuppressDuplicates` — hash `{rule, source, message}` with a short TTL window, skip
  re-fires when enabled. At minimum give the default `task_failed` rule a small non-zero cooldown.

## Files to modify
- `internal/alerts/engine.go:107,131,150-157,168-169,460-474,543`
- `internal/alerts/engine_test.go`

## Test Strategy
- E1: a channel whose `Send` blocks does not prevent a second higher-severity alert from dispatching; overflow drops low-severity before escalation.
- E5: `SuppressDuplicates=true` → two identical events within TTL fire once; distinct messages fire separately.

## Effort
M. One PR.

## ⚠️ Pilot self-stall risk
This is timeout/hang-adjacent — a test with a deliberately-blocking channel can stall Pilot's own agent
(the E2/SMTP failure mode). Use **bounded `select`/timeout** waits in tests. If Pilot stalls on #3328,
**take it manually**.

## Out of Scope
- smtp.go / dispatcher.go beyond passing ctx + counters.
