# Pilot Local Monitoring Stack

Grafana + Prometheus stack for local Pilot development. Scrapes metrics from a running `pilot start` daemon and renders the built-in dashboard.

## Port Assignments

| Service    | Host Port | Container Port | Notes                         |
|------------|-----------|----------------|-------------------------------|
| Prometheus | 9093      | 9090           | Avoids Navigator's 9092       |
| Grafana    | 3334      | 3000           | Avoids Navigator's 3333       |
| Pilot      | 9091      | —              | gateway `/metrics` (host)     |

**Coexistence:** Navigator uses 9092/3333, Pilot gateway uses 9091. This stack runs on 9093/3334 so all three can run simultaneously without port conflicts.

## Quick Start

```bash
# 1. Start Pilot daemon (exposes :9091/metrics)
pilot start

# 2. In another terminal, bring up the monitoring stack
cd deploy/grafana
docker compose up -d

# 3. Open Grafana
open http://localhost:3334   # admin / admin
```

Prometheus UI: http://localhost:9093

## Linux: host-gateway

On Linux, Docker does not resolve `host.docker.internal` by default. The `extra_hosts` entry in `docker-compose.yml` maps it to the host:

```yaml
extra_hosts:
  - "host.docker.internal:host-gateway"
```

This is a no-op on macOS/Windows (Docker Desktop handles it automatically).

## Stop / Clean

```bash
# Stop containers, keep data volumes
docker compose down

# Stop and delete all data (Prometheus TSDB + Grafana state)
docker compose down -v
```

## Troubleshooting

**Prometheus shows `pilot` target as DOWN**
- Verify Pilot is running: `pilot status` or `curl http://localhost:9091/metrics`
- On Linux, confirm `host.docker.internal` resolves inside the container:
  ```bash
  docker compose exec prometheus ping -c1 host.docker.internal
  ```
- Check Prometheus logs: `docker compose logs prometheus`

**Grafana dashboard is blank / no data**
- Confirm Prometheus datasource is green in Grafana → Connections → Data sources
- Check the time range (top-right); Pilot must have been running during that window
- Verify scrape config: http://localhost:9093/targets

**Port already in use**
- Change host ports in `docker-compose.yml` (left side of `ports:` mapping)
- Prometheus 9093 clashes: edit to e.g. `9094:9090`
- Grafana 3334 clashes: edit to e.g. `3335:3000`

## PromQL Reference

Full metric definitions and alerting rules: [Monitoring docs](../../docs/content/deployment/monitoring.mdx)

Key queries used in the dashboard:

```promql
# Success Rate
pilot_success_rate

# Queue Depth
pilot_queue_depth
pilot_failed_queue_depth

# Issue Throughput
rate(pilot_issues_processed_total[5m])

# Execution Duration P95
histogram_quantile(0.95, rate(pilot_execution_duration_seconds_bucket[5m]))

# CI Wait P50
histogram_quantile(0.50, rate(pilot_ci_wait_duration_seconds_bucket[5m]))

# Active PRs by stage
pilot_active_prs

# Circuit Breaker trips
increase(pilot_circuit_breaker_trips_total[5m])

# API Errors
rate(pilot_api_errors_total[5m])
```
