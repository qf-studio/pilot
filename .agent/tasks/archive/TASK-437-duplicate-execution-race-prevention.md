# TASK-437: GH-4648/4649 duplicate-execution race — incident record + prevention cluster

**Created**: 2026-08-03 · **Status**: 🚀 7 issues open (#4656 #4668 #4669 #4670 #4671 #4677 #4678 #4679); #4657 ✅ merged · **Last Updated**: 2026-08-03

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

## Trigger diagnosis (2026-08-03, resolved → dispatched)

- **Heartbeat SIGKILL root cause CONFIRMED from recordings**: both kills fired mid-`make test` — last stream event was `tool_use Bash "make test"` (TG-1785521443743 seq 6350; TG-1785523208156 seq 3346), then true silence >5m while tests ran on the loaded t3.xlarge → `last_event_age>5m0s` → SIGKILL 137. claude-code's stream is silent during local tool runs by design; `last_event_age` is the wrong liveness signal. → **#4668** (process-tree liveness check before kill). GH-4521 was the false-silence variant; this is true silence from a healthy child.
- **Collateral finding — intent judge 100% dead since the 07-16 cutover**: 4,321 `context_deadline` subprocess kills (07-16 23:56 → 08-03 10:30, RSS ~250–270MB, every repo, zero successes), hidden by fail-open. → **#4669** (diagnose, restore-or-retire, mandatory failure-streak metric+alert).
- Log window also confirmed the RCA's bypass branch live: gen-0 logged "Single-package scope detected, skipping epic decomposition — executing as single task, planned_subtasks=1" — the exact `isSinglePackageScope` collapse #4655 fences. Non-deterministic planning (gen-0 → 1 subtask, gen-1 → 2) is what flipped it into decomposition on retry.

## Open items

- **PR#4653**: ✅ CLOSED 2026-08-03 (comment links this record; branch `pilot/GH-4649` deleted).
- **Executor session GitHub-powers policy** — nav-research complete 2026-08-03 (verified at `c5208a87`):
  - Child spawns with `--dangerously-skip-permissions` on ALL three arg branches (`backend_claudecode.go:489/502/513`), inherits full daemon env incl. `GITHUB_TOKEN` (`:575` — `append(os.Environ(), "PILOT_EXECUTOR=1")`, nothing stripped), Bash in default allowedTools with no subcommand filter (`backend.go:758-760`). The shared classic PAT spans all owners (`sops/config/github-token-architecture.md`).
  - **Zero prompt-level scope constraints**: `ExecutorPromptHeader` / PILOT EXECUTION MODE / workflow instructions say nothing about sibling issues, labels, or PR scope. `repo_guardrail.go`'s `ValidateTargetRepo` (GH-3027) guards only Pilot's own Go `gh issue create` calls — the LLM's Bash `gh` path has NO interception point.
  - **Zero post-run GitHub side-effect audit**: gates check build/diff/git state only; a session closing/labeling sibling issues is invisible to every check.
  - **⚠️ Live-verified: `qf-studio/pilot` main has NO branch protection** (`GET /branches/main/protection` → 404) — a session could push to main directly; only advisory CLAUDE.md text prevents it. NOTE: enabling protection interacts with autopilot auto-merge + required_checks (TASK-431 class) — operator decision, not a casual toggle.
  - Containment DISPATCHED 2026-08-03 (founder "go"): **#4670** (prompt-level scope rule + post-run GitHub side-effect audit — advisory + detective) · **#4671** (gh-guard shim: PATH-interposed `pilot gh-guard`, Go argv policy, deny sibling/cross-repo mutations, fail-closed for writes — preventive). Option (4) (drop `--dangerously-skip-permissions` for settings deny rules) deliberately not taken now. **Branch protection on main** → TASK-405 founder-decision list item 7 (decide alongside #4671 delivery).

## Second incident: the fix cluster fragmented itself (2026-08-03)

**#4655 was dispatched without `no-decompose`** (authoring miss — the four console/ui issues
filed the same day DID carry it). It fragmented into 7 sequential children (#4659–#4665), all
editing the same ~40-line region of `runner.go`'s epic decision block, each branching from a
different view of `main`:

| Child | PR | Outcome |
|---|---|---|
| #4659 gate function | #4666 | ✅ merged — **on main but called by nothing** (inert, not half-broken) |
| #4660 insert gate call | #4667 | +290, all checks green, **CONFLICTING** |
| #4661 route to coordinator | #4672 | +726/−12 across **10 files incl. unrelated `gh3938_test.go`/`gh4405_test.go`**, CONFLICTING, zero checks |
| #4663 isSinglePackageScope | #4674 | +170, green, mergeable |
| #4664 finalize behavior | #4675 | +81, green, mergeable |
| #4662 planning-fallback | — | heartbeat SIGKILL mid-`go test` (the #4668 bug) |
| #4665 regression test | — | canceled mid-run |

~1,270 lines for a ~150-line change. **Resolution**: all 4 PRs closed + branches deleted,
all children closed `not planned`, parent #4655 closed, whole fix re-filed as **#4677**
(`no-decompose`). Merging the mergeable subset was rejected: the gate's call site lived in
the CONFLICTING #4667, so it would have landed a half-wired intermediate state no reviewer
validated as a whole — the exact failure mode this cluster exists to prevent.

**Cancellation was harder than it should be** (→ #4678): closing an issue does not stop
queued/running executions (#4656's gap), and marking rows `stalled` is read by the dispatcher
as *"dead owner, retry me"* — `dispatch re-pick: prior claim was stall-killed — claiming next
generation without counting toward repick hard cap` — so each stall spun a new generation
(#4655 reached gen-3). The poller also re-dispatched #4655 at 12:03:31Z, 8 min after the issue
was closed. There is no `pilot task cancel` verb. Memories: [[pilot_issue_missing_no_decompose_fragments_single_fix]],
[[pilot_stalled_status_is_retry_not_cancel]].

**Near-miss caught in passing** (→ #4679): #4660/#4661 were closed by the epic flow while
their PRs were open and unmerged. #4657's merged fix would then have auto-closed green,
unlanded PR#4667 purely on "issue closed + conflicting" — no compare-before-close.

**Cleanup self-inflicted one more**: the docs commit `69ea300b` contained the phrase
`compare-before-close #4679`, which GitHub parsed as the closing keyword `close #4679` and
auto-closed the just-filed issue. Reopened; memory written
([[commit_message_hyphenated_close_keyword_autocloses_issues]]). Also closed PR#4676
(GH-4665 regression test, +164 mergeable) — it pins behavior whose call site never landed;
scope folded into #4677.

## Refs

- Issues: **#4677** (single-PR re-file of the coordinator fix, supersedes #4655) · **#4656** (issue-state revalidation, now unblocked) · **#4657** ✅ merged PR#4658 · **#4668** (heartbeat liveness) · **#4669** (dead intent judge) · **#4670** (prompt scope + side-effect audit) · **#4671** (gh-guard shim) · **#4678** (operator cancel verb) · **#4679** (compare-before-close)
- Closed in cleanup: #4655 + children #4659–#4665; PRs #4667/#4672/#4674/#4675 (branches deleted); #4666 merged and retained
- Trace source: nav-research report 2026-08-03 (this doc summarizes it; file:line refs verified at `d959cfd6`).
- Prior art: TASK-401 (#4216 decomposed-children guard — the narrow case), GH-724 (conflict-before-CI check), GH-4646/PR#4647 (terminal-status pattern to mirror), TASK-431 (incident-record precedent).
- Related: TASK-436 (the feature whose dispatch triggered this incident; its main leg shipped via #4648→PR#4652, released v2.251.1).
