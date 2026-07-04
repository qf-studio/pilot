---
name: OnPRCreated check-then-act race lets orphan-reconciler double-register a PR
description: reconciler and the normal poller OnPRCreated callback can both register the same PR, resetting PRState and duplicating approval requests + merge comments
type: pitfall
---

## Symptom

Nightly logs (2026-07-04) showed PRs #3808 and #3820 each going through TWO
full register→CI→escalate cycles: one via `reconciler: registering orphan
PR`, a second via the normal github-poller `OnPRCreated` path ~1–10 min
later — producing two separate `approval_request_id`s per PR. Same failure
mode produced double "PR merged" comments on #3792's linked issue.

## Root cause

`reconcileOrphanPRs` (and `ScanExistingPRs`) do a check-then-act: read
`activePRs[pr.Number]` under `c.mu.RLock()`, release the lock, then — if
untracked — call `c.OnPRCreated(...)` later in the same loop iteration
(`internal/autopilot/controller.go`, `reconcileOrphanPRs`/`ScanExistingPRs`).
Between the check and the call, the normal poller's `OnPRCreated` callback
can register the same PR first. `OnPRCreated` itself then made things worse:
it unconditionally did `c.activePRs[prNumber] = &PRState{...}` — a fresh
struct reset to `StagePRCreated`/`CIPending` — silently overwriting whatever
progress the winning registration had already made (CI wait state,
`ApprovalRequestID`, `MergeNotificationPosted`). The overwritten PR then
replayed the entire pipeline, submitting a second approval request and (if it
reached merge) posting a second "PR merged" comment.

**The general lesson: a caller-side "is it tracked?" check across two lock
acquisitions is not atomic — the source of truth must gate at the single
point that mutates the map, not at every call site that reads it first.**

## Fix (GH-3828)

Made `OnPRCreated` itself the atomic gate: check `activePRs[prNumber]` and
insert under the *same* `c.mu.Lock()` critical section; if already present,
log Debug and return without touching the existing `*PRState`. This makes
registration idempotent regardless of which caller (reconciler, startup
scan, or the real-time poller callback) wins the race — the callers'
own pre-checks become pure optimizations, not correctness-load-bearing.
Approval-request and merge-comment dedup were already correct *per PRState*
(`ApprovalRequestID == ""` guard, `MergeNotificationPosted` flag) — they just
needed the single-PRState-per-PR invariant restored to actually apply.

Added `Metrics.DuplicateRegistrationsSkipped` (plain counter — `OnPRCreated`
has no visibility into which caller lost the race) to make sustained
duplicate-registration attempts observable.

## Where to look

- `internal/autopilot/controller.go` — `OnPRCreated`, `reconcileOrphanPRs`,
  `ScanExistingPRs`
- `internal/autopilot/controller_duplicate_registration_test.go` — regression
  tests: duplicate no-op preserves the same `*PRState` pointer, a `-race`
  concurrent-callers test, and full-flow tests asserting a duplicate
  registration does not spawn a sibling approval request or a second merge
  comment.
