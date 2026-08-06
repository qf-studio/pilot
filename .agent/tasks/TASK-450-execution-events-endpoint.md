# feat(gateway): execution-events read endpoint — pilot leg for the S4 per-card timeline

## Problem

The S4 per-card timeline needs stage-by-stage execution history. `execution_events` exists (`internal/memory/store.go:374-382`: `execution_id, stage, occurred_at, detail`) with a full store API (`ListExecutionEvents` `store.go:1137`), but **no gateway endpoint serves it** — zero `ListExecutionEvents` calls in `internal/gateway/`, and `DashboardStore` (`internal/gateway/dashboard.go:21-29`) doesn't include it. The console's C8 join endpoint exposes no execution id, so a task-scoped route is required alongside the execution-scoped one.

Verified at origin/main 2026-08-06.

## Context (verified)

- Stage vocabulary: **31 values as of 2026-08-06** (`internal/memory/store.go:929-1049`) and it grows — serve `stage` as an opaque string, never enumerate stages in gateway code or docs (the design doc's "22-stage" figure is already stale).
- `handleDashboardHistory` shows the house pattern for a store-backed read route (DTO shaping, project scoping, error envelope) — mirror it.
- C8's console join picks the newest execution for a task (`pilot-console` `handleExecution` semantics) — the task-scoped route here must match that pick-newest rule so the two ends agree.

## Acceptance

1. `GET /api/v1/executions/{id}/events` → ordered (occurred_at ASC) array of `{stage, occurredAt, detail}` for that execution. 404 unknown execution.
2. `GET /api/v1/tasks/{taskId}/events?project=<path>` → same array for the NEWEST execution of that (task, project), plus `{executionId, status}` envelope so the caller learns which execution it got. 404 when the task has no executions. Pick-newest matches C8's rule.
3. `DashboardStore` extended with the needed methods only; auth and project-scoping identical to sibling routes (verify the wrapping before wiring).
4. `detail` is served verbatim BUT run it through the existing redaction/scrubbing helper if one exists in the gateway/dashboard path — verify; if none exists, note it in the PR description (redaction scrubber is a separate S4 leg, do not build it here).
5. Tests: ordering · both routes against a seeded store (explicit `CreatedAt`/`occurred_at` seeding pattern, `store_test.go`) · 404s · auth · project scoping.

## Scope fence

No console/UI changes (console timeline leg is gated on this merging) · no new event writes or stage values · no pagination (executions have bounded event counts; note the decision) · no WebSocket/streaming (live logs is a different S4 leg).

**This task must NOT be decomposed — implement as a single PR.** <!-- pilot:no-decompose -->

## Refs

- Roadmap §S4 (`.agent/system/saas-roadmap.md:37`) "per-card timeline from `execution_events`"; sync design §9
- Research pass 2026-08-06: no existing endpoint, DashboardStore gap, 31-stage vocabulary drift

- **Dispatched**: https://github.com/qf-studio/pilot/issues/4749
