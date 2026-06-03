# TASK-359: Daemon finalization hardening — close Shapes A/B/C

**Status:** 🟢 boundary fixes COMPLETE (4/4 Pilot-eligible layers shipped, v2.166.13–16). **Layer 1 IMPLEMENTED (MANUAL)** on `fix/task-359-layer1-finalize` (2026-06-03) — see "Layer 1 — as built" below. Updated 2026-06-03.

---

## Layer 1 — as built (`fix/task-359-layer1-finalize`, 2026-06-03)

Implemented as a **focused hardening of the epic finalization path** (not a full
direct-path extraction). A `navigator-research` pass refuted two of the original
plan's assumptions and flagged a third as high-risk; the build follows the
evidence:

| Original step | Finding | As built |
|---|---|---|
| 4 — `git.FindMergedPRByBranch` on `GitOperations` | **REFUTED** — that method is on `*github.Client`; the executor holds no github client | Added a new `GitOperations.FindMergedPRByBranch(ctx, branch)` shelling `gh pr list --head <branch> --state merged --json url` (same `gh` dependency `CreatePR` already uses) + pure `parseFirstPRURL` helper |
| 1–2 — extract direct path into `finalizeExecution(ctx,task,path,result,isEpic)` | **PARTIAL** — epic block lacks `git`/`log`/`recorder`/`backendResult` in scope; full extraction needs 4 extra params and rewires the daemon's hot path | New `Runner.finalizeEpicBranchPR(ctx, task, git, result)` gives the **epic** path the direct path's error contract. Direct path (`runner.go:~3336`, already correct) left untouched — lower regression risk |
| 7 — tighten `HasCompletedExecution` to require `pr_url` | **REFUTED** — breaks direct-commit-to-main rows (commit_sha set, pr_url='') and violates `TestTaskCompletionInvariant` | **Deferred.** Layer 1's invariant prevents the empty-PR `completed` row from ever being written, so the OR-clause is no longer reachable for the bug. SQL/schema left unchanged (defense-in-depth follow-up only) |

**What changed:**
- `internal/executor/runner.go` — new `finalizeEpicBranchPR` method; epic block (`~runner.go:1589`) now routes through it. Reordered to **guard → push → harvest → idempotency → CreatePR → invariant** (matches the direct path). Push fail / PR-create fail now set `result.Success=false` instead of warn+continue (Shape A). Pre-create `FindMergedPRByBranch` short-circuit (Shape C). Invariant: `task.CreatePR && PRUrl=="" ⇒ Success=false`.
- `internal/executor/git.go` — `FindMergedPRByBranch` + `parseFirstPRURL`.
- `internal/memory/store.go` — `MarkExecutionCompleted(id, prURL, commitSHA, durationMs)`: one atomic `UPDATE` (status + result fields) replacing the non-atomic two-call write.
- `internal/executor/dispatcher.go` — completion write (`~:708`) now calls `MarkExecutionCompleted`.

**Tests (green):** `TestFinalizeEpicBranchPR_PushFailIsFailure` (Shape A), `TestFinalizeEpicBranchPR_NoCommitsIsCleanSuccess` (reordered guard), `TestParseFirstPRURL`, `TestMarkExecutionCompleted{,_EmptyPRUrl}`. `go test ./internal/executor/ ./internal/memory/`, `go vet`, `gofmt`, and `golangci-lint` all clean.

**Not in this PR (follow-ups):** direct-path GH-3126 ghost-SHA guard parity for the epic path; the step-7 `is_direct_commit` column (defense-in-depth); full single-`finalizeExecution` unification (Option ii) if/when the direct path is next refactored.
**Priority:** P1 — drove ~70% of finalization failures in the studio-sdk extraction; #1 item on Pilot's own roadmap per `pilot-known-bugs` memory
**Repo:** `qf-studio/pilot`
**Area:** `internal/executor/`, `internal/autopilot/`, `internal/adapters/github/`, `internal/memory/`
**Source bugs:** observed across `qf-studio/studio-sdk` PRs #28–#56 during the SDK extraction (2026-06-02 / -03)

---

## Why mostly manual (not Pilot)

Layer 1 unifies `executor.Runner`'s finalization path — Pilot cannot safely
refactor the path it itself runs on (same precedent as TASK-320 Layer B2).
Layers 2 and 3 are boundary patches in autopilot/poller with no overlap with
the executor's hot path, so they're Pilot-eligible using the
`no-decompose` 2-way split recipe (memory: `pilot-known-bugs`, context marker
`2026-06-01-2132`).

---

## Where TASK-356 left off (what already shipped, what didn't)

TASK-356 (v2.166.7–9) shipped two related fixes:

- **#3383** — Epic no-commits guard at `runner.go:1617` plus `CommitSHA`
  harvest gated behind it (foreign-SHA fix). Closes the *symptom* where an
  empty epic worktree recorded its base-HEAD SHA as the "deliverable."
- **#3391 / #3395** — `ScanRecentlyMergedPRs` (`controller.go:2474–2485`)
  widened from `releaseEnabled` only to `releaseEnabled || boardEnabled`,
  so a board-sourced setup writes back to the board on a manual merge.

TASK-356 did **not** address:

1. The **warn-only error contract** in the epic finalization block
   (`runner.go:1594–1645`). Push fail = warn + continue. PR-create fail =
   warn + continue. `epicResult.Success` stays `true`.
2. The **two non-atomic writes** in the Dispatcher (`dispatcher.go:708–712`):
   `UpdateExecutionStatus("completed")` then `UpdateExecutionResult(prURL,
   commitSHA)`. A failure between them leaves `status='completed'` with
   `pr_url=''` — and `HasCompletedExecution` (`store.go:619–630`) accepts the
   row because `(commit_sha != '' OR pr_url != '')` is satisfied.
3. The **`notifyExternalClose` guard** for human recovery PRs
   (`controller.go:2908`).
4. The **`InvalidateCompletion` call** on `pilot-retry-ready` re-dispatch
   (`poller.go:1748–1862`).
5. **Pre-create idempotency** in the epic-finalize path (no check for "is
   this branch's work already on `main`?" before `epicGit.CreatePR()` at
   `runner.go:1636`).

TASK-359 closes those five gaps.

---

## Evidence (studio-sdk PRs #28–#56)

| Shape | Symptom | Cases this session |
|---|---|---|
| **A — stall-before-push / orphan-completion** | impl completes; finalize warn-swallows the push or PR-create failure; row written `status='completed'` with empty `pr_url`; issue stranded open | 7 / 12 connectors (#29, #32, #33, #42, #43, #49, #55) |
| **B — retry-race** | tracked PR closed externally → daemon adds `pilot-retry-ready` even though a human recovery PR is open → re-dispatch resets `pilot/GH-<n>` to broken commit → user's recovery PR closed | 2 (#33, #55 — #55 nearly re-shipped a lint failure + scratch file) |
| **C — late-duplicate-PR** | daemon's epic-finalize opens a PR for a branch whose work is already on `main` (no pre-create merged-work check) | 1 (#46, headRefOid `43e356e7…` identical to merged #45, opened 6m after #45 merged) |

Clean baseline: #38, #40, #50, #54, #56 — direct-path (`no-decompose` route)
or single-PR work that hit none of the warn-only branches.

**Important correction (vs initial diagnosis):** #46 happened on studio-sdk
where `boardEnabled=true`, so `ScanRecentlyMergedPRs` *did* run (TASK-356 fix
in place). The actual root cause for #46 is **missing pre-create idempotency
in the epic-finalize path**, not the gating bug. Layer 1's unified `finalize()`
covers it; Layer 3 remains as defense-in-depth.

---

## Root cause (one structural defect, two boundary bugs)

### Structural defect — divergent finalization contracts (Shape A + Shape C)

`executor.Runner` has **two finalization paths** with the **same `Execute()`
entry but divergent error contracts**:

| Path | Push fails | PR-create fails | Returned `Success` |
|---|---|---|---|
| **Direct** (`runner.go:3375–3511`) | `result.Success = false`, return | `result.Success = false`, return | reflects reality |
| **Epic** (`runner.go:1594–1645`) | `r.log.Warn` only, **continue** (l.1595–1600) | `r.log.Warn` only, **continue** (l.1638–1641) | **always `true`** (set at l.1581, never reset) |

The direct path was hardened incrementally (GH-1389, GH-457, GH-3126,
GH-2743). The epic path received only the no-commits guard (TASK-356,
v2.166.7) and still treats its terminal steps as advisory.

Compounding: Dispatcher's two non-atomic writes plus `HasCompletedExecution`'s
OR-clause make an empty-PR `completed` row legal — see gaps #2 above.

### Boundary bug — `notifyExternalClose` (Shape B)

`controller.go:2908–2938` adds `pilot-retry-ready` whenever a tracked PR is
closed without merge. The only existing guard (GH-2340) checks `pilot-done`.
It does **not** check for a human-authored open PR on the same issue.
Re-dispatch then resets `pilot/GH-<n>` back to the broken commit (`-B` in
`worktree.go:568` / `git.go:81`), overwriting the human's recovery PR.

Compounding: `shouldRetryRetryReadyIssue` (`poller.go:1748–1862`) does **not**
call `InvalidateCompletion` on the prior `completed` row. If the prior run
left `commit_sha` set, `HasCompletedExecution` silently re-skips — infinite
skip loop.

### Adjacent surfaces to sweep (not fix in scope)

1. Decomposer parent rows when `evalStore == nil` (`controller.go:264`)
2. `OnSubIssuePRCreated` race with Shape B (`runner.go:932`)
3. Parallel poller `checkForNewIssues` — `hasOpenPRAwaitingMerge`
   (`poller.go:1178`) not followed by `HasCompletedExecution` consistency check

---

## Recommended fix — unified `finalize()` + targeted boundary patches

Three architectures considered (call-site patches / unified `finalize()` /
`FinalizationLedger` state machine). **Unified `finalize()` chosen** —
eliminates epic/direct divergence at the structural seam without a schema
migration. Boundary bugs land as targeted patches.

### Layer 1 — Unify the finalization core (Shape A + C root cause) [MEDIUM, MANUAL]

**New function** in `internal/executor/runner.go`:

```go
// finalizeExecution runs the post-implementation sequence (push → PR-create)
// in one place, with one error contract, for both direct and epic paths.
// It MUST return result.Success=false on any non-recoverable error, and MUST
// set result.PRUrl when CreatePR is true.
func (r *Runner) finalizeExecution(
    ctx context.Context,
    task *Task,
    executionPath string,
    result *ExecutionResult,
    isEpic bool,
) error
```

**Steps:**

1. Extract the direct path's existing logic (`runner.go:3336–3517`) into
   `finalizeExecution`. Preserves the ghost-SHA guard (l.3411–3434), the
   GH-1389 push-recovery (l.3375–3389), and the title-normalization checks
   (l.3442–3475). All are correct; just relocate.
2. Route the epic finalization block (`runner.go:1589–1646`) through
   `finalizeExecution` with `isEpic=true`. The no-commits guard (l.1617) and
   the SHA harvest (l.1628) **stay where they are** — they're correct
   (TASK-356 shipped).
3. Inside `finalizeExecution`, before `CreatePR`: call
   `git.FindMergedPRByBranch(ctx, task.Branch)` (existing helper used by the
   poller at `poller.go:1596`); if a merged PR exists with our branch,
   record its URL onto `result.PRUrl` and skip `CreatePR`. This closes the
   Shape C surface that v2.166.9 didn't reach.
4. If `task.CreatePR && result.PRUrl == ""` after the PR-create step,
   set `result.Success = false`. This is the invariant Shape A violates.

**Dispatcher transactional write** (`dispatcher.go:708–712`):

- Replace the two `UpdateExecutionStatus` + `UpdateExecutionResult` calls with
  one new `Store.MarkExecutionCompleted(execID, prURL, commitSHA, durationMs)`
  wrapping them in a single SQLite transaction. Pattern available from
  `Store.SelfHealExecutionAfterMerge` (`store.go:1182`).

**Tighten `HasCompletedExecution`** (`store.go:619–630`):

- For PR-mode tasks, require `pr_url != ''` (not just OR-clause). Preserve
  direct-commit path via either a sentinel value or a new `is_direct_commit`
  column. **Decide during Layer-1 PR review.**

**Verification (Layer 1):**

- Unit: table-driven test in `executor/runner_test.go` covering both paths
  (direct + epic), both terminal failure modes (push fail, PR-create fail),
  asserting `result.Success == false` in all failure cases plus
  `result.PRUrl` set when CreatePR succeeded.
- Integration: spin a fake remote (httptest server), force PR-create to
  fail, assert no `status='completed'` row is written and the issue is
  re-eligible for dispatch.
- Negative: revoke push token mid-run; assert `status='failed'`, no
  `completed` row, `pilot-retry-ready` is applied.

### Layer 2 — Boundary fixes for Shape B [SMALL, Pilot-eligible]

**Patch `notifyExternalClose`** (`internal/autopilot/controller.go:2908`):

- Before adding `pilot-retry-ready`, query open PRs for the issue. Reuse
  `c.ghClient.GetIssue` OAuth path already used at l.2919; add a
  `SearchOpenPRsForIssue(ctx, owner, repo, issueNumber)` adapter method in
  `internal/adapters/github/client.go` if no equivalent exists.
- If any open PR exists where `body` contains `Closes #<IssueNumber>` and
  the author is **not** the Pilot bot, skip the retry-ready label and log
  the human-recovery PR URL.

**Patch `shouldRetryRetryReadyIssue`** (`internal/adapters/github/poller.go:1748–1862`):

- Before clearing the processed marker, call
  `Store.InvalidateCompletion(taskID, projectPath)`. Method exists from
  TASK-296 work — verify the exact signature in `store.go` during
  implementation.

**Verification (Layer 2):**

- Manual: open a fake recovery PR on a test issue; force-close the
  daemon's PR; observe that `pilot-retry-ready` is **not** applied and the
  recovery PR is **not** closed.
- Integration: write a `status='completed'` row with `commit_sha` set and
  `pr_url=''`; dispatch a `pilot-retry-ready` re-pickup; assert the row is
  invalidated and execution actually runs.

### Layer 3 — Defense-in-depth on top of v2.166.9 [SMALL, Pilot-eligible]

The v2.166.9 fix widened `ScanRecentlyMergedPRs` from `releaseEnabled` only
to `releaseEnabled || boardEnabled`. Two follow-ups make it bulletproof:

**Ungate `ScanRecentlyMergedPRs` fully** (`controller.go:2480–2485`):

- Remove the `if !releaseEnabled && !boardEnabled` early-return.
- Move the release-trigger and board-writeback actions behind their own
  internal gates (each already checks its own enable flag per comment at
  l.2474–2479).
- Result: a vanilla GH-issue-source-only deployment (no board, no release)
  also gets external-merge observation.

**DB-fallback in `hasMergedWork`** (`internal/adapters/github/poller.go:1596`):

- After GitHub Search API returns no results, query
  `Store.HasCompletedExecution(taskID, projectPath)` as authoritative
  fallback. The DB has no API lag.

**Verification (Layer 3):**

- Manual: run with `releaseEnabled=false, boardEnabled=false`, merge an
  issue externally, observe `ScanRecentlyMergedPRs` still runs.
- Integration: mock GitHub Search to return empty for 60s after a merge,
  assert `hasMergedWork` returns true via the DB fallback.

---

## File-by-file change list

| File | Lines | Action | Layer |
|---|---|---|---|
| `internal/executor/runner.go` | 1589–1646 | Replace inline epic-finalize with `finalizeExecution(ctx, task, ePath, epicResult, true)` call | 1 |
| `internal/executor/runner.go` | 3336–3517 | Extract direct-finalize body into `finalizeExecution(…, false)`; existing function calls it | 1 |
| `internal/executor/runner.go` | new | Add `finalizeExecution` function (unified core) + pre-create merged-work check | 1 |
| `internal/executor/dispatcher.go` | 708–712 | Replace two writes with one `MarkExecutionCompleted` call | 1 |
| `internal/memory/store.go` | 619–630 | Tighten `HasCompletedExecution` (direct-commit sentinel or new column) | 1 |
| `internal/memory/store.go` | new | Add `MarkExecutionCompleted(execID, prURL, commitSHA, durationMs)` transactional method | 1 |
| `internal/autopilot/controller.go` | 2908–2938 | Add human-recovery-PR check before applying `LabelRetryReady` | 2 |
| `internal/adapters/github/client.go` | new (if absent) | `SearchOpenPRsForIssue` helper | 2 |
| `internal/adapters/github/poller.go` | 1748–1862 | Call `InvalidateCompletion` before clearing processed marker on retry-ready | 2 |
| `internal/autopilot/controller.go` | 2480–2485 | Remove `!releaseEnabled && !boardEnabled` gate; move gates inside (defense-in-depth atop v2.166.9) | 3 |
| `internal/adapters/github/poller.go` | 1596 (`hasMergedWork`) | Add DB fallback after Search API empty | 3 |

**Reuses (do not duplicate):**

- `executor.GitOperations.Push` / `CreatePR` / `FindMergedPRByBranch`
  (`internal/executor/git.go`)
- `executor.PRCreator` interface (multi-forge path)
- `Store.SelfHealExecutionAfterMerge` transactional pattern (`store.go:1182`)
- `Store.InvalidateCompletion` (TASK-296; verify signature during implementation)
- `github.HasLabel`, `github.LabelRetryReady`, `github.LabelDone`

---

## Decomposition into Pilot-sized issues

Layers 2 and 3 ship as **4 `no-decompose` Pilot issues** (proven recipe from
`pilot-known-bugs` memory, marker `2026-06-01-2132`):

| GH # | Issue title | Status |
|---|---|---|
| **[#3419](https://github.com/qf-studio/pilot/issues/3419)** | Layer 3a — ungate `ScanRecentlyMergedPRs` | ✅ **SHIPPED** (PR#3424 merged) |
| **[#3417](https://github.com/qf-studio/pilot/issues/3417)** | Layer 2a — `notifyExternalClose` human-PR guard | ✅ **SHIPPED** (PR#3422 merged `9a94b045` → **v2.166.13**; manual merge due to stage approval-misconfig) |
| **[#3418](https://github.com/qf-studio/pilot/issues/3418)** | Layer 2b — `InvalidateCompletion` on retry-ready | ✅ **SHIPPED** (`a9615dda` → **v2.166.14**) — initial no-op was overcome on retry; lands `ExecutionChecker.InvalidateCompletion` + `TestPoller_RetryReady_InvalidatesCompletedExecution`. |
| **[#3420](https://github.com/qf-studio/pilot/issues/3420)** | Layer 3b — `hasMergedWork` DB fallback | ⛔ **no-op'd** (same TASK-320 class). **Needs manual impl.** |

**Status 2026-06-03 (updated):** **3/4 shipped** (#3417 + #3419 → v2.166.13; #3418 → v2.166.14, `a9615dda`). #3420 (Layer 3b — `hasMergedWork` DB fallback) status TBD — the broader `hasMergedWork` work landed earlier under #3269/#3300/`fb49cd6e`/`e9c98ad6`, so #3420 may be redundant; **needs triage** before deciding whether to close or fold into Layer 1. Layer 1 (executor no-op/finalize unification) is still MANUAL and the remaining real blocker.

**Manual (do not file as `pilot`):**

- Layer 1: unify finalization core in `executor.Runner` — 300–400 LOC,
  touches the path Pilot itself runs on. Land in a feature branch, ship
  in a release the user manually deploys before the next SDK connector
  batch (`github`, `linear`, `jira`, `slack`/`telegram`/`discord` (chat
  bridge), `asana`).

---

## Cross-references to existing TASKs

- **TASK-320** (executor false-negative no-op): Layer A+B1 shipped. Layer B2
  deferred for the same MANUAL reason as TASK-359 Layer 1. Treat as
  superseded by TASK-359 Layer 1 once it lands.
- **TASK-321** (phantom `pilot-blocked` on already-merged work): **superseded
  by this TASK** (2026-06-03). TASK-321's 4 PRs map into TASK-359 layers:
  PR-1 → Layer 1 (pre-create `FindMergedPRByBranch` check inside unified
  `finalizeExecution()`); PR-2 → Layer 3 (`hasMergedWork` DB fallback +
  sequential-mode guard); PR-3 → Layer 2 (`InvalidateCompletion` on
  `pilot-retry-ready` addresses the same durable-marker concern from the
  other end); PR-4 → Layer 3 (ungate `ScanRecentlyMergedPRs`). TASK-321
  stays as the root-cause record for the dispatch-idempotency symptom; do
  not file PRs from there.
- **TASK-355** (board-sourced no-op false positive, foreign-SHA): root
  cause shipped in v2.166.7 epic no-commits guard. Inherited; no new
  work needed.
- **TASK-356** (3 daemon findings) — **shipped v2.166.7–9, archived**.
  Finding #1 (epic-decompose work-loss) closed the symptom but not the
  warn-only error contract — that's Layer 1 of TASK-359. Findings #2
  (upstream #2598 approval-misconfig) and #3 (no-status PR cards) are
  separate concerns.

---

## End-to-end verification

After Layers 1–3 land:

1. **Re-run the SDK extraction on the remaining 4 connectors** (`github`,
   `linear`, `jira`, `asana` — `slack`/`telegram`/`discord` chat-bridges
   need separate design first). For each:
   - Without `no-decompose`: should finalize cleanly (Shape A invariant)
   - Force-close one PR mid-CI: should **not** apply `pilot-retry-ready`
     when a human recovery PR is opened first (Shape B invariant)
   - Merge externally with `releaseEnabled=false, boardEnabled=false`:
     `ScanRecentlyMergedPRs` should still run and the issue should move
     to `pilot-done` (Shape C invariant)
2. **Negative test**: deliberately break finalization (e.g. revoke push
   token mid-run). Assert `status='failed'`, no `completed` row, issue
   eligible for re-dispatch.
3. **Smoke**: `make test` and `make lint` clean.
4. **Memory update**: on successful verification, write a Navigator
   `learning` memory: "Daemon finalization unified — Shapes A/B/C closed
   in v<release>". Update `pilot-known-bugs` to remove the three shapes.

---

## What's deliberately not in scope

- **`FinalizationLedger` state machine** (Option iii in the design weigh):
  correct eventual target, but premature mid-extraction. Revisit after
  the remaining connectors land.
- **The `stage` approval-misconfig** (upstream #2598 / TASK-356 #2):
  separate concern; manual-merge workaround is acceptable.
- **No-status PR cards on the board** (TASK-356 #3): separate, recurring
  cleanup; not a finalization-path bug.
- **Chat-adapter SDK design** (slack/telegram/discord don't fit the
  issue-poller mold): separate design effort.
