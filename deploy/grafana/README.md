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

The `pilot-dashboard.json` (uid: `pilot`) contains 8 panels in a 4×2 grid:

| Panel | Type | Query |
|-------|------|-------|
| Success Rate | stat | `pilot_success_rate` |
| Queue Depth | timeseries | `pilot_queue_depth`, `pilot_failed_queue_depth` |
| Issue Throughput | timeseries | `rate(pilot_issues_processed_total[5m])` |
| Active PRs | bargauge | `pilot_active_prs` |
| Execution Duration P95 | timeseries | `histogram_quantile(0.95, rate(pilot_execution_duration_seconds_bucket[5m]))` |
| CI Wait P50 | timeseries | `histogram_quantile(0.50, rate(pilot_ci_wait_duration_seconds_bucket[5m]))` |
| PR Outcomes | timeseries (stacked) | `rate(pilot_prs_merged_total[1h])`, failed, conflicting |
| Circuit Breaker & API Errors | timeseries | `rate(pilot_circuit_breaker_trips_total[5m])`, `rate(pilot_api_errors_total[5m])` |

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
