---
title: Stdout Reader Silent Death on Oversized stream-json Line
created: 2026-07-23
status: active
related: GH-4519, pilot-console#26 (B8 gen-2)
---

# Stdout Reader Silent Death on Oversized stream-json Line

## Problem

B8 gen-2 (pilot-console#26) died after 10m17s with exit 137
`shutdown_terminated`, `stdout_tail` pure base64, all completed work lost —
even though the process was still alive and producing output at the time it
was killed.

## Root Cause

`internal/executor/backend_claudecode.go`'s stdout reader used
`bufio.Scanner` capped at 1MB (`scanner.Buffer(buf, 1024*1024)`). A single
stream-json line over 1MB (e.g. a tool result embedding a base64 blob) makes
`scanner.Scan()` return `false` with `bufio.ErrTooLong` — and the loop had no
`scanner.Err()` check afterward, so this was completely silent.

Once `Scan()` returns false, the goroutine stops calling `Read` on the pipe.
The child process, still mid-write on that oversized line, fills the OS pipe
buffer and blocks in the `write()` syscall — for real, not a bug in the
child. `lastEventAt` (only updated inside the scan loop) freezes at that
moment. 5 minutes later (`DefaultHeartbeatTimeout`), the heartbeat monitor
sees no update and SIGKILLs a process group that was never actually hung —
it was just waiting on a full pipe with nobody reading it. The finished work
already sitting in the model's context is lost along with it.

## Solution

1. Replaced the stdout `bufio.Scanner` with `bufio.NewReaderSize` +
   `readBoundedLine` — a helper built on `(*bufio.Reader).ReadLine()` that
   keeps at most `maxStdoutLineBytes` (1MB) of a line but **keeps draining**
   past that cap instead of aborting, so the pipe never backs up regardless
   of how large a single line gets. Truncated lines get a bounded marker
   (`[line truncated: N bytes] <snippet>`) written to `stdoutTail` instead of
   the raw payload, and are only fed to `parseStreamEvent` if the kept prefix
   happens to still be complete, valid JSON (`json.Valid`) — an oversized
   line's prefix almost never is, since the closing braces got truncated
   away.
2. The heartbeat (`lastEventAt`) now updates on every underlying chunk read
   (`onBytes` callback into `readBoundedLine`), not just on completed lines —
   so heartbeat freeze can no longer happen mid-line even for very large single
   lines.
3. Both the stdout and stderr reader goroutines now log a WARN with the
   terminal read error (skipping plain `io.EOF`) after their loop exits — a
   reader exiting must never be silent again.

## Prevention / Next Time

If a run dies with `shutdown_terminated` and `stdout_tail` looking like raw
base64 or otherwise truncated/binary-ish content with no trailing
stream-json `result` event:

1. Check whether the tail cuts off mid-line with no evidence of a
   `[line truncated:` marker for it — if there's a marker, the new reader
   already recovered gracefully and the failure is something else (the
   marker's reported byte size tells you exactly how big the offending line
   was).
2. If a *new* silent-reader-exit shows up (no WARN log at all despite a dead
   reader), that means some other code path bypassed `readBoundedLine` /
   the `scanner.Err()` check added here — grep
   `internal/executor/backend_claudecode.go` for any additional
   `bufio.Scanner` on the stdout/stderr pipes and apply the same pattern.
3. `maxStdoutLineBytes` (1MB) and `stdoutTruncationSnippetBytes` (256B) are
   deliberately conservative — raising the line cap defeats the point
   (unbounded pipe backpressure risk returns as the cap approaches the OS
   pipe buffer size); raising the snippet size risks the marker itself being
   evicted by `stdoutTail`'s own 64KB tail-truncation policy.
