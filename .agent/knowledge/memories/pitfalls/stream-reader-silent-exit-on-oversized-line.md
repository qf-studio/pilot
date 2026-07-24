---
name: stream-reader-silent-exit-on-oversized-line
description: A stream-reader goroutine that exits without logging scanner.Err() turns one oversized line into a silent wedge — heartbeat freezes, watchdog SIGKILLs, finished work destroyed (B8 gen-2, fixed #4519→PR#4521)
type: pitfall
---

# Stream reader dies silently on oversized line → heartbeat SIGKILL

**What happened (2026-07-23, B8 gen-2 / console GH-26):** the executor's
stdout reader used `bufio.Scanner` with a 1MB line cap
(`backend_claudecode.go`). Claude emitted a stream-json line >1MB (tool
result carrying a base64 blob). `Scan()` returned false with
`bufio.ErrTooLong` and the read loop exited **with no `scanner.Err()`
check** — nothing logged. From there: stdout stopped draining → child
blocked on a full pipe → `lastEventAt` froze → the heartbeat monitor
SIGKILLed the process group 5 minutes later. Exit 137, stdout_tail pure
base64, an entire completed implementation lost. Total run 10m17s.

**Why it bites:** the failure is invisible at every layer. The scanner
error is swallowed; the kill is attributed to "hung process"; the exit
code pattern-matches OOM/stall classes ([[bare-exit-137-mislabeled-oom]]),
sending diagnosis down the wrong path. Any agent subprocess that can echo
file contents or binary data can trigger it.

**How to avoid:**
1. Never let a reader goroutine exit silently — always check and log
   `scanner.Err()` (stdout AND stderr) with task context.
2. Line-capped readers must **drain** oversized lines and continue, not
   abort: `bufio.Reader`-based loop, truncation marker in the tail buffer,
   bounded memory.
3. Keep the heartbeat alive on *bytes flowing*, not only on completed
   lines — a slow flood must not read as a hang.

**Fix:** #4519 → PR#4521 (merged 2026-07-23, on the box with the next
train). Forensic chain in TASK-417 (archived).
