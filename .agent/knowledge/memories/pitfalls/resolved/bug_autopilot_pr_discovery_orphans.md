> **RESOLVED/SUPERSEDED (2026-07-06):** Periodic PR reconciliation now exists (reconcileOrphanPRs) and the OnPRCreated/reconciler double-register interplay is covered by pitfalls/bug_onprcreated_reconciler_double_register.md (mem-047). Salvaged from backup/local-main-2026-05-27 for incident forensics (PR #3107).

---
name: Autopilot PR discovery orphans — PRs invisible to autopilot until daemon restart
description: Autopilot relies on a synchronous OnPRCreated callback for PR discovery; if that callback doesn't fire (executor returned PRNumber=0, orphan-recovery missed an unlabeled issue, etc), the PR is invisible to autopilot until the daemon restarts and ScanExistingPRs runs. Critical reliability gap with no failsafe.
type: pitfall
---

## What goes wrong

Autopilot's PR-discovery is **purely event-driven via `OnPRCreated`** (`internal/autopilot/controller.go:354`). The callback is fired by the GitHub poller at `internal/adapters/github/poller.go:596` AND the gateway handler at `cmd/pilot/main.go:825-826`, gated on `result != nil && result.PRNumber > 0`. If that gate fails for any reason, autopilot will never learn about the PR.

There is **no periodic reconciliation** during normal operation. Only two startup paths register PRs:
- `RestoreState()` at `controller.go:309` — replays existing `autopilot_pr_state` SQLite rows
- `ScanExistingPRs()` at `controller.go:2083` — one-shot `ListPullRequests("open")` at startup that registers any `pilot/GH-*` PR not already in `activePRs`

If a PR is created during a window where `OnPRCreated` doesn't fire correctly, it stays orphaned until the daemon restarts.

## Observed incident (2026-05-26, PR #3107 / issue #3100)

- Issue #3100 (TASK-298-redo) executed by Pilot
- PR #3107 created (`refactor(state_store): consolidate 7 *_processed tables`, branch `pilot/GH-3100`)
- CI failed on **GitHub Actions infrastructure error** (`codeload.github.com 404 on actions/setup-go`)
- Pilot recorded `executions.commit_sha = 84273ab8` (a parent SHA, wrong), `pr_url = ''` — and yet PR #3107 exists on GitHub with the real work
- Autopilot's `autopilot_pr_state` had no row for #3107
- Adding the `pilot` label to PR #3107 did NOT trigger tracking
- **Restarting `pilot start --github` made `ScanExistingPRs` run, which then registered #3107** (stage progressed to `ci_passed` after CI re-run)

## Why OnPRCreated didn't fire (ranked hypotheses)

1. **Executor returned `PRNumber=0`** — most likely. The infrastructure CI failure may have caused the runner's post-PR-create verification (or a parallel check) to return `PRNumber=0`. Gate at `poller.go:596` then skipped `OnPRCreated`. The `executions.pr_url=''` record corroborates this — the executor didn't capture the PR URL even though the PR exists.
2. **`recoverOrphanedIssues` orphan gap** — Issue #3100 had `pilot-in-progress` label remaining when the daemon restarted with the issue label-less. `recoverOrphanedIssues` (`poller.go:401-436`) only queries issues with BOTH the poller label AND `pilot-in-progress` — a label-less stuck issue is invisible to recovery, and the subsequent `HasLabel(InProgress)` gate at `poller.go:721` then permanently skips it.
3. **`ListPullRequests` no pagination** — `client.go:505-511` does a single GET, GitHub defaults to 30 PRs. Startup scan silently truncates if >30 open PRs.

## How to detect orphaned PRs

```bash
# All open PRs from Pilot branches
PILOT_PRS=$(gh pr list --state open --search "head:pilot/" --json number,headRefName --jq '.[].number')

# Compare to what autopilot tracks
TRACKED=$(sqlite3 ~/.pilot/data/pilot.db \
  "SELECT pr_number FROM autopilot_pr_state WHERE stage NOT IN ('failed','merged')")

# Orphans = on GitHub but not in autopilot_pr_state
comm -23 <(echo "$PILOT_PRS" | sort) <(echo "$TRACKED" | sort)
```

If output is non-empty, those PRs are stuck — autopilot won't move them through CI/merge.

## How to recover

**Immediate fix:** restart the Pilot daemon — `pkill -f "pilot start" && pilot start --github --env <env> --dashboard`. The startup `ScanExistingPRs` will pick up orphans. **Caveat:** if >30 open PRs, the orphan beyond 30 is still missed; see fix direction #2 below.

## Fix direction

### 1. Periodic reconciliation loop (P0, structural)
Add a goroutine that runs every 60s in `controller.go` (alongside the existing `Run` loop): query `gh pr list --head pilot/* --state open`, diff against `activePRs`, register any missing PR via the same code path as `ScanExistingPRs`. This closes the entire bug class regardless of why `OnPRCreated` missed.

### 2. Paginate `ListPullRequests` (P1)
`client.go:505-511` — add pagination support, fetch all pages until empty. Currently silently caps at 30.

### 3. Tighten orphan recovery (P1)
`poller.go:401-436` — query for issues with `pilot-in-progress` regardless of poller label, not the intersection. Or: when poller skips an issue at `poller.go:721` due to `pilot-in-progress`, also check if any orphan PR exists for that issue and surface a structured alert.

### 4. Failsafe in OnPRCreated gate (P1)
`poller.go:596`, `main.go:825` — when `result != nil && result.PRNumber == 0`, log a structured warning AND call a new `tryRecoverPR(issueNumber)` that does a one-shot `gh pr list --head pilot/GH-{N}` to find the orphaned PR and call `OnPRCreated` manually. This catches the H1 case where the executor lost the PR number.

### 5. Tighten `IsResultShipped` / `result.PRNumber` capture (related)
The executions.pr_url='' for PR #3107 (which DOES exist on GitHub) indicates the executor's PR-URL capture failed even though `gh pr create` succeeded. See [[bug_pilot_ghost_closes]] Variant 4 — same root cause as the ghost-SHA bug. The capture path needs hardening.

## Related memories

- [[bug_pilot_ghost_closes]] — Variants 4 + 5 share the underlying problem: Pilot's subsystems have inconsistent notions of "this work is alive"
- [[bug_handleconflict_no_refile]] — autopilot's conflict path was previously fixed for one orphan class
- [[bug_poller_silent_stop]] — sibling pattern: poller stops, no alarm
