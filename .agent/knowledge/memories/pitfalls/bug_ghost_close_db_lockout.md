---
name: Ghost-close DB lockout
description: A "completed" executions row with no PR makes the GitHub poller silently skip the issue forever — manual SQL DELETE is the only escape until #2476 lands.
type: project
originSessionId: 560af8f6-11de-44c4-b853-8ada61347335
---
When `executions.status='completed'` exists for `task_id='GH-N'` but no PR was actually opened/merged, the GitHub poller's GH-2242 guard (`internal/adapters/github/poller.go:913-928`) silently skips dispatch on every tick. Reopening the issue, removing `pilot-done`, re-adding `pilot` — none of it helps. The poller looks dead but is running fine; it's deliberately refusing to re-dispatch.

**Why:** The status was written based on Pilot's internal success flag, not observable git state. If `createPR` silently fails but the runner returns success, the row says `completed` and the issue is locked out forever.

**How to apply:** When debugging "Pilot won't pick issue" and the standard checks pass (state OPEN, label `pilot`, no blocking labels, daemon running, ProcessedStore empty), check `executions` table:

```bash
sqlite3 ~/.pilot/data/pilot.db \
  "SELECT id, task_id, datetime(created_at), status FROM executions WHERE task_id='GH-N';"
```

If a `completed` row exists without a corresponding merged PR (cross-check: `gh pr list --search GH-N`), it's a ghost. Unblock:

```bash
sqlite3 ~/.pilot/data/pilot.db \
  "DELETE FROM executions WHERE task_id='GH-N' AND status='completed';"
```

Next poll tick (≤30s) dispatches. Tracked instance: #2382. Systemic fix: #2476.

## 2026-05-04 update — cascade cleanup also creates ghosts

Closing PRs unmerged + cleaning up a cascade chain leaves stale `executions.status='completed'` rows behind. Did 3 today during cascade #2 recovery (GH-2566/2568/2573). Cleanup query for a known list:

```bash
sqlite3 ~/.pilot/data/pilot.db \
  "DELETE FROM executions WHERE task_id IN ('GH-N','GH-M','GH-O') AND status='completed';"
```

Also worth cleaning recent `failed` rows when an issue's PR was closed for merge-conflict and you want it cleanly re-dispatched (failed rows don't trigger lockout but pile up in the executions list — see GH-2564 cleanup on 2026-05-04).

Related to #2589 (daemon-startup label/execution cleaner — design pre-built in `before-compact-2026-05-04-cascade-2-resolved-smoke-pending.md`).
