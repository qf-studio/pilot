---
name: GitHub poller stops polling silently after some trigger
description: Pilot daemon alive with GitHub adapter enabled but polling stops producing execution events — 3+ hour gaps observed 2026-04-17
type: project
originSessionId: 3eb8d5b9-a522-4cc1-b1cb-d3d565061021
---
## Symptom

Pilot daemon running. `pilot doctor` green. GitHub adapter enabled in config, 30s poll interval. Issues labeled `pilot` + state `open` exist on GitHub. **But `executions` table in SQLite has zero new rows for hours.** Dashboard shows queue empty, no errors.

**Why:** Unknown. Possibly dispatcher drain, goroutine leak, or config reload failing silently. The poller literally stops firing but the process keeps the dashboard alive.

**How to apply:** When diagnosing "Pilot not picking up issues":
1. First check `SELECT MAX(created_at) FROM executions` — if > 30 min old, poller is silently dead, not filtering.
2. Restart alone doesn't always fix. ProcessedStore persists across restart; issues previously "processed" stay blocked.
3. `gh issue close X && gh issue reopen X` does NOT bust ProcessedStore either (tested 2026-04-17).
4. Toggling the `pilot` label off/on does NOT bust ProcessedStore.
5. Last-resort: `pilot github run <issue> --repo owner/repo` — direct dispatch, bypasses daemon poller. Proves the task itself works (vs daemon being broken).

## Observed 2026-04-17

- Pilot v2.95.11, GitHub adapter enabled, 30s poll
- Queue empty per dashboard
- `executions` table MAX(created_at) was 17:22Z; at 21:00Z+ still no new entries
- Restart twice, label toggle, close+reopen — all failed to restart polling
- `pilot github run 2356` direct dispatch worked and produced PR #2359

## Where to look if filing a bug

- `internal/adapters/github/poller.go` — `checkForNewIssues`, `Start`, poll loop
- Check for goroutine panic recovery that silently swallows errors
- Check ProcessedStore interaction — maybe poller fetched N items, all got marked processed, next poll returned same N and poller concluded "nothing to do"
- `execution_logs` table had NO "poll" entries — suggests log suppression or missing instrumentation
