# TASK-399: Dashboard history shows shipped issues as running/failed — reconcile persisted execution status against terminal truth

**Status**: ✅ SHIPPED — #4208 closed `pilot-done` (dispatched 2026-07-12)
**Type**: bug (autopilot + memory-store reconciliation)
**Related**: #4185/#4188 (phantom running card, in-memory Monitor — separate panel), #4099 (recoverStaleParentIssues), mem-088 (verify ledger+GitHub before trusting a ✗)

## Problem

The dashboard HISTORY panel shows issues that are **CLOSED COMPLETED and shipped
via a merged PR** as `running` or `infra`/`failed`. Spot-check: 12/12 alarming
rows (GH-4190/4189/4155/4174/4154/4127/4020/4028/4029/4006/4107/4199) are all
CLOSED COMPLETED on GitHub, yet render red/running. Only GH-4206 is actually
running.

**Root cause (research-verified against origin/main):** the HISTORY panel is a
**pure passthrough of the persisted `executions.status` column** — `displayStatus`
(`internal/dashboard/stage_strip.go:75`) renders `exec.Status` verbatim
(`completed→success`, everything else literal); rows load via
`GetRecentExecutions` → `firstNDistinctByTask` (last-attempt-wins,
`internal/dashboard/tui.go:602,644,683`). There is **no in-memory-card influence
on HISTORY** (that's the QUEUE panel / `executor.Monitor`, a different concern).
So a wrong HISTORY status means the **DB row is wrong** and was never reconciled.
Two distinct DB-write gaps produce this:

### Defect A — orphaned `running` rows never healed
`executions.status='running'` is only advanced to a terminal value by the normal
in-process completion path (`cmd/pilot/handler_common.go:205-209` →
`Monitor.Complete/Fail` + the store write). An execution killed mid-run — daemon
`--replace`, crash, restart, or an epic-parent row whose finalize took a
reconciliation branch — leaves `status='running'` **permanently**. Nothing sweeps
these. `executor.Monitor` is in-memory and reset every boot
(`cmd/pilot/main.go:776,1805`); there is **no startup hydrate** reconciling the
persisted `running` rows against terminal reality.

### Defect B — self-heal coverage gaps leave shipped rows `infra`/`failed`
The reconciler that flips stale `infra/failed/no_op/stalled/rate_limited/skipped`
rows to `completed` is `selfHealForPR` (`internal/autopilot/controller.go:452`) →
`SelfHealExecutionAfterMerge` (`internal/memory/store.go:1446`). It runs from
`handleMerging` (in-process merge, `controller.go:1972`) and
`ScanRecentlyMergedPRs` (externally-merged PRs, `controller.go:3930`). It **never
runs** — leaving the row red — when any of:
- the merge fell outside `ScanRecentlyMergedPRs`' **30-min lookback**
  (`scanWindow`, `controller.go:3843-3845,3862`) — e.g. daemon downtime > window,
  or the scan simply hasn't re-covered it;
- `issueNum` resolution fails — it only matches branch names literally prefixed
  `pilot/GH-N` (`Sscanf(pr.Head.Ref, "pilot/GH-%d")`, `controller.go:3870`);
  `issueNum==0` → `selfHealForPR` no-ops (`controller.go:453`);
- `project_path` scope mismatch between the row and the controller scope passed to
  `SelfHealExecutionAfterMerge`;
- epic-parent rows: `selfHealForPR` heals a parent only once **all** children are
  closed (`openSubIssueCount` guard, `controller.go:466-478`) — a parent stays red
  if `resolveParentIssue` (`controller.go:487`) can't confirm.

## Design decisions

| Decision | Choice | Reasoning |
|---|---|---|
| Where the fix lives | autopilot reconciler + memory store (NOT dashboard) | HISTORY is a faithful mirror of `executions.status`; fix the DB truth, render stays as-is |
| Defect A heal target | terminal-evidence reconcile: merged PR on the row's `pr_url`/branch ⇒ `completed`; else the row is stale-running with no live execution ⇒ mark `interrupted`/`failed` | Orphaned running is not "in flight"; must resolve to a truthful terminal state |
| In-flight safety | exclude rows that are genuinely active: any `task_id` present in the live `executor.Monitor` (running set) or with a recent `execution_events` heartbeat within N min | **Must not** flip GH-4206 (real running) — hard regression gate |
| Defect B widening | (1) startup catch-up sweep with a large lookback (not just 30 min); (2) resolve `issueNum` beyond branch-prefix — also parse PR body `Closes #N` / the `Parent: GH-N` marker, and heal off the row's **stored `pr_url`** against merged state; (3) keep the existing terminal `IN(...)` exclusion of `running/queued/pending` | Closes the coverage holes without a dashboard GitHub call (mem-048) |
| no_op / skipped glyphs | **out of scope — already distinct** (`statusIconStyle` tui.go:2404: `no_op→○`, `skipped→·`, pending style, not red; `mutedOutcomes` dims the ladder) | Research confirmed; no change needed |
| Monitor consistency rider | add `monitor.Complete` to `ScanRecentlyMergedPRs`' external-merge branch (`controller.go:3930`) to match `handleMerging`'s sibling (`controller.go:1972`, GH-1336) | Extends the #4188 discipline: any reconciliation that writes a terminal outcome must also retire the QUEUE card |

## Scope (one issue, one PR — shared reconciliation layer)

1. **Reconcile orphaned `running` rows** — startup + periodic sweep in the
   autopilot controller (or a new store method it calls) that finds
   `status='running'` rows NOT currently in flight and resolves each to a terminal
   status from evidence (merged PR on `pr_url`/branch → `completed`; otherwise
   `interrupted`/`failed`). In-flight exclusion is mandatory (Monitor running-set
   + recent-heartbeat guard).
2. **Harden `selfHealForPR` coverage** — (a) startup catch-up sweep with a wide
   lookback; (b) `issueNum` resolution beyond `pilot/GH-N` branch prefix (PR body
   `Closes #N` / `Parent: GH-N`, and heal directly off the row's stored `pr_url`);
   (c) keep the terminal-status exclusion intact.
3. **Monitor consistency rider** — `monitor.Complete` on the external-merge branch.

## Acceptance criteria

- [ ] After a daemon restart, no `executions.status='running'` row survives for a
  task that is not actually executing (verified: only genuinely-live tasks show
  running). GH-4206-style live rows are **never** flipped mid-execution.
- [ ] The 12 confirmed-shipped issues above render green/`success` in HISTORY
  (their DB rows heal to `completed`), driven by the reconcile — not a manual DB edit.
- [ ] A shipped issue whose PR merged outside the 30-min window and/or on a
  non-`pilot/GH-N` branch still heals to `completed`.
- [ ] `SelfHealExecutionAfterMerge` still excludes `running/queued/pending`
  (no regression to the in-flight guard).
- [ ] `no_op`/`skipped` rendering unchanged (already correct).
- [ ] External-merge reconcile calls `monitor.Complete` (QUEUE card retires).

## Regression guards (call out in PR)

- **GH-4206 test**: a row that is genuinely `running` with a live Monitor entry /
  fresh heartbeat MUST NOT be healed by the orphan sweep. Add a table test.
- No new GitHub API calls inside the dashboard or any poll/heal hot loop beyond
  the existing scan cadence (mem-048); reconcile off persisted `pr_url` + existing
  scanner data, not fresh per-row GH lookups.
- Idempotent: re-running the sweep changes nothing once rows are terminal.

## Verify

```bash
go build ./... && go vet ./...
go test -race ./internal/autopilot/... ./internal/memory/... ./internal/executor/... ./internal/dashboard/...
# live: restart daemon → HISTORY shows only genuinely-running tasks as running;
#       previously-red shipped issues (e.g. #4189/#4155/#4006) read success.
```

## Out of scope

- Dashboard render/glyph changes (HISTORY is a faithful mirror; no_op/skipped already distinct).
- QUEUE-panel Monitor rework beyond the one external-merge `monitor.Complete` rider.
- Any change to executor finalization semantics or re-running failed executions.

## Refs (origin/main, research-verified 2026-07-12)

- Render: `internal/dashboard/stage_strip.go:75` (`displayStatus`), `internal/dashboard/tui.go:602,644,683,2404`
- Heal: `internal/autopilot/controller.go:452,466-478,487,1972,3843-3862,3870,3930`; `internal/memory/store.go:872,1446`
- Orphan-running origin: `internal/executor/monitor.go:42`, `cmd/pilot/handler_common.go:205-209`, `cmd/pilot/main.go:776,1805`
- Template pattern: #4188 (`internal/executor/epic.go:1740-1788` — reconciliation path must also touch Monitor)
