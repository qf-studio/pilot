---
name: board-stalled-timestamp-is-created-at-after-self-upgrade-reconciliation
description: pilot-board prints a stalled row's created_at (queue time) as its stall time — after a hot self-upgrade, boot-time dead-owner reconciliation stalls every queued claim and the board makes it look like tasks were killed a minute after pick; read daemon.log timestamps, not the board
type: learning
---

# Board "stalled" timestamp = created_at; self-upgrade reconciliation looks like instant kills

**What happened (2026-09-07):** #5353/#5354/#5355 showed `stalled` at
12:52:32 / 12:52:59 / 12:58:58 — within a minute of filing. In fact those
are their queue-admission times ("task queued behind GH-5351"). The real
event: hot self-upgrade v2.273.0 → v2.273.1 at 14:19:44Z
("restarting with new binary" via exec, PID/uptime unchanged), then
boot-time reconciliation at 14:19:45Z stalled the three claimed `queued`
rows (GH-4392 design, working as intended), and the re-arm sweep re-picked
them at 14:22–14:23Z. No Claude session ever ran for any of them.

## Rules
- Verify stalls in `daemon.log` (`upgrade verified complete`,
  `Reconciling dead-owner execution found at boot`), never from the board's
  time column.
- `ver=` on the board changing while uptime stays high = hot restart
  (`hot-restart-preserves-pid-uptime-false-mismatch`).
- Board fix candidate: render the stall-transition timestamp
  (`completed_at`), not `created_at`. Script is `/usr/local/bin/pilot-board-remote`
  on the box (not in repo).
- Related: `nextretrygeneration-blind-to-dead-owner-nonterminal-claims`.
