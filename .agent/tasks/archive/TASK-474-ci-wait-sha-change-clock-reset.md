# fix(autopilot): reset CI-wait clock when the head SHA changes mid-wait — fourth re-entry vector

**Status**: ✅ CLOSED 2026-08-13 — merged 08-12 12:37Z ([pilot#4859](https://github.com/qf-studio/pilot/issues/4859) → **PR#4861**); **post-merge review APPROVE** ([verdict](https://github.com/qf-studio/pilot/pull/4861#issuecomment-5279255606)): ordering correct (reset precedes confirm-poll and deadline honor, single per-tick PR read), 3 mutants independently killed, tests drive a real mid-wait SHA change. D1 minor **latent**: `ghPR == nil` + in-handler refresh failure + cached SHA skips the reset and lets a stale pending read confirm the old deadline — unreachable today (sole caller passes non-nil, skips tick on fetch failure); follow-up shape: failed refresh ⇒ cannot-confirm ⇒ don't honor the deadline. N1 stale `DiscoveredChecks` snapshot (cosmetic) · N2 fixtures reuse live PR numbers (recurrence of #4857 N2) · N3 comment misattributes ordering guarantee. **LIVE in production since 08-12 14:18Z** — the v2.259.1 self-upgrade hot restart DID take effect (`upgrade verified complete … via "hot restart"` in daemon.log; the "disk≠process" reading was an uptime-heuristic misdiagnosis — `syscall.Exec` preserves PID/etime).
**Created**: 2026-08-12
**Assignee**: Pilot

## Context

PR#4857 (GH-4855) reset the CI-wait clock at the three stage re-entry sites, but its post-merge review (verdict on the PR) found a fourth re-entry vector the spec didn't enumerate — one where the stage never changes, so it is not a `Stage = StageWaitingCI` assignment site:

- `handleWaitingCI` computes `deadlineExceeded` from `CIWaitStartedAt` (internal/autopilot/controller.go:2208) BEFORE the same tick's head-SHA refresh (:2233-2251, the GH-419/457 logic that updates `HeadSHA` when the branch moved).
- Scenario: PR sits in `StageWaitingCI` for 29m; a post-creation commit lands on the branch (self-review push, human push) and triggers a fresh CI run; next tick refreshes `HeadSHA`, `CheckCI(newSHA)` correctly reads the new run as pending, but `deadlineExceeded` — measured from the ORIGINAL wait entry — is true → instant CONFIRMED timeout, `TerminalLabel=pilot-failed`, permanent strand. The exact class PR#4857 fixed, via the one vector it could not see.

## Implementation

1. Reset `CIWaitStartedAt = time.Now()` inside the `sha != ghPR.Head.SHA` branch of `handleWaitingCI` (controller.go:~2234) — a changed head means a new CI run, so the deadline must measure the new run. Ensure the reset happens before the deadline is honored in the same tick (the confirm-poll ordering from PR#4853/#4857 must keep winning: a same-tick success on the new SHA proceeds to CIPassed regardless).

## Acceptance

- Test: PR in `StageWaitingCI` past the deadline with a changed head SHA whose new CI run is `in_progress` → NOT a timeout; stays `StageWaitingCI`; the new run gets a full timeout window measured from the SHA change.
- Test (guard the ordering): same scenario but the new SHA's read returns success → `StageCIPassed` in the same tick.
- GH-4855 suite (5 tests) + GH-4851 suite + GH-4384/4415/4478/4646 family pass unchanged.

## Refs

- PR#4857 post-merge review verdict (comment on the PR) — D1 medium.
- Lineage: GH-4855 ← PR#4853 review ← GH-4851 incident (PR#4846 strand, TASK-467).
