# feat(metrics): C15 — org label on console sync instruments + starter alert rules

## Problem

C9 (console PR #104) shipped the four sync instruments labeled by `connection` only. C15 requires a tenant dimension and alarms. The tenant data is already in scope at every call site — this is label plumbing plus an alert-rules starter, not new collection.

Verified at pilot-console origin/main 2026-08-06.

## Context (verified)

- `internal/metrics/metrics.go`: private registry, 4 instruments — `sync_lag_seconds{connection}` · `card_ops_pending{connection,state}` · `sync_conflicts_total{connection}` · `orphaned_links{connection}`. `/metrics` behind `PILOT_CONSOLE_API_TOKEN` (`main.go:739`).
- Every call site already holds an `orgs.Connection` whose struct carries `OrgID uuid.UUID`: `internal/syncingest/worker.go:286,547,596,699` · `internal/syncoutbound/worker.go:220-221`. Adding the label = passing `conn.OrgID.String()` alongside `conn.ID.String()`.
- The metrics are ~1 day old with no external dashboards yet — adding a label dimension now is non-breaking in practice; do it before consumers exist.

## Acceptance

1. All four instruments gain an `org` label (value `conn.OrgID.String()`); `connection` label kept. One helper signature change, all call sites updated — no site left passing connection-only (grep-assert in a test like the C6 pattern if cheap).
2. `deploy/prometheus/alerts.example.yaml` (new file, example only — no deployment wiring): starter rules with brief annotations — sync lag sustained high per connection · parked ops nonzero for a sustained window · orphaned links nonzero · conflict rate burst. Thresholds conservative and commented as tuning-required.
3. README or package doc note: how to scrape (bearer token) + load the example rules.
4. Tests updated for the new label; `InstrumentCount` unchanged.

## Scope fence

No new instruments · no Alertmanager/deployment infra (operator concern) · no pilot-repo changes · no per-tenant auth on /metrics (single ops token stays).

**This task must NOT be decomposed — implement as a single PR.** <!-- pilot:no-decompose -->

## Refs

- Roadmap §S4 (`saas-roadmap.md:37`) "C15 ops Prometheus w/ tenant labels + alarms"; C9 = console PR #104
- Research pass 2026-08-06: OrgID availability at all call sites

- **Dispatched**: https://github.com/qf-studio/pilot-console/issues/107
- **Shipped**: console PR#108 merged 2026-08-06 11:41Z. Post-merge review clean on cardinality (org UUID coarser than connection — no series multiplication), registration, PromQL validity. One should-fix follow-up filed: console#110 (stale gauge series never deleted → the sustained-nonzero alerts page forever after connection offboarding; + job matchers on example exprs).
