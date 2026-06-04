---
name: TASK-359 Layer 1 shipped — unified epic finalization
description: TASK-359 Layer 1 (unified epic finalize error contract) merged in v2.166.16; smoke-verified AND Shape A/C live-verified on the v2.166.16 daemon via studio-sdk #63/PR#64 (2026-06-04). Destructive Shape B + negative token-revoke remain deferred.
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
- ✅ **Daemon live**: `pilot start --github --env stage --dashboard`, v2.166.16 (built 09:49 UTC).
- ✅ **Shape A + C LIVE-VERIFIED** (2026-06-04, v2.166.16 daemon): dispatched bounded studio-sdk issue [#63](https://github.com/qf-studio/studio-sdk/issues/63) (`add doc.go to github connector`) via board #1 `Todo`. Daemon executed (exec `af2bbf8b`) and finalized in ~100s **clean**: execution row `status=completed` **with** non-empty `pr_url` (`.../studio-sdk/pull/64`) + `commit_sha` — i.e. the atomic `MarkExecutionCompleted` write, **no stranded empty-`pr_url` row** (Shape A). Exactly **one** PR on `pilot/GH-63` (no Shape C duplicate); PR scope bounded to the single `doc.go` (+5/−0). Board card `Todo → In Progress → In Review`; `pilot-in-progress` label cleared (no orphan In-Progress).
- ⏳ **Destructive Shape B (force-close vs human recovery PR), external-merge Shape C, and negative token-revoke remain deferred** — intentionally out of scope for the non-destructive live run; Shape B/negative are unit-covered (`TestFinalizeEpicBranchPR_PushFailIsFailure`).
- ⚠️ **Manual-merge caveat reconfirmed**: PR #64 was merged by hand (`gh pr merge`) → the daemon's on-merge board writeback did **not** fire (board stuck `In Review` ~4.5min); set `Done` by hand. Known carry-forward (manual merges skip on-merge writeback), NOT a Layer 1 regression — the executor finalize path is what Layer 1 fixed and it passed.

### How to apply

- When a Shape A symptom recurs in stage (stranded `completed` row with empty `pr_url`), the regression test surface is the new `TestFinalizeEpicBranchPR_*` suite — extend it before patching `runner.go`.
- When extracting the next SDK connector, **observe the finalize log lines** — Layer 1 should now log a hard error on push failure (not a `log.Warn`).
- If [[bug_epic_decompose_work_loss]] (TASK-356 #1) re-occurs, the `no-decompose` workaround is still the path — Layer 1 hardens the epic-path *error contract* but does NOT fix the epic-decompose work-loss bug. Different layer.

[[TASK-359]] [[TASK-356]] [[learn_pilot_finalization_bottleneck]] [[learn_sdk_extraction_recipe]] [[bug_epic_decompose_work_loss]]
