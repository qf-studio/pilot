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

| Feature | Status | Description |
|---------|--------|-------------|
| **Telegram Bot** | ✅ | Chat-based task execution with voice & image support |
| **GitHub Adapter** | ✅ | Issues, PRs, webhooks |
| **Jira Adapter** | ✅ | Issue sync and updates |
| **Daily Briefs** | ✅ | Scheduled progress reports via Slack/Email/Telegram |
| **Alerting** | ✅ | Task failures, cost thresholds, stuck detection |
| **Cross-Project Memory** | ✅ | Shared context across repositories |
| **Execution Metrics** | ✅ | Token usage, cost tracking, performance analytics |
| **Voice Transcription** | ✅ | SenseVoice (local) + Whisper API fallback |
| **Image Analysis** | ✅ | Multimodal input via Telegram |
| **Structured Logging** | ✅ | JSON logs with correlation IDs |
| **Usage Metering** | ✅ | Billing foundation for Pilot Cloud |

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
- ffmpeg (optional, for voice)

## Quick Start

```bash
# 1. Initialize config
pilot init

# 2. Start Telegram bot
pilot telegram

# 3. Send task via Telegram
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
```

## CLI Reference

```
pilot telegram     Start Telegram bot
pilot start        Start gateway daemon
pilot stop         Stop daemon
pilot status       Show running tasks
pilot brief        Trigger daily brief manually
pilot doctor       Check dependencies and config
pilot version      Show version info
```

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
└── transcription/   Voice-to-text (SenseVoice/Whisper)
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

### In Progress
- [ ] Team management & permissions
- [ ] Cost controls & budgets
- [ ] Approval workflows

### Planned
- [ ] Quality gates
- [ ] Execution replay & debugging
- [ ] Webhooks API
- [ ] GitHub App integration
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
