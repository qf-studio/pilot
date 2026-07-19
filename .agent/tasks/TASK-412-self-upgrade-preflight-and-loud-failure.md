# TASK-412: Self-upgrade must preflight binary writability and fail loudly

**Created**: 2026-07-19 · **Status**: 🚀 Dispatched to Pilot · **Last Updated**: 2026-07-19

## Problem (observed 2026-07-19, v2.242.0 auto-upgrade on the founder box)

Auto-upgrade failed twice (19:09Z, 19:14Z) and the **only trace in
`daemon.log` is progress spam stopping at `percent=70 "Creating backup..."`**
— no ERROR line, no alert. Root cause: the binary was root-owned in
root-owned `/usr/local/bin` while the daemon runs as `ec2-user`;
`createBackup()` (`internal/upgrade/upgrade.go:401`) fails on
`os.OpenFile(binaryPath+BackupSuffix, O_CREATE, …)` with EACCES.

Two defects, independent of the specific box:

1. **No preflight**: the upgrader downloads the full release asset (~30MB,
   progress 0→70%) before discovering it can never install. Writability of
   the binary's directory is checkable in microseconds up front.
2. **Silent failure**: the error returned from `Upgrade()` is not logged at
   ERROR level by the auto-upgrade caller and no alert fires. Operator
   learned of it from the TUI, hours later. A failed upgrade means the fleet
   silently diverges from the released version.

(Operational fix already applied on the box: binary relocated to
ec2-user-owned `/var/lib/pilot/bin/pilot`, `/usr/local/bin/pilot` is now a
symlink — the upgrader resolves symlinks, so self-upgrade works there. This
task is the code-side hardening.)

## Deliverables

1. **Preflight in `internal/upgrade`**: before downloading, verify the
   process can create+write a file in `dir(resolvedBinaryPath)` (create and
   delete a probe file, e.g. `.pilot-upgrade-probe`). On failure return a
   typed error (e.g. `ErrBinaryNotWritable`) that names the dir, the
   process uid, and the remediation hint (relocate binary or fix ownership).
2. **Loud failure at the auto-upgrade call site**: whatever invokes the
   upgrader on release detection must log `level=ERROR` with the full error
   and emit an alert through the alerts engine (severity WARNING is fine —
   it is not availability-affecting) so Telegram/Slack sees it.
3. **Progress log hygiene**: per-percent progress currently logs ~5 lines
   per 10ms at INFO (hundreds of lines per second during download). Throttle
   to ≥1s intervals or 10% steps.
4. Table-driven tests: preflight pass/fail (unwritable dir via chmod 0555
   tempdir), error propagation to the caller, alert emission (mock).

## Constraints

- Upgrade flow semantics unchanged (download → backup → install → rollback).
- No new config keys; preflight is unconditional.
- Use `testutil` fake tokens if any test touches token-shaped strings.

## Acceptance

- [ ] Unwritable binary dir → upgrade aborts BEFORE download, ERROR logged,
      alert emitted, typed error names dir+uid+hint
- [ ] Writable dir → flow unchanged, tests green
- [ ] Download progress ≤1 log line per second

## Refs

- Pilot issue: https://github.com/qf-studio/pilot/issues/4468
- `internal/upgrade/upgrade.go` (`Upgrade()`, `createBackup()`)
- Incident: v2.242.0 upgrade failure on founder box 2026-07-19 19:09Z
- TASK-409 (box layout; binary now symlinked from /usr/local/bin)
