# TASK-437: GH-4648/4649 duplicate-execution race — incident record + prevention cluster

**Created**: 2026-08-03 · **Status**: 🚀 3 prevention issues dispatched (numbers in Refs) · **Last Updated**: 2026-08-03

## Incident (2026-07-31, all UTC, project startups/pilot)

| When | What |
|---|---|
| 18:10 / 18:39 | GH-4648 gen-0/gen-1 die `infra` (heartbeat class); gen-1 got far enough to **decompose** into #4649 (impl) + #4650 (test) at 18:39:45 |
| 18:54:08 | Child #4649 gen-0 SIGKILL'd — heartbeat timeout, exit 137 ("Failed on 1/2") |
| 18:54:35 | **Parent gen-2 claimed — re-implemented the full spec itself** → PR#4652 (created 19:33, merged 19:41:52) |
| 18:57:00 | **Child #4649 gen-1 claimed** (queued behind the serial ProjectWorker; physically ran ~19:33→19:58) |
| 19:43:16 | #4649 closed with `pilot-superseded` — **by no code path**: the label has zero functional callers (`internal/adapters/github/types.go:104`); close was the parent's LLM session improvising via `gh` |
| 19:58:51 | Child's run finished anyway → **PR#4653 opened against the closed issue, born CONFLICTING** (same hunks as merged #4652) |
| 08-01 14:23 | Autopilot: conflict → auto-rebase fail → mechanical-resolution fail → `escalateAndHold` → `stage=failed, ci_status=pending, merge_attempts=0` + `needs-manual-rebase`/`pilot-needs-human` |

Nav-research trace (91 tool calls, verified at `d959cfd6`): every admission/re-check guard is scoped to its **own task_id's ledger or own branch** — `nextRetryGeneration` (dispatcher.go:1390–1425), `decomposedChildrenAllComplete` (dispatcher.go:2778–2829, all-or-nothing → no-op when a child *failed*), `hasTerminalSuccessLedger`, `mergedPRPreflightCheck` (dispatcher.go:662–675, own-branch only), `handleMergeConflict` (controller.go:4544–4625). None asks "has a sibling/parent delivered my scope?" or "is my issue still open?". The epic decision is re-derived per run (runner.go:2224/2253/2290) with two branches that bypass `CreateSubIssues`' `ErrSubIssuesAlreadyExist` recovery entirely: planning-failure fallback (runner.go:2291–2298) and `isSinglePackageScope` collapse (runner.go:2307–2332, epic.go:419–501). `ci_status=pending` is exact: `handleMergeConflict` returns before `CheckCI` runs; `CIStatus` only initialized at controller.go:1635, only written at :1997. GH-4646's `CIConfigMismatch` is post-merge-only — orthogonal.

## Prevention cluster (dispatched)

1. **A — decomposed-parent retry must resume coordinator, never re-implement.** Consult `GetDecomposedChildTaskIDs` unconditionally before the epic-mode decision; children exist + any non-terminal → route into the existing `recoverExistingSubIssues` path (runner.go:2384–2477); both bypass branches covered.
2. **B — revalidate issue state at pickup and before PR creation** (blocked by A — shared files). Closed issue at pickup → finalize superseded, no Execute; PR-creation preflight refuses PRs for closed issues. This alone would have prevented PR#4653.
3. **C — conflicting PR whose issue is closed → close the PR, don't escalate.** In `handleMergeConflict`, closed source issue short-circuits to PR close + terminal, no `needs-manual-rebase`/`pilot-needs-human` noise.

## Open items (not dispatched)

- **Heartbeat SIGKILL cluster**: three infra kills in 45 min (18:10/18:39/18:54) on one issue family triggered the whole race — separate diagnosis needed (box-side; #4521 fixed the stdout-reader variant only).
- **PR#4653 disposition**: still open/conflicting — operator decision (close manually now, or leave as live test case for issue C).
- The ad-hoc LLM `gh` close of #4649: symptom of executor sessions holding broad `gh` powers; issue B makes the *system* own supersede-finalization, but a broader "what may an executor session do to sibling issues" policy question remains.

## Refs

- Issues: A = https://github.com/qf-studio/pilot/issues/4655 · B = https://github.com/qf-studio/pilot/issues/4656 (blocked by #4655) · C = https://github.com/qf-studio/pilot/issues/4657 (independent, autopilot-only)
- Trace source: nav-research report 2026-08-03 (this doc summarizes it; file:line refs verified at `d959cfd6`).
- Prior art: TASK-401 (#4216 decomposed-children guard — the narrow case), GH-724 (conflict-before-CI check), GH-4646/PR#4647 (terminal-status pattern to mirror), TASK-431 (incident-record precedent).
- Related: TASK-436 (the feature whose dispatch triggered this incident; its main leg shipped via #4648→PR#4652, released v2.251.1).
