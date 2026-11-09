# TASK-621: Evidence gate — a mutation acceptance bullet that names "live smoke" is held as a live-service check

**Status**: ✅ MERGED 2026-10-29 — E1 #5971 → PR#5973 merged 20:36Z-ish (in v2.302.0, box 10-30 11:03Z); mutation-form bullets no longer trip the live-smoke hold. Archived 10-30. Was: 🚀 DISPATCHED 2026-10-29 — one leg (see Refs). Found on PR#5969 (TASK-620 L7): the live-smoke hold fired on a mutation bullet whose text contains "live-smoke append"; founder approved by hand after 21 minutes.
**Origin**: PR#5969 escalation 19:32Z, reason `Not verified list names a key-gated / live-service check: restore the pre-PR append after the live-smoke append and drop the step-2 cut -> TestHandleCIPassed_PreReviewAndLiveSmoke fails`.

## Why

`IsLiveSmokeBullet` in `internal/executor/acceptance_evidence.go` is a keyword table (`liveSmokePatterns`). Row 1 matches `live[\s-]*(smoke|test|run|check|…)`. A mutation acceptance bullet (`<change> -> <Test> fails`) that talks about the live-smoke code matches the row on its own words, so `RenderAcceptanceEvidenceSections` appends `liveSmokeHoldNote` to its reason and autopilot's `LiveSmokeNotVerifiedBullets` holds the PR. A mutation bullet is never a key-gated or live-service check; the gate already classifies it as `AcceptanceItemMutation` and records "freeform mutation, run manually". The same false hold waits for every PR in the TASK-620 program, whose acceptance bullets name the live-smoke segment by design.

## Legs — pilot (daemon)

### LEG E1 — repo: qf-studio/pilot — title: fix(executor): live-smoke hold ignores mutation-form Not-verified bullets — a "<change> -> <Test> fails" bullet that mentions live smoke is not a live-service check
probe: present qf-studio/pilot func IsLiveSmokeBullet\(
probe: present qf-studio/pilot const liveSmokeHoldNote
probe: present qf-studio/pilot var mutationArrowRe
probe: absent  qf-studio/pilot func TestIsLiveSmokeBullet_MutationFormIsNotLiveSmoke\(

## Context

PR#5969 (issue #5967) was held for human approval at 19:32Z with the reason "Not verified list names a key-gated / live-service check: restore the pre-PR append after the live-smoke append and drop the step-2 cut -> TestHandleCIPassed_PreReviewAndLiveSmoke fails". That bullet is a mutation acceptance item: the gate classified it as a freeform mutation and rendered it under Not verified with the reason "freeform mutation, run manually". IsLiveSmokeBullet in `internal/executor/acceptance_evidence.go` then matched the words "live-smoke" in the bullet text against row 1 of liveSmokePatterns, RenderAcceptanceEvidenceSections appended liveSmokeHoldNote to the reason, and LiveSmokeNotVerifiedBullets returned the bullet to autopilot, which escalated. A mutation bullet names a code change and a test that must fail; it never names a key-gated or live-service check. The mutation form is already recognised by mutationArrowRe in the same file. Every TASK-620 leg carries mutation bullets about the live-smoke segment, so this false hold recurs on each of them.

## Implementation

1. In IsLiveSmokeBullet (or in both call sites: LiveSmokeNotVerifiedBullets and the writeNotVerified closure in RenderAcceptanceEvidenceSections), return false when the bullet's first line matches the mutation form: mutationArrowRe matches and the right-hand side ends with the word "fails" (case-insensitive, optional trailing period). Keep the keyword table unchanged.
2. Apply the same exclusion to a bullet whose first line is a backticked command (a paste-output item that could not run), only if that is a one-line change; otherwise leave it out and say so in the PR body.
3. Tests, table-driven, beside the GH-5568 cases in `internal/executor/acceptance_evidence_gh5568_test.go`: a mutation bullet containing "live-smoke" is not held; a mutation bullet containing "API key" is not held; a plain bullet "Live smoke on four labelled excerpts" is still held; a bullet whose text is a mutation but whose reason line names a live service is not held (the reason is only consulted for non-mutation bullets); RenderAcceptanceEvidenceSections does not append liveSmokeHoldNote to a mutation bullet.

## Acceptance

- `go test -race ./internal/executor/ -run TestIsLiveSmokeBullet` passes; paste the output into the PR body.
- `go test -race ./internal/executor/ -run TestLiveSmokeNotVerifiedBullets` passes; paste the output into the PR body.
- A PR body whose Not verified list holds only the PR#5969 mutation bullet quoted above yields no live-smoke bullets.
- drop the mutation-form exclusion -> TestIsLiveSmokeBullet_MutationFormIsNotLiveSmoke fails
- exclude every bullet that contains an arrow -> TestIsLiveSmokeBullet_PlainLiveSmokeStillHeld fails

## Out of scope

- The keyword table itself and the autopilot side (controller.go) — unchanged.
- The evidence command rule that rejects shell operators such as a pipe inside quotes (separate finding on the same PR).

## Refs

- .agent/tasks/archive/TASK-620-pre-pr-reviewer-replaces-self-review-and-judge.md

## Refs

- E1 → https://github.com/qf-studio/pilot/issues/5971
