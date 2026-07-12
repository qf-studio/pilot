# Epic parent re-implements shipped child work — fix single-child decomposition, epic PR title rejection, and add cross-task-id dispatch guard

**Status**: ✅ All 3 fixes shipped 2026-07-12. Fixes 1+2 merged via [#4220](https://github.com/qf-studio/pilot/issues/4220)/PR #4221 (decomposer `len<=1` short-circuit + title normalization incl. decomposed-parent path). Fix 3 (cross-task-id guard) landed directly on parent [#4216](https://github.com/qf-studio/pilot/issues/4216) (this doc's own dispatch — branch `pilot/GH-4216`): `Store.GetDecomposedChildTaskIDs` (memory/store.go) + `ProjectWorker.allDecomposedChildrenShipped` (executor/dispatcher.go), wired into `processQueue`'s pickup-time guard alongside `hasTerminalSuccessLedger`. Supersedes the re-filed [#4222](https://github.com/qf-studio/pilot/issues/4222) — close #4222 as duplicate. [#4217](https://github.com/qf-studio/pilot/issues/4217) (Defect B, sibling merge-wait) can proceed.
**Type**: bug (executor epic pipeline — duplicate-PR class, Defect A)
**Evidence**: GH-4211→#4212 live repro 2026-07-12 (PRs #4213 vs #4214, same fix implemented twice)

## Problem

An epic parent whose child already shipped a completed PR gets re-dispatched as a
fresh top-level task and re-implements the work from scratch. Live repro (ledger
`~/.pilot/data/pilot.db`, `executions`/`execution_events`):

```
3034a06e  GH-4211  11:29:41 → decomposed 11:30:22 "into 1 children: #4212"
e8c945e9  GH-4212  11:30:22 → PR #4213 created 12:28:15
3034a06e  GH-4211  failed 12:28:20 "epic PR creation failed: title is not a
          conventional commit: \"GH-4211: Throughput histograms record zero…\""
c5bf7b0a  GH-4211  12:28:41 (re-poll +39s) → re-implemented everything → PR #4214
```

Three bugs chain (all refs origin/main):

### Bug 1 — decomposer: 1-subtask plans always classified multi-package
`isSinglePackageScope` (`internal/executor/epic.go:414`) falls back to
`detectSameComponentFromTitles` (`epic.go:453`) when no directory hints exist;
that helper returns `false` for `len(subtasks) < 2` (`epic.go:454`). There is NO
`len(plan.Subtasks) == 1` short-circuit anywhere in the epic decision branch —
a 1-subtask plan without file-path hints is *guaranteed* multi-package →
pointless single-child epic pipeline. A single subtask can never legitimately
span multiple components.

### Bug 2 — epic finalize: PR title guaranteed non-conventional
`finalizeEpicBranchPR` (`internal/executor/runner.go:1596-1610`) builds the epic
parent's own PR title as `fmt.Sprintf("%s: %s", task.ID, task.Title)` — raw
issue title. `git.CreatePR` (`internal/executor/git.go:177`) validates via
`validatePRTitle` (`internal/executor/title.go:56`), which strips the `GH-N:`
prefix and requires a conventional-commit type. Raw issue titles essentially
never pass. The direct path auto-corrects exactly this via
`autoPrefixTitle`/`inferConventionalPrefix` (`title.go:71,88`, wired
`title.go:204`, `title_rejection.go:82`) — the epic finalize path never calls
them. Any epic whose own branch has ≥1 commit vs base fails finalize
deterministically, leaving the parent open + `failed`.

### Bug 3 — no cross-task-id dispatch guard
Every dedup guard is keyed to a single task_id:
`HasCompletedExecution` (`internal/memory/store.go:833`; consulted
`dispatcher.go:286,407,731,896`), `IsTaskQueued` (`store.go:1652`),
`isParentDone` (`epic.go:654`, parent's own labels/state only), closed-issue
gate (#4186 — parent was open), `MarkProcessed` sub-issue skip
(`epic.go:1433-1438`, `main.go:2332` — covers CHILD numbers only). Nothing says:
"this GH-N has a `decomposed` ledger event and its referenced children shipped
completed executions — don't re-run it as a fresh top-level task."

## Scope (one PR)

1. **Decomposer short-circuit**: in the epic decision branch, `len(plan.Subtasks) <= 1`
   ⇒ treat as single-package / consolidate into direct execution — never create
   a single child issue.
2. **Epic finalize title**: route `finalizeEpicBranchPR`'s title through the same
   `autoPrefixTitle`/`inferConventionalPrefix` machinery as the direct path
   before `CreatePR`.
3. **Cross-task-id guard (defense in depth)**: before dispatching a task_id as a
   fresh top-level task, if `execution_events` has a `decomposed` stage for it,
   parse the child issue numbers from the event detail (`"decomposed into N
   children: #a, #b"`) and check each child's completion (`HasCompletedExecution`
   or equivalent "child shipped" evidence). All children complete ⇒ do NOT
   re-implement: short-circuit to parent finalize/close bookkeeping (log fail-loud
   why). Some children incomplete ⇒ existing epic-resume behavior unchanged.

## Must NOT change

- The fixed GH-3927 class (poller-vs-sequential same-task_id dedup: 4d.2a
  `githubPollerRegistry`/`MarkProcessed`, #4140 ledger double-intake, #4186/#4187
  closed-issue gates) — regression-fence, don't touch semantics.
- Legitimate epic retry when children genuinely failed/incomplete.
- Sibling merge-wait sequencing (`wait_for_merge`) — separate issue (Defect B).

## Acceptance criteria

- [x] A 1-subtask epic plan executes directly; zero child issues created (test) — shipped via #4221.
- [x] Epic parent finalize creates its PR with an auto-prefixed conventional
  title; the GH-4211 title shape passes (test with a raw non-conventional title) — shipped via #4221.
- [x] Re-poll of an open parent whose ledger shows `decomposed` + all children
  completed does NOT produce a fresh implementation run (table test over the
  guard; include children-incomplete → normal path case) — `TestProcessQueue_CrossTaskIDGuard`
  (internal/executor/dispatcher_test.go), 3-case table: all-complete skip,
  one-incomplete falls through, none-completed falls through.
- [x] Fail-loud logging on the new guard's skip decision — `slog.Warn` in
  `ProjectWorker.processQueue` (dispatcher.go), logs task_id + full child list + evidence PR URL.
- [x] `go test -race ./internal/executor/... ./internal/memory/... ./cmd/pilot/...` green;
  full `go test -race ./...` green (stress gate — no new API calls in poll cycle, mem-048).

## Verify

```bash
go build ./... && go vet ./... && go test -race ./...
```

## Refs

- Forensics: `.agent/tasks/TASK-401-epic-parent-duplicate-reimplementation.md` (this doc), research 2026-07-12
- Prior fixed class (contrast): TASK-368 Step 0 (GH-3927), #4110/#4114, #4140, #4182/#4186/#4187
- Sibling defect (separate): merge-wait sequencing — see follow-up issue (Defect B)
