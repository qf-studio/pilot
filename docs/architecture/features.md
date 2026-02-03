# Feature Matrix

Complete feature status for Pilot.

## Legend

| Symbol | Meaning |
|--------|---------|
| ✅ | Fully implemented and working |
| ⚠️ | Implemented but not wired to CLI |
| 🚧 | Partial implementation |

---

## Core Execution

| Feature | Status | CLI Command | Notes |
|---------|--------|-------------|-------|
| Task execution | ✅ | `pilot task` | Claude Code subprocess |
| Branch creation | ✅ | `--no-branch` disables | Auto `pilot/TASK-XXX` |
| PR creation | ✅ | `--create-pr` | Via `gh pr create` |
| Progress display | ✅ | - | Lipgloss visual bar |
| Navigator detection | ✅ | - | Auto-prefix if `.agent/` exists |
| Dry run mode | ✅ | `--dry-run` | Show prompt only |
| Verbose output | ✅ | `--verbose` | Stream raw JSON |
| Task dispatcher | ✅ | - | Per-project queue |

## Input Adapters

| Feature | Status | CLI Command | Notes |
|---------|--------|-------------|-------|
| Telegram bot | ✅ | `pilot start --telegram` | Long-polling mode |
| Telegram voice | ✅ | - | OpenAI Whisper |
| Telegram images | ✅ | - | Vision support |
| Telegram chat mode | ✅ | - | Conversational responses |
| Telegram research | ✅ | - | Deep analysis to chat |
| Telegram planning | ✅ | - | Plan with Execute/Cancel |
| GitHub polling | ✅ | `pilot start --github` | 30s interval |
| GitHub run issue | ✅ | `pilot github run` | Manual trigger |
| Linear webhooks | ⚠️ | - | Needs gateway running |
| Jira webhooks | ⚠️ | - | Needs gateway running |

## Notifications

| Feature | Status | Notes |
|---------|--------|-------|
| Slack notifications | ✅ | Task updates |
| Telegram replies | ✅ | Auto in telegram mode |
| GitHub comments | ✅ | PR/issue updates |
| Outbound webhooks | ⚠️ | Config exists |

## Alerts & Monitoring

| Feature | Status | Config Key | Notes |
|---------|--------|------------|-------|
| Alert engine | ✅ | `alerts.enabled` | Event-based |
| Slack alerts | ✅ | `alerts.channels[].type=slack` | - |
| Telegram alerts | ✅ | `alerts.channels[].type=telegram` | - |
| Email alerts | ⚠️ | `alerts.channels[].type=email` | Implemented |
| Webhook alerts | ✅ | `alerts.channels[].type=webhook` | - |
| PagerDuty alerts | ⚠️ | `alerts.channels[].type=pagerduty` | Implemented |
| Custom rules | ✅ | `alerts.rules[]` | Configurable |
| Cooldown periods | ✅ | `alerts.defaults.cooldown` | Avoid spam |

## Quality Gates

| Feature | Status | Config Key | Notes |
|---------|--------|------------|-------|
| Quality gate runner | ✅ | `quality.enabled` | Pre-completion checks |
| Test gates | ✅ | `quality.gates[].type=test` | Run test commands |
| Lint gates | ✅ | `quality.gates[].type=lint` | Run lint commands |
| Build gates | ✅ | `quality.gates[].type=build` | Compile check |
| Retry on failure | ✅ | `quality.max_retries` | Auto-retry with feedback |

## Memory & Learning

| Feature | Status | CLI Command | Notes |
|---------|--------|-------------|-------|
| Execution history | ✅ | - | SQLite store |
| Cross-project patterns | ✅ | `pilot patterns` | Pattern learning |
| Pattern search | ✅ | `pilot patterns search` | Keyword search |
| Pattern stats | ✅ | `pilot patterns stats` | Usage analytics |
| Knowledge graph | ✅ | - | Internal only |

## Replay & Debug

| Feature | Status | CLI Command | Notes |
|---------|--------|-------------|-------|
| Execution recording | ✅ | - | Auto-saved |
| List recordings | ✅ | `pilot replay list` | Filter by project/status |
| Show recording | ✅ | `pilot replay show` | Metadata view |
| Interactive replay | ✅ | `pilot replay play` | TUI viewer |
| Analyze recording | ✅ | `pilot replay analyze` | Token/phase breakdown |
| Export recording | ✅ | `pilot replay export` | HTML/JSON/Markdown |

## Reports & Briefs

| Feature | Status | CLI Command | Notes |
|---------|--------|-------------|-------|
| Daily briefs | ✅ | `pilot brief` | Scheduled |
| Weekly briefs | ✅ | `pilot brief --weekly` | Manual trigger |
| Slack delivery | ✅ | - | Via config |
| Metrics summary | ✅ | - | Include in briefs |

## Autopilot

| Feature | Status | CLI Command | Notes |
|---------|--------|-------------|-------|
| Autopilot controller | ✅ | `--autopilot=ENV` | Orchestrates PR lifecycle |
| CI monitoring | ✅ | - | Polls check status |
| Auto-merge | ✅ | - | Merges after CI/approval |
| Feedback loop | ✅ | - | Handles post-merge failures |
| Telegram notifications | ✅ | - | PR status updates |
| Dashboard panel | ✅ | `--dashboard` | Live autopilot status |
| Environment gates | ✅ | - | dev/stage/prod behavior |

## Self-Management

| Feature | Status | CLI Command | Notes |
|---------|--------|-------------|-------|
| Version check | ✅ | `pilot version` | Shows current |
| Auto-upgrade | ✅ | `pilot upgrade` | Downloads latest |
| Config init | ✅ | `pilot init` | Creates default |
| Setup wizard | ✅ | `pilot setup` | Interactive config |
| Shell completion | ✅ | `pilot completion` | bash/zsh/fish |
| Doctor check | ⚠️ | `pilot doctor` | System health |

---

## Summary

| Category | ✅ Working | ⚠️ Implemented | 🚧 Partial |
|----------|-----------|----------------|-----------|
| Core Execution | 8 | 0 | 0 |
| Input Adapters | 8 | 2 | 0 |
| Notifications | 3 | 1 | 0 |
| Alerts & Monitoring | 6 | 2 | 0 |
| Quality Gates | 5 | 0 | 0 |
| Memory & Learning | 5 | 0 | 0 |
| Replay & Debug | 6 | 0 | 0 |
| Reports & Briefs | 4 | 0 | 0 |
| Autopilot | 7 | 0 | 0 |
| Self-Management | 5 | 1 | 0 |
| **Total** | **57** | **6** | **0** |
