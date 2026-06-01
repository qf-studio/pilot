# TASK-354: Board card orphans in "In Progress" on non-PR outcomes (no terminal transition)

**Status:** open — found during TASK-319 go-live smoke test (2026-06-01)
**Priority:** P2 — board-as-source-of-truth is misleading without it
**Severity:** medium (correctness of the board lifecycle)
**Pilot:** yes (poller/controller wiring; not executor core)
**Related:** [[TASK-319]] (board lifecycle loop), TASK-355 (the no-op that exposed it)

## Context

The TASK-319 board lifecycle writes `Todo → In Progress` on pickup, `→ In Review` on PR
creation, `→ Done` on merge, and `→ Blocked` on exec/CI failure. The smoke test
(`qf-studio/studio-sdk#11`) proved sourcing + `Todo → In Progress` work live, but surfaced a
gap: **any outcome that doesn't produce a PR and isn't a controller-level exec/CI failure leaves
the card stuck in "In Progress" forever.** Two such paths observed in one session:

1. **Spec-guard block** — the poller moves the card to In Progress on pickup, then the
   spec-validator slaps `pilot-spec-incomplete`/`pilot-blocked` and never executes. Card orphaned.
2. **Execution no-op** — execution "completes" with no commit/PR (see TASK-355). No PR → no
   `In Review`; not a controller failure → no `Blocked`. Card orphaned in In Progress.

The `→ Blocked` transition only fires in the autopilot controller on iteration-limit / size-guard /
CI failure (`controller.go:917/951/1004`). The **poller-side** block/no-op paths have no board write.

## Approach

- On the poller-side terminal-but-unsuccessful paths, write `statuses.Failed` (Blocked) — guarded
  identically to the existing transitions (`boardSync != nil && IssueNodeID != "" && Failed != ""`):
  - when the spec-guard blocks an issue (`pilot-spec-incomplete`/`pilot-blocked` applied), and
  - when execution returns a no-op / no-deliverable result.
- Alternatively, **don't** write In Progress until the issue clears the spec-guard and is confirmed
  dispatched for execution (move the In-Progress write to *after* spec validation), so a blocked
  issue never enters In Progress in the first place. Cleaner; pairs well with also writing Blocked
  on no-op.
- Ensure idempotent (TASK-319 PR-3 already no-ops same-column writes).

## Acceptance

- [ ] A spec-blocked board issue ends in `Blocked` (or never leaves `Todo`), not orphaned in In Progress.
- [ ] A no-op / no-deliverable execution moves the card to `Blocked`.
- [ ] Successful PR path still flows In Progress → In Review → Done (regression guard).
- [ ] Table-driven tests with a fake board-sync transport for the block + no-op paths.
- [ ] `make test` + `make lint` green.

## Evidence (smoke test, 2026-06-01)
- `studio-sdk#11`: card moved `Todo → In Progress`; spec-block (first run) then no-op (second run)
  both left it In Progress with no terminal write. Manually moved to Blocked to un-orphan.
