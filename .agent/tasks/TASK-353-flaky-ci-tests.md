# TASK-353: stop the flaky-CI noise — stress pagination-mock + bounded waits + briefs panic guard

**Status:** in progress (MANUAL, test-only) · **Created:** 2026-06-01 · **Found via:** TASK-322 Wave-3 manual batch — the `stress` + `briefs` packages reded CI across all 6 fix PRs + reruns, generating a flood of failure emails.

## Context

Two test issues spammed CI during the Wave-3 batch:

1. **`stress` package — broken by C6 (TASK-346), not just flaky.** C6 made `ListIssues`
   paginate (`per_page=100&page=N` until a short page). All 4 stress mocks in
   `stress/memory_test.go` return the full 1000-issue list for **every** request, so
   post-C6 `ListIssues` loops to `maxPages=50` (≈50k items/poll) and the poll starves →
   the unbounded wait loop `for processedCount < numIssues { sleep }` hangs to the
   **600s whole-package timeout**, failing the entire CI run. Verified locally:
   `TestMemory_ProcessedMapGrowth` processed 0/1000 in 25s post-C6; 0.79s after the mock fix.
2. **`briefs` package — rare SQLite timing flake + a panic.** `TestGeneratorWithProjectFilter`
   used non-fatal `t.Errorf` on `len(brief.Completed) != 1` then indexed `brief.Completed[0]`
   → `panic: index out of range [0] with length 0` on the rare flake. (DB is already isolated
   via `os.MkdirTemp`; the "got 0" timing flake is rare — 10/10 locally — and per
   `learning_flaky_briefs_generator_test` is "rerun, don't chase".)

## Approach (test-only)

1. **stress:** make all 4 mocks pagination-aware — full list on `page=1`, empty `[]` on
   `page>=2` so `ListIssues` pagination terminates. Add a `waitForProcessed(t, get, want, timeout)`
   helper that bounds the busy-wait and `t.Fatalf`s on deadline instead of hanging to 600s
   (converts any future starvation into a fast, localized failure that doesn't sink the whole run).
2. **briefs:** change the `len(brief.Completed) != 1` check to `t.Fatalf` so the rare flake
   fails cleanly instead of panicking on the `[0]` access.

## Acceptance

- [x] `go test ./stress/` green and fast (no 600s timeout); `TestMemory_ProcessedMapGrowth` ~1s
- [x] all 4 stress mocks return `[]` for `page>=2`; busy-waits bounded with a deadline
- [x] `briefs` index-out-of-range panic guarded (Fatalf before `[0]`)
- [ ] CI green on the PR (whole-repo `-race`)

## Refs

- Regression source: C6 / PR #3369 (TASK-346) pagination
- `learning_flaky_briefs_generator_test`, TASK-342 kickoff gate #5 (stress starvation)
- Files: `stress/memory_test.go`, `internal/briefs/generator_test.go`
