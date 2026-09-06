---
name: task-stuck-and-lane-starvation-fire-on-terminal-and-needs-human
description: task_stuck re-fires for tasks already at 100% (Completed/NeedsHuman) and lane_starvation counts pilot-needs-human issues — the 2026-07-05 task_stuck fix covered progress emission, not terminal states
type: pitfall
---

# task_stuck + lane_starvation fire on terminal / needs-human states (09-06 Slack flood)

**Observed 2026-09-06 in `#pointer`:** of ~40 alerts, ~30 were:
- `Task GH-263 stuck at 100% (NeedsHuman) for 1h49m` · `Task GH-5318 stuck at 100% (Completed)
  for 1h49m` — every 15 min (rule cooldown), for tasks that had already ENDED.
- `Lane qf-studio/auth-service has 1 open pilot-labeled issue(s) but nothing queued/running
  for 387 consecutive poll cycles` — every 30 min, where that one issue was #512 carrying
  `pilot-needs-human` + `needs-manual-rebase` (correctly excluded from admission, so
  "nothing queued" is the intended state, not starvation).

## Why the old fix did not cover it
`pitfalls/resolved/bug_task_stuck_alert_spam.md` (TASK-337, 2026-07-05) fixed *progress never
emitted* + *per-rule cooldown rotation*. `evaluateStuckTasks` still keys on "progress
unchanged for N minutes" and the terminal transition (Completed / NeedsHuman / Failed) does
not evict the task from `taskLastProgress` — a task parked at 100% is "unchanged" forever.
`handleLaneStarvation` counts open `pilot`-labeled issues without subtracting the
`pilot-needs-human` / `pilot-blocked` / `pilot-spec-incomplete` exclusions the poller applies.

## Mitigation applied (config, box, 09-06)
`task_stuck` cooldown 15m → 1h · explicit `lane_starvation` rule cooldown 30m → 2h · all alerts
moved to `#pilot-reports` so they no longer bury approval cards. Noise reduced, not fixed.

## Fix (code, not yet filed)
1. `evaluateStuckTasks`: skip/evict entries whose last status is terminal (Completed, Failed,
   NeedsHuman, Held-with-reason) — only `Executing`/`Started`-class states can be stuck.
2. `handleLaneStarvation`: apply the same label exclusion set the poller uses (single source:
   the admission filter), so a lane whose only open issues are human-owned is not "starved".
3. Per-task dedup already exists — keep it; add a test for the 100%-Completed case.

## Related
`bug_task_stuck_alert_spam` (resolved, superseded in part) · `alert-engine-counts-operator-cancels`
· `reference_slack_notifications_routing.md`
