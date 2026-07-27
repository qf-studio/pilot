# fix(executor): epic abort path orphans child executions as permanent `running` — finalize children on parent failure + evictor must heal the ledger row

**Created**: 2026-07-27 · **Status**: Draft · **Last Updated**: 2026-07-27

## Problem

When an epic parent dies (e.g. `epic PR creation failed`), its child
executions can be left in `executions.status = 'running'` forever. The
dashboard then renders zombie rows — meter 100% (event ladder finished) with
status "running" — the header over-counts running tasks, and the admission
claim treats the children as in-flight, blocking any retry.

Live occurrence (founder box, 2026-07-26, project `auth-service`):

```
GH-470  running  created 23:20:53   ← zombie; its work FINISHED (PR auth-service#475, 23:49:16Z)
GH-469  running  created 22:43:59   ← zombie
GH-468  running  created 22:33:11   ← zombie
GH-467  completed        22:23:35
GH-431  infra            22:22:10   ← epic parent: "epic PR creation failed:
                                       No commits between main and pilot/GH-431"
```

The daemon *noticed* the orphans and did nothing durable:

```
00:40:38 WARN evicting orphaned stuck-task entry task_id=GH-468 stuck_for=2h0m0s
01:08:38 WARN evicting orphaned stuck-task entry task_id=GH-469 stuck_for=2h1m0s
01:44:38 WARN evicting orphaned stuck-task entry task_id=GH-470 stuck_for=2h0m0s
```

Eviction (`internal/alerts/engine.go:699`) removes only the in-memory
stuck-task tracker entry; the DB row stays `running`.

Same parent-failure signature hit twice that day: GH-435 (18:37:43Z) and
GH-431 (23:49:23Z), both `epic PR creation failed: … No commits between main
and pilot/GH-NNN (createPullRequest)`.

Related smell in the same window (may share a root cause — investigate, fix if
in scope): GH-4211 observation warnings for the child PR
`no execution row for task` then `execution row has no started_at`
(pr=475 task_id=GH-470) — a child row created without `started_at`.

## This is the TASK-404 lifecycle class

TASK-404 B1 routed the epic sub-issue **success/failure-per-child** path
through `ExecutionLifecycle.Finish` (`finalizeSubIssueExecution`,
`internal/executor/epic.go:2475`, call sites ~2721–2802). The gap: when the
**parent** aborts (`internal/executor/runner.go` ~1790/1808 — sets
`result.Error = "epic PR creation failed: …"`, reports progress, returns),
nothing sweeps the children that already have non-terminal ledger rows.

## Fix

> **Refinement 2026-07-27** (issue comment on GH-4560): in the live incident
> all three children had actually SUCCEEDED — PRs auth-service#472/#474/#475
> merged; only Finish was skipped. Finalize by ACTUAL outcome, not blanket
> `stalled`: reached `pr_created` → `completed`; never reached it → `stalled`.
> The "No commits" umbrella failure is EXPECTED when children merge directly
> to main — treat as benign parent outcome, never as child failure.

1. **Epic abort path finalizes children.** On any parent-terminal failure
   (`epic PR creation failed`, title rejection, decompose abort), enumerate the
   epic's child executions with non-terminal status and route each through
   `ExecutionLifecycle.Finish` with the child's actual outcome (see refinement
   above), error naming the parent's failure where the child had none. Use the
   existing lifecycle API — do NOT add a second write path
   (that is the FK-787 class TASK-404 exists to kill).
2. **Orphan evictor heals the ledger.** When the stuck-task evictor
   (`internal/alerts/engine.go:699`) evicts an entry whose execution row is
   still non-terminal, transition the row via the lifecycle API (status
   `stalled`, error noting orphan eviction) instead of leaving the DB claiming
   `running`. This is the safety net for any future path that abandons rows.
3. **Claims**: verify that transitioning the row to terminal releases/expires
   the admission claim so the task is retryable (the whole point of the heal).
   If claims need explicit release, do it in the same transition.

## Acceptance criteria

- Test: epic parent aborts at PR-creation with ≥1 child execution row in
  `running` → child rows become `stalled` with parent-failure error; ladder
  events preserved.
- Test: evictor fires on an entry whose row is `running` → row transitions to
  `stalled` via lifecycle; a second eviction of an already-terminal row is a
  no-op (no double-Finish error).
- After heal, a re-dispatch of the child task is admitted (claim not blocked
  by the zombie row).
- All writes go through `ExecutionLifecycle` — no raw status UPDATEs.
- `go test ./internal/executor/ ./internal/alerts/` green.

## Out of scope

- The `No commits between main and pilot/GH-NNN` empty-umbrella-branch failure
  itself (why the epic branch had no commits) — separate defect; do not fix
  here, but keep its error text in the finalization message.
- Dashboard rendering (`stage_strip.go`) — it correctly renders what the
  ledger says; no display change needed.

## Refs

- Live incident: auth-service GH-431 epic, children GH-467–470, 2026-07-26
- Prior art: TASK-404 (`.agent/tasks/TASK-404-execution-lifecycle-chokepoint.md`), GH-4243 (B1)
- Display-mismatch background: GH-4368, GH-3927/GH-4064
