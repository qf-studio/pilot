# TASK-356: Pilot-core findings from the SDK M2 board-loop run (2026-06-01)

**Status:** Finding #1 ✅ **SHIPPED — PR #3383, released v2.166.7** (2026-06-01); #2/#3 open.
**Priority:** P1 (finding #1) + P2/ops (#2, #3)
**Related:** [[TASK-319]] (board loop), [[TASK-354]] (orphaned cards / non-PR transitions), [[TASK-355]] (no-op false-positive class), [[TASK-325]] (scope/size merge gate)
**Source:** interactive loop session — drove `qf-studio/studio-sdk` #11 epic to done (M2.1 #15, M2.2 #19+#21, M2.3 #23+#25 all merged).

---

## Finding #1 — Epic auto-decomposition DISCARDS the child's work (P1, pilot-core, MANUAL)

**Severity:** high — silently loses completed work and records a false `completed` no-op.

When `executor.DetectComplexity` classifies a task as `ComplexityEpic` (`internal/executor/complexity.go`)
and the issue lacks the `no-decompose` label, the autopilot (`internal/executor/epic.go` +
`decompose.go`) spawns a **child issue** with an `<!--autopilot-meta\nparent: GH-N\ninherited-spec: true-->`
marker and a condensed spec. Observed failure (studio-sdk #16 → child #17):

1. Parent #16 dispatched (exec `7d81cb0e`, branch `pilot/GH-16`, **empty** worktree — it only orchestrates).
2. Child #17 executed the real port in **its own** worktree (`pilot/GH-17`), committing file-by-file
   (client/types/converter/notifier + poller written — ~7 files over 26 min).
3. Child #17 was then **CLOSED with no merge**, its branch **never pushed** to the remote, worktree reset to base.
4. Parent #16 recorded `status=completed`, `commit_sha=9416e92` (= base/main HEAD, a **foreign SHA**),
   `0 files`, **no PR**. Card orphaned In Progress. **26 minutes of real work — gone.**

**Root cause hypothesis:** the parent execution finalizes against its own (empty) worktree while the
work lives in the child's worktree; the child's branch is never pushed/merged back. A worktree/execution
attribution split (same family as [[TASK-323]] + [[TASK-355]]).

**Epic triggers** (`detectEpic`, line 236): epic keyword (`epic|roadmap|multi-phase|milestone`) **AND**
(`phaseCount>=3` OR `checkboxCount>=5` OR `wordCount>200`). A *thorough* spec written to defeat the no-op
ironically trips this and routes into the work-losing path.

**Escape hatch that works:** the **`no-decompose`** label (`decompose.go:52`, `NoDecomposeLabel`) downgrades
epic→complex so the task runs **directly** in one worktree (verified: #18/#20/#22/#24 all ran direct, no
child, produced real PRs).

**Fix directions:** (a) push/merge the child branch before closing it (don't discard); (b) the parent must
adopt the child's commits, not finalize an empty worktree; (c) at minimum, never record `completed`+foreign-SHA
when 0 commits were produced (fold into [[TASK-355]]).

**✅ Implemented — PR #3383 (manual; Pilot can't fix its own execution guard, Wave 0 / TASK-320 B2 precedent):**
- **`epic.go ExecuteSubIssues`** — work-loss guard: a child runs with `CreatePR=true`, so `Success && CommitSHA!="" && PRUrl==""`
  means committed-but-undelivered work. Fail loud + halt the epic, leaving the child issue **open for recovery**
  instead of closing it (covers fix direction (c), and stops the silent discard half of (a)).
- **`runner.go` epic finalization** — harvest the parent `CommitSHA` only **after** the no-commits guard passes,
  so the orchestrator worktree's foreign base SHA is never recorded as the epic deliverable (fix direction (c)).
- Tests: `TestExecuteSubIssues_WorkLossGuard_CommitsButNoPR` + `_NoCommitsNoPR_NoGuard`; `TestSequentialEpicFlow`
  no-PR case updated to the corrected contract. Full `internal/executor` package green.
- **Still open (follow-up):** fix direction (a)/(b) auto-recovery — actually push/adopt the stranded child branch
  rather than only failing loud. The `no-decompose` label remains the reliable escape hatch in the meantime.
- Knowledge: [[epic-decompose-discards-child-work]] (graph `mem-023`).

---

## Finding #2 — Large PRs blocked by stage approval-misconfig; require manual merge (ops; upstream #2598)

`environments.stage.require_approval: true` while `approval.enabled: false` (+ `pre_merge.enabled: false`).
PRs that **escalate to approval** (large/size-gated — small PRs bypass) get an autopilot comment
`🚧 Merge blocked: approval not wired` and never auto-merge. Observed: tiny PRs #13(+66)/#15(+27) auto-merged;
large PRs #19(+2811)/#21(+2060)/#23(+3157)/#25(+2301) all blocked → **manual `gh pr merge` required** (the
comment itself recommends this; tracked in qf-studio/pilot #2598).

**Side effect:** a manual merge skips the autopilot's on-merge board write-back (`controller.go:1245`), so the
card stays In Review even though the issue is closed `pilot-done` → needs a **manual → Done** move. ([[TASK-354]] family.)

**Fix directions:** wire approval (enable + approver) OR make the stage approval gate degrade gracefully when
approval is disabled; and make the on-merge board write-back fire for externally-merged PRs.

---

## Finding #3 — Autopilot adds PR cards with NO Status to the board (P2)

Every PR the autopilot opens for a board-sourced issue is **added to the project as its own card with no
Status** → recurring "no status" clutter (the issue card already tracks state). Observed: #13/#15/#17/#19/#21/#23/#25
all needed manual removal. Recurs on every PR.

**Fix directions:** don't add PRs to the board (track issues only), OR set the PR card's Status to mirror the
issue, OR disable the project's PR auto-add workflow.

---

## Positive learning — the no-decompose split recipe (works)

To land a large connector port via the board loop **reliably**:
1. Split into ~plane-sized (≤~1.5k LOC) sub-issues whose intermediate states compile (e.g. client/data
   layer first, then poller/cleanup/adapter).
2. Lean spec (no epic keywords, ≤4 checkboxes) **+ the `no-decompose` label**.
3. Each runs direct (~20 min incl. a slow ~9→21 min finalize), produces a clean scoped PR.
4. Manual-merge the large PR, manually move the card → Done, delete the no-status PR card.

Verified across gitlab (#18/#20) + azuredevops (#22/#24) — 4/4 clean, zero scope-drift, zero pilot deps.
