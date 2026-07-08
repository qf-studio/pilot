---
name: --replace restarts SIGKILL in-flight executions and mislabel them oom_killed
description: pilot start --replace sends a bare SIGTERM via pgrep (cmd/pilot/commands.go killExistingTelegramBot) with no drain coordination; runner.CancelAll() SIGKILLs the live Claude subprocess ~1s later. Exit 137 is unconditionally classified oom_killed (backend_claudecode.go), the infra-noise path silently drops the in-progress label, and stale-recovery writes NO execution_events — so the row looks like it just stopped logging. All in-flight work is discarded and re-run from scratch. Open fixes: #4100 (drain + classifier), #4101 (event trail).
type: pitfall
---

Observed 2026-07-08 (execution `5ce9bc2c`, GH-4050): a 10:26 UTC `--replace`
killed ~10 minutes of epic planning; the re-run at 17:18 cost $3.49 / 41k
output tokens on its own. Recurs on EVERY restart that lands mid-execution.

**Why:** `--replace` restarts kill in-flight work three ways at once —

1. `killExistingTelegramBot` (`cmd/pilot/commands.go`) fires a cross-process
   `SIGTERM` via `pgrep` with zero dispatcher drain; the shutdown path gets
   ~1s before `runner.CancelAll()` SIGKILLs the Claude subprocess.
2. Exit 137/139 is unconditionally classified `oom_killed`
   (`internal/executor/backend_claudecode.go`) — restart kills pollute the
   DB/dashboard as OOM incidents.
3. `recoverStaleRunningTasks`/`recoverStaleQueuedTasks` and the dispatcher's
   infra classification mutate `executions.status` WITHOUT writing
   `execution_events` — the audit trail has no terminal event, so
   root-causing requires raw daemon.log + store SQL cross-referencing.

**Diagnosis shortcut:** a row whose `execution_events` stream stops without
a terminal event + `error` mentioning oom/context-canceled + a daemon
restart at that timestamp = restart kill, not OOM.

**Debugging note:** `adapter_processed` is an UPSERT — earlier mark/unmark
history for an issue is unrecoverable from the live DB.

Fixes queued: #4100 (graceful drain via existing `internal/upgrade/graceful.go`
machinery + explicit "terminated by --replace" classification), #4101
(events on every recovery transition). The GracefulUpgrader drain already
exists for self-directed hot-upgrades — `--replace` just bypasses it.

Related: [[pattern_selfheal_duplicate_completed_rows]] (how these orphans
surface as duplicate completed rows after the retry merges).
