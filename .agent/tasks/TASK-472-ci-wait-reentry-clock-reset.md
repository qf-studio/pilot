# fix(autopilot): reset CI-wait clock on WaitingCI re-entry; TerminalLabel on the API-failure timeout path

**Status**: 🚀 Dispatched 2026-08-12 → [pilot#4855](https://github.com/qf-studio/pilot/issues/4855) (`pilot` + `no-decompose`; from PR#4853 post-merge review D1–D3)
**Created**: 2026-08-12
**Assignee**: Pilot

## Context

Post-merge review of PR#4853 (GH-4851, verdict on the PR) confirmed the confirm-poll fix but found the new confirmed-timeout + TerminalLabel combination creates a fresh terminal-stranding class on the CI re-trigger paths:

- Three paths re-enter `StageWaitingCI` WITHOUT resetting `CIWaitStartedAt`: infra-outage rerun (internal/autopilot/controller.go:2902), auto-rebase (:5318), mechanical go.mod resolution (:5434) — contrast :5709-5710 and :5781-5782, which do reset. Scenario: PR waits 25m → CI fails → infra-outage rerun → re-enters WaitingCI with the original clock → rerun still `in_progress` at minute 31 → deadline exceeded, same-tick read = pending → instant CONFIRMED timeout stamped `TerminalLabel=pilot-failed`. Pre-#4853 this blind-timed-out too but self-healed via retry-ready on close; now it is a permanent strand with no spawned fix issue and no owner.
- `TerminalLabel` is not persisted (`SavePRState`/`LoadAllPRStates`, internal/autopilot/state_store.go:638-792 omit it; the types.go:1166-1168 comment claims restart-safety that is false for the timeout branch — a timed-out PR stays open indefinitely and the human close may come days and several restarts later). Mostly healed by re-adoption (`RestoreState` skips StageFailed rows at controller.go:1769; the reconciler re-adopts within 60s), residual: a close landing in the re-adoption window reaches `notifyExternalClose` with empty TerminalLabel and no claim → retry-ready re-armed — the GH-4851 incident shape, minutes-wide, restart-gated.
- The consecutive-API-failure branch (controller.go:2267-2275; five `CheckCI` errors → StageFailed) still produces the exact GH-4851 incident fingerprint: zero successful polls, ci_status at adoption default, no TerminalLabel → external close defaults to retry-ready.

## Implementation

1. Reset `CIWaitStartedAt = time.Now()` at the three WaitingCI re-entry sites (controller.go:2902, :5318, :5434) — they explicitly trigger new CI, so the deadline must measure the new run.
2. Stamp the same TerminalLabel on the consecutive-API-failure branch (:2267-2275) so that path can no longer arm retry-ready via external close.
3. Close the TerminalLabel restart gap: either persist `terminal_label` in the PR-state store AND have `notifyExternalClose` consult it for the close-in-re-adoption window, or — if that cannot close the window — fix the stale types.go:1166-1168 comment and add a regression test documenting the accepted residual. State the choice in the PR description.

## Acceptance

- Test per re-entry path (at minimum the infra-outage rerun): re-entry at minute 25, rerun `in_progress` at minute 31 → NOT a timeout; deadline measured from re-entry.
- Test: five consecutive `CheckCI` errors → StageFailed carries a TerminalLabel; external close does not arm `pilot-retry-ready`.
- GH-4851 suite + GH-4384/4415/4478/4646 family pass unchanged.

## Refs

- PR#4853 post-merge review verdict (comment on the PR) — D1 major, D2 medium, D3 minor.
- Incident lineage: GH-4851 ← PR#4846 strand (TASK-467).
