---
name: TASK-359 Layer 1 shipped — unified epic finalization
description: TASK-359 Layer 1 (unified epic finalize error contract) merged in v2.166.16; smoke-verified; live Shape A/B/C verification deferred to next SDK extraction batch.
type: learning
originSessionId: nav-start-resume-2026-06-04
---
**TASK-359 Layer 1 SHIPPED in v2.166.16 (PR [#3441](https://github.com/qf-studio/pilot/pull/3441), merged 2026-06-04 11:46 CET).**

Closes the boundary layers of [[learn_pilot_finalization_bottleneck]]. All 4 Pilot-eligible TASK-359 layers now in production:

| Layer | Release | What it fixes |
|---|---|---|
| 2a (#3417) | v2.166.13 | (boundary) |
| 2b (#3418) | v2.166.14 | (boundary) |
| 3a (#3419) | v2.166.14/15 | (boundary) |
| 3b (#3420 → #3438) | v2.166.15 | hasMergedWork DB fallback for Search API lag |
| **1 (#3441) — MANUAL** | **v2.166.16** | **unified epic finalization error contract** |

### What Layer 1 actually changed (the durable Shape A/C fix)

`internal/executor/runner.go` — new `Runner.finalizeEpicBranchPR(ctx, task, git, result)` extracted from the inline epic block (~runner.go:1699). Ordering matches the (already correct) direct path:

```
guard → push → harvest → idempotency → CreatePR → invariant
```

**Before (the warn-only error contract):** push/PR-create failures were `log.Warn`+continue → execution still marked `Success=true` → dispatcher wrote a `completed` row with empty `pr_url` (Shape A — the dominant ~70% finalization failure).

**After:** push/PR-create fail → `result.Success=false` → no `completed` row. Invariant `task.CreatePR && PRUrl=="" ⇒ Success=false` enforced. Pre-create `FindMergedPRByBranch` short-circuit handles Shape C (duplicate PR on already-merged work).

Atomic completion write: `internal/memory/store.go` adds `MarkExecutionCompleted(id, prURL, commitSHA, durationMs)` — single `UPDATE` replacing the prior two-call status+result pattern (dispatcher.go:708).

### Evidence-based deviations from the original spec

1. `FindMergedPRByBranch` was NOT on `GitOperations` (it's on `*github.Client`) → added a `gh`-CLI helper instead (cheaper than threading a `*github.Client` into the executor).
2. Full direct-path extraction needs 4 extra params + rewires the daemon hot path for no correctness gain → focused on **epic-path hardening only**; direct path (already correct) untouched.
3. Original spec Step 7 (tighten `HasCompletedExecution` to require `pr_url`) **REFUTED** — breaks direct-commit rows + `TestTaskCompletionInvariant`. **Deferred**; Layer 1's invariant prevents the bad row at the source.

### Verification status

- ✅ **Smoke green** (2026-06-04): `go test ./internal/executor/ ./internal/memory/`, `make test-short`, `make lint` (0 issues), `go vet`, `gofmt -l` clean. New tests pass: `TestFinalizeEpicBranchPR_{PushFailIsFailure,NoCommitsIsCleanSuccess}`, `TestParseFirstPRURL`, `TestMarkExecutionCompleted{,_EmptyPRUrl}`.
- ✅ **Daemon live**: PID 95677, `pilot start --github --env stage --dashboard`, v2.166.16 (built 09:49 UTC).
- ⏳ **Live Shape A/B/C verification deferred to next SDK batch** (`github` → `linear` → `jira` → `asana` per [[learn_sdk_extraction_recipe]]). The SDK extraction itself IS the verification — re-run the proven `no-decompose` 2-PR recipe per connector and watch finalize.
- ⏳ **Negative test deferred** — covered by unit `TestFinalizeEpicBranchPR_PushFailIsFailure`; live revoke-token test pending.

### How to apply

- When a Shape A symptom recurs in stage (stranded `completed` row with empty `pr_url`), the regression test surface is the new `TestFinalizeEpicBranchPR_*` suite — extend it before patching `runner.go`.
- When extracting the next SDK connector, **observe the finalize log lines** — Layer 1 should now log a hard error on push failure (not a `log.Warn`).
- If [[bug_epic_decompose_work_loss]] (TASK-356 #1) re-occurs, the `no-decompose` workaround is still the path — Layer 1 hardens the epic-path *error contract* but does NOT fix the epic-decompose work-loss bug. Different layer.

[[TASK-359]] [[TASK-356]] [[learn_pilot_finalization_bottleneck]] [[learn_sdk_extraction_recipe]] [[bug_epic_decompose_work_loss]]
