---
name: Flaky briefs/TestGeneratorGenerate reds CI on unrelated PRs
description: internal/briefs TestGeneratorGenerate intermittently fails "expected 1 blocked task, got 0" — a SQLite/timing flake, not a real failure. Re-run the job; don't chase it on unrelated PRs.
type: learning
---

`go test ./internal/briefs/ -run TestGeneratorGenerate` intermittently fails in CI with
`generator_test.go:100: expected 1 blocked task, got 0` (and a sibling warning about leftover
`pilot.db`/`-shm`/`-wal` in the temp dir). It is a SQLite-integration / ordering-or-timing flake:
on TASK-333 (#3325) it failed once, then passed 7/7 locally (`-count=5`) and passed clean on a
plain `gh run rerun --failed`. The PR touched only `internal/gateway` + `internal/pilot` — nothing
in `briefs` — so the red was pure noise.

**Why:** matches the marker's standing note that most CI red on this repo is flake/phantom-block/stall
noise, not failed deliveries. Burning time root-causing it on an unrelated PR is wasted effort.

**Second symptom (2026-06-01, TASK-322 Wave-3 D4 / PR #3358):** the sibling test
`TestGeneratorWithProjectFilter` flaked with `panic: runtime error: index out of range [0] with length 0`
(not the "expected 1 blocked task" assertion — same package, different surface). The PR touched only
`internal/memory/graph.go` (KG atomic write), nothing in `briefs` → pure flake. **But the autopilot
treated the red as real, closed the PR, and spawned a phantom CI-fix issue (#3359) chasing a non-bug** —
the unfixed **B4 premature-CIFailure** problem (TASK-322 #3346) compounding the flake into a wasted
fix-cascade. So this flake doesn't just cost a rerun; on the daemon it can burn a whole fix-issue loop.

**How to apply:** when a webhook/gateway/pilot/alerts/memory PR's `test` job fails *only* on a
`internal/briefs` test (`TestGeneratorGenerate`, `TestGeneratorWithProjectFilter`, assertion OR panic)
and the diff doesn't touch `internal/briefs`, treat it as flaky: confirm with a couple of local `-count`
runs, then `gh run rerun <run-id> --failed` and proceed to merge on green. If the daemon already spun a
CI-fix issue off the flake, kill that cascade rather than letting it "fix" correct code. If it ever fails
deterministically, the real fix is a task (seed the blocked-task fixture deterministically / isolate the
temp SQLite db / guard the empty-slice index). Related: [[learning_audit_safeguard_bypass]].
