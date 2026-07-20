# SOP: grom terminal dashboard on live Pilot metrics

**Created**: 2026-07-20 · Verified live same day.

Three-hop chain — every hop must be up, and the tunnel is the one that dies:

```
box:9091 (/metrics, hand-rolled exporter)
  → ~/bin/pilot-tunnel            # SSM port-forward → localhost:9091 (FOREGROUND — keep terminal open)
  → pilot-prometheus container    # deploy/grafana/docker-compose.yml; scrapes host.docker.internal:9091
                                  # every 30s, serves PromQL API on localhost:9093
  → grom                          # repo ../grot (module qf-studio/grom)
```

## Run

```bash
# Terminal 1 (stays open):
~/bin/pilot-tunnel        # healthy = prints "Waiting for connections" after the session line

# Once (persists):
cd ~/Projects/startups/pilot/deploy/grafana && docker compose up -d prometheus

# Terminal 2:
cd ~/Projects/startups/grot
./grom run --config examples/pilot.yaml --prom http://localhost:9093
```

Keys: `r` refresh · `+`/`-` time range · `enter` zoom · `t` theme · `q` quit.

## Troubleshooting (in this order)

1. **Empty/`no data` everywhere** → tunnel is dead. Check
   `curl -s localhost:9091/metrics | head -1`, then
   `curl -s localhost:9093/api/v1/targets` (health/lastError). A tunnel whose
   output stopped at "Starting session…" without "Waiting for connections"
   FAILED TO BIND (port already held — e.g. a second tunnel). Instant queries
   go empty ~5min after the last good scrape (staleness).
2. **Only windowed panels empty** (prs/3h, tokens/5m, execs/1h, pipeline
   latency) → no Pilot activity in the window. Not a bug; idle daemon = flat.
3. **Per-model / CI panels empty after daemon restart** → exporter emits those
   series only after post-restart events; lifetime stats (success rate,
   cumulative cost) hydrate from SQLite and survive.
4. **Panel semantics**: success-by-model is attempt-level and polluted by
   `model=unknown` (zero-token, never-invoked rows) — #4483 tracks the fix.
   Headline success rate is issue-level-corrected since #4070.

## Refs

- `deploy/grafana/` (compose, prometheus.yml, grafterm json — grafterm is the
  abandoned predecessor; grom replaces it)
- grot repo `examples/pilot.yaml` — the maintained Pilot dashboard config
- pilot-aws skill § "Metrics tunnel"
