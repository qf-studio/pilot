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

**How to apply:** when a webhook/gateway/pilot/alerts PR's `test` job fails *only* on
`TestGeneratorGenerate` (or another `briefs` integration test) and the diff doesn't touch `internal/briefs`,
treat it as flaky: confirm with a couple of local `-count` runs, then `gh run rerun <run-id> --failed`
and proceed to merge on green. If it ever starts failing deterministically, the fix is a real task
(seed the blocked-task fixture deterministically / isolate the temp SQLite db). Related:
[[learning_audit_safeguard_bypass]].
