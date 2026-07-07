---
name: stress-test-unbounded-wait-loop
description: TestMemory_LargePayloads used a raw unbounded busy-wait loop instead of the waitForProcessed(t, ...) helper, so any stall in dispatch hung the goroutine until the 600s package timeout killed the whole stress suite — sniping 3 unrelated PRs (#3918, #3955, #3960).
type: pitfall
---

**Every wait-for-processed-count loop in `stress/memory_test.go` must use the
`waitForProcessed(t, get, want, timeout)` helper** (added by TASK-353), never
a raw `for get() < want { time.Sleep(...) }` loop with no deadline.

**Why:** TASK-353 introduced `waitForProcessed` specifically to bound these
loops after they caused whole-package 600s timeouts, but the migration missed
`TestMemory_LargePayloads` (line ~342) — it kept the old unbounded form. When
the underlying poller dispatch stalled (a real but separate concurrency issue,
tracked as GH-3959 / mem-048 family), the test goroutine parked in
`time.Sleep` at `memory_test.go:343` for the full 10 minutes, killing the
`test` CI job package-wide. This sniped three unrelated PRs whose diffs never
touched `stress/`: #3918 (GH-3903), #3955 (GH-3952 alerts wiring), and #3960
(same branch, retry). Each looked like a real CI failure to the autopilot
fix-request bot, but the fix-request logs only captured the runner
provisioning preamble, not the panic — see
[[ci-fix-request-excerpts-head-of-log-instead-of-failure]].

**How to apply:**
1. When adding or reviewing a wait loop in `stress/*_test.go`, always call
   `waitForProcessed(t, func() int64 { return atomic.LoadInt64(&counter) },
   want, timeout)` — never hand-roll the loop.
2. If a new test needs a different wait condition (not a simple counter),
   still bound it with a `deadline := time.Now().Add(timeout)` check inside
   the loop (see `TestMemory_RepeatedStartStop`'s per-cycle pattern) so a
   stall fails fast with a clear message instead of riding to the package
   timeout.
3. This is a symptom fix, not a root cause fix — the actual dispatch stall in
   the poller is tracked separately by GH-3959 (open as of 2026-07-07,
   scoped to `stress/` root-cause + no-decompose).

Related: [[poller-api-calls-deadlock-stress-suite]] (same stress-suite-timeout
failure mode, different root cause); GH-3959 (root-cause task, still open);
GH-3961 (this fix).
