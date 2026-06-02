# TASK-332: Metrics for the alert dispatch path

**Wave:** 2 (S) · **Pilot** · **Severity:** HIGH · **Audit ref:** TASK-322 §high
"No metrics on the alert dispatch path: dropped events, delivery failures, per-channel results invisible"

---

## Problem

The alerting subsystem — the thing that tells operators something is wrong — has **zero metrics**.
Dropped events (`engine.go:153`), per-channel delivery failures (`engine.go:551-555`,
`dispatcher.go:185-190`), retries, and total alerts fired are only logged, never counted. The
`PrometheusExporter` (`gateway/prometheus.go`) exposes ~20 autopilot metrics but nothing from
`internal/alerts`. A misconfigured channel (wrong Slack channel, expired PagerDuty key, SMTP auth
failure) fails silently forever — the monitoring system is itself unmonitored.

## Approach
- Add a small metrics surface to the `alerts` package: counters
  `alerts_fired_total{rule,severity}`, `alert_delivery_total{channel,type,result}`,
  `alert_events_dropped_total`, and an `alert_queue_depth` gauge.
- Increment a delivery-failure counter in `sendToChannel` and a drop counter in `ProcessEvent`.
- Wire it into the gateway Prometheus exporter via a second `MetricsSource`
  (mirror how autopilot metrics are exported in `gateway/prometheus.go`).

## Files to modify
- `internal/alerts/engine.go` (or a new `internal/alerts/metrics.go`)
- `internal/alerts/dispatcher.go`
- `internal/gateway/prometheus.go`
- corresponding `*_test.go`

## Test Strategy
- Unit: a forced delivery failure increments `alert_delivery_total{result="failure"}`; an `eventCh`
  overflow increments `alert_events_dropped_total`. Assert the exporter renders the new series.

## Effort
S (~2h). One PR.

## Out of Scope
- Event-loop decoupling / SuppressDuplicates (E1/E5) — separate tasks (also touch `engine.go`; sequence
  after this if filed concurrently).
