<p align="center">
  <pre>
   ██████╗ ██╗██╗      ██████╗ ████████╗
   ██╔══██╗██║██║     ██╔═══██╗╚══██╔══╝
   ██████╔╝██║██║     ██║   ██║   ██║
   ██╔═══╝ ██║██║     ██║   ██║   ██║
   ██║     ██║███████╗╚██████╔╝   ██║
   ╚═╝     ╚═╝╚══════╝ ╚═════╝    ╚═╝
  </pre>
  <strong>AI That Ships Your Tickets</strong>
</p>

<p align="center">
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-BSL_1.1-blue.svg" alt="License: BSL 1.1"></a>
  <a href="https://github.com/alekspetrov/pilot/actions"><img src="https://github.com/alekspetrov/pilot/workflows/CI/badge.svg" alt="CI"></a>
  <a href="https://goreportcard.com/report/github.com/alekspetrov/pilot"><img src="https://goreportcard.com/badge/github.com/alekspetrov/pilot" alt="Go Report Card"></a>
  <a href="https://golang.org"><img src="https://img.shields.io/badge/Go-1.22+-00ADD8.svg" alt="Go Version"></a>
</p>

---

Autonomous AI development pipeline. Receives tickets, implements features, creates PRs.

## Features

### Core Execution

| Feature | Since | Description |
|---------|-------|-------------|
| Autopilot | v0.3.2 | CI monitoring, auto-merge, feedback loop (dev/stage/prod) |
| Task Decomposition | v0.4.0 | Complex tasks auto-split into sequential subtasks |
| Sequential Execution | v0.2.0 | Wait for PR merge before next issue |
| Quality Gates | v0.3.0 | Test/lint/build validation with auto-retry |
| Execution Replay | v0.3.0 | Record, playback, analyze, export (HTML/JSON/MD) |

### Intelligence

| Feature | Since | Description |
|---------|-------|-------------|
| Research Subagents | v0.4.0 | Haiku-powered parallel codebase exploration |
| Model Routing | v0.3.0 | Haiku (trivial) → Sonnet (standard) → Opus (complex) |
| Navigator Integration | v0.2.0 | Auto-detected `.agent/`, skipped for trivial tasks |
| Cross-Project Memory | v0.2.0 | Shared patterns and context across repositories |

### Integrations

| Feature | Since | Description |
|---------|-------|-------------|
| Telegram Bot | v0.1.0 | Chat-based tasks with voice transcription & images |
| GitHub Polling | v0.2.0 | Auto-pick issues with `pilot` label |
| Asana Adapter | v0.4.0 | Webhooks with HMAC verification, task sync |
| Jira Adapter | v0.2.0 | Issue sync and updates |
| Daily Briefs | v0.2.0 | Scheduled reports via Slack/Email/Telegram |
| Alerting | v0.2.0 | Task failures, cost thresholds, stuck detection |

### Infrastructure

| Feature | Since | Description |
|---------|-------|-------------|
| Dashboard TUI | v0.3.0 | Live monitoring, token/cost tracking, autopilot status |
| Hot Upgrade | v0.2.0 | Self-update with `pilot upgrade` |
| Cost Controls | v0.3.0 | Budget limits with hard enforcement |
| Multiple Backends | v0.2.0 | Claude Code + OpenCode support |
| BYOK | v0.2.0 | Bring your own Anthropic key, Bedrock, or Vertex |
| Structured Logging | v0.1.0 | JSON logs with correlation IDs |

### Sequential Execution Mode

Process one issue at a time, waiting for PR merge before picking up the next:

```bash
pilot start --github --sequential
```

Or configure in `~/.pilot/config.yaml`:

```yaml
orchestrator:
  execution:
    mode: sequential
    wait_for_merge: true
    poll_interval: 30s    # Check PR status every 30s
    pr_timeout: 1h        # Give up after 1 hour
```

**Benefits:**
- Prevents merge conflicts between concurrent PRs
- Ensures clean git history
- Safer for production workflows

### GitHub Issue Polling

Automatically pick up GitHub issues labeled with `pilot`:

```bash
pilot start --github
```

**How it works:**
1. Polls repository every 30s for issues with `pilot` label
2. Adds `pilot/in-progress` label when starting
3. Creates branch `pilot/GH-{number}`
4. Executes task with Claude Code
5. Creates PR and adds `pilot/done` label
6. In sequential mode, waits for PR merge before next issue

## Installation

### Homebrew (recommended)

```bash
brew tap alekspetrov/pilot
brew install pilot
```

### From Source

```bash
git clone https://github.com/alekspetrov/pilot
cd pilot
make build
sudo make install-global
```

### Go Install

```bash
go install github.com/alekspetrov/pilot/cmd/pilot@latest
```

### Update

```bash
brew upgrade pilot
# or
brew reinstall pilot
```

### Requirements

- Go 1.22+ (build only)
- [Claude Code CLI](https://github.com/anthropics/claude-code) 2.1.17+
- OpenAI API key (optional, for voice transcription)

### Environment Variables

Pilot uses Claude Code for AI execution, which respects these environment variables:

| Variable | Description |
|----------|-------------|
| `ANTHROPIC_API_KEY` | Custom Anthropic API key (uses your own account instead of Claude Code's) |
| `ANTHROPIC_BASE_URL` | Custom API endpoint (for proxies or enterprise deployments) |
| `CLAUDE_CODE_USE_BEDROCK` | Set to `1` to use AWS Bedrock instead of Anthropic API |
| `CLAUDE_CODE_USE_VERTEX` | Set to `1` to use Google Vertex AI instead of Anthropic API |

**Example: Using your own API key**
```bash
export ANTHROPIC_API_KEY=sk-ant-...
pilot start --telegram
```

**Example: Using AWS Bedrock**
```bash
export CLAUDE_CODE_USE_BEDROCK=1
export AWS_REGION=us-east-1
pilot start --github
```

## Quick Start

```bash
# 1. Initialize config
pilot init

# 2. Start Pilot with your preferred input
pilot start --telegram              # Telegram bot
pilot start --github                # GitHub issue polling
pilot start --telegram --github     # Both

# 3. Send task via Telegram or create GitHub issue with 'pilot' label
"Start TASK-07"
```

## Configuration

Config location: `~/.pilot/config.yaml`

```yaml
version: "1.0"

gateway:
  host: "127.0.0.1"
  port: 9090

adapters:
  telegram:
    enabled: true
    bot_token: "${TELEGRAM_BOT_TOKEN}"
    chat_id: "${TELEGRAM_CHAT_ID}"

  github:
    enabled: true
    token: "${GITHUB_TOKEN}"
    repo: "owner/repo"
    pilot_label: "pilot"
    polling:
      enabled: true
      interval: 30s
      label: "pilot"

orchestrator:
  execution:
    mode: sequential           # "sequential" or "parallel"
    wait_for_merge: true       # Wait for PR merge before next task
    poll_interval: 30s         # How often to check PR status
    pr_timeout: 1h             # Max wait time for PR merge

projects:
  - name: "my-project"
    path: "~/Projects/my-project"
    navigator: true
    default_branch: main

daily_brief:
  enabled: true
  schedule: "0 8 * * *"
  timezone: "Europe/Berlin"
  channels:
    - type: telegram
      channel: "${TELEGRAM_CHAT_ID}"

alerts:
  enabled: true
  channels:
    - name: telegram-alerts
      type: telegram
      severities: [critical, error, warning]

memory:
  path: ~/.pilot/data
  cross_project: true

executor:
  backend: claude-code          # "claude-code" (default) or "opencode"
  # Claude Code respects ANTHROPIC_API_KEY for BYOK (Bring Your Own Key)
  # Set env var to use your own Anthropic account:
  #   export ANTHROPIC_API_KEY=sk-ant-...
  # Or use cloud providers:
  #   export CLAUDE_CODE_USE_BEDROCK=1  # AWS Bedrock
  #   export CLAUDE_CODE_USE_VERTEX=1   # Google Vertex AI
  opencode:
    binary: opencode
    model: anthropic:claude-sonnet-4-20250514
```

## CLI Reference

### Core Commands

#### `pilot start` - Start Pilot with config-driven inputs

```bash
pilot start                          # Config-driven (reads ~/.pilot/config.yaml)
pilot start --telegram               # Enable Telegram polling
pilot start --github                 # Enable GitHub issue polling
pilot start --telegram --github      # Enable both
pilot start --dashboard              # With TUI dashboard
pilot start --no-gateway             # Polling only (no HTTP server)
pilot start --sequential             # Sequential execution mode
pilot start --parallel               # Parallel execution mode (legacy)
```

| Flag | Description |
|------|-------------|
| `--telegram` | Enable Telegram polling (overrides config) |
| `--github` | Enable GitHub issue polling (overrides config) |
| `--linear` | Enable Linear webhooks (overrides config) |
| `--dashboard` | Show TUI dashboard for real-time task monitoring |
| `--no-gateway` | Run polling adapters only (no HTTP gateway) |
| `--sequential` | Sequential execution: wait for PR merge before next issue |
| `--parallel` | Parallel execution: process multiple issues concurrently |
| `--project`, `-p` | Project path (default: config default or cwd) |
| `--replace` | Kill existing bot instance before starting |

#### `pilot task` - Execute tasks with Claude Code

```bash
pilot task "Add user authentication"                    # Run in current directory
pilot task "Fix login bug" -p ~/Projects/myapp          # Specify project
pilot task "Add feature" --create-pr                    # Auto-create GitHub PR
pilot task "Refactor API" --verbose                     # Stream Claude output
pilot task "Update docs" --dry-run                      # Preview without running
pilot task "Quick fix" --no-branch                      # Skip branch creation
pilot task "Implement feature" --backend opencode       # Use OpenCode backend
```

| Flag | Short | Description |
|------|-------|-------------|
| `--project` | `-p` | Project path (default: current directory) |
| `--create-pr` | | Push branch and create GitHub PR after execution |
| `--verbose` | `-v` | Stream raw Claude Code JSON output |
| `--dry-run` | | Show prompt without executing |
| `--no-branch` | | Don't create a new git branch |
| `--backend` | | Executor backend: `claude-code` (default) or `opencode` |

#### `pilot brief` - Generate daily/weekly briefs

```bash
pilot brief                   # Show scheduler status
pilot brief --now             # Generate and send immediately
pilot brief --weekly          # Generate weekly summary
```

| Flag | Description |
|------|-------------|
| `--now` | Generate and send brief immediately |
| `--weekly` | Generate weekly summary instead of daily |

### Analytics Commands

#### `pilot metrics` - Execution metrics and analytics

```bash
pilot metrics summary              # Last 7 days overview
pilot metrics summary --days 30    # Last 30 days
pilot metrics daily                # Daily breakdown
pilot metrics projects             # Per-project stats
pilot metrics export --format csv  # Export to CSV
```

#### `pilot usage` - Usage metering for billing

```bash
pilot usage summary               # Billable usage summary
pilot usage daily                 # Daily usage breakdown
pilot usage projects              # Per-project usage
pilot usage events --limit 50     # Raw usage events
pilot usage export --format json  # Export for billing
```

#### `pilot patterns` - Cross-project learned patterns

```bash
pilot patterns list               # List all patterns
pilot patterns search "auth"      # Search by keyword
pilot patterns stats              # Pattern statistics
```

#### `pilot upgrade` - Self-update to latest version

```bash
pilot upgrade                    # Check and upgrade
pilot upgrade check              # Only check for updates
pilot upgrade --force            # Skip task completion wait
pilot upgrade --no-restart       # Don't restart after upgrade
pilot upgrade rollback           # Restore previous version
```

| Flag | Description |
|------|-------------|
| `check` | Only check for available updates |
| `rollback` | Restore the previous version from backup |
| `--force`, `-f` | Skip waiting for running tasks |
| `--no-restart` | Don't restart after upgrade |
| `--yes`, `-y` | Skip confirmation prompt |

### System Commands

```bash
pilot start        # Start Pilot with configured inputs
pilot stop         # Stop daemon
pilot status       # Show running tasks
pilot init         # Initialize configuration
pilot version      # Show version info
pilot upgrade      # Self-update to latest version
```

### Global Flags

| Flag | Description |
|------|-------------|
| `--config` | Config file path (default: `~/.pilot/config.yaml`) |
| `--help` | Show help for any command |

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                          PILOT                              │
├──────────────┬──────────────────────────────────────────────┤
│ Gateway      │ HTTP/WebSocket server, routing               │
│ Adapters     │ Telegram, Slack, GitHub, Jira, Linear        │
│ Executor     │ Claude Code process management               │
│ Orchestrator │ Task planning, phase management              │
│ Memory       │ SQLite + cross-project knowledge graph       │
│ Briefs       │ Scheduled reports, multi-channel delivery    │
│ Alerts       │ Failure detection, cost monitoring           │
│ Metrics      │ Token usage, execution analytics             │
└──────────────┴──────────────────────────────────────────────┘
```

## Development

```bash
make deps        # Install dependencies
make build       # Build binary
make test        # Run tests
make lint        # Run linter
make dev         # Development mode with hot reload
```

### Project Structure

```
cmd/pilot/           CLI entry point
internal/
├── adapters/        Telegram, Slack, GitHub, Jira, Linear
├── alerts/          Alerting system
├── banner/          ASCII TUI startup display
├── briefs/          Daily briefs generation & delivery
├── config/          Configuration management
├── executor/        Claude Code process management
├── gateway/         HTTP/WebSocket server
├── health/          Dependency & feature checks
├── logging/         Structured logging
├── memory/          Cross-project memory store
├── metrics/         Execution analytics
└── transcription/   Voice-to-text (Whisper API)
```

## Documentation

| Document | Description |
|----------|-------------|
| [CLAUDE.md](CLAUDE.md) | AI assistant configuration |
| [LICENSE](LICENSE) | BSL 1.1 license terms |
| [.agent/tasks/](.agent/tasks/) | Task documentation |
| [.agent/sops/](.agent/sops/) | Standard operating procedures |

## Roadmap

### Completed
- [x] Gateway & executor foundation
- [x] Telegram bot with voice/image
- [x] GitHub & Jira adapters
- [x] Daily briefs & alerting
- [x] Cross-project memory
- [x] Execution metrics & logging
- [x] Usage metering
- [x] GitHub App integration (status checks, PR API)
- [x] GitHub issue polling with `pilot` label
- [x] Sequential execution mode with PR merge waiting
- [x] Quality gates (test/lint/build validation)
- [x] Cost controls & budgets
- [x] Model routing (complexity-based Haiku/Sonnet/Opus)
- [x] Hot upgrade (`pilot upgrade`)
- [x] Navigator auto-detection

### In Progress
- [ ] Team management & permissions
- [ ] Approval workflows

### Planned
- [ ] Execution replay & debugging
- [ ] Webhooks API
- [ ] Pilot Cloud (hosted SaaS)

## License

**Business Source License 1.1** - see [LICENSE](LICENSE)

| Use Case | Allowed |
|----------|---------|
| Internal use | ✅ |
| Self-hosting | ✅ |
| Modification & forking | ✅ |
| Non-competing products | ✅ |
| Competing SaaS | ❌ (requires license) |

Converts to **Apache 2.0** after 4 years.

## Contributing

Contributions welcome. Please open an issue first for major changes.

```bash
# Fork, clone, branch
git checkout -b feature/my-feature

# Make changes, test
make test

# Submit PR
```

---

<p align="center">
  <sub>Built with Claude Code + Navigator</sub>
</p>
