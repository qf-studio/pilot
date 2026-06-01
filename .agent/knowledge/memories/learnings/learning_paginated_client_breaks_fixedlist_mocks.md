---
name: Adding pagination to a client silently breaks fixed-list httptest mocks (600s CI timeout)
description: When you change a client method to paginate (per_page+page until a short page), every httptest mock that returns the SAME full list for every request now loops to maxPages and returns N×pages items — starving callers and hanging tests to the package timeout. Make mocks return [] for page>=2, and bound test busy-waits with a deadline.
type: learning
metadata:
  type: learning
---

C6 (TASK-346, #3369) changed `github.Client.ListIssues` to paginate:
`per_page=100&page=N` until a page returns `< perPage` items (`maxPages=50`). This silently
**broke the `stress` package mocks** (`stress/memory_test.go`): they return the full 1000-issue
list for *every* request, so post-C6 `ListIssues` requested page 1 (1000 ≥ 100 → keep going),
page 2 (1000 again), … to page 50 → ~50k items per poll. The parallel poller then decoded a
50k-element list per cycle, the dispatch starved, and the test's **unbounded** busy-wait
`for processedCount < numIssues { time.Sleep(50ms) }` hung until the **600s whole-package
timeout** — failing the entire CI run and emailing a failure on every unrelated PR + rerun.
(`TestMemory_ProcessedMapGrowth`: 0/1000 in 25s post-C6; 0.8s after the mock fix.)

**Why it's sneaky:** the client change compiles, the client's own unit tests pass (their mocks
were updated), but distant integration/stress mocks in another package that hand-roll a fixed-list
HTTP handler are not covered by the client's tests and only fail at the slow package timeout —
which reads as "flaky stress test", not "pagination regression".

**How to apply:**
1. When you make a client method paginate, grep for **every** httptest handler that serves that
   endpoint (`grep -rn "Encode(issues)\|/issues" --include=*_test.go`) and make them
   pagination-terminating: return the list on `page=1`/absent and `[]` for `page>=2`
   (`if p := r.URL.Query().Get("page"); p != "" && p != "1" { w.Write([]byte("[]")); return }`).
2. **Never leave a test busy-wait unbounded.** `for cond { sleep }` must have a deadline that
   `t.Fatalf`s — a hang then costs the 600s package timeout (sinking the whole run + spamming
   failure emails) instead of a fast, localized failure. See `stress/memory_test.go waitForProcessed`.
3. A 600s `FAIL <pkg>` with no `--- FAIL: <Test>` line is almost always a hang/timeout, not an
   assertion — look for an unbounded wait, not a logic bug. Related:
   [[learning_flaky_briefs_generator_test]].
