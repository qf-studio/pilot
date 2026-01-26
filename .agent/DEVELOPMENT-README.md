# Pilot Development Navigator

**AI that ships your tickets.**

## ⚠️ CRITICAL: Core Architecture Constraint

**NEVER remove Navigator integration from `internal/executor/runner.go`**

The `BuildPrompt()` function MUST include `"Start my Navigator session"` prefix when `.agent/` exists. This is Pilot's core value proposition:

```go
// Check if project has Navigator initialized
agentDir := filepath.Join(task.ProjectPath, ".agent")
if _, err := os.Stat(agentDir); err == nil {
    sb.WriteString("Start my Navigator session.\n\n")  // ← NEVER REMOVE
}
```

**Incident 2026-01-26**: This was accidentally removed during "simplification" refactor. Pilot without Navigator = just another Claude Code wrapper with zero value.

---

## Quick Navigation

| Document | When to Read |
|----------|--------------|
| CLAUDE.md | Every session (auto-loaded) |
| This file | Every session (navigator index) |
| `.agent/tasks/TASK-XX.md` | When working on specific task |
| `.agent/system/ARCHITECTURE.md` | When modifying core components |
| `.agent/sops/*.md` | Before modifying integrations (Telegram, Linear, etc.) |

## Current State

### Implementation Status

| Component | Status | Location |
|-----------|--------|----------|
| Gateway (WebSocket + HTTP) | ✅ Complete | `internal/gateway/` |
| Linear Adapter | ✅ Complete | `internal/adapters/linear/` |
| Slack Adapter | ✅ Complete | `internal/adapters/slack/` |
| Executor (Claude Code) | ✅ Complete | `internal/executor/` |
| Memory (SQLite) | ✅ Complete | `internal/memory/` |
| Config System | ✅ Complete | `internal/config/` |
| TUI Dashboard | ✅ Complete | `internal/dashboard/` |
| Orchestrator (Python) | ✅ Complete | `orchestrator/` |
| CLI Commands | ✅ Complete | `cmd/pilot/` |
| **Progress Display** | ✅ Complete | `internal/executor/progress.go` |

### Week 1-2 Progress ✅

- [x] Go project setup
- [x] Gateway skeleton (WebSocket + HTTP)
- [x] Config system (YAML parsing)
- [x] Linear adapter (webhook receiver)
- [x] Basic CLI (`pilot start`, `pilot status`)

### Week 3-4 Progress ✅

- [x] Wire orchestrator to gateway
- [x] Ticket → Navigator task conversion
- [x] Python bridge for LLM planning
- [x] Go ↔ Python IPC (subprocess)
- [x] Pilot core integration
- [x] Tests (24 passing)

### Week 5-6 Progress ✅

- [x] Real-time progress via `--output-format stream-json`
- [x] Phase-based progress (Exploring → Implementing → Testing → Committing)
- [x] Visual progress bar with lipgloss styling
- [x] Autonomous execution with `--dangerously-skip-permissions`
- [x] **Navigator deep integration** - parse Navigator phases, status blocks, exit signals
- [x] Navigator skill detection (nav-start, nav-loop, nav-task, etc.)
- [x] File-based progress (.agent/ writes → Checkpoint/Documenting phases)
- [ ] End-to-end testing with real Linear webhook
- [x] **TASK-03**: Git & PR workflow (branch, commit SHA, PR creation)

## Active Tasks

### Roadmap Features (from README)

- **TASK-12**: Pilot Cloud (Hosted) (Status: 📋 Planned)
  - File: `.agent/tasks/TASK-12-pilot-cloud.md`
  - Created: 2026-01-26
  - SaaS version with managed infrastructure, OAuth, usage-based billing

- **TASK-11**: Cross-Project Memory (Status: 📋 Planned)
  - File: `.agent/tasks/TASK-11-cross-project-memory.md`
  - Created: 2026-01-26
  - Share patterns and learnings across projects

- **TASK-10**: Daily Briefs (Status: 📋 Planned)
  - File: `.agent/tasks/TASK-10-daily-briefs.md`
  - Created: 2026-01-26
  - Automated summary of completed work, progress, blockers

- **TASK-09**: Jira Adapter (Status: 📋 Planned)
  - File: `.agent/tasks/TASK-09-jira-adapter.md`
  - Created: 2026-01-26
  - Jira Cloud/Server integration for enterprise teams

- **TASK-08**: GitHub Issues Adapter (Status: ✅ Complete)
  - File: `.agent/tasks/TASK-08-github-issues-adapter.md`
  - Completed: 2026-01-26
  - GitHub Issues as ticket source for open-source projects

### Monitoring & Observability

- **TASK-13**: Execution Metrics & Analytics (Status: 📋 Planned)
  - File: `.agent/tasks/TASK-13-execution-metrics.md`
  - Track success rates, token usage, costs, execution times

- **TASK-14**: Alerting System (Status: 📋 Planned)
  - File: `.agent/tasks/TASK-14-alerting-system.md`
  - Proactive notifications for stuck tasks, failures, budget exceeded

- **TASK-15**: Structured Logging (Status: 📋 Planned)
  - File: `.agent/tasks/TASK-15-structured-logging.md`
  - JSON logs, levels, rotation, log management integration

### Monetization & Enterprise

- **TASK-16**: Usage Metering & Billing (Status: 📋 Planned)
  - File: `.agent/tasks/TASK-16-usage-metering.md`
  - Foundation for usage-based billing (tasks, tokens, compute)

- **TASK-17**: Team Management (Status: 📋 Planned)
  - File: `.agent/tasks/TASK-17-team-management.md`
  - Multi-user support, roles, permissions, audit log

- **TASK-18**: Cost Controls & Budgets (Status: 📋 Planned)
  - File: `.agent/tasks/TASK-18-cost-controls.md`
  - Spending limits, budget alerts, runaway task protection

### Safety & Quality

- **TASK-19**: Approval Workflows (Status: 📋 Planned)
  - File: `.agent/tasks/TASK-19-approval-workflows.md`
  - Human approval at key stages (pre-execution, pre-merge)

- **TASK-20**: Quality Gates (Status: 📋 Planned)
  - File: `.agent/tasks/TASK-20-quality-gates.md`
  - Enforce tests, lint, coverage before PR creation

### Developer Experience

- **TASK-21**: Execution Replay & Debugging (Status: 📋 Planned)
  - File: `.agent/tasks/TASK-21-execution-replay.md`
  - Record and replay executions for debugging

- **TASK-22**: Webhooks API (Status: 📋 Planned)
  - File: `.agent/tasks/TASK-22-webhooks-api.md`
  - Outbound webhooks for custom integrations

- **TASK-23**: GitHub App Integration (Status: 📋 Planned)
  - File: `.agent/tasks/TASK-23-github-app.md`
  - PR comments, status checks, deep GitHub integration

### Telegram Features

- **TASK-07**: Telegram Voice Support (Status: 📋 Planned)
  - File: `.agent/tasks/TASK-07-telegram-voice-support.md`
  - Created: 2026-01-26
  - Voice transcription via SenseVoice (15x faster than Whisper)

- **TASK-06**: Telegram Image Support (Status: ✅ Complete)
  - File: `.agent/tasks/TASK-06-telegram-image-support.md`
  - Completed: 2026-01-26
  - Enable image analysis via Telegram bot

- **TASK-05**: Bot Singleton Detection (Status: 📋 Planned)
  - File: `.agent/tasks/TASK-05-bot-singleton.md`
  - Graceful handling when another bot instance is running

### Completed

- **TASK-04**: Telegram UX Improvements (Status: ✅ Complete)
  - File: `.agent/tasks/TASK-04-telegram-ux.md`
  - Completed: 2026-01-26

- **TASK-03**: Git & PR Workflow (Status: ✅ Complete)
  - File: `.agent/tasks/TASK-03-git-pr-workflow.md`
  - Completed: 2026-01-26

## Project Structure

```
pilot/
├── cmd/pilot/           # CLI entrypoint
├── internal/
│   ├── gateway/         # WebSocket + HTTP server
│   ├── adapters/        # Linear, Slack integrations
│   ├── executor/        # Claude Code process management
│   ├── memory/          # SQLite + knowledge graph
│   ├── config/          # Configuration loading
│   └── dashboard/       # Terminal UI
├── orchestrator/        # Python LLM logic
├── configs/             # Example configs
└── .agent/              # Navigator docs
```

## Key Files

### Gateway
- `internal/gateway/server.go` - Main server with WebSocket + HTTP
- `internal/gateway/router.go` - Message and webhook routing
- `internal/gateway/sessions.go` - WebSocket session management
- `internal/gateway/auth.go` - Authentication handling

### Adapters
- `internal/adapters/linear/client.go` - Linear GraphQL client
- `internal/adapters/linear/webhook.go` - Webhook handler
- `internal/adapters/slack/notifier.go` - Slack notifications

### Executor
- `internal/executor/runner.go` - Claude Code process spawner with stream-json parsing
- `internal/executor/progress.go` - Visual progress bar display (lipgloss)
- `internal/executor/monitor.go` - Task state tracking
- `internal/executor/git.go` - Git operations (planned)

### Memory
- `internal/memory/store.go` - SQLite storage
- `internal/memory/graph.go` - Knowledge graph
- `internal/memory/patterns.go` - Global pattern store

## Development Commands

```bash
# Build
make build

# Run in development
make dev

# Run tests
make test

# Format code
make fmt
```

## Configuration

Copy `configs/pilot.example.yaml` to `~/.pilot/config.yaml`.

Required environment variables:
- `LINEAR_API_KEY` - Linear API key
- `SLACK_BOT_TOKEN` - Slack bot token

## Integration Points

### Linear Webhook
- Endpoint: `POST /webhooks/linear`
- Triggers on: Issue create with "pilot" label
- Handler: `internal/adapters/linear/webhook.go`

### Claude Code
- Spawned by: `internal/executor/runner.go`
- Command: `claude -p "prompt" --verbose --output-format stream-json --dangerously-skip-permissions`
- Working dir: Project path from config
- Progress: Phase-based updates parsed from stream-json events
- Phases: Starting → Exploring → Implementing → Testing → Committing → Completed

### Slack
- Notifications: Task started, progress, completed, failed
- Handler: `internal/adapters/slack/notifier.go`

## Progress Display

`pilot task` shows real-time visual progress:

```
⏳ Executing task with Claude Code...

   Implementing   [████████████░░░░░░░░] 60%  TASK-34473  45s

   [14:35:15] Claude Code initialized
   [14:35:18] Analyzing codebase...
   [14:35:25] Creating App.tsx
   [14:35:40] Installing dependencies...
   [14:35:55] Committing changes...

───────────────────────────────────────
✅ Task completed successfully!
   Duration: 52s
```

### Phases (Standard)
| Phase | Triggers | Progress |
|-------|----------|----------|
| Starting | Init | 0-5% |
| Branching | git checkout/branch | 10% |
| Exploring | Read/Glob/Grep | 15% |
| Installing | npm/pip install | 30% |
| Implementing | Write/Edit | 40-70% |
| Testing | pytest/jest/go test | 75% |
| Committing | git commit | 90% |
| Completed | result event | 100% |

### Navigator Phases (Auto-detected)
| Phase | Detection | Progress |
|-------|-----------|----------|
| Navigator | `Navigator Session Started` | 10% |
| Analyzing | `WORKFLOW CHECK` | 12% |
| Task Mode | `TASK MODE ACTIVATED` | 15% |
| Loop Mode | `nav-loop` skill | 20% |
| Research | `PHASE: → RESEARCH` | 25% |
| Implement | `PHASE: → IMPL` | 50% |
| Verify | `PHASE: → VERIFY` | 80% |
| Checkpoint | `.agent/.context-markers/` write | 88% |
| Completing | `EXIT_SIGNAL: true` | 92% |
| Complete | `LOOP COMPLETE` / `TASK MODE COMPLETE` | 95% |

Navigator status blocks provide real progress via `Progress: N%` field.

## Documentation Loading Strategy

1. **Every session**: This file (2k tokens)
2. **Feature work**: Task doc (3k tokens)
3. **Architecture changes**: System doc (5k tokens)
4. **Integration work**: Relevant adapter code

Total: ~12k tokens vs 50k+ loading everything.
