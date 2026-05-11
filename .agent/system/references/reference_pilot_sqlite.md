---
name: Pilot SQLite DB location and diagnostic tables
description: Where Pilot persists state on disk and which tables to query when debugging poller/autopilot issues
type: reference
originSessionId: 3eb8d5b9-a522-4cc1-b1cb-d3d565061021
---
## Location

**Real DB**: `~/.pilot/data/pilot.db` (WAL mode, ~8MB+ with usage)

**Misleading empty file**: `~/.pilot/pilot.db` — zero bytes, leftover/unused. Don't query this.

## Key tables for debugging

| Table | What it holds | When to check |
|---|---|---|
| `executions` | Every task execution attempt. Columns: `id`, `task_id`, `project_path`, `status` (pending/running/completed/failed), `error`, `created_at`, `completed_at`, `model_name`, `task_labels` (JSON), cost+token metrics | "Did Pilot run task X?" — `SELECT task_id, status, created_at FROM executions ORDER BY created_at DESC LIMIT 10` |
| `execution_logs` | Structured log lines from runs. Columns: `execution_id`, `timestamp`, `level`, `message`, `component` | "What happened during this run?" |
| `adapter_processed` | Generic processed-issue store. PK: `(adapter, issue_id)`. Columns: `adapter`, `issue_id`, `processed_at`, `result` | "Why won't poller pick up issue X?" — `SELECT * FROM adapter_processed WHERE adapter='github' AND issue_id='1234'` |
| `autopilot_pr_state` | PR state machine state per PR. Columns: `pr_number`, `issue_number`, `stage`, `updated_at` | "Is PR X being tracked by autopilot?" |
| `autopilot_pr_failures` | PR failure history for retry budget | "Why did autopilot give up on PR X?" |
| `autopilot_processed` | Autopilot-specific processed PRs | "Has autopilot already handled this PR?" |
| `linear_processed`, `jira_processed`, etc. | Per-adapter legacy processed stores | Adapter-specific pickup issues |

## Common debugging queries

```sql
-- Recent executions (last hour)
SELECT task_id, status, created_at, substr(error,1,100)
FROM executions
WHERE created_at > datetime('now', '-1 hour')
ORDER BY created_at DESC;

-- Is issue #1234 blocked by ProcessedStore?
SELECT * FROM adapter_processed
WHERE adapter='github' AND issue_id='1234';

-- What PRs is autopilot currently tracking?
SELECT pr_number, issue_number, stage, updated_at
FROM autopilot_pr_state
ORDER BY updated_at DESC;

-- Last execution_logs entry (proves daemon is alive and processing)
SELECT MAX(timestamp) FROM execution_logs;
```

## How to use

When Pilot "won't pick up" an issue or "dashboard empty but issues exist":
1. Check `executions` for recent activity — if MAX(created_at) is hours ago, daemon is silently not dispatching
2. Check `adapter_processed` for the specific issue — if present, poller will skip it
3. Check `autopilot_pr_state` for related PRs — if stuck in a stage, state machine is blocked

## Related escape hatch

If poller is silent, `pilot github run <issue> --repo owner/repo` dispatches directly,
bypassing the daemon poller. Useful for diagnosis: if direct dispatch works, the poller
is the culprit, not the task.
