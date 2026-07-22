---
name: stall-status-reused-as-hold-marker
description: ExecStatusStalled is both the watchdog's genuine-kill outcome AND the dispatcher's own escalate-and-hold marker — carve-outs and idempotency checks keyed on Status alone get this backwards (GH-4502)
type: pitfall
---

# Reusing a status value as both a real outcome and an internal marker breaks naive Status-only checks

**What happened (GH-4502, 2026-07-22):** fixing the "4 stall-kills wedge a
healthy task" bug (pilot-console GH-24) required carving stall-killed drops
out of `dispatcherRepickHardCap`'s shared `consecutiveDrops` counter, mirroring
the existing operator-cancel/restart-churn carve-outs. Both of the naive
"just check `Status == "stalled"`" approaches broke existing behavior:

1. **Ordering bug:** `escalateStalledTask` (the hard-cap hold path) marks the
   claimed execution `Status: "stalled"` as its own hold marker — completely
   unrelated to a genuine watchdog kill. If the new stall carve-out
   (`priorClaimWasStalled`) is consulted *before* the hard-cap check, the
   *next* poll tick after a hard-cap escalation reads that same "stalled"
   status and (wrongly) grants a fresh carved-out generation forever,
   defeating the hard cap it just tripped. Fix: read/check
   `consecutiveDrops >= dispatcherRepickHardCap` first — that counter is
   status-independent and stays sticky across an escalation — before ever
   consulting `priorClaimWasStalled`.
2. **Idempotency bug:** the shared `escalateStalledTask` helper's "already
   escalated, don't re-alert" guard originally checked `Status == "stalled"`
   alone. For the *stall-cap* class specifically, `Status == "stalled"` is
   the very condition that triggers escalation in the first place (a genuine
   fresh stall-kill), so the guard misfired and suppressed the alert on the
   very first legitimate trip. Fix: match on the exact prior `Error` reason
   string too (each escalation reason bakes in dropCount/cap, so a repeat
   call reproduces the identical string; a fresh genuine stall-kill carries
   the runner's own different message and never matches).

## Why it matters

Any time a terminal/observable status enum value is reused internally as a
"this subsystem already handled it" marker, naive equality checks on that
one field can't distinguish "real occurrence" from "our own prior handling of
a real occurrence." Look for *all* consumers of the status value (not just
the one you're adding) before assuming a single-field check is safe, and
prefer pairing the check with something that only your own handling sets
(exact message text, a dedicated marker field, a separate timestamp) rather
than the shared enum alone.

## Fix

`internal/executor/dispatcher.go`: hard-cap check ordered before
`priorClaimWasStalled`; `escalateStalledTask` idempotency requires
`Status == "stalled" && Error == reason`, not status alone. Regression-pinned
by `TestDispatcher_BeginWithGenerationRetry_HardCapIsIdempotent` (ordering) and
`TestDispatcher_BeginWithGenerationRetry_StallCapEscalatesWithDistinctReason`
(idempotency).

Related: [[claim-lost-drops-count-toward-hard-cap]], [[hard-cap-rearm-in-memory-gate]].
