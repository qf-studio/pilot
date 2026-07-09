# Pilot Local Monitoring Stack

Local Prometheus + Grafana stack for developing against Pilot's `/metrics` endpoint. Runs on dedicated ports so it coexists with Navigator's monitoring stack.

## Quick Start

```bash
cd deploy/grafana

# Start the stack
docker compose up -d

# Verify Prometheus is scraping Pilot
open http://localhost:9093/targets

# Open Grafana (admin / admin)
open http://localhost:3334
```

The Pilot dashboard loads automatically at `http://localhost:3334/d/pilot`.

## Ports

| Service    | Host Port | Container Port | Notes                                |
|------------|-----------|----------------|--------------------------------------|
| Prometheus | 9093      | 9090           | UI at http://localhost:9093          |
| Grafana    | 3334      | 3000           | UI at http://localhost:3334          |
| Pilot      | 9091      | —              | Scraped via `host.docker.internal`   |

**Coexistence rationale:**
- Navigator's monitoring stack uses ports 9092 (Prometheus) and 3333 (Grafana)
- Pilot's gateway HTTP server listens on 9091
- This stack uses 9093/3334 to avoid all conflicts

## Linux: `host.docker.internal` Setup

On Linux, `host.docker.internal` is not resolved automatically. The compose file adds `extra_hosts: host.docker.internal:host-gateway` for Prometheus, which requires Docker Engine 20.10+.

If your Docker version is older, replace `host.docker.internal:9091` in `prometheus.yml` with your host's IP:

```bash
# Find host IP from inside a container
docker run --rm alpine ip route | awk '/default/ {print $3}'
```

## Stop and Clean

```bash
# Stop containers (keeps volumes)
docker compose down

# Stop and remove all data volumes
docker compose down -v
```

## Dashboard Panels

The `pilot-dashboard.json` (uid: `pilot`) contains 12 panels across 5 rows (y=0..32):

| # | Panel | Type | PromQL |
|---|-------|------|--------|
| 1 | Success Rate | stat | `pilot_success_rate` |
| 2 | Queue Depth | timeseries | `pilot_queue_depth`, `pilot_failed_queue_depth` |
| 3 | Issue Throughput | timeseries | `rate(pilot_issues_processed_total[5m])` |
| 4 | Active PRs | bargauge | `pilot_active_prs` |
| 5 | Execution Duration P95 | timeseries | `histogram_quantile(0.95, rate(pilot_execution_duration_seconds_bucket[5m]))` |
| 6 | CI Wait P50 | timeseries | `histogram_quantile(0.50, rate(pilot_ci_wait_duration_seconds_bucket[5m]))` |
| 7 | PR Outcomes | timeseries (stacked) | `rate(pilot_prs_merged_total[1h])`, failed, conflicting |
| 8 | Circuit Breaker & API Errors | timeseries | `rate(pilot_circuit_breaker_trips_total[5m])`, `rate(pilot_api_errors_total[5m])` |
| 9 | Tokens/5m | timeseries (stacked area) | `rate(pilot_tokens_consumed_total[5m])` legend `{{model}}/{{direction}}` |
| 10 | Cumulative Cost (USD) | stat | `sum(pilot_execution_cost_usd_total)` |
| 11 | Cost/hour | timeseries | `rate(pilot_execution_cost_usd_total[5m]) * 3600` legend `{{model}}` |
| 12 | Executions by Result | bargauge | `pilot_executions_total` legend `{{model}}/{{result}}` |

## Terminal Dashboard (grafterm)

`grafterm-pilot.json` renders the pipeline-breakdown view in the terminal via
[grafterm](https://github.com/slok/grafterm) instead of Grafana's web UI. It
points at the same Prometheus instance the compose stack exposes on 9093, so
start the stack first:

```bash
go install github.com/slok/grafterm/cmd/grafterm@latest
cd deploy/grafana
grafterm -c grafterm-pilot.json
```

| # | Widget | PromQL |
|---|--------|--------|
| 1 | Pipeline Breakdown (P50): queue wait → execution → time-to-PR → CI wait → approval wait → merge | `histogram_quantile(0.50, sum(rate(pilot_queue_wait_seconds_bucket[15m])) by (le))`, `pilot_execution_duration_seconds`, `pilot_time_to_pr_seconds`, `pilot_ci_wait_duration_seconds`, `pilot_approval_wait_seconds`, `pilot_pr_time_to_merge_seconds` |
| 2 | PR Time to Merge (P50/P95) | `histogram_quantile(0.50\|0.95, sum(rate(pilot_pr_time_to_merge_seconds_bucket[15m])) by (le))` |
| 3 | CI Wait Duration (P50/P95) | `histogram_quantile(0.50\|0.95, sum(rate(pilot_ci_wait_duration_seconds_bucket[15m])) by (le))` |

`pilot_queue_wait_seconds`, `pilot_time_to_pr_seconds`, and
`pilot_approval_wait_seconds` are registered but not yet observed (GH-4128
plumbing; observers land in GH-4130) — their series render as no-data until
that lands. grafterm doesn't support native stacked/area rendering, so the
breakdown widget overlays all six stage histograms as separate lines on one
graph rather than a true stacked area chart.

## Troubleshooting

**Prometheus shows Pilot target as `DOWN`:**
- Confirm Pilot is running: `curl http://localhost:9091/metrics`
- On Linux, check `host.docker.internal` resolves (see Linux section above)
- Check Prometheus logs: `docker compose logs prometheus`

**Grafana dashboard not loading:**
- Wait ~10 seconds after `docker compose up -d` for provisioning to complete
- Check Grafana logs: `docker compose logs grafana`
- Verify datasource health at http://localhost:3334/connections/datasources

**Port already in use:**
- 9093 conflict: another Prometheus instance may be running
- 3334 conflict: check for other Grafana instances
- Stop conflicting containers or adjust ports in `docker-compose.yml`

## PromQL Reference

Full metric list and query examples: [docs/content/deployment/monitoring.mdx](../../docs/content/deployment/monitoring.mdx)
