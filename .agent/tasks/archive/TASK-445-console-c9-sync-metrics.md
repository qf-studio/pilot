# feat(metrics): C9 — console Prometheus: sync_lag_seconds, card_ops_pending, sync_conflicts_total, orphaned_links

## Context

Blocked by: console#97 (C8 dispatch leg — serialized to avoid `main.go`/worker-package collisions).

S4 board track, leg C9 — the observability leg feeding the S4 exit criterion ("canary tenant operated dashboard-only for a week": you can't verify a week of sync health without lag/parked/conflict numbers) and the later C15 ops-Prometheus scrape.

Verified at `pilot-console` HEAD `f1658e3`: **zero metrics infrastructure exists.** No `prometheus/*` dependency in `go.mod`, no `expvar`, no `/metrics` route. The only "metrics" string in the repo is the instance-proxy passthrough allowlist (`internal/proxy/proxy.go:41`) — that is the *tenant daemon's* metrics, proxied; unrelated.

Do NOT read or clone sibling repos — everything needed is embedded.

### Facts that shape the wiring

- Single `*http.ServeMux` on `cfg.Addr` (default `:8090`), built in `main.go:130-174`; route registration idiom is `registerX(mux, cfg, logger, ...)` functions called before the server starts; simplest precedent `health.Register(mux)` (`internal/health/health.go:7`).
- `:8090` fronts the public API — an ungated `/metrics` would leak connection UUIDs and op counts. The repo already has a static bearer credential: `cfg.APIToken`, checked with `subtle.ConstantTimeCompare` in `internal/proxy/proxy.go` (`DualAuthMiddleware`, `:178`). Reuse that posture.
- `internal/reqlog/reqlog.go:22-26` `skipPaths = {"/health","/ready","/live"}` — without an entry, every scrape emits an `http_request` log line under `PILOT_CONSOLE_REQUEST_LOG=1` (the local stack sets it).
- Worker seams (all unexported; C9 plumbs a recorder in via constructors, matching how C6 plumbed the budgeter): ingest `deltaPoll`/`maybeSweep` (`internal/syncingest/worker.go:243/512`); outbound `tick`/`processOp`/`parkOp` (`internal/syncoutbound/worker.go:168/263/383`). Ingest's `ReconcileAndCommit` returns `syncengine.Result` — verify at HEAD what conflict information `Result` carries before deciding where conflicts are counted.
- Op-count source of truth is the DB, not worker memory: `card_ops` has index `card_ops_worker_idx (connection_id, state, next_attempt_at)` — a `GROUP BY state` count per connection is cheap.
- House test rules: stdlib `testing`, hand-rolled fakes, tests drive unexported worker steps directly (`w.deltaPoll(...)`, `w.processOp(...)`), DB-gated tests via `newTestStore` (`internal/board/store_test.go:22`).

## Acceptance

1. **Dependency**: add `github.com/prometheus/client_golang` (latest stable) — this PR is explicitly sanctioned to introduce it. Use a **private registry** (`prometheus.NewRegistry()`), not the global default, so the binary exposes exactly what this leg defines (no accidental go_/process_ collectors unless deliberately added — include `collectors.NewGoCollector()` + `NewProcessCollector`, they're free and ops-useful).

2. **Package `internal/metrics`** (flat): a `Metrics` struct owning the registry and typed instruments, `New() *Metrics`, `Handler() http.Handler` (promhttp for the private registry). The four canonical instruments, labeled by `connection` (connection UUID string) — exact names from the design doc:
   - `sync_lag_seconds{connection}` — gauge: seconds between now and the connection's delta watermark, set at the end of every committed delta pass; also set on pass-abandon using the stale watermark (lag visibly grows when polling is stuck or cooling down — this is the metric that surfaces C6 backoff, design §7).
   - `card_ops_pending{connection,state}` — gauge, `state ∈ pending|parked`: refreshed each outbound tick from a new DB count (see AC 4).
   - `sync_conflicts_total{connection}` — counter: incremented per conflict row committed by reconcile.
   - `orphaned_links{connection}` — gauge: set after each fully-successful sweep to the connection's orphaned-link count; untouched by abandoned sweeps.
   All recorder methods must be nil-receiver-safe (a nil `*Metrics` records nothing) so workers stay constructible without metrics, same posture as C6's nil budgeter.

3. **Ingest instrumentation** (`internal/syncingest`): `NewWorker` gains the recorder (narrow package-local interface, implemented by `*metrics.Metrics`, nil-legal). Set `sync_lag_seconds` per AC 2; count conflicts from reconcile results per pass (verify `syncengine.Result`'s conflict surface first — if `Result` does not expose a count, count committed `sync_conflicts` via the store instead and say so in the PR); set `orphaned_links` in the sweep path from the links it just evaluated (`ListLinksByConnection` is already in the worker's store interface).

4. **Outbound instrumentation** (`internal/syncoutbound`): `NewWorker` gains the recorder. Add a board-store count method `CountOpsByState(ctx, connectionID uuid.UUID) (map[board.OpState]int, error)` (`internal/board/ops.go`, single `GROUP BY` query; DB-gated test) and refresh `card_ops_pending` per connection per tick from it.

5. **Route + gating**: `registerMetrics(mux, cfg, logger, m)` in `main.go`. When `cfg.APIToken == ""` → do not register, `logger.Warn("PILOT_CONSOLE_API_TOKEN not set; /metrics disabled")`. When set → `GET /metrics` requires `Authorization: Bearer <token>` (constant-time compare, mirroring proxy's static-auth branch); missing/wrong → 401 `{"error":"unauthorized"}`. Add `"/metrics"` to reqlog `skipPaths`. No new config knobs.

6. **Wiring**: `main.go` builds one `*metrics.Metrics` when EITHER sync worker is enabled OR `cfg.APIToken != ""`, passes it to both worker constructors (nil where a worker is disabled is fine — recorders are nil-safe), registers the route. Startup log: `metrics enabled` with instrument count. The bare binary (no env) must run unchanged with no metrics goroutines and no route.

7. **Tests**: `internal/metrics` — instrument registration (scrape the private registry via `promhttp` + `httptest`, assert the four families present with expected labels), nil-safety. Worker tests (existing fake patterns, driving unexported steps): committed pass sets lag from the new watermark · abandoned pass still refreshes lag from the stale watermark · successful sweep sets orphan gauge, abandoned sweep leaves it · outbound tick refreshes pending/parked gauges from the count method · conflict commit increments the counter. Route tests: 401 without/with-wrong bearer · 200 + `text/plain` exposition with correct bearer · unregistered when token empty (404).

8. `make build`, `make test`, `make lint` green (CI lint job runs without Postgres — keep DB-gated tests skippable). Conventional-commit PR title.

## Implementation

Files: `internal/metrics/metrics.go` + `metrics_test.go`, `internal/board/ops.go` (+store test), `internal/syncingest/worker.go` (+tests), `internal/syncoutbound/worker.go` (+tests), `internal/reqlog/reqlog.go` (one-line skipPaths), `main.go`, `go.mod`/`go.sum`.

Sequencing: metrics package + route (self-contained, testable) → store count method → outbound gauges → ingest lag/conflicts/orphans → main wiring.

**Verify-before-relying**: (a) `syncengine.Result` fields at HEAD — where conflict counts actually surface (`internal/syncengine/reconcile.go`); (b) C6's final constructor signatures for both workers (this leg lands after C6 and must extend, not fight, its parameter additions); (c) proxy's exact constant-time bearer-compare idiom before copying it.

**This task must NOT be decomposed — implement as a single PR.** <!-- pilot:no-decompose -->

**Scope fence (do NOT build):** ops-Prometheus deployment/scrape config + alarms (C15, infra repo) · tenant-instance metrics aggregation (the proxy already passes the daemon's `/metrics` through per-instance) · budgeter-internal metrics beyond what `sync_lag_seconds` implies (add later if the cooldown proves opaque) · request-level HTTP metrics middleware · dashboards.

## Refs

- **Status**: 🚀 Dispatched to Pilot 2026-08-05 · Pilot issue: https://github.com/qf-studio/pilot-console/issues/98 (labels: pilot, no-decompose; gated on #97, tail of the wave-3 chain)
- Depends on: console#87–#94 (merged) · console#95 (C5) + #96 (C6) + #97 (C8) chained ahead
- Canonical design: `qf-studio/pilot` `.agent/system/saas-kanban-sync-design.md` §9 (C9 metric names verbatim), §7 (`sync_lag_seconds` as the rate-pressure surface) — embedded above
- Console facts verified 2026-08-05 at `f1658e3`
