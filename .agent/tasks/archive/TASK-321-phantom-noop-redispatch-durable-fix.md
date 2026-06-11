# TASK-321: Phantom `pilot-blocked` on already-merged work — durable fix

**Status:** ⚠️ **SUPERSEDED by [TASK-359](TASK-359-daemon-finalization-hardening.md) (2026-06-03)** — the four PRs proposed below are absorbed into TASK-359 layers (see mapping at the end). Keep this doc as the **root-cause record** for the dispatch-idempotency / no-op-guard symptom; do not file the PRs from here. **Active plan: TASK-359.**
**Priority:** P1 — autopilot marks *completed* work as failed; pollutes the board/queue and erodes trust in the signal
**Repo:** `qf-studio/pilot`
**Origin:** 2026-05-29 — 7 queue entries (#3238/3240/3243/3244/3252/3253/3257/3260) all failed with the same error after their PRs had already merged. Investigated via Navigator research (2 agents). Sibling of TASK-320 (which shipped Layers A+B1) and TASK-288 (poller false-positive).

---

## Symptom

Dashboard queue shows tasks as `✗ failed` / `pilot-blocked` even though their PRs merged to `main`. Every failure carries the identical error:

```
no new commit produced — worktree HEAD matches base branch parent
```

All work was confirmed present on `main` — **zero work lost**. The red is entirely a misclassification of completed work.

## Root cause (verified on `origin/main` @ v2.160.1)

An issue is **dispatched a second time after its PR already merged**. The executor builds a worktree from `origin/main` that already contains the merged change → makes no new commit → the ghost-SHA guard fires and is classified as a permanent failure.

```
runner.go:2365  pre-push guard   ┐ commitSHAIsNew()==false (HEAD ancestor of origin/main)
runner.go:3286  post-push guard  ┘   → result.Success=false
                                      → result.Error="no new commit produced — …"
runner.go:38    permanentFailurePatterns contains "no new commit produced"  (GH-3230)
handlers.go:409 IsPermanentFailure==true → AddLabels(pilot-blocked) + failure comment
                (note: handler does NOT close the issue)
```

**The core defect — the guard is semantically blind.** `git_freshness.go:commitSHAIsNew` only asks *"is this SHA already an ancestor of origin/base?"* It cannot distinguish:

| Case | What happened | Correct outcome | Current outcome |
|---|---|---|---|
| (a) model false-negative | Model read code, judged "looks correct," refused to edit a spec'd change | retry / escalate (TASK-320 Layer B2) | `pilot-blocked` |
| (b) legitimate empty diff | Work was **already merged to main** before this run started | **Success → close issue** | `pilot-blocked` |

There is **no executor code path that returns `Success=true` for the already-merged case.** That is the bug.

### Three feeder gaps that let case (b) occur

1. **Merge→done timing window.** `pilot-in-progress` is removed *synchronously* in the handler callback (`handlers.go:312`), but `pilot-done` is applied *asynchronously* up to ~60s later by the autopilot ticker (`controller.go:2186`, `handleMerging` label sequence `controller.go:1196`/`1199`/`1208`). The 30s poll tick (`main.go` poll_interval) can find the issue *open, `pilot`-labeled, no in-progress, no done* in that window.
2. **`hasMergedWork` skipped for fresh candidates.** The correct pre-dispatch guard is only consulted inside the retry/restart branches (`poller.go:828`, `:1082`, inside `if processed {}`). A fresh candidate — e.g. after a daemon restart, or after `unmarkProcessed` (`poller.go:1224`) deleted the durable `adapter_processed` row on a failed no-op run — bypasses it.
3. **Sequential vs parallel asymmetry.** `HasCompletedExecution` (`poller.go:1101`) exists only in `checkForNewIssues` (parallel mode); the sequential `findOldestUnprocessedIssue` (`poller.go:720`) lacks it. And a no-op writes a `status='failed'` execution row, so even that guard (which queries `status='completed'`) wouldn't match.

### What already shipped (and why it wasn't enough)

- **GH-3230 / TASK-320 Layers A+B1** (`c5e7e489`, on main): prompt directive `EvidenceBackedSpecDirective` + terminal classification of `"no new commit produced"`. Targets case (a); makes case (b) *deterministically blocked* instead of retried. Confirmed reaches sub-issues (`epic.go:1487 → executeWithOptions → BuildPrompt`, directive at `prompt_builder.go:87` before the LocalMode branch).
- **GH-3240 dedup** (`feb43621`/#3256, merged **18:19:33**, on main): `subIssuePollerSkip` (`runner.go:331/388/840`, `epic.go:1298`) marks epic-created sub-issues in the poller. Closes the **epic→poller** race only. Failures continued *after* it merged (e.g. #3260 at 19:12), proving the merged-work/timing/restart gaps are separate and still live.
- **TASK-320 Layer B2** (escalated in-executor re-invocation): explicitly **deferred**, never shipped.

## Proposed fix (decompose into PR-sized issues)

**PR-1 — Executor: already-merged ⇒ success (the core fix).**
At both guard sites (`runner.go:2365`, `:3286`), before setting `Success=false`, query whether a merged PR already exists for this task/issue (reuse `hasMergedWork`'s search or `IsTaskShipped`). If yes: set `Success=true`, leave `CommitSHA` empty, add a `result.AlreadyShipped` signal, and let the handler **close the issue + mark `pilot-done`** instead of `pilot-blocked`. Keep the permanent-failure path only for the genuine no-PR no-op. *Files:* `internal/executor/runner.go`, `internal/executor/git_freshness.go` (or a new helper), `cmd/pilot/handlers.go` (success-with-no-commit branch → close issue).

**PR-2 — Poller: guard fresh candidates + both modes.**
Call `hasMergedWork` on fresh candidates in `findOldestUnprocessedIssue` (`poller.go:720`), not just the retry branch; add the same guard to sequential mode that parallel mode has. *Files:* `internal/adapters/github/poller.go`.

**PR-3 — Dedup durability: don't unmark on permanent failure.**
On a permanent/no-op failure, do **not** `unmarkProcessed` (`poller.go:1224`) — retain the `adapter_processed` row so a restart doesn't re-dispatch. (Issue is `pilot-blocked` anyway; keep the durable marker as defense-in-depth.) *Files:* `internal/adapters/github/poller.go`, `internal/autopilot/state_store.go`.

**PR-4 — Close the merge→done window.**
In `handleMerging` apply `pilot-done` + close *before or atomically with* the in-progress removal observed by the poller, OR have the handler set the durable processed marker at merge time. *Files:* `internal/autopilot/controller.go`, `cmd/pilot/handlers.go`.

## Acceptance criteria

- [ ] An issue whose PR already merged, when dispatched again, ends as **closed + `pilot-done`** (or skipped pre-dispatch) — never `pilot-blocked`.
- [ ] A genuine no-op (no merged PR, model refused a spec'd edit) still ends `pilot-blocked` (TASK-320 behavior preserved).
- [ ] Daemon restart mid-window does not re-dispatch a shipped issue (durable marker retained).
- [ ] Sequential and parallel poller modes apply the same merged-work guard.
- [ ] Table-driven tests: already-merged→success; genuine-no-op→blocked; restart-after-merge→skip. `make test` + `make lint` green.

## ⚠️ Handoff caution

Handing this to Pilot is **recursively risky**: PR-1 edits the very no-op guard that mis-fires, and the executor implementing it could trip its own guard. Recommend: implement PR-1 in a tightly-scoped issue with explicit before/after code (carry the `EvidenceBackedSpecDirective`), review the diff carefully, and merge manually. PR-2/3/4 are lower-risk and safe for the normal `pilot` loop.

## Cross-refs

- [[TASK-320-executor-false-negative-noop-fix]] — shipped Layers A+B1; B2 still deferred.
- TASK-288 (poller false-positive, archived), TASK-298 (`adapter_processed` consolidation).
- Research agents (2026-05-29): dispatch-idempotency + no-op-classification traces with file:line evidence.

---

## ⇒ Mapping into TASK-359 (2026-06-03)

TASK-359's research surfaced the same symptom from a different angle (studio-sdk extraction Shapes A/B/C, 9/10 connectors) and identified the **structural** cause (epic vs direct finalize-path divergence in `runner.go`) that TASK-321 missed. The 4 PRs proposed above land inside TASK-359:

| TASK-321 PR | Absorbed into |
|---|---|
| PR-1 — already-merged ⇒ success in the guard | **TASK-359 Layer 1** — pre-create `FindMergedPRByBranch` check inside the unified `finalizeExecution()` (`runner.go`) |
| PR-2 — guard fresh candidates + sequential mode | **TASK-359 Layer 3** — `hasMergedWork` DB fallback (`poller.go:1596`); plus the sequential-mode guard remains a clean follow-up that can ship as part of the same Pilot issue |
| PR-3 — don't unmark on permanent failure | **TASK-359 Layer 2** — `InvalidateCompletion` on `pilot-retry-ready` re-dispatch (`poller.go:1748–1862`) addresses the same durable-marker concern from the other end |
| PR-4 — close merge→done window | **TASK-359 Layer 3** — ungate `ScanRecentlyMergedPRs` (`controller.go:2480–2485`) makes the window observable; close-on-merge atomicity is downstream of that |

**Net change vs TASK-321:** the fix now goes through one unified `finalizeExecution()` instead of two separate guard-site patches (PR-1 at `runner.go:2365` + `:3286`). Better blast-radius profile + closes Shapes A & B that TASK-321 didn't see.
