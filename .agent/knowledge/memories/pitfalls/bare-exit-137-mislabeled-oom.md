---
name: bare-exit-137-mislabeled-oom
description: classifyClaudeCodeError labeled every unexplained exit 137/139 "oom_killed" with zero kernel evidence — heartbeat/watchdog self-kills and external SIGKILLs (e.g. the orphan-running sweep) got the same label as a real kernel OOM
type: pitfall
---

# Bare exit 137/139 is not proof of OOM — classifier needs kernel evidence

**What happened (GH-4412, 2026-07-17):** `classifyClaudeCodeError` in
`internal/executor/backend_claudecode.go` treated exit code 137 (SIGKILL) or
139 (SIGSEGV) as `oom_killed` any time the run's `ctx.Err()` was nil — no
dmesg/cgroup check, no other corroborating signal. Two independent kill paths
route around that guard entirely:

1. **Heartbeat-timeout kill** and **watchdog-timeout kill** (both in the same
   file) call `cmd.Process.Kill()` directly rather than cancelling `ctx`, so
   `ctx.Err() != nil` stays false for them. A hung-process kill from Pilot's
   own heartbeat watchdog was mislabeled `oom_killed` even though Pilot
   killed it on purpose.
2. **Any external SIGKILL** — a `kill -9`, a buggy sweep (the parent GH-4412
   story: the orphan-running sweep killing live executions), a genuine
   kernel OOM — all produce the identical exit code 137. Without checking
   dmesg, the classifier can't tell these apart, yet it always answered "OOM".

## Fix
- Track heartbeat/watchdog self-kills with an `atomic.Bool` set at the same
  place `cmd.Process.Kill()` succeeds; feed that into classification as
  `selfKillReason` so it's tagged `shutdown_terminated` (not OOM) same as the
  GH-4105 context-cancellation case.
- For the remaining unexplained-kill case, added `hasKernelOOMEvidence(pid,
  since)` — best-effort `dmesg -T` scan for a `Killed process <pid>` line
  timestamped after the subprocess's start time. Only when that returns true
  does the classifier emit `oom_killed`; otherwise it emits `timeout`
  ("no kernel OOM evidence found") — the same bucket already used for
  stderr-detected signal kills.
- dmesg is frequently inaccessible in production (`kernel.dmesg_restrict=1`
  on hardened hosts) — absence of evidence must never be treated as evidence
  of OOM, so failure/permission-denied/no-match all fold to "unconfirmed",
  never silently default back to OOM.

## Recommended Approach
When a subprocess-killing code path exists outside of `ctx` cancellation
(heartbeat, watchdog, or any future direct `Process.Kill()` call), it must
set an explicit self-kill flag threaded into error classification — don't
rely on `ctx.Err()` alone as the "did we do this on purpose" signal. When
classifying an ambiguous kill signal (SIGKILL/SIGSEGV) as a specific root
cause (OOM, in this case), require positive evidence (dmesg / cgroup
`memory.events` once #4401's per-subprocess cgroup delegation lands) rather
than inferring cause from exit code alone.

## Related
- GH-4412 (this fix) / GH-4412-1, GH-4412-2 (sibling subtasks: orphan-running
  sweep threshold + live-worker check)
- GH-4105 (prior partial fix: ctx-cancellation-aware shutdown_terminated)
- GH-2112 (original OOM-via-exit-code detection)
- `internal/executor/backend_claudecode.go` (`classifyClaudeCodeError`,
  `hasKernelOOMEvidence`, `dmesgHasRecentOOMKill`)
- #4401 (cgroup work — natural home for a `memory.events` oom_kill check
  once subprocesses run inside a delegated cgroup)

---
**Captured**: 2026-07-17
**Confidence**: 0.9
**Concepts**: executor, error-classification, oom, sigkill, heartbeat, watchdog, dmesg, retry
