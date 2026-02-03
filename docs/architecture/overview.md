# Architecture Overview

Pilot is a Go-based autonomous AI development pipeline.

## System Overview

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

### Task Execution

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
                          │   Dispatcher    │  ← Per-project queue
                          │ (serialization) │    coordinates tasks
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

## Core Packages

| Package | Purpose |
|---------|---------|
| `pilot` | Top-level orchestration |
| `executor` | Claude Code process management |
| `config` | YAML configuration loading |
| `memory` | SQLite + knowledge graph |
| `alerts` | Event-based alerting |
| `quality` | Quality gates (test/lint) |
| `dashboard` | Bubbletea TUI |
| `briefs` | Daily/weekly summaries |
| `replay` | Execution recording viewer |
| `upgrade` | Self-update mechanism |

## Adapters

| Package | Purpose | Status |
|---------|---------|--------|
| `adapters/telegram` | Telegram bot + voice | ✅ Fully wired |
| `adapters/github` | GitHub Issues + PR ops | ✅ Fully wired |
| `adapters/slack` | Notifications | ✅ Fully wired |
| `adapters/linear` | Linear webhooks | ⚠️ Implemented |
| `adapters/jira` | Jira webhooks | ⚠️ Implemented |

## Claude Code Integration

Pilot spawns Claude Code as a subprocess:

```go
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

## Navigator Integration

When a project has `.agent/` directory, Pilot auto-activates Navigator:

```go
if _, err := os.Stat(filepath.Join(task.ProjectPath, ".agent")); err == nil {
    sb.WriteString("Start my Navigator session.\n\n")
}
```

This provides structured planning and execution phases.

## Database Schema

SQLite database at `~/.pilot/memory.db`:

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

-- Task queue
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

## Security

- **Tokens**: Use environment variables or config file
- **Test secrets**: Use `internal/testutil/tokens.go` for fake tokens
- **Sandbox**: Claude Code runs with `--dangerously-skip-permissions` (trusted context)
- **Webhooks**: HMAC validation for incoming webhooks
