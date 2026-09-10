feat(fleet): hourly usage rollup from daemon metrics + GET /api/v1/usage (S5 usage-rollup leg, fleet design item 16)

## Context

Roadmap S5 (saas-roadmap in the pilot repo's Navigator docs) calls for billing groundwork: an hourly poll of each tenant instance's daemon `/api/v1/metrics` endpoint, persisted into a `usage_rollup` table, plus a minimal read API for a future usage page. Flat-plan billing for now, no Stripe metering yet.

The console already has the instance-proxy recipe this job should reuse: `internal/proxy/proxy.go` resolves an instance's private IP via EC2 describe and attaches an org-scoped gateway bearer token before calling the daemon's `/api/v1/{tail}` routes (GET-only allowlist, 10 s upstream timeout). A closer precedent for a background (non-request) poller using the same resolve-IP / token / GET recipe against the daemon's `/api/v1/queue` exists only on branch pilot/GH-215 as an exec-activity reader under internal/fleet; it has not merged. Do not depend on it landing first, just mirror its shape: gateway token from the secrets package, EC2 describe for the IP, a plain http client with a timeout, GET with a bearer header, decode JSON, fail closed on any error.

The daemon's `/api/v1/metrics` (pilot repo, not this repo) returns JSON with `totalCostUSD`, `totalTokens`, `inputTokens`, `outputTokens`, `totalTasks`, `succeededTasks`, `failedTasks`, and a rolling `window` block (days, totalCostUsd, costPerDeliveredUsd, issuesAttempted, issuesDelivered, deliveryRate, attemptSuccessRate). Treat this response as opaque and versioned: the daemon may add fields, and unknown fields must not break decoding.

`internal/dashboardapi/routes.go` currently documents that daemon-ledger cost stats deliberately stay SPA-side via the live proxy rather than being persisted server-side. This issue supersedes that stance for the specific case of historical billing rollups. The live SPA-side path is unaffected.

## Design

**Table.** New migration 0018_usage_rollup (up/down pair, same plain CREATE/ALTER style as `internal/db/migrations/0017_instance_ready_checks.up.sql`):

| Column | Type | Notes |
|---|---|---|
| id | uuid PK | default gen_random_uuid() |
| org_id | uuid NOT NULL | owning org; no RLS policy (RLS is dormant repo-wide per the doc comment in `internal/fleet/store.go`) |
| instance_id | uuid NOT NULL | the polled instance |
| period_start | timestamptz NOT NULL | hour bucket start, UTC, truncated |
| total_cost_usd | numeric NOT NULL | from daemon totalCostUSD |
| total_tokens | bigint NOT NULL | from daemon totalTokens |
| total_tasks | int NOT NULL | from daemon totalTasks |
| succeeded_tasks | int NOT NULL | from daemon succeededTasks |
| failed_tasks | int NOT NULL | from daemon failedTasks |
| raw_response | jsonb NOT NULL DEFAULT '{}' | full decoded daemon response, forward-compat and debugging without a migration |
| created_at | timestamptz NOT NULL DEFAULT now() | |

Unique index on (instance_id, period_start). This is the idempotency key: re-running the same hour for the same instance upserts (ON CONFLICT DO UPDATE) instead of duplicating, so a partial tick can be retried safely.

**Job cadence.** A new poller in the fleet package (file name at implementer's discretion) ticking hourly, following the reconciler's ticker pattern in `internal/fleet/reconciler.go`. Poll every instance with desired_state running, read through CrossOrg() because this is a cross-tenant background sweep, the same justification the reconciler and consolectl already use (see the doc comment in `internal/fleet/store.go`).

**Multi-replica guard.** The repo has no advisory-lock or leader-election primitive today (no pg_try_advisory_lock usage anywhere). The reconciler avoids double-ticking by running as a separate single-instance process (`cmd/consolectl/run.go`, the consolectl section of the README), not by locking. Wire the rollup poller the same way: inside consolectl run alongside the reconciler, or as its own single-task ECS service, NOT inside the request-serving replicas of the main app. If a shared-loop approach is chosen instead, an explicit Postgres advisory lock is in scope for this issue, not deferred.

**Per-instance failure handling.** A single instance failing (unreachable, non-200, bad token, undecodable body) must not abort the tick for other instances: log and continue, matching the reconciler's per-row error isolation. Timeout per call 10 s, matching the proxy's upstream timeout constant.

**Org scoping.** Table writes go through CrossOrg() (background sweep, no caller org), the same pattern `scripts/check-fleet-org-scoping.sh` (the check-fleet-org-scoping Makefile target) already permits for the reconciler, provisioner and config pusher. The read API below goes through ForOrg(orgID), matching the FleetStore interface shape in `internal/instances/handlers.go`.

## API

GET `/api/v1/usage`. New package under internal (suggested name usageapi) mirroring the shape of `internal/dashboardapi/routes.go`: narrow OrgStore and UsageStore interfaces, a Deps struct carrying the stores, the Authenticate middleware and a logger, and a Register(mux, deps) function wired from `main.go` next to the existing dashboardapi and boardapi Register calls. Session-authenticated, org resolved from the session principal. No bearer or instance-id path like the proxy; this is BFF-only.

Response, rollup rows for the caller's org, last 30 days by default, optional days query parameter:

```json
{
  "instanceId": "uuid",
  "days": 30,
  "totalCostUsd": 12.34,
  "totalTokens": 123456,
  "totalTasks": 42,
  "series": [
    {"periodStart": "2026-09-01T00:00:00Z", "costUsd": 0.51, "tokens": 5200, "tasks": 2}
  ]
}
```

## Out of scope

- The usage page UI in pilot-console-ui (separate issue, blocked on this one).
- Stripe usage-based metering or invoicing. Flat-plan billing only; this issue is groundwork.
- Per-execution event-level usage. This rollup is hourly aggregate only.
- RLS on the new table. Org isolation relies on the ForOrg/CrossOrg split like everywhere else.

## Tests

- Migration up/down round-trip in the existing `internal/db` harness; the RLS down-test (`internal/db/rls_test.go`) must still pass.
- Poller: one instance erroring does not block the others; re-polling the same hour upserts, no duplicate rows; CrossOrg usage passes check-fleet-org-scoping.
- GET usage: org-scoped (no cross-tenant rows), empty state before any rollup, days parameter bounds.
- Table-driven mapping test daemon-response to rollup row, including a payload with unknown extra fields.

## Acceptance

- [ ] Migration 0018 merged; RLS down-test green
- [ ] Hourly poller runs as a single-replica process (consolectl or dedicated single-task service) and writes idempotent usage_rollup rows via CrossOrg()
- [ ] check-fleet-org-scoping passes
- [ ] GET `/api/v1/usage` returns the org-scoped series, wired in `main.go`
- [ ] Per-instance poll failures are logged and skipped, never fatal to the tick
