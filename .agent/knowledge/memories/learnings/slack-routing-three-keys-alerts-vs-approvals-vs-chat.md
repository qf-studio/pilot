---
name: slack-routing-three-keys-alerts-vs-approvals-vs-chat
description: Slack destinations are set by exactly three config keys (alerts.channels[].slack.channel, adapters.slack.approval.channel, adapters.slack.channel); renaming the alert channel re-routes built-in rules too; config-side rule conditions lack lane/deadlock fields but handlers fall back to defaults
type: learning
---

# Slack routing: three keys decide every destination (2026-09-06 #pointer spam fix)

**Symptom:** founder losing merge-approval cards in `#pointer` under ~40 alerts/day
(`task_stuck` re-firing every 15 min for tasks already at 100% Completed/NeedsHuman,
`lane_starvation` every 30 min for repos whose only open issue is `pilot-needs-human`).
`#engineering` had zero Pilot traffic since 2026-07-16 — the config's alert channel was
*named* `slack-engineering` but pointed at `#pointer`.

## Where each Slack message picks its channel
| Message class | Key | Fallback |
|---|---|---|
| All alerts (alerts engine) | `alerts.rules[].channels[]` → `alerts.channels[].slack.channel` | rule `channels: []` = broadcast to every enabled channel whose `severities` match |
| Approval asks (pre-merge etc.) | `adapters.slack.approval.channel` | → `adapters.slack.channel` → DM `Approvers[0]` (`internal/approval/slack.go resolveChannel`) |
| Chat replies, task lifecycle notifier | `adapters.slack.channel` | none |
| PR merged / released / CI-fix-created | **no Slack path** — only `autopilot/telegram_notifier.go` implements the notifier | — |

No per-project Slack channel exists. A DM channel id (`D…`) is a valid `approval.channel`
value; buttons still work over HTTP interactivity (`socket_mode: false` on the box).

## Built-in rules and config overrides
- `alerts.FromConfigAlerts` unions `defaultRules()` by *Type*: any type absent from the
  persisted `alerts.rules` list is appended with `channels: []` (broadcast). So renaming or
  repointing the one Slack alert channel re-routes lane_starvation / deadlock / pr_stuck too.
- `internal/config` `AlertConditionConfig` has NO `lane_starvation_poll_cycles`,
  `deadlock_timeout`, `pr_stuck_timeout`, `failed_queue_threshold`, `api_error_rate_per_min`.
  Defining such a rule in config zeroes the condition. Safe only where the engine handler has
  a zero fallback: lane_starvation (`threshold <= 0` → 3) and deadlock (`timeout == 0` → 1h)
  do; pr_stuck / failed_queue / api_error_rate do NOT — never author those in config.

## Applied 2026-09-06 (box `~/.pilot/config.yaml`, backup `.bak-20260906-slack-routing`)
`approval.channel: 'D09HGS3BR4J'` (founder bot DM) · alert channel `slack-monitoring` →
`#pilot-reports` (C0C0SFL7L2C) · `task_stuck` cooldown 1h · explicit `lane_starvation` rule
with cooldown 2h. Restart required (config read once at startup).

## Still open (code, not config)
`task_stuck` must ignore terminal statuses; `lane_starvation` must exclude
`pilot-needs-human`/`pilot-blocked` issues. Cooldowns only dampen the noise.
