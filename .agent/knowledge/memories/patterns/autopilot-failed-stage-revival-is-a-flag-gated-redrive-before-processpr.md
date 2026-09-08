---
name: autopilot-failed-stage-revival-is-a-flag-gated-redrive-before-processpr
description: the only way a StageFailed PR comes back to life is a narrow, flag-gated redrive in processAllPRs BEFORE ProcessPR (reAdoptHeldRebasePR, redriveFailedPRForBaseRetarget) — reset Stage=waiting_ci + CIWaitStartedAt, keep attempt counters; OnPRCreated is registration-only and the orphan reconciler skips tracked PRs
type: pattern
---

# Failed-stage revival = flag-gated redrive before ProcessPR

**Shape (controller.go, 2026-09-08 research for #5378):**
- `processAllPRs` (~9082-9095) runs `reAdoptHeldRebasePR` (~7649-7688,
  gated on `RebaseHoldActive`) and `redriveFailedPRForBaseRetarget`
  (~7739-7779) BEFORE `ProcessPR`. Each: `Stage=waiting_ci`,
  `CIWaitStartedAt=now`, preserve `RebaseAttempts`/`MergeAttempts`, persist.
- `ProcessPR` itself never leaves `failed`.
- `OnPRCreated` (~2558-2568) is idempotent by `activePRs[prNumber]` membership
  only — a revision run's "PR created" notification for a tracked failed PR
  is swallowed at Debug.
- `reconcileOrphanPRs` (~8299, `if tracked { continue }` ~8329) only
  registers untracked PRs.
- No `FailureReason` on `PRState`: free-text `Error` (~25 sites) + hold
  booleans `RebaseHoldActive` / `BreakerHoldActive` (~7581-7587).

**Rule:** to make a new failure class recoverable, add a purpose-built hold
boolean set ONLY at that failure's `escalateAndHold` site, and a third redrive
in `processAllPRs` keyed on it + a head-SHA change. Never string-match
`Error`; never add stage logic to `OnPRCreated`. First application: size-guard
hold → pilot#5378.
