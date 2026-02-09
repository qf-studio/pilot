# Pilot: AI That Ships Your Tickets

**Navigator plans. Pilot executes.**

## ⚠️ WORKFLOW: Navigator + Pilot Pipeline

**This Claude Code session uses Navigator for planning, Pilot for execution.**

| Phase | Tool | Action |
|-------|------|--------|
| 1. Plan | `/nav-task` | Design solution, create implementation plan |
| 2. Execute | GitHub Issue | Create issue with `pilot` label |
| 3. Review | PR Review | Check Pilot's PR, request changes if needed |
| 4. Ship | Merge | Merge PR when approved |

### Quick Commands

```bash
# Plan a feature (Navigator)
/nav-task "Add rate limiting to API endpoints"

# Hand off to Pilot
gh issue create --title "Add rate limiting" --label pilot --body "..."

# Check Pilot's queue
gh issue list --label pilot --state open

# Review and merge
gh pr view <number> && gh pr merge <number>
```

### Rules

- ✅ Use `/nav-task` for planning and design
- ✅ Create GitHub issues with `pilot` label for execution
- ✅ Review every PR before merging
- ❌ DO NOT write code directly in this session
- ❌ DO NOT make commits manually
- ❌ DO NOT create PRs manually

**Pilot runs in a separate terminal** (`pilot start --telegram --github`) and auto-picks issues labeled `pilot`.

---

## Project Overview

Pilot is an autonomous AI development pipeline that:
- Receives tickets from Linear/Jira/Asana
- Plans and executes implementation using Claude Code
- Creates PRs and notifies via Slack
- Learns patterns across projects

## Quick Start

```bash
# Build
make build

# Run
./bin/pilot start

# Or development mode
make dev
```

## Architecture

```
Gateway (Go)      → WebSocket control plane + HTTP webhooks
Adapters          → Telegram, GitHub, GitLab, Azure DevOps, Linear, Jira, Slack
Executor          → Claude Code process management + Navigator integration
Autopilot         → CI monitoring, auto-merge, feedback loop, release pipeline
Memory            → SQLite + knowledge graph
Dashboard         → Terminal UI (bubbletea)
```

## Project Structure

```
pilot/
├── cmd/pilot/           # CLI entrypoint
├── internal/
│   ├── gateway/         # WebSocket + HTTP server
│   ├── adapters/        # Telegram, GitHub, GitLab, AzureDevOps, Linear, Jira, Slack
│   ├── executor/        # Claude Code runner + intent judge
│   ├── autopilot/       # CI monitor, auto-merge, release pipeline
│   ├── alerts/          # Alert engine + multi-channel dispatch
│   ├── memory/          # SQLite + knowledge graph
│   ├── config/          # YAML config
│   ├── dashboard/       # TUI (bubbletea)
│   └── testutil/        # Safe test token constants
├── docs/                # Nextra v2 documentation site
└── .agent/              # Navigator docs
```

## Code Standards

- **Go**: Follow standard Go conventions, `go fmt`, `golangci-lint`
- **Python**: PEP 8, type hints, dataclasses
- **Architecture**: KISS, DRY, SOLID
- **Testing**: Table-driven tests for Go

## Test Token Guidelines

When writing tests that need API tokens or secrets:

- ❌ **DON'T** use realistic patterns that trigger GitHub push protection:
  - `xoxb-123456789012-1234567890123-abcdefghij` (Slack)
  - `sk-abcdefghijklmnopqrstuvwxyz123456` (OpenAI)
  - `ghp_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx` (GitHub PAT)
  - `AKIAIOSFODNN7EXAMPLE` (AWS)

- ✅ **DO** use obviously fake tokens:
  - `test-slack-bot-token`
  - `fake-api-key`
  - `test-github-token`

- ✅ **DO** use constants from `internal/testutil/tokens.go`:
  ```go
  import "github.com/anthropics/pilot/internal/testutil"

  token := testutil.FakeSlackBotToken
  ```

**Why?** GitHub's push protection blocks realistic-looking secrets even in test files. 9 branches were blocked for hours due to this.

## Key Commands

```bash
make build          # Build binary
make dev            # Run in dev mode
make test           # Run tests
make lint           # Run linter
make fmt            # Format code
make install-hooks  # Install git pre-commit hooks
make check-secrets  # Check for secret patterns in tests
```

## Configuration

Config file: `~/.pilot/config.yaml`

Required env vars:
- `LINEAR_API_KEY`
- `SLACK_BOT_TOKEN`

## Commit Guidelines

- Format: `type(scope): description`
- Types: feat, fix, refactor, test, docs, chore
- Reference tasks: `feat(gateway): add webhook handler TASK-01`

## Navigator Integration

This project uses Navigator for planning, Pilot for execution:

```bash
/nav-start              # Start session, load context
/nav-task "feature"     # Plan implementation
gh issue create ...     # Hand off to Pilot
```

Documentation in `.agent/`:
- `DEVELOPMENT-README.md` - Navigator index
- `tasks/` - Implementation plans
- `system/` - Architecture docs

## Forbidden Actions

- ❌ No secrets in code
- ❌ No package.json modifications without approval
- ❌ No bulk doc loading (use Navigator lazy loading)
- ❌ No Claude Code mentions in commits

## Development Workflow

1. Start Navigator: `/nav-start`
2. Plan feature: `/nav-task "description"`
3. Create issue: `gh issue create --title "..." --label pilot --body "..."`
4. Wait for Pilot to execute and create PR
5. Review PR: `gh pr view <n>`
6. Merge when ready: `gh pr merge <n>`

## Current Status

**Version:** v0.24.1 | **101 features implemented** | **6 unwired**

**Core:**
- ✅ Task execution with Navigator integration
- ✅ Autopilot: CI monitor, auto-merge, feedback loop, tag-only release
- ✅ Intent judge in execution pipeline
- ✅ Rich PR comments with execution metrics
- ✅ Epic decomposition with sub-issue PR wiring
- ✅ Self-review, quality gates, effort routing

**Adapters:** Telegram (voice, images, 5 modes), GitHub, GitLab, Azure DevOps, Linear, Jira, Slack

**Dashboard:** Sparkline cards, SQLite persistence, epic-aware history, hot upgrade

**Docs:** Nextra v2 at pilot.quantflow.studio, auto-deploy via GitLab CI

<!-- GitHub integration verified -->
