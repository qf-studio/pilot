---
name: Local Grafana + Prometheus stack
description: Where the local observability stack lives and how to run it. Pre-provisioned 8-panel "Pilot Pipeline" dashboard scrapes Pilot daemon's /metrics on host:9091.
type: reference
originSessionId: 4f6a94e6-d5cd-4b19-a4a0-c93b6769ad05
---
**Location**: `deploy/grafana/` (Pilot repo, shipped via TASK-53 / GH-2834 on 2026-05-08).

**Files** (mirrors Navigator's `.agent/grafana/` layout):
- `docker-compose.yml` — `prom/prometheus` + `grafana/grafana`
- `prometheus.yml` — scrape `host.docker.internal:9091` every 15s
- `grafana-datasource.yml` — provisions Prometheus ds, **`uid: prometheus`** (literal — locked to avoid Grafana 12.x regression)
- `grafana-dashboards.yml` — file provider for dashboards
- `pilot-dashboard.json` — 8 panels, queries verbatim from `docs/content/deployment/monitoring.mdx`
- `README.md` — runbook

**Ports** (chosen to coexist with Pilot gateway 9091 and Navigator's stack 9092/3333):
- Prometheus: **9093**
- Grafana: **3334**
- Login: `admin` / `admin` (override via `GF_SECURITY_ADMIN_PASSWORD` env)

**Run / stop**:
```bash
docker compose -f deploy/grafana/docker-compose.yml up -d
open http://localhost:3334/d/pilot
docker compose -f deploy/grafana/docker-compose.yml down       # keep volumes
docker compose -f deploy/grafana/docker-compose.yml down -v    # wipe data
```

**Caveats**:
- Pilot daemon's in-memory exporter zeros all counters on restart unless TASK-54 restore-from-SQLite is shipping (it is, v2.136+) — even then, only persisted snapshot fields are restored, histograms reset.
- Linux: add `extra_hosts: ["host.docker.internal:host-gateway"]` to both services (macOS Docker Desktop resolves natively).
- Token / cost panels need TASK-55 metrics shipped (v2.137+) — `pilot_tokens_consumed_total`, `pilot_execution_cost_usd_total`.

**Navigator's stack** is at `/Users/aleks.petrov/Projects/startups/navigator/.agent/grafana/` — different scrape target (Claude Code's OTel `:9464`), different metrics. Same shape, ports 9092/3333. The two stacks coexist.
