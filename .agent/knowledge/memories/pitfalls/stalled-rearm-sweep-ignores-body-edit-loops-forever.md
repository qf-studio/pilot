---
name: stalled-rearm-sweep-ignores-body-edit-loops-forever
description: re-arming a stalled task by editing the issue BODY does nothing — sweepStalledRearm only accepts label/reopen events newer than the stall, then recordClaimLostDrop re-arms a 16-min backoff forever with no drop-count backstop (GH-5346: 76 drops, 33 h). Re-arm with a label event (retry-1 → retry-ready) instead.
type: pitfall
---

# Stalled re-arm sweep ignores body edits and loops forever

**What happened (2026-09-07 09:21Z → 09-08 10:07Z, pilot#5346):** task stalled
("consecutive identical failures", gen 1). Operator re-armed by editing the
body ("new branch from main") and left `pilot-retry-1` on. Never re-picked.
Ledger `repick_backoff` row: `claim_lost_drops=76`, `next_allowed_at` always
16 min ahead. Log every ~5.5 min:

```
dispatch  task_id=GH-5346 "skip reason: repick-backoff cooldown, NOT a completed execution…"
github-sdk-poller number=5346 "Skipping re-dispatch — completed execution exists"   ← false
```

**Mechanics:** `cmd/pilot/rearm_stalled.go` `tryRearmStalled` needs a
`labeled`/`reopened` event newer than `completed_at`; a body edit is not one.
On miss, line ~232 calls `recordClaimLostDrop` → window re-armed
(`30s·2^min(drops−1,5)`, cap 960 s), `claim_lost_drops` never hits the hard cap
by design. The #5297 3-drop pilot-strip backstop is on a different path.

## Rules
- **Re-arm stalled tasks with a LABEL event**, never a body edit alone:
  `gh issue edit N --remove-label pilot-retry-1 --add-label pilot-retry-ready`
  (or cycle `pilot`). Pickup ≤ 16 min.
- Body edits are still needed for content changes — do both.
- Same evidence-narrowness class as the 08-26 "manual `pilot-blocked` strip
  bypasses the #5215 probe" note. Fix filed: pilot#5376 (accept body-edit
  evidence; 3-miss backstop → needs-human + comment; fix the poller log).
