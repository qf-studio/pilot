# fix(autopilot): guard handleReviewRequested against poisoned review claims; owner-health gaps in dedup + declined classification

**Status**: ✅ Merged + REVIEWED 2026-08-12 → [pilot#4856](https://github.com/qf-studio/pilot/issues/4856) → **PR#4858** (squash 5a5e0bfb). **Verdict: HOLDS WITH DEFECTS** (posted on the PR) — all 3 spec items implemented faithfully; interaction checks pass (held-PR surfaces via alert+label, claim-release race single-winner, GH-4852 scenarios unchanged, composes with PR#4857; `-race` green). Follow-ups: **D1 (MAJOR test gap)** — the `(0, nil)` half of the review guard is untested; mutation `if err != nil` survives the whole suite; producers (crash between create+record · concurrent claim · lookupErr fail-open) flow through the untested branch whose failure = the original bug; cheap fix = seed `ClaimSpawnedFix` without `RecordSpawnedFixIssue` → assert PR/branch intact. **D2 (minor)** — dedup paths now REPLACE a resumable owner (open+`pilot-needs-clarification` is documented resumable) → duplicate executors + review-branch clobbering window + open zombie + repeated alerts; fix = reuse-don't-replace in the dedup re-checks. **N1 parity** — `CreateFailureIssue` create-error path still poisons its claim forever (no `ReleaseSpawnedFix`); CI path deserves the same treatment. Live on the box since the 11:02Z restart (v2.259.0-14).
**Created**: 2026-08-12
**Assignee**: Pilot

## Context

Post-merge review of PR#4854 (GH-4852, verdict on the PR): both headline strand fixes hold (mutation-verified), but the review-path claim-before-create ordering introduced a regression at an unguarded caller, plus two owner-health gaps:

- **Claim poisoning meets an unguarded caller**: `CreateReviewIssue` (internal/autopilot/feedback_loop.go:577-593) can return `(0, nil)` when the claim row exists with no recorded issue — the common case is a transient `CreatePilotIssue` error on the first attempt, which leaves the claim taken with `issue_number=0` forever (nothing releases it). `handleReviewRequested` (controller.go:3114-3117) checks only `err != nil` → proceeds with issueNum=0: closes the reviewed PR (:3157), deletes the branch (:3161-3164), review round discarded; `notifyExternalClose`'s fallback finds no `issue_number>0` row → retry-ready → the source re-runs FROM SCRATCH instead of addressing review feedback. The CI path guards this exact shape (GH-4459, controller.go:2761-2784, escalate-and-hold). Regression vs pre-#4854: create errors were simply retried next tick.
- **Dedup-hit branch skips the owner-health re-check** its `CreateFailureIssue` twin performs (feedback_loop.go:584-592 returns `existing` unverified; compare :317-338): crash-in-window + human closes the revision issue during downtime → dedup returns the dead issue → `spawnReviewIssue` sets TerminalLabel (controller.go:3053-3055) → `notifyExternalClose` takes the TerminalLabel branch (:7571-7573), bypassing the health-checked fallback → `pilot-failed` toward a corpse.
- **Preflight-declined owner reads as alive**: `classifyOwnerHealth` (owner_death.go:36-44) counts only closed-without-pilot-done as dead; a preflight-declined fix issue stays OPEN with `pilot-needs-clarification` and never dispatches. In the exact TASK-468 D1 ordering: the reaction correctly re-arms retry-ready, then the fallback re-designates the declined zombie with `pilot-failed` minutes later — contradictory audit trail + a burned retry rung.

## Implementation

1. **GH-4459-style guard in `handleReviewRequested`**: on `issueNum <= 0` with nil error, escalate-and-hold (PR + branch intact), mirroring controller.go:2761-2784. Also release (or complete) the claim row when `CreatePilotIssue` fails so a transient error cannot poison the claim forever.
2. **Health re-check on the dedup-hit branch** of `CreateReviewIssue` — mirror the `CreateFailureIssue` twin (feedback_loop.go:317-338).
3. **Classify preflight-declined owners as dead**: treat open + `pilot-needs-clarification` as ownerDead in `classifyOwnerHealth`, or skip fallback designation when the source already carries retry-ready.

Optional nits if in the area: the false "rearmed" alert fires before the issueAlreadyClosed gate (controller.go:7606-7629); the decline reaction and external-close fallback can double-fire `emitOwnerDeathAlert` for the same source (no dedup).

## Acceptance

- Test: transient `CreatePilotIssue` error on a review round → next tick does NOT close the PR or delete the branch; escalates and holds; a later successful create proceeds normally (claim not poisoned).
- Test: dedup-hit returning a closed revision issue → health re-check prevents TerminalLabel toward the corpse.
- Test: preflight-declined (open + `pilot-needs-clarification`) owner → classified dead; no re-designation after the reaction re-arms the source.
- GH-4841 / GH-4852 / GH-4826 / owner-death suites pass unchanged.

## Refs

- PR#4854 post-merge review verdict (comment on the PR) — D1 major, D2/D3 minor, D4/D5 nits.
- GH-4459 guard precedent (controller.go:2761-2784) · TASK-468 D1 · GH-4842.
