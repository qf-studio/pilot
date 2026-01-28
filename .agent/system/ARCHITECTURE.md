# Pilot Architecture

**Last Updated:** 2026-01-28 (GH-52 audit)

## System Overview

Pilot is a Go-based autonomous AI development pipeline that:
- Receives tickets from Telegram, GitHub Issues, Linear, or Jira
- Plans and executes implementation using Claude Code
- Creates branches, commits, and PRs
- Sends notifications via Slack/Telegram

```
                     ┌─────────────────────────────────────────┐
                     │              CLI (cmd/pilot)            │
                     │  start | task | telegram | github | ... │
                     └─────────────────┬───────────────────────┘
                                       │
                     ┌─────────────────▼───────────────────────┐
                     │           internal/pilot                │
                     │  Orchestration + Component Coordination │
                     └──────────┬─────────────────┬────────────┘
                                │                 │
         ┌──────────────────────┼─────────────────┼───────────────────────┐
         │                      │                 │                       │
┌────────▼────────┐  ┌──────────▼──────────┐  ┌──▼─────────────┐  ┌──────▼──────┐
│    Adapters     │  │     Executor        │  │    Memory      │  │   Gateway   │
│ telegram/github │  │ Claude Code Runner  │  │ SQLite + Graph │  │ HTTP + WS   │
│ linear/jira/    │  │ Progress Display    │  │ Patterns Store │  │ Webhooks    │
│ slack           │  │ Git Operations      │  └────────────────┘  └─────────────┘
└─────────────────┘  │ Quality Gates       │
                     │ Alerts Integration  │
                     └─────────────────────┘
```

## Data Flow

### Task Execution Flow (Primary)

```
                 User Message                    GitHub Issue
                      │                              │
                      ▼                              ▼
               ┌────────────┐               ┌──────────────┐
               │  Telegram  │               │GitHub Poller │
               │  Handler   │               │(30s interval)│
               └─────┬──────┘               └──────┬───────┘
                     │                              │
                     └──────────────┬───────────────┘
                                    │
                                    ▼
                          ┌─────────────────┐
                          │   Dispatcher    │  ← GH-46 task queue
                          │ (per-project    │    coordinates concurrent
                          │  serialization) │    requests
                          └────────┬────────┘
                                   │
                                   ▼
                          ┌─────────────────┐
                          │    Executor     │
                          │  runner.Execute │
                          └────────┬────────┘
                                   │
                    ┌──────────────┼──────────────┐
                    │              │              │
                    ▼              ▼              ▼
            ┌─────────────┐ ┌─────────────┐ ┌─────────────┐
            │ Git Ops     │ │Claude Code  │ │Progress     │
            │ (checkout,  │ │(subprocess  │ │Display      │
            │ branch, PR) │ │ stream-json)│ │(lipgloss)   │
            └─────────────┘ └─────────────┘ └─────────────┘
```

### Command → Package Mapping

| CLI Command | Primary Package(s) | Description |
|-------------|-------------------|-------------|
| `pilot start` | pilot, gateway, dashboard | Start daemon with optional TUI |
| `pilot task` | executor, alerts, quality | Execute single task |
| `pilot telegram` | adapters/telegram, executor | Telegram bot mode |
| `pilot github run` | adapters/github, executor | Execute GitHub issue |
| `pilot brief` | briefs, memory, adapters/slack | Generate daily reports |
| `pilot patterns` | memory | Query cross-project patterns |
| `pilot replay` | replay | Debug execution recordings |
| `pilot status` | config | Show configuration status |
| `pilot upgrade` | upgrade | Self-update binary |
| `pilot budget` | budget | View cost controls |
| `pilot team` | teams | Manage team permissions |
| `pilot tunnel` | tunnel | Cloudflare tunnel management |
| `pilot webhooks` | webhooks | Outbound webhook config |
| `pilot doctor` | health, config | System health check |

## Package Architecture

### Core Packages (Wired in main.go)

| Package | Purpose | Key Files |
|---------|---------|-----------|
| `pilot` | Top-level orchestration | `pilot.go` |
| `executor` | Claude Code process management | `runner.go`, `git.go`, `progress.go`, `dispatcher.go` |
| `config` | YAML configuration loading | `config.go`, `schema.go` |
| `memory` | SQLite + knowledge graph | `store.go`, `graph.go`, `patterns.go` |
| `logging` | Structured slog logging | `logger.go` |
| `alerts` | Event-based alerting | `engine.go`, `dispatcher.go`, `channels.go` |
| `quality` | Quality gates (test/lint) | `executor.go`, `gates.go` |
| `dashboard` | Bubbletea TUI | `tui.go` |
| `banner` | CLI visual branding | `banner.go` |
| `briefs` | Daily/weekly summaries | `generator.go`, `formatter.go` |
| `replay` | Execution recording viewer | `player.go`, `viewer.go`, `analyzer.go` |
| `upgrade` | Self-update mechanism | `upgrader.go` |

### Adapter Packages

| Package | Purpose | Status |
|---------|---------|--------|
| `adapters/telegram` | Telegram bot + voice | Fully wired |
| `adapters/github` | GitHub Issues + PR ops | Fully wired |
| `adapters/slack` | Notifications | Fully wired |
| `adapters/linear` | Linear webhooks | Implemented, not in main CLI |
| `adapters/jira` | Jira webhooks | Implemented, not in main CLI |

### Supporting Packages

| Package | Purpose | Status |
|---------|---------|--------|
| `gateway` | HTTP + WebSocket server | Implemented, used by pilot.Start() |
| `orchestrator` | Python bridge for LLM | Implemented, used internally |
| `approval` | Human-in-the-loop | Implemented, optional feature |
| `budget` | Cost controls | Implemented, CLI command exists |
| `teams` | Team permissions | Implemented, CLI command exists |
| `tunnel` | Cloudflare tunnel | Implemented, CLI command exists |
| `webhooks` | Outbound webhooks | Implemented, CLI command exists |
| `health` | Health checks | Implemented, used by doctor |
| `testutil` | Test utilities | Test-only, not runtime |
| `transcription` | Voice → text (OpenAI) | Used by telegram adapter |

## Key Integration Points

### Claude Code Integration

```go
// internal/executor/runner.go
cmd := exec.Command("claude",
    "-p", prompt,
    "--verbose",
    "--output-format", "stream-json",
    "--dangerously-skip-permissions",
)
```

Output parsing via `stream-json` events:
- `system` → initialization
- `assistant` → text responses
- `tool_use` → tool calls (Read, Write, Bash, etc.)
- `tool_result` → tool outputs
- `result` → final outcome

### Navigator Integration

```go
// internal/executor/runner.go:BuildPrompt()
if _, err := os.Stat(filepath.Join(task.ProjectPath, ".agent")); err == nil {
    sb.WriteString("Start my Navigator session.\n\n")  // CRITICAL
}
```

**DO NOT REMOVE** - This is Pilot's core value proposition.

### Progress Detection

```go
// internal/executor/runner.go - phase detection
switch {
case strings.Contains(text, "Navigator Session Started"):
    phase = "navigator"
case strings.Contains(text, "TASK MODE ACTIVATED"):
    phase = "task-mode"
case strings.Contains(text, "PHASE: → RESEARCH"):
    phase = "research"
case strings.Contains(text, "PHASE: → IMPL"):
    phase = "implementing"
// ...
}
```

### Alerts Bridge

```go
// internal/executor/alerts.go
type AlertEventProcessor interface {
    ProcessEvent(event alerts.Event)
}

// Used in runner.Execute() to emit:
// - EventTypeTaskStarted
// - EventTypeTaskProgress
// - EventTypeTaskCompleted
// - EventTypeTaskFailed
```

## Configuration Structure

```yaml
# ~/.pilot/config.yaml
gateway:
  host: "127.0.0.1"
  port: 9090

adapters:
  telegram:
    enabled: true
    bot_token: "..."
  github:
    enabled: true
    repo: "owner/repo"
    polling:
      enabled: true
      interval: 30s
      label: "pilot"
  slack:
    enabled: true
    bot_token: "..."

projects:
  - name: "my-project"
    path: "/path/to/project"
    navigator: true

memory:
  path: "~/.pilot/memory.db"

alerts:
  enabled: true
  channels: [...]
  rules: [...]

quality:
  enabled: true
  gates: [...]
```

## Database Schema (SQLite)

```sql
-- Task executions
CREATE TABLE executions (
    id TEXT PRIMARY KEY,
    task_id TEXT,
    project_path TEXT,
    status TEXT,
    started_at DATETIME,
    completed_at DATETIME,
    duration_ms INTEGER,
    output TEXT,
    error TEXT,
    commit_sha TEXT,
    pr_url TEXT
);

-- Cross-project patterns
CREATE TABLE cross_patterns (
    id TEXT PRIMARY KEY,
    title TEXT,
    description TEXT,
    type TEXT,
    scope TEXT,
    confidence REAL,
    occurrences INTEGER,
    is_anti_pattern BOOLEAN
);

-- Task queue (GH-46)
CREATE TABLE task_queue (
    id TEXT PRIMARY KEY,
    project_path TEXT,
    task_json TEXT,
    status TEXT,
    created_at DATETIME,
    started_at DATETIME,
    completed_at DATETIME
);
```

## Test Coverage

| Package | Test Files | Status |
|---------|-----------|--------|
| adapters/github | 5 | ✅ Pass |
| adapters/slack | 2 | ✅ Pass |
| adapters/telegram | 7 | ✅ Pass |
| adapters/jira | 3 | ✅ Pass |
| adapters/linear | 3 | ✅ Pass |
| alerts | 4 | ✅ Pass |
| approval | 2 | ✅ Pass |
| briefs | 4 | ✅ Pass |
| budget | 2 | ✅ Pass |
| config | 1 | ✅ Pass |
| executor | 15 | ✅ Pass |
| gateway | 4 | ✅ Pass |
| logging | 2 | ✅ Pass |
| memory | 8 | ✅ Pass |
| orchestrator | 1 | ✅ Pass |
| quality | 3 | ✅ Pass |
| replay | 4 | ✅ Pass |
| teams | 1 | ✅ Pass |
| tunnel | 6 | ✅ Pass |
| upgrade | 1 | ✅ Pass |
| webhooks | 1 | ✅ Pass |

**Packages without tests:** banner, dashboard, health, pilot, testutil, transcription

## Build & Deploy

```bash
# Build
make build    # → ./bin/pilot

# Test
make test     # go test ./...

# Lint
make lint     # golangci-lint

# Development
make dev      # Build + run with hot reload
```

Binary versioning: `v0.2.0-{commits}-g{sha}`

## Security Considerations

1. **Tokens in tests**: Use `internal/testutil/tokens.go` for fake tokens
2. **API keys**: Environment variables or config file
3. **Sandbox mode**: Claude Code runs with `--dangerously-skip-permissions` (trusted context)
4. **Webhook secrets**: HMAC validation for incoming webhooks

---

## Appendix: Full Package Audit (GH-52)

**Audit Date:** 2026-01-28

| Package | Exists | Imported | Wired in main.go | Has Tests | Tests Pass |
|---------|--------|----------|------------------|-----------|------------|
| adapters/github | ✅ | ✅ | ✅ | ✅ | ✅ |
| adapters/jira | ✅ | ✅ | ❌ | ✅ | ✅ |
| adapters/linear | ✅ | ✅ | ❌ | ✅ | ✅ |
| adapters/slack | ✅ | ✅ | ✅ | ✅ | ✅ |
| adapters/telegram | ✅ | ✅ | ✅ | ✅ | ✅ |
| alerts | ✅ | ✅ | ✅ | ✅ | ✅ |
| approval | ✅ | ✅ | ❌ | ✅ | ✅ |
| banner | ✅ | ✅ | ✅ | ❌ | N/A |
| briefs | ✅ | ✅ | ✅ | ✅ | ✅ |
| budget | ✅ | ✅ | ❌ | ✅ | ✅ |
| config | ✅ | ✅ | ✅ | ✅ | ✅ |
| dashboard | ✅ | ✅ | ✅ | ❌ | N/A |
| executor | ✅ | ✅ | ✅ | ✅ | ✅ |
| gateway | ✅ | ✅ | ❌ | ✅ | ✅ |
| health | ✅ | ✅ | ❌ | ❌ | N/A |
| logging | ✅ | ✅ | ✅ | ✅ | ✅ |
| memory | ✅ | ✅ | ✅ | ✅ | ✅ |
| orchestrator | ✅ | ✅ | ❌ | ✅ | ✅ |
| pilot | ✅ | ✅ | ✅ | ❌ | N/A |
| quality | ✅ | ✅ | ✅ | ✅ | ✅ |
| replay | ✅ | ✅ | ✅ | ✅ | ✅ |
| teams | ✅ | ✅ | ❌ | ✅ | ✅ |
| testutil | ✅ | ✅ | ❌ | ❌ | N/A |
| transcription | ✅ | ✅ | ❌ | ❌ | N/A |
| tunnel | ✅ | ✅ | ❌ | ✅ | ✅ |
| upgrade | ✅ | ✅ | ✅ | ✅ | ✅ |
| webhooks | ✅ | ✅ | ❌ | ✅ | ✅ |

**Summary:**
- 27 packages total (24 + 5 adapter subpackages - 2 counted above)
- 100% exist and are imported somewhere
- 52% wired directly in main.go
- 79% have test files
- 100% of tested packages pass
