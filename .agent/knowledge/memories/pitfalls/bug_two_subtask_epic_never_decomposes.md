---
name: Two-subtask epics never decompose — int(n*0.8) threshold collapse (GH-4304)
description: detectSameComponentFromTitles in internal/executor/epic.go truncated its ">80% of titles" threshold with int(), which collapses to 1 for n=2 subtasks — any word in even ONE of two titles then satisfied count>=1, so every 2-child epic was misclassified single-package-scope and direct-executed instead of decomposed. Fixed by rounding the threshold up (ceil via integer math). Recurrence risk if any similar ">=X% of N" gate reintroduces int() truncation for small N.
type: pitfall
---
Root cause of the epic-lifecycle canary's persistent `child-count: 0 child issue(s) created` failures (issue #4265, alert re-triggered on every scheduled run 2026-07-13 through 2026-07-14), which autopilot repeatedly mis-attributed to whatever PR happened to merge just before each scheduled run (most recently GH-4304 → PR #4299, a dashboard-only change with zero relation to epic planning).

**Why:** `detectSameComponentFromTitles` (`internal/executor/epic.go`) computes `threshold := int(float64(len(subtasks)) * 0.8)` then treats any word with `count >= threshold` as evidence all subtasks share one component/package. For `n=2`, `int(1.6) == 1` — so a word appearing in just ONE of the two titles (count=1) already meets `count >= 1`. Since virtually any two real subtask titles share at least one non-stopword ≥3 chars, this made the function return `true` almost unconditionally whenever an epic planned exactly 2 children — collapsing `isSinglePackageScope` to always-true and skipping decomposition entirely (`decomposition_skipped reason=single_package_scope`), even for subtasks touching completely unrelated files (e.g. `version.go` vs `CHANGELOG-CANARY.md`).

The bug was invisible in `TestDetectSameComponentFromTitles` because every existing fixture used n=3 or n=5 subtasks, where the same truncation happens to either not matter (n=5: `int(4.0)==4`, no rounding error) or still under-triggers less severely. Nobody had a 2-subtask fixture.

**How to apply:**
- Any heuristic gate phrased as "≥X% of N items" that computes its integer threshold via `int(float64(n) * pct)` is vulnerable to this same collapse at small N — audit for `int()` truncation vs a proper ceiling when reviewing similar threshold code.
- Fix: `threshold := (len(subtasks)*4 + 4) / 5` (integer-math `ceil(n*0.8)`), not `int(n*0.8)`.
- When a repeating canary/alert issue keeps citing "Original PR: #NNNN" as if each new merge caused the same failure, check the failure signature first (here: `decomposition_skipped reason=single_package_scope`, unrelated to dashboard/HISTORY code) before trusting the autopilot's causal attribution — CI-post-merge issues are filed against whatever merged most recently before a scheduled canary tick, not necessarily the actual cause.
- Test coverage gap to watch for elsewhere in this codebase: any table-driven test for a percentage/ratio threshold should include a case at the smallest N the gate can see in production (here, n=2, since `PlanEpic` can legitimately plan exactly 2 subtasks).
