---
title: Heartbeat Monitor Killing Healthy Runs During Long Local Tool Calls
created: 2026-08-03
status: active
related: GH-4668, GH-4648, GH-4649, GH-4521, GH-4401, TASK-437
---

# Heartbeat Monitor Killing Healthy Runs During Long Local Tool Calls

## Problem

`ClaudeCodeBackend`'s heartbeat monitor (`internal/executor/backend_claudecode.go`)
kills the subprocess whenever `last_event_age > heartbeatTimeout` (default 5m).
That signal is wrong whenever a *local tool call* (e.g. `make test`, `go
build`) legitimately runs longer than the timeout: the stream-json protocol
emits nothing between the `message_stop`/`tool_use` line that starts the tool
and the tool-result line that ends it — local execution is silent by design,
not by malfunction.

Two healthy runs (GH-4648, GH-4649, 2026-07-31) were SIGKILLed mid-`make
test` this way, 22+ minutes and thousands of stream events destroyed each
time; the retry storm they triggered produced a duplicate-PR race
(TASK-437). This is a distinct class from GH-4521 (stall watchdog killed by
silent model *turns*) and GH-4401 (RLIMIT_AS child kills) — same "healthy
child killed" family, different mechanism.

## Root Cause

`last_event_age` (time since the last stdout byte) was the *only* liveness
signal. It cannot distinguish "hung" from "busy with a silent local tool,"
and the pilot repo's own `go test ./...` (3.5–6+ min on the shared
t3.xlarge box) is squarely in the danger zone of the 5m default.

## Solution

Before killing on `last_event_age > timeout`, check **process-group
liveness**, not just stdout silence:

1. `internal/executor/heartbeat_monitor.go` — `heartbeatMonitor.evaluate()`
   is the pure, fake-clock-testable decision function. Given the current
   silence age, it either does nothing (age within timeout), grants grace
   (process group has live descendants and/or advancing CPU ticks), or
   kills (idle group, probe error, or the task-level watchdog deadline was
   reached — that hard backstop is never extended by grace).
2. `internal/executor/heartbeat_liveness_linux.go` —
   `probeProcessLiveness(pgid)` scans `/proc/*/stat` for every process
   sharing the tracked subprocess's process group id (== its own PID, since
   `configureProcessGroup`/GH-4503 makes it its own group leader via
   `Setpgid`), returning descendant count and summed `utime+stime`.
3. `internal/executor/heartbeat_liveness_other.go` — non-Linux platforms
   (no `/proc`) always report zero descendants/ticks with a nil error, which
   reproduces exactly today's kill-on-silence behavior — a deliberate
   degrade, not a gap.
4. The **watchdog goroutine's own absolute deadline (`opts.WatchdogTimeout`,
   typically 30m/1h) still exists unchanged** as the true backstop; the
   heartbeat monitor additionally checks that same deadline itself before
   granting grace, so the two mechanisms can never disagree even though
   they run as separate goroutines.
5. Probe failure (e.g. `/proc` unreadable) fails **toward** killing — an
   unreadable process tree is not evidence of liveness.

## Prevention / Next Time

If a new "healthy session killed by the heartbeat monitor" incident
surfaces:

1. Check the kill log line — post-GH-4668 it always includes `reason=`
   (`no_activity` / `probe_error` / `watchdog_deadline`) and
   `descendants=N cpu_delta_ticks=X`. `probe_error` points at `/proc`
   access, not at the child being hung.
2. If the tool call in question backgrounds work *outside* the tracked
   process group (e.g. `nohup`/`setsid` inside the child, or a tool that
   reparents its own children away from the group), `probeProcessLiveness`
   will see zero descendants even though real work is happening — the pgid
   assumption (descendant pgid == leader pid) is the load-bearing
   invariant; verify it still holds for the new tool.
3. Do not "fix" a new instance by raising `DefaultHeartbeatTimeout` alone —
   any fixed timeout eventually loses to a long-enough suite. Liveness is
   the correct signal; a modest timeout bump is fine as a complement, not a
   substitute.
