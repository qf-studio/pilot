# Decomposition Integrity — Wave 2 (TASK-361 follow-up)

## Context

Wave 1 (PR #3527, v2.183.0) hardened the *counting* path (`openSubIssueCount` cross-check, open-PR supersession veto, scope fence). Live runs since (GH-3530/#3535/#3537/#3546/#3553) proved four defects remain. Root cause of the worst one is now pinned:

1. **Premature parent close with child-PR attribution** — `WaitForExecution` (dispatcher.go:501) treats only `completed|failed|cancelled` as terminal, so a parent epic whose worker writes a TASK-358 status (`no_op`, `skipped`, …) **hangs the handler**. When a child's PR later merges, `selfHealForPR` (controller.go:267, TASK-352) promotes the **parent's** row to `completed` and stamps the **child's PR URL** onto it. The hung handler wakes → posts "✅ Pilot completed! Duration 0s + child's PR" → the poller registers the child PR under the **parent's** issue number → `handleMerging` closes the parent + `pilot-done` while siblings are open.
2. **Supersession kills PR-less children** — wave 1's veto only protects children with an *open* PR; #3537 (no PR yet) was auto-closed "redundant", its slice lost.
3. **Hallucinated/empty children** — no validation of empty `Description` at sub-issue creation; no assertion that the plan's parent matches the dispatched task (#3538/#3553 claimed `parent: GH-201`).
4. **`pilot-failed` stacked on `pilot-done`** — `ErrParentDone` re-dispatches are treated as failures.

**Goal:** the controller's count-verified path (`maybeCloseParentIssue`/`recoverStaleParentIssues`) becomes the ONLY closer of decomposed parents; children are only superseded on positive evidence their own slice shipped; junk children are rejected at creation; benign skips stop stacking labels.

**Execution route: MANUAL** (Pilot must not modify its own completion logic — TASK-320 B2 rationale). One branch `fix/decomposition-integrity-wave2` off `origin/main` in a fresh worktree, one PR, hand-merge. ⚠️ Local repo root is ~47 commits behind — base on `origin/main`, all line anchors below are origin/main.

## Fix 1 — premature parent close (load-bearing, do first)

**1a. `internal/executor/dispatcher.go` `WaitForExecution` (:501)** — make all TASK-358 worker statuses terminal:
`completed, failed, cancelled, declined, no_op, rate_limited, skipped, stalled, infra` → return. Closes the hang→self-heal→false-✅ window at the source.

**1b. `internal/autopilot/controller.go` `handleMerging` close block (:1417)** — before close/`pilot-done`/comment, guard with wave-1 `openSubIssueCount(ctx, prState.IssueNumber)` (:1660): if open children > 0, log + skip the whole close block (defer to count-verified path). Fail-open on error so leaf issues keep closing.

**1c. `internal/autopilot/controller.go` `selfHealForPR` (:267)** — gate the parent arm: only heal the parent's row (and stamp PR URL) when `openSubIssueCount(parent) == 0` (last child merged — preserves TASK-352 intent). Otherwise log + skip.

**1d. `cmd/pilot/handlers.go` TASK-321 already-merged close path (:465-479)** — before close+`pilot-done`, check open children via client helpers (`GetOpenSubIssueCount` client.go:1133 / `SearchOpenSubIssues` :1066); if open > 0, fall through to awaiting-merge treatment.

Deliberate non-change (flag in PR): keep epic `IssueResult.PRNumber/PRURL` — the parent's own epic PR legitimately needs autopilot merge management; 1b makes registration harmless.

## Fix 2 — supersession needs child evidence

**`internal/adapters/github/poller.go` `skipSupersededByParent` (:2008)** — after the parent closed+done check and the wave-1 open-PR veto (:2038), require positive evidence the child's own slice shipped before closing:
- `FindMergedPRByBranch(pilot/GH-<child>)` (client.go:1022) **or** `execChecker.HasCompletedExecution("GH-<child>", projectPath)` (already wired on Poller, :52-57)
- Neither → log + `return false` (dispatch the child). Fail-open on lookup errors.

## Fix 3 — reject junk children at creation

**(a) `internal/executor/epic.go` `CreateSubIssues` entry (:981)** — filter subtasks with empty/whitespace `Description` (warn-log each; if ALL empty → error "refusing to create empty sub-issues"). One choke point covers both creator paths (adapter :1057, gh CLI :1195). Build a new plan value; don't mutate caller's.

**(b) `internal/executor/runner.go` (:1633, before `CreateSubIssues`)** — wrong-parent assertion: `plan.ParentTask.ID != task.ID` → failed `ExecutionResult` ("epic plan parent %q does not match dispatched task %q"), creator never invoked. (Only production call site; zero signature churn.)

## Fix 4 — ErrParentDone is a benign skip, not a failure

- `internal/executor/epic.go` (:595): add `func IsParentDoneSkip(errStr string) bool` (substring match on `ErrParentDone`).
- `cmd/pilot/handlers.go` (after `pilot-in-progress` removal, :365-367): if execErr or `hr.Result.Error` matches → log, `issueResult.Success = true`, return early. No `pilot-failed`, no ❌ comment; Success=true keeps the poller from re-dispatch-looping.

## Tests (table-driven, httptest mocks — mirror wave 1 idioms)

- `TestHandleMerging_DefersCloseWhenChildrenOpen` + `_ClosesLeafWhenNoChildren` (controller_test.go, idiom :4581)
- `TestSelfHealForPR_SkipsParentWithOpenChildren` / `_HealsParentWhenLastChildMerges`
- `TestWaitForExecution_ClassifiedOutcomesAreTerminal` (dispatcher_test.go — proves return-not-hang)
- `TestHandleGitHubIssue_AlreadyMergedNoOp_DoesNotCloseDecomposedParent` (cmd/pilot)
- `TestPoller_CheckForNewIssues_DoesNotSupersedeChildWithoutShippedEvidence` + `_SupersedesChildWithMergedBranchPR` + `_SupersedesChildWithCompletedExecution`; keep `_DoesNotSupersedeWithOpenPR` green (poller_test.go :2896+)
- `TestCreateSubIssues_SkipsEmptyDescriptionSubtasks` + `_FailsWhenAllDescriptionsEmpty`; `TestExecuteEpic_RejectsForeignParentPlan` (epic/runner tests via `planEpicFn` override)
- `TestHandleGitHubIssue_ParentDoneSkip_NoFailureLabel`

## Verification

1. `go build ./... && go test ./internal/executor/... ./internal/autopilot/... ./internal/adapters/github/... ./cmd/pilot/... -count=1` + `golangci-lint run` on touched packages; `gofmt -w` changed files only.
2. After merge: cut release manually (daemon doesn't release `fix/*`), `pilot upgrade`, restart daemon.
3. Live check on the next decomposed epic (TASK-361 checklist): parent stays open until ALL children's PRs merge; completion comments name the right branch/PR; no `pilot-superseded` on a child without merged work; re-dispatch of a done parent leaves no `pilot-failed`.

## Risks (state in PR)

- Autopilot disabled → decomposed parents linger open until next daemon start (`recoverStaleParentIssues` backstop). Pre-existing gap, now explicit.
- `openSubIssueCount` adds 1-2 API calls per merge (same budget as existing recovery sweep); fail-open preserves leaf closes.
- 1a changes handler timing for all adapters (no-op runs return promptly instead of hanging) — strictly better; watch `handler_common_test.go` expectations.
- Fix 2 trade-off: a child whose slice shipped without branch-PR/execution row dispatches once and no-ops — acceptable per "never lose work".

## Files touched

`internal/executor/dispatcher.go` · `internal/autopilot/controller.go` · `cmd/pilot/handlers.go` · `internal/adapters/github/poller.go` · `internal/executor/epic.go` · `internal/executor/runner.go` + their test files.
