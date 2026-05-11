---
name: OAuth cascade incident series (2026-05-03 / 5-04 / 5-08)
description: Three related incidents producing the same OAuth-titled spurious sub-issues. #1 (5/3) decomposer template bug; #2 (5/4) executor prompt copy; #3 (5/8) closed-parent re-dispatch — different mechanism, same symptom (LLM hallucination from sparse parent input, not a prompt leak). Hardened cumulatively via #2562 prompt fix, #2592 invariant test, #2594 escalation gates, and TASK-50 / PR #2872 closed-parent gate.
type: project
originSessionId: a45a0b36-53c9-4751-93ff-3cd0d8b24386
---
## Cascade #1 — 2026-05-03

Decomposer fix (#2494, commit 28b24a43) shipped with template substitution bug: every sub-issue body was the literal placeholder `Parent: GH-201\n\n` (GH-201 = unrelated Jan 2026 dashboard ticket) and titles copied verbatim from parent. Pilot's executor saw a sub-issue titled `feat(auth): add OAuth provider integration` with empty body and improvised a full OAuth provider system in `internal/gateway/oauth.go`. Each merge re-triggered the decomposer (dedup couldn't catch siblings — all referenced wrong parent GH-201), spawning ~28 sub-issues across 12 providers. **10 merged before the cascade was caught at v2.116.0.**

Reverted in PR #2558. Root cause filed as #2559. Planner-side fix in PR #2562.

## Cascade #2 — 2026-05-04 (recurrence)

#2562 only patched `internal/executor/epic.go`. The same OAuth example string lived in `internal/executor/workflow.go:163` (executor's runtime prompt, separate from planner). Daemon resumed, autopilot's `fix(ci):` recursion + permissive stage env auto-merged a second 512-LoC OAuth contamination via squash-merge.

Detection was harder than #1: GitHub returned `mergedAt: null` on `gh pr view` for the squash-merged PR (see `pattern_squash_merge_mergedat_null.md`), so labels suggested PR was still open while the commit was on main.

### Recovery (all merged 2026-05-04)
| PR | What |
|---|---|
| #2581 | `fix(executor)`: remove OAuth example from `workflow.go:163` (true root cause) |
| #2582 | `revert`: roll back commit `858f092d` (-512 LoC) |
| #2592 | `test(executor)`: cross-prompt invariant — walks every `.go` under `internal/executor/` + `internal/autopilot/`, scans every multi-line raw-string for forbidden literals + `feat\([a-z]+\): [a-z]` regex. ALL_CAPS placeholders pass. Verified to fail when re-introducing pre-fix `workflow.go`. |
| #2594 | `feat(autopilot)`: two escalation gates in `MergePR`. `ScopeDriftReason` (PR title type/scope must match issue) + `SizeFloorReason` (>200 net additions). Both escalate to human approval; never silently merge. Cascade-2 reproduction tests included. |

Smoke test 2026-05-04 19:05: PR #2597 (`chore(memory)`, 1 LoC) — gates correctly abstained, scope preserved, no glyphs. Stage env hit a separate config gap (#2598: `approval.pre_merge.enabled: false` deadlocks `require_approval: true`) — manually merged.

### Lesson — applies to ALL future prompt-leak fixes

**#2562 was scoped wrong.** When fixing a prompt leak, scan EVERY embedded prompt string in the codebase, not just the one that surfaced. The cross-prompt invariant test in #2592 makes recurrence a build-break going forward, but you must verify it fails on the pre-fix code or your scan is too narrow.

See `feedback_check_all_prompts_for_leaks.md` and SOP at `.agent/sops/integrations/prompt-leak-fix-checklist.md` (commit 70553a72).

### Detection signatures (next time)
- Many issues with identical/templated `feat(auth):` titles appearing within minutes
- Sub-issue body is literal `Parent: GH-N\n\n` with nothing else
- `gh pr view --json mergedAt` returns null but `git log origin/main` has the SHA — check `git log` not the API alone

## Cascade #3 — 2026-05-08 (different mechanism)

**Symptom identical to #1/#2** — issues titled `feat(auth): add OAuth provider integration`, body `<!--autopilot-meta\nparent: GH-201\ninherited-spec: true\n-->\n\nParent: GH-201\n\n`. **64 dupes spawned across the day** before the fix landed.

**Mechanism differs.** This is *not* a prompt leak. PR #2562's prompt fix held — `internal/executor/epic.go:317` `buildPlanningPrompt` is clean (ALL_CAPS placeholders only). The invariant test #2592 still passes. The leak is upstream of the prompt: the autopilot reconciliation loop was re-invoking `CreateSubIssues(GH-201)` on every poll despite GH-201 being `MERGED, pilot-done` since 2026-01-29. Each invocation fed a sparse closed-parent context to the planner, and the LLM hallucinated `feat(auth): add OAuth provider integration` as the most plausible subtask given the project's heavy OAuth domain memory (34 OAuth-related rows in `memories` table from real GH-252/259/260/378/394/404 work).

**Why prior guards didn't fire:**
- `ProcessedStore` is keyed on the child, not the parent — new child IDs bypass.
- `executions.status='completed'` lockout (GH-2242) gates execution dispatch, not sub-issue creation.
- `queryOpenSubIssues` (`epic.go:746`) only checked OPEN siblings — closing dupes reset the guard.
- TASK-43 label-inheritance fix landed but parent's `pilot-done` wasn't read at the `CreateSubIssues` entry point.

### Recovery
| PR / Issue | What |
|---|---|
| TASK-50 / GH-2865 / PR #2872 | `fix(executor)`: refuse `CreateSubIssues` for closed/done parents — `isParentDone(t *Task) bool` + caller gate. Shipped v2.139.0 / v2.140.0. |
| GH-2866 (sentinel) | Open issue, no labels, body `Parent: GH-201` — kept dedup guard tripped during the bridge window. Close after 2h clean soak. |
| GH-2882 / TASK-56 | `fix(executor)`: treat `ErrSubIssuesAlreadyExist` as recovery, not failure (`runner.go:1293`). PR #2929 closed without merge — needs re-investigation before re-filing. |

### Lesson — closed parents are not safely idempotent
Future fix surface: ALL paths that decide "should we run X on parent P?" must check parent state, not just child state. Add this as a pre-commit forensic on any new dispatch / decompose / reconcile entry point.

### What worked
- Revert pattern: `git revert --no-edit` each merge in reverse-chron, no conflicts, single PR for the cascade
- Killing daemon FIRST, then forensics
- Stage env hardened to `require_approval: true` (do NOT lower without filing a hardening ticket)
- Ghost-execution-row cleanup (3 today: GH-2566/2568/2573) to unblock re-dispatch

### Open follow-up tickets (filed 2026-05-04, no `pilot` label — manual triage)
- #2587 — `fix(executor): disable extractParentTypeScope fallback when parent title comes from cascade artefact`
- #2588 — `fix(autopilot): cap autopilot-fix recursion when failing PR adds files outside issue scope`
- #2589 — `fix(daemon): on startup, reset stale pilot-in-progress labels older than N minutes` (design pre-built in marker)
- #2590 — `docs: SOP for cascade-detection forensics`
- #2591 — `fix(memory): converge ProcessedStore + executions table into single source of truth`
- #2598 — `fix(autopilot): wire approval.pre_merge for stage env or document fail-closed deadlock`
