---
name: Slack notifications & routing (founder box)
description: Which Slack channel receives what from the Pilot daemon, the three config keys that decide it, alert cadence, what has no Slack path, and how to change/verify routing
type: reference
---

# Slack notifications & routing — founder box (as of 2026-09-06)

Workspace `T09HF3HDDU6`, bot **Quant Flow MCP Bot** (`B09HGS3BQUA`, user `U09HGS3BR18`).
Box config: `/home/ec2-user/.pilot/config.yaml` (read ONCE at daemon start — every routing
change needs a restart; see `.claude/skills/pilot-aws/SKILL.md` § Stop/start/restart).

## Destinations

| Channel | ID | Receives | Config key |
|---|---|---|---|
| `#pilot-reports` | `C0C0SFL7L2C` | **all alerts** (info/warning/critical) | `alerts.channels[slack-monitoring].slack.channel` |
| Founder bot DM | `D09HGS3BR4J` | **merge-approval asks** + decisions (buttons work: `socket_mode: false` → HTTP interactivity) | `adapters.slack.approval.channel` |
| `#pointer` | `C0BHQ9FGFPY` | chat replies, task-lifecycle notifier (`TaskStarted/Completed/Failed`), anything with no more specific key | `adapters.slack.channel` |
| `#engineering` | `C09HCNM09GV` | nothing from Pilot since 2026-07-16 (not referenced by any key) | — |
| `#infrastructure` | `C0BV37L87C1` | humans only (Nelya / AWS consolidation) | — |
| Telegram `283716179` | — | daily brief 08:00 NY · receipts digest 18:00 Berlin · prod-env approvals · PR merged/released/CI-fix notices | `orchestrator.daily_brief` / `receipts_digest` / `autopilot.environments.prod.approval_source` |

**History:** before 09-06 all three Slack keys pointed at `#pointer` (the alert channel was
*named* `slack-engineering` but never pointed at `#engineering`), so approval cards drowned in
~40 alerts/day. Founder ask 09-06: alerts to a monitoring channel, asks back in the DM.

## The three keys (nothing else picks a Slack channel)

1. `alerts.rules[].channels[]` (names) → `alerts.channels[].slack.channel`. Rule `channels: []`
   = broadcast to every enabled alert channel whose `severities` match.
2. `adapters.slack.approval.channel` → fallback `adapters.slack.channel` → fallback DM to
   `approval.pre_merge.approvers[0]` (`internal/approval/slack.go resolveChannel`, GH-4772).
3. `adapters.slack.channel` — everything else (`internal/adapters/slack/notifier.go`).

There is **no per-project Slack channel** (`ProjectConfig` has none) and **no thread
support** for alerts/approvals (only chat replies thread). `approval_source` picks the
*platform* (telegram/slack/github-review), never the channel.

## What never reaches Slack
PR merged · released · CI-fix issue created · release notes — the `autopilot.Notifier`
interface has one implementation (`internal/autopilot/telegram_notifier.go`). Adding a Slack
implementation is a code change (`controller.SetNotifier` has no production caller today).

## Alert rules on the box (post 09-06)

| Rule | Severity | Cooldown | Note |
|---|---|---|---|
| `task_stuck` | warning | **1h** (was 15m) | still fires on terminal states — pitfall `task-stuck-and-lane-starvation-fire-on-terminal-and-needs-human` |
| `task_failed` | warning | 0 | one per failure |
| `consecutive_failures` | critical | 30m | counts operator cancels (pitfall `alert-engine-counts-operator-cancels`) |
| `lane_starvation` | warning | **2h** (was 30m) | explicit rule added; threshold stays default 3 cycles (zero-fallback) |
| `autopilot_deadlock` | critical | 1h | built-in default, follows channel rename |
| `pr_stuck_waiting_ci` | info | 15m | built-in default |
| `daily_spend`, `budget_depleted` | — | — | disabled |
| every other `alerts.defaultRules()` type | default | default | unioned in by Type at load (GH-4866) |

**Do not author in config** rules whose condition fields the config-side
`AlertConditionConfig` lacks and whose handler has no zero fallback (`pr_stuck_waiting_ci`,
`failed_queue_high`, `api_error_rate_high`) — the threshold would silently zero. Safe to
author: `lane_starvation`, `deadlock` (both fall back to defaults).

## Changing routing (procedure)
1. Backup: `cp config.yaml config.yaml.bak-<date>-<why>` (as ec2-user, keep 0600).
2. Edit with PyYAML-validated text substitution (PyYAML 5.4.1 is on the box; no `yq`).
   Never round-trip-dump the whole file (comments/quoting lost).
3. `python3 -c "import yaml; yaml.safe_load(open('config.yaml'))"`.
4. Restart per pilot-aws skill when the queue is idle (in-flight executions die and re-pick).
5. Verify: next alert in `#pilot-reports`; next `awaiting_approval` card in the DM with buttons.

## Related
- SOP `.agent/sops/autopilot/approval-channel-routing.md` (GH-4380 platform routing + 09-06 destination)
- Memory `slack-routing-three-keys-alerts-vs-approvals-vs-chat`
- Pitfall `slack-approval-socket-mode-unroutable` (buttons dead on Socket Mode)
