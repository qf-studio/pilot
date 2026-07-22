---
title: Stall Watchdog — Silent Model Turn Class of Fixes
created: 2026-07-22
status: active
related: GH-4501, GH-4357, GH-4364, pilot-console#24, TASK-416
---

# Stall Watchdog — Silent Model Turn Class of Fixes

## Problem

The stall watchdog (`internal/executor/watchdog.go`) kills a healthy session
whenever no stdout line arrives for `stall_timeout` (default 3m). The reset
logic itself has always been correct — `EventHandler` fires on every raw
stdout line, unconditionally, and resets the idle clock regardless of parsed
event type (`runner.go`, `EventHandler` closure at the backend.Execute call
site). The recurring defect class is *upstream*: some legitimate activity
produces zero stdout for longer than `stall_timeout`, so there's nothing to
reset the clock on.

Two instances so far:

- **#4357** (background tasks): a backgrounded Bash command / sub-agent emits
  no events until it completes. Fixed by tracking in-flight
  `task_started`/`task_notification` pairs and suspending the idle clock
  while any are outstanding.
- **#4501** (silent model turns): a single high-effort/thinking turn produces
  ONE complete `assistant` stream-json event at the very end, with zero bytes
  written during the turn itself — because the CLI wasn't invoked with
  `--include-partial-messages`. No CLI signal exists to suspend on, unlike
  background tasks.

## Root Cause (GH-4501 specifically)

`claude` is spawned with `--verbose --output-format stream-json` but without
`--include-partial-messages` (three call sites in
`internal/executor/backend_claudecode.go`, `executeWithFromPR`). Without that
flag, the CLI batches an entire turn into one `assistant` event emitted only
at turn end.

## Solution

1. Add `--include-partial-messages` to all three spawn-arg sites. The CLI
   then streams a `{"type":"stream_event","event":{"type":"..."}}` wrapper
   (`message_start`, `content_block_start/delta/stop`, `message_delta`,
   `message_stop`) throughout the turn. Any line — including these — already
   resets the watchdog via the existing unconditional `EventHandler` call, so
   no watchdog-side change was needed for this half of the fix.
2. `parseStreamEvent` classifies these as `EventTypeStreamDelta`, a type with
   deliberately **no case** in `processBackendEvent`'s switch — this is what
   keeps them from being re-processed as complete `assistant`/`tool_use`
   events (double counting) and from generating per-delta log lines (no log
   call fires for an unhandled switch case).
3. Defense-in-depth: `effortAwareStallTimeout` (`watchdog.go`) raises the
   floor to 10m for `selectedEffort == "high"` or `complexity ==
   ComplexityComplex`, in case a turn produces genuinely zero stdout of any
   kind (not even a partial delta) for an extended stretch. An explicit
   `stall_timeout_ms` config higher than the floor always wins; disabled
   stall detection (negative `StallTimeoutMs`) is never re-enabled by this.

## Prevention / Next Time

If a new "healthy session killed as stalled" incident surfaces:

1. Check whether the activity in question emits **any** stdout at all during
   its silent stretch (`stdoutTail` capture in the execution's replay log or
   `StdoutTail` on a failed result, GH-4395). If yes, the reset logic is
   almost certainly fine — look upstream at what's suppressing output.
2. If it's a backgrounded task class → mirror #4357 (track start/notification
   pairs, suspend the idle clock).
3. If it's a silent-CLI class (nothing to suspend on) → mirror #4501: find
   the CLI/backend flag that turns silence into a heartbeat, and/or raise the
   effort/complexity-aware floor in `effortAwareStallTimeout`.
4. Verify against the *installed* CLI version — flag availability and event
   shapes are not guaranteed stable across `claude` CLI releases; run
   `claude --help` and a smoke invocation before assuming a flag exists.
