# feat(metrics): 30-day rolling window for headline cost/success — windowed store stats, TUI cards, dashboard JSON, Prometheus gauges

## Context

Operator decision 2026-08-05: headline cost/success numbers must reflect a **rolling 30-day window**, not lifetime aggregates. Lifetime numbers blend model eras and are actively misleading: the ledger shows avg cost per completed execution went $0.31–0.32/mo (Feb–Mar, sonnet-4-6 era) → $3.40 (Jul) → $4.05 (Aug, sonnet-5), while the TUI headline still shows a lifetime-flavored ~$0.80/task. History is NOT deleted — it stays queryable; only the headline surfaces change.

All facts below verified at HEAD `697cdd39`.

### Where the misleading numbers come from today

- **TUI `$/task`** (`internal/dashboard/tui.go:1914-1919` `renderCostCard`, detail `~$%.2f/task`): `CostPerTask = TotalCostUSD / TotalTasks`, computed identically at `tui.go:771-774`, `:1133-1135`, `:1227-1229`, `:1243-1245`. Numerator = `store.GetLifetimeTokens(path)` (`internal/memory/store.go:3636-3659`) — lifetime, filters `tokens_total > 0`, **no canary filter**. Denominator = `store.GetLifetimeTaskCounts(path)` (`store.go:3687-3714`) — lifetime, **has** canary filter, no token filter. **Mismatched populations: the ratio is dirty even before the era problem.**
- **Web/desktop JSON** `GET /api/v1/metrics` (`internal/gateway/dashboard.go:91-155`): same two lifetime calls; client computes ratios.
- **Prometheus** (`internal/gateway/prometheus.go`): everything DB-derived is all-time via boot-time hydration (`internal/autopilot/metrics_hydrator.go:32-109`). `pilot_success_rate` (`prometheus.go:292-295`, formula `internal/autopilot/metrics.go:613-629`) = lifetime `completed/(completed+failed)` — 79.9%-flavored. **No metric has a time window.**
- Already-windowed surfaces that do NOT change: `pilot metrics summary --days N` (`cmd/pilot/metrics.go`), briefs (period-scoped by construction), 7-day sparklines (`GetDailyMetrics`), `/budget` (month-to-date over `usage_events`).

### House patterns to follow (exact)

- **Time windows are Go-side `time.Time` params against raw `created_at >= ? AND created_at < ?`** — never SQL string literals, never `datetime('now',...)` on executions (only precedent for that is `autopilot/state_store.go:929`, a different table). Canonical: `GetDailyMetrics` (`internal/memory/metrics.go:319-322`) + caller `tui.go:855-866` (`now.AddDate(0, 0, -7)`). The driver opens with `?_time_format=sqlite` (`store.go:42-51`) and `SaveExecution` stamps `created_at` Go-side (`store.go:651-660`), so bound-time comparison is sound. Legacy pre-2026-07-16 `+02:00` string rows are outside any 30-day window from here on — note it, don't handle it.
- **Canary exclusion**: `COALESCE(is_canary, 0) = 0` (GH-4240/TASK-436 idiom, e.g. `GetLifetimeTaskCounts`).
- **DB-derived absolute values exposed as Prometheus GAUGES, not counters** — the GH-4511 precedent (`pilot_prs_merged_lifetime` gauge, `prometheus.go:117-123`, hydrator `:104-109`).
- **No mocks** — `make check-mocks` CI gate; memory tests use a real SQLite store on a temp dir; the time-seeding fixture pattern is `store_test.go:4182/:4229` (`SaveExecution` with explicit `CreatedAt`).

## Acceptance

1. **Store: `GetWindowedStats`** (`internal/memory/store.go` or `metrics.go`):
   ```go
   type WindowedStats struct {
       WindowDays        int
       TotalCostUSD      float64 // SUM(estimated_cost_usd), window, canary-excluded, ALL executions (retries included)
       IssuesAttempted   int     // COUNT(DISTINCT task_id||'|'||project_path) in window (canary-excluded)
       IssuesDelivered   int     // COUNT(DISTINCT ... ) with a completed execution in window
       CostPerDelivered  float64 // TotalCostUSD / IssuesDelivered (0 when none) — the honest "$/issue"
       AttemptCompleted  int     // executions with status='completed'
       AttemptFailed     int     // status='failed'
       AttemptSuccessRate float64 // completed/(completed+failed), 0 when empty — neutral classes excluded
       DeliveryRate      float64 // IssuesDelivered/IssuesAttempted, 0 when empty
   }
   func (s *Store) GetWindowedStats(projectPath string, since time.Time) (WindowedStats, error)
   ```
   One population: `created_at >= ?` + `COALESCE(is_canary,0)=0` (+ optional `project_path = ?`), applied identically to every aggregate in the method (this fixes the mismatched-population bug by construction). `no_op`/`infra`/`skipped`/`declined*`/`stalled`/`rate_limited` are **neutral**: excluded from `AttemptSuccessRate`'s denominator, included in `IssuesAttempted` only if the issue never completed AND had a completed-or-failed attempt — simplest correct cut: `IssuesAttempted` counts distinct issues having ≥1 execution with status IN ('completed','failed'); state this definition in the doc comment. Cost sums have **no** `tokens_total` gate (a failed retry that burned tokens is real spend; a $0 row adds 0 anyway).

2. **Config knob**: `DashboardConfig.StatsWindowDays` (`internal/config/config.go:363-367`), yaml `stats_window_days`, default `30`, min `1` — eager validation per house style. Used by the TUI, the gateway JSON handler, and the Prometheus refresh (one knob, three consumers).

3. **TUI** (`internal/dashboard/tui.go`): the cost card becomes windowed — value = window `TotalCostUSD`, detail = `~$X.XX/issue · 30d` (from `CostPerDelivered`; label reflects the configured window). The task card's `✓ N ✗ N` + `nonFailureSuffix` breakdown becomes windowed via the same `GetWindowedStats` call (keep the 9-way muted breakdown by adding the per-status window counts OR keep `nonFailureSuffix` lifetime-sourced and mark it — pick the former; it's one GROUP BY). All four `CostPerTask` computation sites (`:771`, `:1133`, `:1227`, `:1243`) collapse onto the windowed refresh; live in-session increments (`:1222`, `:1236-1245`) still add optimistically and are corrected by the every-5th-tick `storeRefreshCmd` re-query (existing behavior, now converging to the window — document this in a comment). 7-day sparklines unchanged.

4. **Gateway JSON** (`internal/gateway/dashboard.go`): response gains a `window` object — `{days, totalCostUsd, costPerDeliveredUsd, issuesAttempted, issuesDelivered, deliveryRate, attemptSuccessRate}`. Existing lifetime fields REMAIN (desktop app back-compat) but the handler adds the canary filter to their numerator by switching `GetLifetimeTokens` to a canary-filtered variant (see AC 6). Update `dashboard_test.go`.

5. **Prometheus** (`internal/gateway/prometheus.go` + `internal/autopilot/metrics.go`/`metrics_hydrator.go`): four NEW gauges, existing metrics untouched (Grafana depends on them):
   - `pilot_window_cost_usd{window="30d"}` · `pilot_window_cost_per_delivered_usd{window="30d"}` · `pilot_window_delivery_rate{window="30d"}` · `pilot_window_attempt_success_rate{window="30d"}`
   HELP strings must state the window semantics + neutral-class exclusion. Values refreshed **periodically, not per-scrape** (no DB query in the scrape path): re-run the windowed query on a ticker (≤5 min staleness; wire it where the hydrator's store handle already lives — an autopilot-loop ticker or a small goroutine started next to the hydrator, matching existing lifecycle/shutdown patterns). Boot hydration seeds them immediately.

6. **Population hygiene fix**: `GetLifetimeTokens` gains the `COALESCE(is_canary,0)=0` filter (it's the only lifetime aggregate the TASK-436 wave missed — verified list in this spec's provenance). Update `TestGetLifetimeTokens*` accordingly and add an excludes-canary test mirroring `TestGetLifetimeTaskCounts_ExcludesCanarySameProject` (`store_test.go:2054`).

7. **Tests** (real store, temp dir, no mocks): `GetWindowedStats` — rows inside vs outside the window via explicit `CreatedAt` seeding (the `store_test.go:4182` pattern) · canary row excluded from every aggregate · retry cost summed into `TotalCostUSD` while the issue counts once in `IssuesDelivered` · neutral statuses absent from `AttemptSuccessRate` denominator and from `IssuesAttempted` (an issue with only a `no_op` execution counts nowhere) · project filter · empty window → zero rates, no division panic. TUI — `TestHydrateFromStore_*` updated for windowed values; card render test asserts the `30d` label. Config — default + min validation. Prometheus — gauge families present with HELP, values match a seeded store.

8. `make build`, `make test`, `make lint` green. Conventional-commit PR title. **Test tokens per `internal/testutil` rules.**

## Implementation

Files: `internal/memory/store.go` (or `metrics.go`) + `store_test.go`, `internal/config/config.go` (+test), `internal/dashboard/tui.go` + `tui_test.go`, `internal/gateway/dashboard.go` + `dashboard_test.go`, `internal/gateway/prometheus.go`, `internal/autopilot/metrics.go` + `metrics_hydrator.go` (+tests).

Sequencing: store method (pure SQL, fully tested) → config knob → TUI swap → gateway JSON → Prometheus gauges + refresh ticker → hygiene fix (AC 6) last, it's independent.

**Verify-before-relying**: (a) exact `Model` refresh flow around `storeRefreshCmd` (`tui.go:1106-1145`) before collapsing the four computation sites; (b) where the hydrator's store handle lives and the cleanest existing ticker to piggyback for AC 5's refresh; (c) whether the desktop app reads `costPerTask` from the JSON (it does not exist today — the client computes; do not add a lifetime `costPerTask` field).

**This task must NOT be decomposed — implement as a single PR.** <!-- pilot:no-decompose -->

**Scope fence (do NOT build):** deleting/archiving ledger rows (history stays) · a TUI period-selector UI (config knob only) · changing existing Prometheus metric names/semantics · brief formula changes (`GetBriefMetrics` denominator inflation is real but period-scoped already — journaled, separate task if wanted) · `pilot metrics` CLI changes (already windowed via `--days`) · model-era segmentation surfaces (the per-model lifetime counters already exist; era charts are a later ask) · touching `usage_events`/budget paths.

## Refs

- **Status**: 🚀 Dispatched to Pilot 2026-08-05 · Pilot issue: https://github.com/qf-studio/pilot/issues/4735 (labels: pilot, no-decompose)
- Ledger analysis behind the decision (2026-08-05, box DB): monthly cost era table + last-30d per-issue stats (median $2.45 / avg $3.66 per delivered issue; 81.0% per-issue delivery, 92.6% excluding neutral finals) — recorded in `.agent/system/saas-roadmap.md` v9.9 session notes and this task's provenance
- Aggregate-site inventory + canary-filter coverage map verified at `697cdd39` (research pass 2026-08-05): the only lifetime aggregate missing the canary filter is `GetLifetimeTokens`
- Timestamp discipline: `store.go:42-51` (`_time_format=sqlite`), GH-4332; legacy `+02:00` rows are pre-2026-07-16 and cannot enter a 30-day window
