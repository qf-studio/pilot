<p align="center">
  <img src="assets/pilot-logo.png" alt="Pilot" width="120" />
</p>

<h1 align="center">Pilot</h1>

<p align="center">
  <strong>AI that ships your tickets while you sleep</strong>
</p>

<p align="center">
  <a href="https://github.com/alekspetrov/pilot/releases"><img src="https://img.shields.io/github/v/release/alekspetrov/pilot?style=flat-square" alt="Release"></a>
  <a href="https://github.com/alekspetrov/pilot/blob/main/LICENSE"><img src="https://img.shields.io/badge/license-BSL--1.1-blue?style=flat-square" alt="License"></a>
  <a href="https://github.com/alekspetrov/pilot/stargazers"><img src="https://img.shields.io/github/stars/alekspetrov/pilot?style=flat-square" alt="Stars"></a>
  <a href="https://github.com/alekspetrov/pilot/actions"><img src="https://img.shields.io/github/actions/workflow/status/alekspetrov/pilot/ci.yml?style=flat-square" alt="Build"></a>
</p>

<p align="center">
  <a href="#install">Install</a> •
  <a href="#quick-start">Quick Start</a> •
  <a href="#how-it-works">How It Works</a> •
  <a href="#features">Features</a> •
  <a href="https://discord.gg/pilot">Discord</a>
</p>

<br />

<p align="center">
  <img src="assets/demo.gif" alt="Pilot Demo" width="700" />
</p>

---

## The Problem

You have 47 tickets in your backlog. You agonize over which to prioritize. Half are "quick fixes" that somehow take 2 hours each. Your PM asks for status updates. Sound familiar?

## The Solution

Pilot picks up tickets from GitHub, Linear, or Jira—plans the implementation, writes the code, runs tests, and opens a PR. You review and merge. That's it.

```
┌─────────────┐      ┌─────────────┐      ┌─────────────┐      ┌─────────────┐
│   Ticket    │ ───▶ │   Pilot     │ ───▶ │   Review    │ ───▶ │   Ship      │
│  (GitHub)   │      │  (AI dev)   │      │   (You)     │      │  (Merge)    │
└─────────────┘      └─────────────┘      └─────────────┘      └─────────────┘
```

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/alekspetrov/pilot/main/install.sh | bash
```

Or with Homebrew:

```bash
brew install alekspetrov/tap/pilot
```

## Quick Start

**1. Configure** (one time)

```bash
export ANTHROPIC_API_KEY="sk-..."
export GITHUB_TOKEN="ghp_..."
```

**2. Start Pilot**

```bash
pilot start --github --telegram
```

**3. Label an issue**

Add the `pilot` label to any GitHub issue. Pilot picks it up, implements it, and opens a PR.

That's it. Go grab coffee. ☕

## How It Works

```
You label issue "pilot"
        │
        ▼
┌───────────────────┐
│  Pilot claims it  │  ← Adds "pilot-in-progress" label
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
│  Runs tests       │  ← Quality gates: test, lint, build
└───────┬───────────┘
        │
        ▼
┌───────────────────┐
│  Opens PR         │  ← Links to issue, includes summary
└───────┬───────────┘
        │
        ▼
    You review
        │
        ▼
      Merge 🚀
```

## Features

| Feature | Description |
|---------|-------------|
| **GitHub/Linear/Jira** | Pick up tickets from your existing workflow |
| **Telegram Bot** | Chat with Pilot, send tasks via voice/text |
| **Quality Gates** | Auto-runs tests, lint, build before PR |
| **Autopilot Mode** | Auto-merge when CI passes (configurable) |
| **Navigator Integration** | Learns your codebase patterns over time |
| **Cost Controls** | Budget limits, model routing, complexity detection |
| **Dashboard** | Terminal UI showing tasks, tokens, costs |

## Autopilot Modes

Control how much autonomy Pilot has:

```bash
# Fast iteration - skip CI, auto-merge
pilot start --autopilot=dev --github

# Balanced - wait for CI, then auto-merge
pilot start --autopilot=stage --github

# Safe - wait for CI + human approval
pilot start --autopilot=prod --github
```

## Telegram Integration

Talk to Pilot like a junior dev:

```
You: "Add rate limiting to the /api/users endpoint"
Pilot: "On it. I'll use a token bucket algorithm with Redis. Creating issue..."
Pilot: "PR #142 ready for review: https://github.com/..."
```

Send voice messages, images, or text. Pilot understands context.

```bash
pilot start --telegram --github
```

## Dashboard

Real-time visibility into what Pilot is doing:

```
┌─ Pilot Dashboard ─────────────────────────────────────────┐
│                                                           │
│  Status: ● Running    Autopilot: stage    Queue: 3       │
│                                                           │
│  Current Task                                             │
│  ├─ GH-156: Add user authentication                       │
│  ├─ Phase: Implementing (65%)                             │
│  └─ Duration: 2m 34s                                      │
│                                                           │
│  Token Usage          Cost                                │
│  ├─ Input:  124k      Today:    $4.82                    │
│  ├─ Output:  31k      This Week: $28.40                  │
│  └─ Total:  155k      Budget:    $100.00                 │
│                                                           │
│  Recent Tasks                                             │
│  ├─ ✅ GH-155  Fix login redirect      1m 12s   $0.45   │
│  ├─ ✅ GH-154  Add dark mode toggle    3m 45s   $1.20   │
│  └─ ✅ GH-153  Update dependencies     0m 34s   $0.15   │
│                                                           │
└───────────────────────────────────────────────────────────┘
```

```bash
pilot start --dashboard --github
```

## vs Other Tools

| | Pilot | Devin | Cursor | Claude Code |
|---|:---:|:---:|:---:|:---:|
| Autonomous execution | ✅ | ✅ | ❌ | ❌ |
| Picks up tickets | ✅ | ❌ | ❌ | ❌ |
| Self-hosted | ✅ | ❌ | ❌ | ✅ |
| Opens PRs | ✅ | ✅ | ❌ | ❌ |
| Telegram/Slack | ✅ | ❌ | ❌ | ❌ |
| Cost controls | ✅ | ❌ | ❌ | ❌ |
| Free | ✅ | ❌ | ❌ | ✅ |

## Configuration

```yaml
# ~/.pilot/config.yaml
github:
  token: ${GITHUB_TOKEN}
  repos:
    - owner/repo1
    - owner/repo2
  label: pilot

telegram:
  token: ${TELEGRAM_BOT_TOKEN}
  allowed_users:
    - your_username

autopilot:
  mode: stage

budget:
  daily_limit: 10.00
  monthly_limit: 100.00
```

## Commands

```bash
pilot start          # Start Pilot daemon
pilot task "..."     # Run single task
pilot status         # Show current status
pilot logs           # Stream logs
pilot upgrade        # Self-update to latest
```

## Requirements

- macOS or Linux
- Claude Code CLI (`npm install -g @anthropic-ai/claude-code`)
- Anthropic API key

## FAQ

<details>
<summary><strong>Is this safe?</strong></summary>

Pilot runs in your environment with your permissions. It can only access repos you configure. All changes go through PR review (unless you enable auto-merge). You stay in control.
</details>

<details>
<summary><strong>How much does it cost?</strong></summary>

Pilot is free. You pay for Claude API usage (~$0.50-2.00 per typical task). Set budget limits to control costs.
</details>

<details>
<summary><strong>What tasks can it handle?</strong></summary>

Best for: bug fixes, small features, refactoring, tests, docs, dependency updates.

Not ideal for: large architectural changes, security-critical code, tasks requiring human judgment.
</details>

<details>
<summary><strong>Does it learn my codebase?</strong></summary>

Yes. Pilot uses Navigator to understand your patterns, conventions, and architecture. It gets better over time.
</details>

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). We welcome PRs!

## License

[BSL 1.1](LICENSE) © Aleksei Petrov

Free for internal use and non-competitive products. Converts to Apache 2.0 after 4 years.

---

<p align="center">
  <strong>Stop agonizing over tickets. Let Pilot ship them.</strong>
</p>

<p align="center">
  <a href="https://github.com/alekspetrov/pilot">⭐ Star on GitHub</a> •
  <a href="https://discord.gg/pilot">💬 Join Discord</a> •
  <a href="https://twitter.com/pikiselev">🐦 Follow Updates</a>
</p>
