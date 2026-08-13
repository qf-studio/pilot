---
name: hot-restart-preserves-pid-uptime-false-mismatch
description: Self-upgrade hot restart is syscall.Exec — PID and ps etime survive, so board "uptime" CANNOT prove the process is stale; the "disk≠process mismatch" signature (3 recorded occurrences 08-08→08-13) was the same misdiagnosis three times, ending in an unnecessary operator restart
type: pitfall
---

# Hot restart preserves PID/uptime — "disk≠process mismatch" was a false signature

**What happened (pinned 2026-08-13):** Three sessions (08-08, 08-11, 08-12
markers + DEVELOPMENT-README) recorded a recurring "self-upgrade replaced
the disk binary but the process never restarted" signature, with the rule
"board `ver` reads disk, `uptime` is truth". On 08-13 an operator restart
was performed to "activate v2.259.1" — then the daemon log showed
`upgrade verified complete from=v2.259.0-14 to=v2.259.1 via="hot restart"`
at 08-12 14:18:40Z. The process had been on v2.259.1 for 20 hours. Same
verified-complete line exists for EVERY train since 07-31. The restart leg
never failed; the heuristic was wrong.

**Why the heuristic lies:** the restart leg is `syscall.Exec`
(`internal/upgrade/restart.go:78`, via `hot.go:199`) — it replaces the
process image **in place**. The PID is unchanged, so `ps` etime (what the
board renders as `uptime`) measures time since **fork**, not since the
last exec. After a hot upgrade: `ver` (disk read) shows the new version,
`uptime` still shows days — which pattern-matches perfectly to "installed
but not restarted" while meaning nothing of the sort.

**How to verify what the process is actually running (until pilot#4864
ships a real surface):**
- `grep 'upgrade verified complete' ~/.pilot/logs/daemon.log` — this line
  is logged by the NEW image at boot (`internal/upgrade/reconcile.go:48`,
  GH-3600 self-verify; `main.go:515-530`). Its `from=`/`to=` fields are
  proof. The failure counterpart is `previous upgrade did NOT take effect`.
- The TUI banner (`internal/dashboard/tui.go:1555`) renders the running
  image's compiled-in version.
- NOT valid: board `ver` (shells the disk binary), board `uptime`
  (etime survives exec), `pilot doctor` (compares the invoked binary,
  `internal/health/health.go:416`), gateway /health (hardcodes "0.1.0",
  `internal/gateway/server.go:557`).

**Root gap (dispatched as pilot#4864 / TASK-476):** no machine-readable
running-version surface existed — `pilot_build_info` metric, real /health
version, doctor disk-vs-running check.

**Broader lesson:** same family as [[name-your-ledger]] / the 07-27 frozen-
laptop-DB misdiagnosis — before acting on a "recurring signature", check
whether the signal's data source can even observe the thing it claims.
A signature that has "recurred" N times with zero direct evidence is N
repetitions of one unverified inference.

**Confirmed in the wild + resolved (2026-08-13 14:19Z):** the box hot-restarted
v2.259.1→v2.259.2 via `syscall.Exec`; three hours later board `uptime` still
read 06:22 (from the 11:27Z start), yet the process genuinely ran v2.259.2.
The resolution (pilot#4864/PR#4865, live in v2.259.2) is a machine-readable
running-version surface — `curl localhost:9091/metrics | grep pilot_build_info`
and `/health` now report the PROCESS version directly. Use those, plus the
`upgrade verified complete … via "hot restart"` log line; never uptime.

Related: [[bug_daemon_autoupgrade_reverts_dev_binary]] ·
[[one-method-two-interface-contracts-self-upgrade-drain]]
