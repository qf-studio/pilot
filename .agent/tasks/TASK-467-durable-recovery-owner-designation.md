# fix(autopilot): recovery-owner designation must survive restart — persist it and extend the seam to the review path

**Status**: ✅ **RESOLVED 2026-08-11 — manually merged 21:50Z (squash dd9c945) after pre-merge review; incident closed out.** Review verdict (posted on the PR): **HOLDS WITH DEFECTS** — spec items 1–3 delivered (option 1b: durable-claim consult in `notifyExternalClose`; review path gets designate-before-close via new `spawnReviewIssue` seam + durable claim; rehydration tests mutation-verified real). D1 major (fallback never health-checks the fix issue → dead owner = permanent strand; converts 4849-D1 race from benign to strand) → **tracked in #4852** (dispatched, with 4849-D1 + D3). Root-cause defect (CI wait evaluates deadline before the only `CheckCI` read, controller.go:2152 vs :2206; adoption starts the clock blind at handlePRCreated :2129; ci_status=pending fingerprint = zero polls) → **#4851 dispatched, running same-minute**. Post-merge: daemon applied `pilot-done` + branch cleanup but SKIPPED the issue-close leg (#4841 closed manually 21:55Z); residues: `autopilot_pr_state` row 4846 stays failed/pending until tag-ancestry backfill after next train · bogus terminal failed rung in `execution_events` (controller.go:1701 unguarded — noted out-of-scope in #4851). → Archive once #4852 lands.
**Created**: 2026-08-11
**Assignee**: Pilot

## Context

PR#4840 (GH-4826) built the right seam: `spawnFailureIssue` (internal/autopilot/controller.go:2462) designates exactly one recovery owner per CI-failure close, and TerminalLabel is set iff `CreateFailureIssue` returns a live issue. Post-merge review found the designation is **in-memory only** — one daemon restart in the wrong window resurrects the exact double-arm incident the PR fixed. The daemon has a documented history of mid-flow restarts (GH-4807).

Crash window: `TerminalLabel` (internal/autopilot/types.go:1169) has **no column** in `SavePRState`/`GetPRState` (internal/autopilot/state_store.go:638-727). A crash between the PR close (controller.go:2720) and the end-of-ProcessPR persist (controller.go:2093) rehydrates the PR label-less at StageCIFailed; `checkExternalMergeOrClose` (processAllPRs, controller.go:6881) runs before any handler, has no StageCIFailed guard, and the self-close markers (controller.go:582-591) are also in-memory — so `notifyExternalClose` (controller.go:7455-7458) defaults to `pilot-retry-ready` while the spawned fix issue is live. Double-arm, restored by restart.

Same shape on the review path: `handleReviewRequested` still closes before designating — direct `CreateReviewIssue` (controller.go:3007), close (controller.go:3050), designation after (controller.go:3065), in-memory.

## Implementation

1. **Persist the ownership decision.** Either add a `terminal_label` column to the PR-state store (SavePRState/GetPRState/RestoreState roundtrip, migration included) **or** make `notifyExternalClose` consult the durable `autopilot_spawned_fixes` claim before defaulting to retry-ready. Prefer whichever is smaller and closes BOTH the pre-merge (controller.go:2672) and post-merge (controller.go:4283) rungs; state your choice in the PR description.
2. **Extend the designate-before-close seam to `handleReviewRequested`** — route it through `spawnFailureIssue`-style ordering (designate, then close), or persist its designation the same way as (1).
3. **Crash/rehydration test**: simulate close → crash (no persist) → RestoreState → external-close scan; assert retry is NOT armed while a spawned fix issue is designated. This is the test PR#4840's review flagged as missing (structural string-match only today, gh4826_test.go:443).

Out of scope: owner-death recovery (preflight-rejected fix issues) — separate issue; iteration-cap sites (controller.go:2594/2616) spawn nothing, leave them.

## Acceptance

- Daemon restart at ANY point between PR close and persist does not arm `pilot-retry-ready` when a spawned fix issue is the designated owner (proven by the rehydration test, not by inspection).
- Review path shares the designate-before-close ordering or durable designation.
- Existing GH-4826 tests still pass; no schema change beyond the one migration if option (1a) chosen.

## Refs

- Review verdict: https://github.com/qf-studio/pilot/pull/4840#issuecomment-5253781955 (D1, D4)
- Prior work: PR#4840 (GH-4826), incident GH-4820, restart history GH-4807
