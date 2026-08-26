---
name: outage-shape-is-jobs-never-run-not-jobs-fail
description: A CI-platform outage manifests as jobs that never run (timeouts, zero check-runs, startup_failure) — NOT as failing jobs; the platform-outage breaker correlates only failure-class CI failures, so it stayed closed through a confirmed GitHub Actions major outage
type: pitfall
---

# An outage doesn't fail your jobs — it never runs them

**Incident 2026-08-26.** GitHub Actions entered a **major outage at 15:11:58Z**.
Every CI anomaly that afternoon traced to it, and none of them looked like an
outage:

- **pilot PR#5231**: run reported `conclusion: failure` while **all 8 jobs sat
  `queued` and never started**. Autopilot burned its 30m `waiting_ci` budget at
  `last_status=pending`, then set `StageFailed`. The code was fine.
- **pilot-console PR#221**: **zero workflow runs** for 70 min while sibling PRs
  opened minutes later got a full set of checks.
- **pilot-console PR#218**: `startup_failure`.

Hours were spent diagnosing these as three unrelated code/config problems.

## Why the breaker didn't save us

`platform_breaker.enabled: true` was live in production. It stayed closed the
whole time, because `platformBreaker.Observe` (`internal/autopilot/controller.go`)
is called **only from the CI-failure handling path** — it correlates failures
carrying a `failureClass`. An outage generates none of those:

- the CI-timeout path sets `prState.Stage = StageFailed` **directly**, never
  calling `Observe`;
- a SHA with zero check-runs never reaches the failure path at all.

So the signal the breaker exists to detect is invisible to it. Fix dispatched
as [[pilot#5236]].

## The two shapes, and why both are dangerous

| Shape | Current outcome |
|---|---|
| Checks **never appear** | grace expires → combined-status `TotalCount==0` → **`CISuccess`** → auto-merged ([[pilot#5233]], fix merged as PR#5235) |
| Checks appear then **time out** | `StageFailed` → PR treated as if the code were broken; destructive actions armed |

Neither is protected, and they are opposite errors — one merges untested code,
the other blames good code.

**How to apply**: when several PRs across repos go strange at once —
timeouts, missing checks, `startup_failure`, jobs stuck `queued` — **check
`githubstatus.com` before diagnosing any of them individually**
(`curl -s https://www.githubstatus.com/api/v2/summary.json`). A run whose
`conclusion` is `failure` while its jobs are all `queued` is the tell: the
platform failed the run, the code never executed. Do not re-run CI during an
active outage — it produces more false signal. Pause dispatch until it clears.
Related: [[ci-infra-failure-misclassified-as-code]].
