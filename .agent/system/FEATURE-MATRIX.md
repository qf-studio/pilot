# Pilot Feature Matrix

**Last Updated:** 2026-02-13 (v0.24.1)

## Legend

| Symbol | Meaning |
|--------|---------|
| ✅ | Fully implemented and working |
| ⚠️ | Implemented but not wired to CLI |
| 🚧 | Partial implementation |
| ❌ | Not implemented |

---

## Core Execution

| Feature | Status | Package | CLI Command | Config Key | Notes |
|---------|--------|---------|-------------|------------|-------|
| Task execution | ✅ | executor | `pilot task` | - | Claude Code subprocess |
| Branch creation | ✅ | executor | `--no-branch` disables | - | Auto `pilot/TASK-XXX` |
| PR creation | ✅ | executor | `--create-pr` | - | Via `gh pr create` |
| Progress display | ✅ | executor | - | - | Lipgloss visual bar |
| Navigator detection | ✅ | executor | - | - | Auto-prefix if `.agent/` exists |
| AGENTS.md loading | ✅ | executor | - | - | LoadAgentsFile reads project AGENTS.md (v0.24.1) |
| Dry run mode | ✅ | executor | `--dry-run` | - | Show prompt only |
| Verbose output | ✅ | executor | `--verbose` | - | Stream raw JSON |
| Task dispatcher | ✅ | executor | - | - | Per-project queue (GH-46) |
| Sequential execution | ✅ | executor | `--sequential` | `orchestrator.execution.mode` | Wait for PR merge before next issue |
| Self-review | ✅ | executor | - | - | Auto code review before PR push (v0.13.0) |
| Auto build gate | ✅ | executor | - | - | Minimal build gate when none configured (v0.13.0) |
| Epic decomposition | ✅ | executor | - | `decompose.enabled` | PlanEpic + CreateSubIssues for complex tasks (v0.20.2) |
| Haiku subtask parser | ✅ | executor | - | - | Structured extraction via Haiku API, regex fallback (v0.21.0) |

## Intelligence

| Feature | Status | Package | CLI Command | Config Key | Notes |
|---------|--------|---------|-------------|------------|-------|
| Complexity detection | ✅ | executor | - | - | Heuristic-based: trivial/simple/medium/complex/epic |
| Model routing | ✅ | executor | - | - | Haiku (trivial), Opus 4.6 (all others) (v0.20.0) |
| Effort routing | ✅ | executor | - | - | Map complexity to Claude thinking depth (v0.20.0) |
| LLM intent classification | ✅ | adapters/telegram | - | - | Pattern-based intent detection for Telegram messages |
| Intent judge (pipeline) | ✅ | executor | - | - | Wired into execution pipeline for task classification (v0.24.0) |
| Research subagents | ✅ | executor | - | - | Haiku-powered parallel codebase exploration |

## Input Adapters

| Feature | Status | Package | CLI Command | Config Key | Notes |
|---------|--------|---------|-------------|------------|-------|
| Telegram bot | ✅ | adapters/telegram | `pilot start --telegram` | `adapters.telegram` | Long-polling mode |
| Telegram voice | ✅ | transcription | - | `adapters.telegram.transcription` | OpenAI Whisper |
| Telegram images | ✅ | adapters/telegram | - | - | Vision support |
| Telegram chat mode | ✅ | adapters/telegram | - | - | Conversational responses (v0.6.0) |
| Telegram research | ✅ | adapters/telegram | - | - | Deep analysis to chat (v0.6.0) |
| Telegram planning | ✅ | adapters/telegram | - | - | Plan with Execute/Cancel (v0.6.0) |
| GitHub polling | ✅ | adapters/github | `pilot start --github` | `adapters.github.polling` | 30s interval |
| GitHub run issue | ✅ | adapters/github | `pilot github run` | `adapters.github` | Manual trigger |
| GitLab polling | ✅ | adapters/gitlab | `pilot start --gitlab` | `adapters.gitlab` | Full adapter with webhook support |
| Azure DevOps | ✅ | adapters/azuredevops | `pilot start --azuredevops` | `adapters.azuredevops` | Full adapter with webhook support |
| Linear webhooks | ✅ | adapters/linear | - | `adapters.linear` | Wired in pilot.go, gateway route + handler registered |
| Jira webhooks | ✅ | adapters/jira | - | `adapters.jira` | Wired in pilot.go, gateway route + handler + orchestrator |

## Output/Notifications

| Feature | Status | Package | CLI Command | Config Key | Notes |
|---------|--------|---------|-------------|------------|-------|
| Slack notifications | ✅ | adapters/slack | - | `adapters.slack` | Task updates |
| Telegram replies | ✅ | adapters/telegram | - | - | Auto in telegram mode |
| GitHub comments | ✅ | adapters/github | - | - | PR/issue updates |
| Rich PR comments | ✅ | main | - | - | Execution metrics (duration, tokens, cost, model) in PR comments (v0.24.1) |
| Outbound webhooks | ✅ | webhooks | `pilot webhooks` | `webhooks` | Dispatches task.started/completed/failed/progress events |

## Alerts & Monitoring

| Feature | Status | Package | CLI Command | Config Key | Notes |
|---------|--------|---------|-------------|------------|-------|
| Alert engine | ✅ | alerts | `pilot task --alerts` | `alerts.enabled` | Event-based |
| Slack alerts | ✅ | alerts | - | `alerts.channels[].type=slack` | - |
| Telegram alerts | ✅ | alerts | - | `alerts.channels[].type=telegram` | - |
| Email alerts | ✅ | alerts | - | `alerts.channels[].type=email` | SMTP sender + wired to dispatcher |
| Webhook alerts | ✅ | alerts | - | `alerts.channels[].type=webhook` | - |
| PagerDuty alerts | ✅ | alerts | - | `alerts.channels[].type=pagerduty` | Wired to dispatcher, HTTP-verified tests |
| Custom rules | ✅ | alerts | - | `alerts.rules[]` | Configurable conditions |
| Cooldown periods | ✅ | alerts | - | `alerts.defaults.cooldown` | Avoid spam |

## Quality Gates

| Feature | Status | Package | CLI Command | Config Key | Notes |
|---------|--------|---------|-------------|------------|-------|
| Quality gate runner | ✅ | quality | - | `quality.enabled` | Pre-completion checks |
| Test gates | ✅ | quality | - | `quality.gates[].type=test` | Run test commands |
| Lint gates | ✅ | quality | - | `quality.gates[].type=lint` | Run lint commands |
| Build gates | ✅ | quality | - | `quality.gates[].type=build` | Compile check |
| Retry on failure | ✅ | quality | - | `quality.max_retries` | Auto-retry with feedback |

## Memory & Learning

| Feature | Status | Package | CLI Command | Config Key | Notes |
|---------|--------|---------|-------------|------------|-------|
| Execution history | ✅ | memory | - | `memory.path` | SQLite store |
| Lifetime metrics | ✅ | memory | - | - | Token/cost/task counts persist across restarts (v0.21.2) |
| Cross-project patterns | ✅ | memory | `pilot patterns` | - | Pattern learning |
| Pattern search | ✅ | memory | `pilot patterns search` | - | Keyword search |
| Pattern stats | ✅ | memory | `pilot patterns stats` | - | Usage analytics |
| Knowledge graph | ✅ | memory | - | - | Internal only |

## Dashboard

| Feature | Status | Package | CLI Command | Config Key | Notes |
|---------|--------|---------|-------------|------------|-------|
| TUI dashboard | ✅ | dashboard | `--dashboard` | - | Bubbletea terminal UI |
| Token metrics card | ✅ | dashboard | - | - | Sparkline + lifetime totals (v0.18.0) |
| Cost metrics card | ✅ | dashboard | - | - | Sparkline + cost/task (v0.18.0) |
| Queue metrics card | ✅ | dashboard | - | - | Current queue depth, succeeded/failed (v0.21.2) |
| Autopilot panel | ✅ | dashboard | - | - | Live PR lifecycle status |
| Task history | ✅ | dashboard | - | - | Recent 5 completed tasks |
| Hot upgrade key | ✅ | dashboard | `u` key | - | In-place upgrade from dashboard |
| SQLite persistence | ✅ | dashboard | - | - | Metrics survive restarts (v0.21.2) |

## Replay & Debug

| Feature | Status | Package | CLI Command | Config Key | Notes |
|---------|--------|---------|-------------|------------|-------|
| Execution recording | ✅ | replay | - | - | Auto-saved |
| List recordings | ✅ | replay | `pilot replay list` | - | Filter by project/status |
| Show recording | ✅ | replay | `pilot replay show` | - | Metadata view |
| Interactive replay | ✅ | replay | `pilot replay play` | - | TUI viewer |
| Analyze recording | ✅ | replay | `pilot replay analyze` | - | Token/phase breakdown |
| Export recording | ✅ | replay | `pilot replay export` | - | HTML/JSON/Markdown |

## Reports & Briefs

| Feature | Status | Package | CLI Command | Config Key | Notes |
|---------|--------|---------|-------------|------------|-------|
| Daily briefs | ✅ | briefs | `pilot brief` | `orchestrator.daily_brief` | Scheduled |
| Weekly briefs | ✅ | briefs | `pilot brief --weekly` | - | Manual trigger |
| Slack delivery | ✅ | briefs | - | `orchestrator.daily_brief.channels` | - |
| Metrics summary | ✅ | briefs | - | `orchestrator.daily_brief.content.include_metrics` | - |

## Cost Controls

| Feature | Status | Package | CLI Command | Config Key | Notes |
|---------|--------|---------|-------------|------------|-------|
| Budget tracking | ✅ | budget | `pilot budget` | `budget` | View daily/monthly usage via memory store |
| Daily/monthly limits | ✅ | budget | `pilot task --budget` | `budget.daily_limit` | Enforcer blocks tasks when exceeded |
| Per-task limits | ✅ | budget | - | `budget.per_task` | TaskLimiter wired to executor in main.go (v0.24.1) |
| Budget in polling mode | ✅ | budget | - | - | Enforcer checks budget before picking issues in GitHub/Linear pollers |
| Alerts on overspend | ✅ | alerts | - | `alerts.rules[].type=budget` | Enforcer fires alert callbacks at thresholds |

## Team Management

| Feature | Status | Package | CLI Command | Config Key | Notes |
|---------|--------|---------|-------------|------------|-------|
| Team CRUD | ✅ | teams | `pilot team` | `teams` | Wired to Pilot struct + `--team` flag (GH-633) |
| Permissions | ✅ | teams | `--team` | `team.enabled` | Pre-execution RBAC check in Runner (GH-634) |
| Project mapping | ✅ | teams | `--team-member` | `team.member_email` | Project access validation in poller + CLI (GH-635) |

## Infrastructure

| Feature | Status | Package | CLI Command | Config Key | Notes |
|---------|--------|---------|-------------|------------|-------|
| Cloudflare tunnel | ✅ | tunnel | `pilot start --tunnel` | `tunnel` | Auto-start tunnel, prints webhook URLs |
| Gateway HTTP | ✅ | gateway | `pilot start` | `gateway` | Internal server, wired in main.go |
| Gateway WebSocket | ✅ | gateway | - | - | Session management active in gateway |
| Health checks | ✅ | health | `pilot doctor` | - | System validation, 32 unit tests |
| OpenCode backend | ✅ | executor | `--backend opencode` | `executor.backend` | HTTP/SSE alternative to Claude Code |

## Approval Workflows

| Feature | Status | Package | CLI Command | Config Key | Notes |
|---------|--------|---------|-------------|------------|-------|
| Approval engine | ✅ | approval | `--autopilot=prod` | `approval` | Wired to autopilot controller |
| Slack approval | ✅ | approval | - | `adapters.slack.approval` | Interactive messages, registered in main.go |
| Telegram approval | ✅ | approval | - | - | Inline keyboards, registered in main.go |
| Rule-based triggers | ✅ | approval | - | `approval.rules[]` | RuleEvaluator with 4 matchers wired into Manager (GH-636) |

## Autopilot (v0.19.1)

| Feature | Status | Package | CLI Command | Config Key | Notes |
|---------|--------|---------|-------------|------------|-------|
| Autopilot controller | ✅ | autopilot | `--autopilot=ENV` | - | Orchestrates PR lifecycle |
| CI monitoring | ✅ | autopilot | - | - | Polls check status with HeadSHA refresh (v0.18.0) |
| Auto-merge | ✅ | autopilot | - | - | Merges after CI/approval |
| Feedback loop | ✅ | autopilot | - | - | Creates fix issues for CI failures |
| CI fix on original branch | ✅ | autopilot | - | - | `autopilot-meta` comment embeds branch (v0.19.1) |
| PR scanning on startup | ✅ | autopilot | - | - | Resumes tracking existing PRs |
| Telegram notifications | ✅ | autopilot | - | - | PR status updates |
| Dashboard panel | ✅ | dashboard | `--dashboard` | - | Live autopilot status |
| Environment gates | ✅ | autopilot | - | - | dev/stage/prod behavior |
| Tag-only release | ✅ | autopilot | - | - | CreateTag() → GoReleaser handles full release (v0.24.1) |

**Environments:**
- `dev`: Skip CI, auto-merge immediately
- `stage`: Wait for CI, then auto-merge
- `prod`: Wait for CI + human approval

## Self-Management

| Feature | Status | Package | CLI Command | Config Key | Notes |
|---------|--------|---------|-------------|------------|-------|
| Version check | ✅ | upgrade | `pilot version` | - | Shows current |
| Auto-upgrade | ✅ | upgrade | `pilot upgrade` | - | Downloads latest |
| Hot upgrade | ✅ | upgrade | `u` key in dashboard | - | Graceful task wait + restart (v0.18.0) |
| Config init | ✅ | config | `pilot init` | - | Creates default |
| Setup wizard | ✅ | main | `pilot setup` | - | Interactive config |
| Shell completion | ✅ | main | `pilot completion` | - | bash/zsh/fish |
| Doctor check | ✅ | health | `pilot doctor` | - | System health, wired in main.go |

---

## Feature Summary

| Category | ✅ Working | ⚠️ Implemented | 🚧 Partial | ❌ Missing |
|----------|-----------|----------------|-----------|-----------|
| Core Execution | 14 | 0 | 0 | 0 |
| Intelligence | 6 | 0 | 0 | 0 |
| Input Adapters | 12 | 0 | 0 | 0 |
| Output/Notifications | 5 | 0 | 0 | 0 |
| Alerts & Monitoring | 8 | 0 | 0 | 0 |
| Quality Gates | 5 | 0 | 0 | 0 |
| Memory & Learning | 6 | 0 | 0 | 0 |
| Dashboard | 8 | 0 | 0 | 0 |
| Replay & Debug | 6 | 0 | 0 | 0 |
| Reports & Briefs | 4 | 0 | 0 | 0 |
| Cost Controls | 5 | 0 | 0 | 0 |
| Team Management | 3 | 0 | 0 | 0 |
| Infrastructure | 5 | 0 | 0 | 0 |
| Approval Workflows | 4 | 0 | 0 | 0 |
| Autopilot | 10 | 0 | 0 | 0 |
| Self-Management | 6 | 0 | 0 | 0 |
| **Total** | **107** | **0** | **0** | **0** |

---

## Usage Patterns

### Minimal Setup (Task Execution Only)
```yaml
# ~/.pilot/config.yaml
projects:
  - name: my-project
    path: ~/code/my-project
    navigator: true
```
```bash
pilot task "Add user authentication"
```

### Telegram Bot Mode
```yaml
adapters:
  telegram:
    enabled: true
    bot_token: "your-bot-token"
    transcription:
      provider: openai
      openai_key: "your-openai-key"
```
```bash
pilot start --telegram --project ~/code/my-project
```

### GitHub Polling Mode
```yaml
adapters:
  github:
    enabled: true
    repo: "owner/repo"
    polling:
      enabled: true
      interval: 30s
      label: "pilot"
```
```bash
# Start with GitHub polling, picks up issues labeled "pilot"
pilot start --github
# Or combine with Telegram
pilot start --telegram --github
```

### Autopilot Mode (v0.19.1)
```bash
# Fast iteration - auto-merge without CI
pilot start --autopilot=dev --telegram --github

# Balanced - wait for CI, then auto-merge
pilot start --autopilot=stage --telegram --github --dashboard

# Production - CI + manual approval required
pilot start --autopilot=prod --telegram --github --dashboard
```

### Full Production Setup
```yaml
gateway:
  host: "0.0.0.0"
  port: 9090

adapters:
  telegram: { enabled: true, bot_token: "..." }
  github: { enabled: true, repo: "...", polling: { enabled: true } }
  slack: { enabled: true, bot_token: "..." }

alerts:
  enabled: true
  channels:
    - name: slack-ops
      type: slack
      slack: { channel: "#pilot-alerts" }
  rules:
    - name: task-failed
      type: task_failed
      channels: [slack-ops]

quality:
  enabled: true
  gates:
    - name: tests
      type: test
      command: "make test"
    - name: lint
      type: lint
      command: "make lint"
```
