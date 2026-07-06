> **SALVAGED 2026-07-06** from `backup/local-main-2026-05-27` (never landed on main; status frozen as of 2026-05-26 Wave-5 planning).

# TASK-302: Autopilot PR reconciliation loop (failsafe for OnPRCreated misses)

**Wave:** 4 (M) · **Severity:** P0 (critical-component reliability gap) · **Source:** autopilot-orphan incident 2026-05-26 (PR #3107 / issue #3100)

---

## Problem

Autopilot's PR-discovery is purely event-driven via `OnPRCreated` callback (`internal/autopilot/controller.go:354`). If that callback misses a fire — for any of multiple known causes — the PR is permanently orphaned until the daemon restarts. There is no periodic reconciliation during normal operation.

**Observed failure modes** (any one of these orphans a PR):
1. Executor returns `result.PRNumber == 0` (gate at `internal/adapters/github/poller.go:596` and `cmd/pilot/main.go:825`) — happens when post-create verification fails (e.g., CI-already-red, network blip during PR-URL capture)
2. `recoverOrphanedIssues` (`poller.go:401-436`) skips label-less stuck issues; `HasLabel(InProgress)` gate at `poller.go:721` then permanently skips them
3. `ListPullRequests` (`internal/adapters/github/client.go:505-511`) has no pagination; startup `ScanExistingPRs` silently caps at 30 open PRs

**Incident (2026-05-26):** PR #3107 (TASK-298-redo) was created on remote but invisible to autopilot for ~40 min until daemon restart. CI initially failed on GitHub Actions infra error (codeload 404), executor recorded `pr_url=''` despite the PR existing. See `.agent/knowledge/memories/pitfalls/bug_autopilot_pr_discovery_orphans.md`.

## Approach

### Step 1 — Reconciliation loop in `controller.go` (~90 min)

Add a periodic reconciliation goroutine alongside the existing `Run` loop:

```go
// In controller.go, inside Start() or a new startReconciler():
func (c *Controller) startReconciler(ctx context.Context) {
    ticker := time.NewTicker(60 * time.Second)
    defer ticker.Stop()
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            c.reconcileOrphanPRs(ctx)
        }
    }
}

func (c *Controller) reconcileOrphanPRs(ctx context.Context) {
    prs, err := c.gh.ListPullRequests(ctx, "open") // paginated — see Step 2
    if err != nil {
        slog.Warn("reconciler: list PRs failed", "err", err)
        return
    }
    for _, pr := range prs {
        if !strings.HasPrefix(pr.HeadBranch, "pilot/GH-") {
            continue
        }
        c.activeMu.Lock()
        _, tracked := c.activePRs[pr.Number]
        c.activeMu.Unlock()
        if tracked {
            continue
        }
        // Found an orphan — register via same code path as OnPRCreated
        slog.Warn("reconciler: registering orphan PR",
            "pr", pr.Number, "branch", pr.HeadBranch)
        // Reuse ScanExistingPRs' insertion logic OR call OnPRCreated directly
        c.registerOrphan(pr)
        autopilotMetrics.OrphanRegistered.Inc()
    }
}
```

### Step 2 — Paginate `ListPullRequests` (~45 min)

`internal/adapters/github/client.go:505-511` — current single GET caps at 30. Add pagination:

```go
func (c *Client) ListPullRequests(ctx context.Context, state string) ([]*PullRequest, error) {
    var all []*PullRequest
    page := 1
    for {
        prs, err := c.listPullRequestsPage(ctx, state, page, 100)
        if err != nil {
            return nil, err
        }
        all = append(all, prs...)
        if len(prs) < 100 {
            break
        }
        page++
        if page > 50 { // safety limit
            slog.Warn("ListPullRequests: hit pagination safety limit", "count", len(all))
            break
        }
    }
    return all, nil
}
```

### Step 3 — Metric for orphan registrations (~15 min)

New counter: `pilot_autopilot_orphan_pr_registered_total{trigger="reconciler"|"startup_scan"}`. Spikes indicate `OnPRCreated` is missing fires — a signal worth alerting on.

### Step 4 — Tests (~60 min)

- Unit (`controller_test.go`): seed an open PR that's NOT in `activePRs`, run one reconciler tick, assert the PR is now in `activePRs` AND `autopilot_pr_state` has a row
- Unit: PR already tracked → no duplicate registration
- Unit (paginated `ListPullRequests`): mock GitHub to return 100+ PRs across 2 pages, assert all returned

### Step 5 — Operational doc (~30 min)

Update `.agent/sops/autopilot/` (create dir if needed) with a runbook entry:
- Detection query for orphan PRs (already in pitfall memory, copy here)
- How the reconciler closes the gap
- What to do if metric shows persistent orphan registrations (root-cause investigation pointer)

## Files to modify

- `internal/autopilot/controller.go` (reconciler loop + orphan registration)
- `internal/autopilot/controller_test.go` (or new `controller_reconciler_test.go`)
- `internal/adapters/github/client.go` (pagination)
- `internal/adapters/github/client_test.go` (pagination test)
- `internal/autopilot/metrics.go` (or wherever counters live — add `OrphanRegistered`)
- New: `.agent/sops/autopilot/orphan-pr-recovery.md`

## Test Strategy

- Unit + integration as above
- Manual: kill `OnPRCreated` callback (force result.PRNumber=0 in a test build), create a PR via the executor, wait 60s, observe reconciler logs registering it

## Effort

M (~4h total). One PR.

## Out of scope

- Hardening `OnPRCreated`'s upstream (executor PR-URL capture) — covered by [[TASK-300]] and a follow-up TASK
- Orphan recovery for label-less issues — covered by a separate sibling task
- Reconciliation alarms / paging — observability work, separate

## Closes / blocks

- Closes orphan-PR class of bugs described in `.agent/knowledge/memories/pitfalls/bug_autopilot_pr_discovery_orphans.md`
- Does NOT block TASK-300 or TASK-301 — parallel-safe
