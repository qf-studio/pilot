# TASK-354: Board card orphans in "In Progress" on non-PR outcomes (no terminal transition)

**Status:** scope-narrowed (2026-06-09) — defense-in-depth fix queued for Pilot
**Priority:** P3 — no live orphans on the board; theoretical gap only
**Severity:** low (was medium; downgraded after live verification)
**Pilot:** yes (poller/spec-guard wiring; not executor core)
**Related:** [[TASK-319]] (board lifecycle loop), TASK-355 (the no-op that originally exposed this), TASK-320/321/341 (no-op classification trail)

## Original incident

TASK-319 go-live smoke test (2026-06-01) on `qf-studio/studio-sdk#11`: the card
moved `Todo → In Progress` then was stranded after a no-op outcome on a
subsequent dispatch — no terminal write moved it to `Blocked`. Manually
unstranded.

## Reconciled scope (2026-06-09 audit)

Re-reading the live state of `cmd/pilot/handlers.go` + Studio SDK board:

| Path | Behavior today | Status |
|---|---|---|
| Genuine no-op (`hr.Result.Error` contains `noOpErrorMarker`, no PR/merge) | `handlers.go:502-520` adds `pilot-failed`/`pilot-blocked` + writes `boardStatuses.Failed` | **already fixed** by TASK-320/321/341 |
| Already-merged no-op | `handlers.go:458-479` writes `Done`, closes issue | already fixed (TASK-321) |
| Awaiting-merge no-op (open PR exists) | `handlers.go:480-501` deliberately writes nothing — the open PR is the deliverable | intentional, not a bug |
| `execErr != nil` | `handlers.go:372-389` writes `Failed` | covered |
| `hr.Result.Success` w/ no commits & no PR | `handlers.go:393-405` writes `Failed` | covered |
| Title rejection / declined | `handlers.go:437-457` writes `Failed` | covered |
| `pilot-in-progress` label orphan (label-level, not board-level) | Periodic sweep in `internal/adapters/github/poller.go` (`sweepStrandedIssues`) | **shipped in PR [#3495](https://github.com/qf-studio/pilot/pull/3495), 2026-06-09** |
| **Spec-guard block** (`applySpecGuard` in `cmd/pilot/spec_guard.go`) | Returns at `handlers.go:311` before the In-Progress write at `:323`. **First-dispatch card stays in Todo (good).** On re-dispatch of a card already in In Progress (e.g., body edited thin after first run), nothing moves it. `applySpecGuard()` has no `boardSync`/`boardStatuses`/`NodeID` access. | **theoretical gap remains** — never observed live |

### Live verification (2026-06-09)

- Studio SDK board (`qf-studio/projects/1`): 38 cards · 31 Done · 2 In Review · 0 In Progress · 5 (none).
- **Zero orphans in "In Progress".**
- Zero `pilot-spec-incomplete` issues on `qf-studio/studio-sdk` (the board's source repo). The spec-guard path has never fired against a board-sourced card since TASK-319 shipped.
- The 5 "(none)" status cards are a separate anomaly tracked in [[TASK-360]] (cards added to the project without a Status field).

## Remaining work — defense-in-depth only

Thread `boardSync`, `boardStatuses`, and `issue.NodeID` into
`applySpecGuard(ctx, client, owner, repo, issue, reasons)` and write
`boardStatuses.Failed` after the labels are applied, using the same guard the
other sites use:

```go
if boardSync != nil && issue.NodeID != "" && boardStatuses.Failed != "" {
    if err := boardSync.UpdateProjectItemStatus(ctx, issue.NodeID, boardStatuses.Failed); err != nil {
        slog.Warn("board sync on spec-guard failed", "issue", issue.Number, "error", err)
    }
}
```

Rationale: on first dispatch the card is in Todo and would transition Todo →
Blocked, which is clearer signal than "card silently stayed Todo while issue
got labels". On re-dispatch of an already-In-Progress card, this closes the
only path that can leave a board orphan today.

## Acceptance

- [ ] `applySpecGuard()` writes `boardStatuses.Failed` to the board (best-effort, idempotent via TASK-319 PR-3 same-column no-op guard).
- [ ] Successful PR path still flows In Progress → In Review → Done (regression guard via existing controller tests).
- [ ] Table-driven test in `cmd/pilot/spec_guard_test.go` (new) using `mockBoardSyncer` style from `internal/autopilot/controller_test.go:6853-6867` — exercise: (a) writes Failed when boardSync set + NodeID set, (b) no-ops when boardSync nil, (c) no-ops when NodeID empty.
- [ ] `make test` + `make lint` green.

## Out of scope

- The 5 "(none)" status cards on the board → [[TASK-360]].
- Any change to the awaiting-merge no-op path (`handlers.go:480-501`) — that's intentional behavior.

## Evidence trail

- Original: `studio-sdk#11` (closed, now Done), 2026-06-01.
- PR [#3495](https://github.com/qf-studio/pilot/pull/3495) — periodic sweep for `pilot-in-progress` label orphans (merged 2026-06-09).
- 2026-06-09 audit (this entry) — board state and code paths re-verified against `main` @ v2.179.0.
