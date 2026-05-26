# SOP: Autopilot Orphan PR Recovery

**Category:** Autopilot reliability  
**Implemented:** 2026-05-26  
**Source incident:** PR #3107 (TASK-298-redo) invisible to autopilot for ~40 min

---

## What is an orphan PR?

A Pilot PR (branch `pilot/GH-*`) that exists on GitHub but is not tracked in `autopilot_pr_state`. Autopilot cannot advance, merge, or release an orphan — it is invisible to all processing loops.

---

## Automatic recovery (since TASK-302)

`Controller.startReconciler` runs every 60 seconds alongside the main processing loop. It lists all open PRs and registers any `pilot/GH-*` branch PR not yet in `activePRs`. No manual intervention is needed for transient misses.

**Metric to watch:** `pilot_autopilot_orphan_pr_registered_total{trigger="reconciler"}`  
A sustained spike (>0 per hour) means `OnPRCreated` is consistently missing fires — investigate the root cause below.

---

## Detection query

```bash
# Find pilot/ PRs not tracked in autopilot_pr_state
PILOT_PRS=$(gh pr list --state open --search "head:pilot/" --json number --jq '.[].number')
TRACKED=$(sqlite3 ~/.pilot/data/pilot.db \
  "SELECT pr_number FROM autopilot_pr_state WHERE stage NOT IN ('failed','merged')")
comm -23 <(echo "$PILOT_PRS" | sort) <(echo "$TRACKED" | sort)
```

An empty result means no orphans. Any numbers printed are orphan PR numbers.

---

## Known orphan causes

| # | Root cause | Signal |
|---|---|---|
| 1 | Executor returns `pr_url=""` (post-create verification failed) | `executor` log: `pr_url=''` despite PR existing |
| 2 | `ListPullRequests` pagination cap (fixed in TASK-302: now 100/page) | Repos with >30 open PRs at startup |
| 3 | `OnPRCreated` callback gate at `poller.go:596` filters `PRNumber==0` | Poller log: `skipping pr_number=0` |
| 4 | Daemon restart between PR create and `OnPRCreated` fire | Reconciler logs `registering orphan PR` on first tick post-restart |

---

## Manual recovery (if reconciler is disabled or pre-TASK-302)

```bash
# 1. Identify orphan PR number and issue number from branch name
gh pr view <PR_NUMBER>   # e.g. branch = pilot/GH-100 → issue 100

# 2. Force registration via sqlite directly (emergency only)
sqlite3 ~/.pilot/data/pilot.db <<SQL
INSERT OR REPLACE INTO autopilot_pr_state
  (pr_number, pr_url, issue_number, branch_name, head_sha, stage, ci_status, created_at)
VALUES
  (<PR>, '<URL>', <ISSUE>, 'pilot/GH-<ISSUE>', '<SHA>', 'pr_created', 'pending', datetime('now'));
SQL

# 3. Restart daemon — RestoreState will pick it up
pilot start
```

---

## Escalation

If `reconciler` metric stays elevated (>3 orphans/hour) after daemon restart:
1. Check executor logs for `pr_url=''` patterns — likely TASK-300 ghost-SHA issue
2. Check poller logs for `skipping pr_number=0` — fix executor to always return PR number
3. File issue with `pilot` label referencing this SOP
