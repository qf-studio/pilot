# fix(memory): canary flag on declined-preflight rows — sdk interface leg + pilot stamping leg

**Status**: ✅ Leg 1 MERGED + REVIEWED 2026-08-11 → sdk PR#112 (**verdict: APPROVE, additive-clean, no sdk follow-up**; posted on the PR). **NOT YET RELEASED** — latest sdk tag v0.33.0; next sdk train tags it. Leg 2 [pilot#4845](https://github.com/qf-studio/pilot/issues/4845) UNLABELED and **NEEDS RE-ANCHOR before labeling** (reviewer-confirmed drift): stamping goes in the NEW `core.ExecutionSaverV2.SaveDeclinedExecutionRecord(core.DeclinedExecutionRecord{…RepoOwner, RepoName})` — NOT the legacy method; implement with a **VALUE receiver** on `storeExecutionSaver` (poller wires a value; pointer receiver silently keeps the legacy path) + `var _ core.ExecutionSaverV2 = storeExecutionSaver{}`; treat empty RepoOwner/RepoName as fall-back-to-path (sdk lax-parse Low defect). Gate: first sdk tag containing e426f27 + pin bump.
**Created**: 2026-08-11
**Assignee**: Pilot

## Context

PR#4837 (GH-4833) fixed canary stamping for executed tasks via `ProjectRepo` precedence. Post-merge review found the second construction site: `storeExecutionSaver.SaveDeclinedExecution` (cmd/pilot/main.go:4098-4109), called from studio-sdk's pre-flight judge (poller.go:1622 sdk-side), writes `declined-preflight` execution rows with `IsCanary` unset. Canary issues declined at preflight land as `is_canary=0`.

Impact: zero cost skew (declined rows carry no cost/tokens) but fleet **volume** pollution — `GetBriefMetrics.TotalTasks` (internal/memory/store.go:2001), `WindowedStats.AttemptTotal` (store.go:3884), and `GetIssueLevelCounts.Attempted` (store.go:3922, where any row marks an issue attempted-never-shipped, dragging fleet ship-rate).

Why two legs: the `ExecutionSaver` interface lives in **studio-sdk** and carries no repo identity — the `projectPath` it passes is the same colliding shared path that caused GH-4833. Repo identity must be plumbed through the SDK interface before pilot can stamp correctly.

## Implementation

**Leg 1 — studio-sdk** (issue filed in qf-studio/studio-sdk): extend the `ExecutionSaver` seam so the decline path carries repo identity — add the repo (owner/name) of the declined issue to the `SaveDeclinedExecution` signature or its record struct, threaded from the poller call site (poller.go:1622 area). Backward-compatible evolution per the sdk's convention (new method or widened struct — follow how sdk#109 evolved a client seam). Tag a release.

**Leg 2 — pilot (THIS issue; gated: dispatch only after leg 1 is released and the sdk pin is bumped)**:
1. Bump the sdk pin to the leg-1 release.
2. In `storeExecutionSaver.SaveDeclinedExecution` (cmd/pilot/main.go:4098-4109), resolve the project via `FindProjectByRepo` with the now-available repo identity (same precedence idiom as PR#4837's `handleIssueGeneric` fix, cmd/pilot/handlers.go:892-897) and stamp `IsCanary`.
3. Fold-in: `declined-preflight` status never matches the `status = 'declined'` bucket in the 9-way breakdowns — either include it in that bucket in the queries or add it explicitly; state the choice.
4. Test: e2e — declined canary issue produces a row with `is_canary=1` in real SQLite; fleet `WindowedStats.AttemptTotal` excludes it under canary scoping.

## Acceptance

- Leg 1: sdk seam carries repo identity; sdk tests green; release tagged.
- Leg 2: declined canary rows stamped `is_canary=1`; fleet volume metrics exclude them; the 9-way-breakdown decision implemented and tested.

## Refs

- Review verdict: https://github.com/qf-studio/pilot/pull/4837#issuecomment-5253747201
- Prior work: PR#4837 (GH-4833) · sdk seam precedent: TASK-461 / sdk#109 → sdk PR#110
