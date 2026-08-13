# fix(observability): expose the running daemon version — hot restarts are invisible to every status surface

**Status**: ✅ MERGED + REVIEWED 2026-08-13 → [pilot#4864](https://github.com/qf-studio/pilot/issues/4864) → **PR#4865** (`7660794f`) — **post-merge review APPROVE** ([verdict](https://github.com/qf-studio/pilot/pull/4865#issuecomment-5280911885)): all 3 legs faithful (gauge on the real exporter from the ldflags var · /health plumbed at both entrypoints · doctor probes config port with graceful degrade); 3/3 mutants killed; upgrade flow untouched. Follow-up candidates (not filed): **D1 minor** — the `payload.Version == ""` degrade leg (health.go:502-505) says "daemon not reachable" for a reachable pre-4865 daemon, wrong wording during exactly the first post-merge upgrade window · N1 gauge absent (metrics 503) when autopilot metrics unwired · N2 probe hardcodes 127.0.0.1, ignores `cfg.Gateway.Host`. D2 (FEATURE-MATRIX date) fixed in docs same day. **Remaining: next train + box restart puts it live → operator repoints `pilot-board-remote` `ver` at `pilot_build_info` → then archive.**
**Created**: 2026-08-13
**Assignee**: Pilot

## Context

The self-upgrade restart leg works — `syscall.Exec` in place
(`internal/upgrade/restart.go:78`), verified at boot by the GH-3600
reconcile (`internal/upgrade/reconcile.go:48`, logged `upgrade verified
complete … via "hot restart"` on every train 07-31 → 08-12). But **no
status surface reports what the running process is**, so three sessions
(08-08 → 08-13) misdiagnosed healthy hot-restarted daemons as "installed
but never restarted", culminating in an unnecessary operator restart on
08-13. The signal set is structurally blind:

- Board `ver` shells the **disk** binary (`/usr/local/bin/pilot version` →
  compiled-in `version` var of the just-installed file, `cmd/pilot/commands.go:438`).
- Board `uptime` is `ps` etime — **survives `syscall.Exec`** (PID
  unchanged), so it cannot detect a hot restart.
- Gateway `/health` hardcodes `"version": "0.1.0"` (`internal/gateway/server.go:557`).
- Metrics (:9091) have no version gauge.
- `pilot doctor` staleness check compares the **invoked** binary's version
  (`internal/health/health.go:416-454`) — run on the box it reads the new
  disk binary and reports "up to date" regardless of the process.
- Staleness alert counts `ReleasesBehind` ≥ 3 (`cmd/pilot/main.go:3735`) —
  a genuinely stale process 1 patch behind would never alert either.

Memory: `hot-restart-preserves-pid-uptime-false-mismatch` (pitfall).

## Implementation

1. **`pilot_build_info` metric**: register a gauge
   `pilot_build_info{version="<version>", commit="<vcs rev>"} 1` on the
   :9091 registry at daemon startup, sourced from the running process's
   compiled-in `version` var (same source as the TUI banner,
   `internal/dashboard/tui.go:1555`). Standard build-info idiom; value
   changes across a hot restart with no PID change.
2. **Real `/health` version**: replace the hardcoded `"0.1.0"` in
   `internal/gateway/server.go:557` with the real version (plumb the
   `version` var; do not shell out).
3. **`pilot doctor` disk-vs-running check**: extend the staleness check to
   fetch the running daemon's version from the local metrics endpoint
   (config port, default :9091) or `/health`, and compare THREE values:
   running process vs disk binary vs latest release. Report
   `running ≠ disk` explicitly ("hot restart pending or failed — check
   daemon.log for 'upgrade verified complete'"). Degrade gracefully (skip
   with a note) when no daemon is reachable.

## Acceptance

- `curl -s localhost:9091/metrics | grep pilot_build_info` returns the
  running version + commit; unit test asserts the gauge is registered and
  labeled from the version var.
- `/health` returns the real version; test updated (no "0.1.0" literal
  left in `internal/gateway/`).
- Doctor: table-driven tests for the three-way comparison — running=disk
  (ok), running≠disk (explicit mismatch wording), daemon unreachable
  (graceful skip). No change to upgrade-flow behavior.
- Existing upgrade/reconcile suites pass unchanged.

## Out of scope (recorded, not this issue)

Latent defects found during the same research, none implicated in a live
failure: `state.Save` is the last fallible step AFTER the binary is
committed (`internal/upgrade/graceful.go:137-139` — an error there aborts
before the restart leg); repeat-attempt backup poisoning
(`internal/upgrade/upgrade.go:458-482` copies the already-new disk binary
over `.backup`); `ReleasesBehind` threshold is count-based, blind to age.
Operator follow-up once shipped: point `/usr/local/bin/pilot-board-remote`'s
`ver` at the build-info metric instead of the disk binary.

## Refs

- Issue: https://github.com/qf-studio/pilot/issues/4864
- Research map: self-upgrade flow checker.go:112 → main.go:3718 →
  hot.go:109 → graceful.go:75 → restart.go:46 → reconcile.go:48 (session
  2026-08-13).
- Misdiagnosis record: 08-11/08-12 markers + DEVELOPMENT-README (signature
  retired 08-13) · memory `hot-restart-preserves-pid-uptime-false-mismatch`.
