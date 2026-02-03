# Pilot

<p align="center">
  <strong>AI that ships your tickets while you sleep</strong>
</p>

---

## The Problem

You have 47 tickets in your backlog. You agonize over which to prioritize. Half are "quick fixes" that somehow take 2 hours each. Your PM asks for status updates. Sound familiar?

## The Solution

Pilot picks up tickets from GitHub, Linear, Jira, or Asana—plans the implementation, writes the code, runs tests, and opens a PR. You review and merge. That's it.

```
┌─────────────┐      ┌─────────────┐      ┌─────────────┐      ┌─────────────┐
│   Ticket    │ ───▶ │   Pilot     │ ───▶ │   Review    │ ───▶ │   Ship      │
│  (GitHub)   │      │  (AI dev)   │      │   (You)     │      │  (Merge)    │
└─────────────┘      └─────────────┘      └─────────────┘      └─────────────┘
```

## Quick Start

```bash
# 1. Install
brew tap alekspetrov/pilot
brew install pilot

# 2. Initialize config
pilot init

# 3. Start Pilot
pilot start --github              # GitHub issue polling
pilot start --telegram            # Telegram bot
pilot start --telegram --github   # Both

# 4. Create a GitHub issue with 'pilot' label, or message your Telegram bot
```

That's it. Go grab coffee. ☕

## How It Works

```
You label issue "pilot"
        │
        ▼
┌───────────────────┐
│  Pilot claims it  │  ← Adds "pilot/in-progress" label
└───────┬───────────┘
        │
        ▼
┌───────────────────┐
│  Creates branch   │  ← pilot/GH-{number}
└───────┬───────────┘
        │
        ▼
┌───────────────────┐
│  Plans approach   │  ← Analyzes codebase, designs solution
└───────┬───────────┘
        │
        ▼
┌───────────────────┐
│  Implements       │  ← Writes code with Claude Code
└───────┬───────────┘
        │
        ▼
┌───────────────────┐
│  Quality gates    │  ← Test, lint, build validation
└───────┬───────────┘
        │
        ▼
┌───────────────────┐
│  Opens PR         │  ← Links to issue, adds "pilot/done"
└───────┬───────────┘
        │
        ▼
    You review
        │
        ▼
      Merge 🚀
```

## Features Overview

### Core Execution

| Feature | Description |
|---------|-------------|
| **Autopilot** | CI monitoring, auto-merge, feedback loop (dev/stage/prod modes) |
| **Task Decomposition** | Complex tasks auto-split into sequential subtasks |
| **Sequential Execution** | Wait for PR merge before next issue (prevents conflicts) |
| **Quality Gates** | Test/lint/build validation with auto-retry |
| **Execution Replay** | Record, playback, analyze, export (HTML/JSON/MD) |

### Intelligence

| Feature | Description |
|---------|-------------|
| **Research Subagents** | Haiku-powered parallel codebase exploration |
| **Model Routing** | Haiku (trivial) → Sonnet (standard) → Opus (complex) |
| **Navigator Integration** | Auto-detected `.agent/`, skipped for trivial tasks |
| **Cross-Project Memory** | Shared patterns and context across repositories |

### Integrations

| Feature | Description |
|---------|-------------|
| **Telegram Bot** | Chat, research, planning, tasks + voice & images |
| **GitHub Polling** | Auto-pick issues with `pilot` label |
| **Linear/Jira/Asana** | Webhooks and task sync |
| **Daily Briefs** | Scheduled reports via Slack/Email/Telegram |
| **Alerting** | Task failures, cost thresholds, stuck detection |

### Infrastructure

| Feature | Description |
|---------|-------------|
| **Dashboard TUI** | Live monitoring, token/cost tracking, autopilot status |
| **Hot Upgrade** | Self-update with `pilot upgrade` |
| **Cost Controls** | Budget limits with hard enforcement |
| **Multiple Backends** | Claude Code + OpenCode support |
| **BYOK** | Bring your own Anthropic key, Bedrock, or Vertex |

## License

**[Business Source License 1.1](https://github.com/alekspetrov/pilot/blob/main/LICENSE)** © Aleksei Petrov

| Use Case | Allowed |
|----------|---------|
| Internal use | ✅ |
| Self-hosting | ✅ |
| Modification & forking | ✅ |
| Non-competing products | ✅ |
| Competing SaaS | ❌ (requires license) |

Converts to **Apache 2.0** after 4 years.
