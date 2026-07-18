---
title: Subprocess OOM Tuning SOP
created: 2026-05-21
status: active
related: TASK-287, GH-3028
---

# Subprocess OOM Tuning SOP

How to set the `subprocess_limits.max_rss_mb` cap based on observed telemetry.

## Background

Pilot v2.148.0+ samples the Claude Code subprocess RSS every 10 s and writes
`peak_rss_mb` to the `executions` table. Cap enforcement is disabled by default
(`subprocess_limits.enabled: false`). Enable after collecting ≥1 week of data.

## Step 1 — Collect Baseline Data

Leave `subprocess_limits.enabled: false` for at least one week. After that:

```sql
-- Connect to ~/.pilot/pilot.db (or project-specific DB)
SELECT
  AVG(peak_rss_mb)                         AS avg_mb,
  MAX(peak_rss_mb)                         AS max_mb,
  COUNT(CASE WHEN peak_rss_mb > 0 THEN 1 END) AS sampled_rows,
  COUNT(*)                                  AS total_rows
FROM executions
WHERE created_at > datetime('now', '-7 days')
  AND peak_rss_mb > 0;
```

Also find the p99:

```sql
SELECT peak_rss_mb
FROM executions
WHERE peak_rss_mb > 0
  AND created_at > datetime('now', '-7 days')
ORDER BY peak_rss_mb DESC
LIMIT 1
OFFSET (
  SELECT CAST(COUNT(*) * 0.01 AS INTEGER)
  FROM executions
  WHERE peak_rss_mb > 0
    AND created_at > datetime('now', '-7 days')
);
```

## Step 2 — Set Cap

Recommended cap = p99(peak_rss_mb) × 1.5.

Example: if p99 = 2 048 MB → set cap = 3 072 MB.

```yaml
# ~/.pilot/config.yaml
executor:
  subprocess_limits:
    enabled: true
    max_rss_mb: 3072        # adjust to your p99 × 1.5
    sample_interval_sec: 10
```

## Step 3 — Monitor After Enabling

Watch for Pilot-side OOM kills in the dashboard:
- Status shows `oom_killed` with `retry: N/2 (oom_killed)`
- `dmesg` should be **silent** — no kernel OOM killer lines

If legitimate tasks are being killed (cap too tight), raise `max_rss_mb` by 25%.

## Step 4 — Linux cgroup v2 memory.max (GH-4401)

On Linux, Pilot creates a per-subprocess cgroup v2 leaf
(`/sys/fs/cgroup/pilot/pilot-<pid>/`) and sets `memory.max` to the configured
cap. This enforces actual resident memory (with reclaim before any kill) —
it does **not** touch virtual address space, unlike the RLIMIT_AS
implementation this replaced (GH-4401: RLIMIT_AS broke 100% of Linux
executor subprocesses by making every Node/V8 HTTPS fetch fail instantly
with a generic `fetch failed` ~25ms after a successful TLS handshake —
never do this again).

cgroup v2 requires root or a delegated subtree. If creation fails (not
mounted, no permission), a WARN log is emitted once per subprocess and
execution degrades to telemetry-only mode. Run `pilot doctor` to check
`subprocess_limits cgroup v2 available` — it warns explicitly when the cap
will silently degrade.

Independent of cgroup availability, Pilot also always sets
`NODE_OPTIONS=--max-old-space-size=<max_rss_mb>` on the subprocess — a
cooperative V8 heap bound that costs nothing and never breaks networking.

## Disabling the Cap (Emergency Rollback)

Set `enabled: false` and restart Pilot — no binary change required.

Cap disabled = byte-identical execution behavior (no bench regression).
The RSS sampler continues running for telemetry.

## Related

- TASK-287 plan: `.agent/tasks/TASK-287-claude-code-subprocess-oom-hardening.md`
- Source: `internal/executor/rss_sampler.go`, `resource_limits_linux.go`
- Retry: `internal/executor/retry.go` OOM strategy (2 attempts, 10 s backoff)
