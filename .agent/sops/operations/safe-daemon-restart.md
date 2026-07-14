# SOP: Safe Pilot Daemon Restart (no orphans, no double-daemon)

**Category:** operations
**Created:** 2026-07-14
**Trigger:** Restarting the Pilot daemon after a config change, binary rebuild, or crash.

## Why this exists

2026-07-14 incident: restarting the `--dashboard` daemon from an assistant/background
shell created an **invisible headless orphan** while the old TUI daemon kept running
— two `pilot start` processes polled the same repos concurrently. Two autopilot
controllers both scanning the same merged PR (#4299), plus a scheduled-canary false
failure and zero fix-issue dedup, produced **4 duplicate CI-fix issues**
(#4301/#4302/#4304/#4305). Root cause: **there is no adapter-agnostic single-instance
lock** — the only guard is the Telegram 409 conflict, which is gated behind `--telegram`
and absent for github-only/headless runs (`cmd/pilot/main.go:1885,1964`).

Related: `.agent/system/incident-duplicate-cifix-2026-07-14.md` (full root cause);
fix issues for the durable lock (A3) + spawn dedup (A1) tracked as `pilot` issues.

## Hard rules

- ❌ **NEVER start a second `pilot start` "just for github" or "just headless."** That is
  exactly how the orphan controller arises. One process owns github + telegram + dashboard + tunnel.
  As of GH-4311, a second `pilot start` against the same `Memory.Path` now refuses outright
  (adapter-agnostic `flock` on `<Memory.Path>/pilot.lock`, naming the holder pid) — this is a
  backstop, not a reason to skip the enumerate/confirm steps below.
- ❌ **NEVER restart the user's `--dashboard` daemon from a non-interactive/background shell.**
  `--dashboard` is a bubbletea TUI that needs a real TTY; backgrounding it (`nohup ... &`)
  detaches the TUI (invisible orphan) and does not render. **The restart is the operator's
  action in their own terminal.** An assistant may `make build` + install the binary, but
  must not launch/relaunch the daemon.
- ✅ `pilot start --replace` is now safe to use as the routine restart mechanism (GH-4311):
  it SIGTERMs the pid recorded in the lock file and waits (bounded) for the lock to actually
  release before acquiring it itself — no more coarse `pkill` with no confirmation the target
  exited. `pilot stop` / `pilot restart` are also available for a clean single-command handoff.

## Procedure (manual, serial)

1. **Enumerate:** `pgrep -fa "pilot start"` — expect exactly one PID. Ignore transient
   children (a PID that shows in `pgrep` but has no command under `ps -o command= -p <pid>`
   has already exited — a subprocess flicker, not a daemon).
2. **Stop all cleanly:** `pkill -f "pilot start"`, then re-run `pgrep -fa "pilot start"` and
   confirm **zero** matches. Do not skip the confirm — SIGTERM is best-effort; the process
   may still be draining a tick.
3. **Kill strays:** verify no orphaned `--tunnel` cloudflared or headless dashboard child lingers.
4. **Start exactly one**, in your terminal, with the full flag set for the whole session:
   `GITHUB_TOKEN=$(gh auth token) pilot start --dashboard --github --telegram --tunnel --replace`
   (drop `--dashboard`/`--tunnel` only if you genuinely don't want them — but still one process).
5. **Verify:** `pgrep -f "pilot start" | wc -l` → `1`; TUI renders; `curl -s localhost:9091/metrics | grep pilot_queue_depth` responds; no Telegram 409 in `~/.pilot/logs/daemon.log`.

## When a config change needs a restart

Adding/removing a `projects:` entry, changing adapter credentials, or editing
`ci_checks`/gates all require a restart — the config is read once at startup and pollers
are wired then (`Config.Reload()` only fires after self-upgrade). Sequence: edit config →
validate it parses (`python3 -c "import yaml,sys; yaml.safe_load(open('~/.pilot/config.yaml'))"`)
→ follow the restart procedure above. Dispatched issues for the new repo sit unpolled until
the restart activates its poller.

## The durable fix (A3) — shipped GH-4311

`internal/singleton` provides an OS-level `flock` on `<Memory.Path>/pilot.lock`
(`LOCK_EX|LOCK_NB`, pid written in, held for process lifetime), acquired in `runPollingMode`
before any adapter wires. A second `pilot start` against the same lock refuses and names the
holder pid; `--replace` SIGTERMs the holder and waits for the lock to release before
acquiring it. `pilot stop` (SIGTERM + wait for release) and `pilot restart` (stop, then
exec into `pilot start` in the same terminal/TTY) round out the single-handoff story. The
manual procedure above remains the belt-and-suspenders check — the lock stops a second
daemon from *running*, but doesn't by itself verify strays like a leftover `--tunnel`
cloudflared process.

Independently, the fix-issue spawn dedup (A1) lives in the shared SQLite store so even a
momentary lock gap during handoff cannot produce duplicates.
