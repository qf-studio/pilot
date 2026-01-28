# Pilot: AI That Ships Your Tickets

**Navigator guides. Pilot executes.**

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
Adapters          → Linear (inbound), Slack (outbound)
Orchestrator (Py) → LLM-powered task planning
Executor          → Claude Code process management
Memory            → SQLite + knowledge graph
Dashboard         → Terminal UI (bubbletea)
```

## Project Structure

```
pilot/
├── cmd/pilot/           # CLI entrypoint
├── internal/
│   ├── gateway/         # WebSocket + HTTP server
│   ├── adapters/        # Linear, Slack
│   ├── executor/        # Claude Code runner
│   ├── memory/          # SQLite + knowledge
│   ├── config/          # YAML config
│   └── dashboard/       # TUI
├── orchestrator/        # Python LLM logic
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

This project uses Navigator for context efficiency:

```
"Start my Navigator session"
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

1. Load Navigator: "Start my Navigator session"
2. Read task doc from `.agent/tasks/`
3. Implement with tests
4. Run `make test && make lint`
5. Commit with conventional format
6. Update task doc status

## Current Status

**Week 1-2 Complete**:
- ✅ Gateway (WebSocket + HTTP)
- ✅ Linear adapter
- ✅ Slack adapter
- ✅ Executor foundation
- ✅ Memory system
- ✅ Config system
- ✅ TUI dashboard
- ✅ CLI commands

**Next (Week 3-4)**:
- Wire orchestrator to gateway
- Full Claude Code integration
- End-to-end flow testing

<!-- GitHub integration verified -->
