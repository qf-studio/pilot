---
name: self-upgrade (hot upgrade) only fires on a TUI 'u' keypress — there is no automatic/unattended trigger, so it silently stops the moment nobody is watching the dashboard
description: GH-3790/D7 — daemon stayed on v2.201.2 through 8 releases (v2.202-v2.206) for 7+ hours; root cause traced from daemon.log, not inferred from reading code alone
metadata:
  type: pitfall
---

**Symptom:** background version checks keep firing forever ("update available"
logged every 5 min in `~/.pilot/logs/daemon.log`, tracking each new release as
it ships), but the daemon never actually installs any of them — no
`"restarting with new binary"` line appears for hours, across many releases.

**Root cause (traced via daemon.log timestamps, not just code reading):**

The self-upgrade pipeline has three legs, and only the first is automatic:

1. `internal/upgrade/checker.go` `VersionChecker.run()` — background goroutine,
   ticks every `DefaultCheckInterval` (5 min), always runs, never stops on its
   own. **Confirmed always firing** — logged continuously 11:06→22:37 on
   2026-07-03 even while current=2.201.2 sat behind 6 releases.
2. `cmd/pilot/main.go:2833` `versionChecker.OnUpdate(...)` — the *only*
   consumer of a detected update. It logs and pushes a dashboard notification
   (`dashboard.NotifyUpdateAvailable`). **It never enqueues an upgrade.**
3. `cmd/pilot/main.go:2847` `case <-upgradeRequestCh:` — this is the *only*
   code path that calls `hotUpgrader.PerformHotUpgrade`. The *only* writer to
   `upgradeRequestCh` is `internal/dashboard/tui.go:1017` `case "u":` — a
   literal keyboard keypress inside the Bubbletea TUI. Grepped the full repo:
   zero other senders to that channel, no idle timer, no cron, no autopilot
   hook. Outside `--dashboard` mode the whole subsystem (`VersionChecker` +
   `HotUpgrader`) is never even constructed (gated by
   `if dashboardMode && program != nil` at main.go:2828).

So "self-upgrade" has always meant: *a human watching the TUI presses 'u'
when they notice the banner.* GH-369's original design doc even lists an
"auto-trigger if idle for 5 min" and an `auto_upgrade_on_idle` config knob —
neither was ever implemented; only the manual key handler shipped.

**Evidence this is attention-gated, not a regression:** `restarting with new
binary` events across `~/.pilot/logs/daemon.log` cluster tightly around times
the operator was actively at the terminal (e.g. 2026-06-26 09:33/12:05/12:52/
14:24 during a heavy live-debug session — the same session as mem-040's
(`bug_daemon_autoupgrade_reverts_dev_binary.md`) 12:32→12:52 dev binary
revert. On 2026-07-03 the pattern breaks cleanly: restart at 12:50:23
(2.201.0→v2.201.2, during active TASK-378 dispatch work), then **zero**
restart attempts — no success, no failure, no error logged, just silence —
for the entire 12:50→20:01 window while `v2.201.3` through `v2.207.0` shipped
underneath it, resuming only at 20:01:12 when the daemon was manually
restarted (fresh process, not a hot-upgrade exec).

**How to apply:** don't assume the daemon self-heals onto new releases just
because it's running with `--dashboard`. If nobody is watching that specific
terminal, it will happily run 8+ releases stale with no error anywhere. The
real fix (tracked as GH-3790, split into follow-on subtasks) needs an
unattended trigger — e.g. auto-invoke `PerformHotUpgrade` from `OnUpdate`
after N consecutive detections (with the existing task-draining/graceful
wait already in `HotUpgrader`), gated by config rather than a keypress — plus
a fail-loud ">N releases behind" doctor/alert check per the GH-3790 issue
body, since silent staleness is the actual incident, not just the missing
trigger.

**Resolved (GH-3790-3):** see
[[decision_self_upgrade_auto_trigger_and_staleness_check]] (mem-045).
`OnUpdate` now auto-enqueues the hot upgrade (config-gated via
`upgrade.auto_hot_upgrade`, keypress kept as a manual override), and
`VersionChecker` fails loud past `upgrade.stale_release_threshold` releases
behind (WARN log + alert + `pilot doctor` check). Still open: the
checker+hot-upgrader subsystem remains dashboard-mode-only — non-dashboard
polling mode still has no self-upgrade path at all, automatic or manual.
