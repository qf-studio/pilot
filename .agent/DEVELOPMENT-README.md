# Pilot Development Navigator

**AI that ships your tickets.**

## ⚠️ WORKFLOW: Plan Here, Pilot Executes

**This Claude Code session is for PLANNING ONLY.**

| Do ✅ | Don't ❌ |
|-------|---------|
| Research & explore codebase | Write code |
| Design solutions & plans | Make commits |
| Create GitHub issues (`--label pilot`) | Create PRs |
| Review Pilot's work | Execute tasks directly |

### How It Works
```
┌─────────────────┐     gh issue create      ┌─────────────────┐
│  Claude Code    │ ──────────────────────► │  GitHub Issue   │
│  (Plan & Design)│     --label pilot        │  (with pilot)   │
└─────────────────┘                          └────────┬────────┘
                                                      │
                                                      ▼
┌─────────────────┐     auto-picks up        ┌─────────────────┐
│  Review PR      │ ◄────────────────────── │  Pilot Bot      │
│  Give feedback  │                          │  (executes)     │
└─────────────────┘                          └─────────────────┘
```

### Quick Commands
```bash
# Create ticket for Pilot
gh issue create --title "TASK-XX: Description" --label pilot --body "Details..."

# Check Pilot's queue
gh issue list --label pilot --state open

# Check what Pilot completed
gh issue list --label pilot-done --state open
```

---

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
| `.agent/system/FEATURE-MATRIX.md` | **What's implemented vs not** |
| `.agent/system/ARCHITECTURE.md` | System design, data flow |
| `.agent/tasks/TASK-XX.md` | Active task details |
| `.agent/sops/*.md` | Before modifying integrations |
| `.agent/.context-markers/` | Resume after break |

## Current State

**Full implementation status:** `.agent/system/FEATURE-MATRIX.md`

### Key Components

| Component | Status | Notes |
|-----------|--------|-------|
| Task Execution | ✅ | Claude Code subprocess with Navigator |
| Telegram Bot | ✅ | Long-polling, voice, images |
| GitHub Polling | ✅ | 30s interval, auto-picks `pilot` label |
| Alerts Engine | ✅ | Slack, Telegram, webhooks |
| Quality Gates | ✅ | Test/lint/build gates with retry |
| Task Dispatcher | ✅ | Per-project queue (GH-46) |
| Dashboard TUI | ✅ | Token usage, cost, history |
| Hot Upgrade | ✅ | Self-update via `pilot upgrade` |

### Needs Verification

| Component | Issue |
|-----------|-------|
| Linear Webhooks | Needs gateway running |
| Jira Webhooks | Needs gateway running |
| Email Alerts | Implemented, untested |
| PagerDuty | Implemented, untested |

## Active Work

**Source of truth: GitHub Issues with `pilot` label**

```bash
# See current queue
gh issue list --label pilot --state open

# See what's in progress
gh issue list --label pilot-in-progress --state open
```

### In Progress

| GH# | Title | Status |
|-----|-------|--------|
| 54 | Speed Optimization (complexity detection, model routing, timeout) | 🔄 Pilot executing |

### Backlog (Create issues as needed)

| Priority | Topic | Why |
|----------|-------|-----|
| 🔴 P1 | Execution timeout | Prevent stuck tasks (GH-17 ran 92+ min) |
| 🔴 P1 | Verify quality gates wiring | May not be fully wired |
| 🟡 P2 | Cost controls | Budget protection |
| 🟡 P2 | Approval workflows verification | Team safety |
| 🟢 P3 | Telegram commands | Power user UX |

**For accurate feature status, see:** `.agent/system/FEATURE-MATRIX.md`

---

## Completed (2026-01-28)

| Item | What |
|------|------|
| GH-52 | Full codebase audit → FEATURE-MATRIX.md, ARCHITECTURE.md |
| GH-51 | Cleanup → 40+ TASK files archived |
| GH-46 | Task queue with per-project coordination |
| Workflow | Plan-only mode established |

Full archive: `.agent/tasks/archive/`

## Project Structure

```
pilot/
├── cmd/pilot/           # CLI entrypoint
├── internal/
│   ├── gateway/         # WebSocket + HTTP server
│   ├── adapters/        # Linear, Slack, Telegram, GitHub, Jira
│   ├── executor/        # Claude Code process management + alerts bridge
│   ├── alerts/          # Alert engine + dispatcher + channels
│   ├── memory/          # SQLite + knowledge graph
│   ├── config/          # Configuration loading
│   ├── dashboard/       # Terminal UI (bubbletea)
│   └── testutil/        # Safe test token constants
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
- `internal/executor/runner.go` - Claude Code process spawner with stream-json parsing + slog logging
- `internal/executor/alerts.go` - AlertEventProcessor interface (avoids import cycles)
- `internal/executor/progress.go` - Visual progress bar display (lipgloss)
- `internal/executor/monitor.go` - Task state tracking

### Alerts
- `internal/alerts/engine.go` - Event processing, rule evaluation, cooldowns
- `internal/alerts/dispatcher.go` - Multi-channel alert dispatch
- `internal/alerts/channels.go` - Slack, Telegram, Email, Webhook, PagerDuty
- `internal/alerts/adapter.go` - EngineAdapter bridges executor → alerts engine

### Dashboard
- `internal/dashboard/tui.go` - Bubbletea TUI with token usage, cost, task history

### Memory
- `internal/memory/store.go` - SQLite storage
- `internal/memory/graph.go` - Knowledge graph
- `internal/memory/patterns.go` - Global pattern store

### Testing
- `internal/testutil/tokens.go` - Safe fake tokens for all test files

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
- Alerts: Task lifecycle events emitted via `AlertEventProcessor` interface

### Slack
- Notifications: Task started, progress, completed, failed
- Handler: `internal/adapters/slack/notifier.go`

## CLI Flags

### `pilot start`
- `--dashboard` - Launch TUI dashboard with live task monitoring
- `--daemon` - Run in background

### `pilot task`
- `--verbose` - Stream raw Claude Code JSON output
- `--create-pr` - Create GitHub PR after execution
- `--alerts` - Enable alert engine for this task
- `--dry-run` - Show prompt without executing
- `--no-branch` - Run on current branch

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

### Execution Report (GH-49)

After task completion, `pilot task` displays a structured execution report:

```
───────────────────────────────────────
📊 EXECUTION REPORT
───────────────────────────────────────
Task:       GH-47
Status:     ✅ Success
Duration:   3m 42s
Branch:     pilot/GH-47
Commit:     a1b2c3d
PR:         #48

🧭 Navigator: Active
   Mode:    nav-task

📈 Phases:
  Research     45s   (20%)
  Implement    2m    (54%)
  Verify       57s   (26%)

📁 Files Changed:
  M runner.go
  A quality.go
  M TASK-20.md

💰 Tokens:
  Input:    45k
  Output:   12k
  Cost:     ~$0.82
  Model:    claude-sonnet-4-5
───────────────────────────────────────
```

Navigator detection shown at start:
- `🧭 Navigator: ✓ detected (.agent/ exists)` if Navigator initialized
- `⚠️ Navigator: not found (running raw Claude Code)` otherwise

## Documentation Loading Strategy

1. **Every session**: This file (2k tokens)
2. **Feature work**: Task doc (3k tokens)
3. **Architecture changes**: System doc (5k tokens)
4. **Integration work**: Relevant adapter code

Total: ~12k tokens vs 50k+ loading everything.
