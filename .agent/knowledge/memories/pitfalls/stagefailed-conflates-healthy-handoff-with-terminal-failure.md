---
name: stagefailed-conflates-healthy-handoff-with-terminal-failure
description: StageFailed + TerminalLabel=LabelFailed are set on HEALTHY continuation hand-offs (fix/revision issue spawned successfully) exactly as on genuine terminal failures — the ledger and dashboard count successful revisions as pipeline failures (GH-5227 research, unfixed leg 2)
type: pitfall
---

# StageFailed means "this rung is done", not "the work failed" — and the ledger can't tell the difference

**Found during GH-5227 verification (2026-08-27, nav-research).** `StageFailed`
is set by ~25 sites in `internal/autopilot/controller.go`, covering both
genuinely terminal outcomes (iteration limits, merge-conflict caps, release
failure) AND the *normal, healthy* revision cycle: after `spawnFailureIssue`
(~3749) or `spawnReviewIssue` (~4128) succeeds, the old PR is closed by design
and staged `StageFailed` (~3770, ~4143). Both spawn seams also always set
`TerminalLabel = github.LabelFailed` (~3408, ~4004) — even on success.

**Consequence:** `notifyExternalClose` (~9118) branches on `TerminalLabel`
(LabelFailed vs LabelSuperseded), so a successful hand-off to a revision issue
gets `ReclassifyCompletionAsFailed` + unconditional `c.monitor.Fail` (~9190).
Every routine review-revision cycle is recorded as a pipeline failure in the
execution ledger, dashboard, and `RecordPRFailed` metrics. This is what made
PR#615 (GH-5227) read as "defective" when it was a healthy hand-off.

**Why it hasn't been fixed casually:** disambiguation today is via auxiliary
in-memory flags (`BreakerHoldActive`, `RebaseHoldActive`, `TerminalLabel`),
not distinct Stage values. Introducing e.g. `StageSuperseded` for healthy
hand-offs touches: `ProcessPR` switch (~2805 terminal no-op), `RestoreState`
skip-list, `release_backfill.go:198` two-stage check,
`isStackBaseCandidateStage` (~6815), and the three re-drive guards
(`reAdoptHeldRebasePR` 7167, `redriveFailedPRForBaseRetarget` 7257,
`ReDriveBreakerHeldPRs` 7336) which gate on `StageFailed` + hold-flag.
`notifyExternalClose` needs the least change (already keys off TerminalLabel).

**How to apply:** treat this as the planned leg 2 of TASK-486 (out-of-scope
section has the full consumer inventory). Feeds [[TASK-460]] false-evidence
class — failure metrics currently overcount by the healthy-revision rate.
Do not trust `RecordPRFailed` / dashboard fail counts as defect rates until
this lands. Related: [[bug_autopilot_silent_pr_close_notifyexternalclose_convergence]].
