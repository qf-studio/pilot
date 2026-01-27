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
| `.agent/product/AUDIENCE.md` | GTM strategy, personas, messaging |
| `.agent/product/PRICING.md` | Tier structure, competitor comparison |
| `.agent/product/ONBOARDING.md` | First 5-ticket experience, setup wizard |
| `.agent/product/COMPETITIVE.md` | ClawdBot, Cursor, Copilot, Devin teardown |

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
| **Structured Logging** | ✅ Complete | `internal/executor/runner.go` |

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

## Prioritized Backlog

**Pick tasks in order. Higher = more user value.**

### 🔴 P1: Critical (Blocking User Success)

| # | Task | File | Why |
|---|------|------|-----|
| 1 | **TASK-30**: Setup Wizard | `TASK-30-setup-wizard.md` | Users can't use features without knowing what's missing |
| 2 | **TASK-20**: Quality Gates | `TASK-20-quality-gates.md` | Broken PRs destroy trust |
| 3 | **TASK-19**: Approval Workflows | `TASK-19-approval-workflows.md` | Teams won't adopt without safety controls |

### 🟡 P2: High (Significant Value)

| # | Task | File | Why |
|---|------|------|-----|
| 4 | **TASK-29**: Multi-Project Support | `TASK-29-multi-project-support.md` | Most devs work on multiple projects |
| 5 | **TASK-28**: Speed Optimization | `TASK-28-speed-optimization.md` | Slow = abandoned |
| 6 | **TASK-26**: Hot Version Upgrade | `TASK-26-hot-version-upgrade.md` | Friction-free updates |

### 🟢 P3: Medium (Enterprise/Polish)

| # | Task | File | Why |
|---|------|------|-----|
| 7 | **TASK-17**: Team Management | `TASK-17-team-management.md` | Enterprise sales blocker |
| 8 | **TASK-18**: Cost Controls | `TASK-18-cost-controls.md` | Budget protection |
| 9 | **TASK-25**: Telegram Commands | `TASK-25-telegram-commands.md` | Power user UX |

### ⚪ P4: Low (Internal/Nice-to-Have)

| # | Task | File | Why |
|---|------|------|-----|
| 10 | **TASK-32**: Nav Index Sync | `TASK-32-nav-index-sync.md` | Internal workflow |
| 11 | **TASK-21**: Execution Replay | `TASK-21-execution-replay.md` | Debug aid |
| 12 | **TASK-22**: Webhooks API | `TASK-22-webhooks-api.md` | Integration feature |
| 13 | **TASK-24**: Tech Debt | `TASK-24-tech-debt-cleanup.md` | Internal cleanup |
| 14 | **TASK-27**: CLI Enhancements | `TASK-27-pilot-cli.md` | Polish |

---

## Completed Tasks

- **TASK-33**: Branch Workflow Fix ✅ 2026-01-27
- **TASK-31**: PR Workflow Improvements ✅ 2026-01-27
- **TASK-23**: GitHub App Integration (Phase 1) ✅ 2026-01-27
- **TASK-16**: Usage Metering & Billing ✅ 2026-01-27
- **TASK-15**: Structured Logging ✅ 2026-01-26
- **TASK-14**: Alerting System ✅ 2026-01-26
- **TASK-13**: Execution Metrics ✅ 2026-01-26
- **TASK-12**: Pilot Cloud (Foundation) ✅ 2026-01-26
- **TASK-11**: Cross-Project Memory ✅ 2026-01-26
- **TASK-10**: Daily Briefs ✅ 2026-01-26
- **TASK-09**: Jira Adapter ✅ 2026-01-26
- **TASK-08**: GitHub Issues Adapter ✅ 2026-01-26
- **TASK-07**: Telegram Voice Support ✅ 2026-01-26
- **TASK-06**: Telegram Image Support ✅ 2026-01-26
- **TASK-05**: Bot Singleton Detection ✅ 2026-01-26
  - File: `.agent/tasks/TASK-05-bot-singleton.md`
  - Completed: 2026-01-26
  - `--replace` flag, `CheckSingleton()`, clear error messages

### Completed

- **TASK-33**: Branch Workflow Fix ✅ 2026-01-27
- **TASK-31**: PR Workflow Improvements ✅ 2026-01-27
- **TASK-23**: GitHub App Integration (Phase 1) ✅ 2026-01-27
- **TASK-16**: Usage Metering & Billing ✅ 2026-01-27
- **TASK-15**: Structured Logging ✅ 2026-01-26
- **TASK-14**: Alerting System ✅ 2026-01-26
- **TASK-13**: Execution Metrics ✅ 2026-01-26
- **TASK-12**: Pilot Cloud (Foundation) ✅ 2026-01-26
- **TASK-11**: Cross-Project Memory ✅ 2026-01-26
- **TASK-10**: Daily Briefs ✅ 2026-01-26
- **TASK-09**: Jira Adapter ✅ 2026-01-26
- **TASK-08**: GitHub Issues Adapter ✅ 2026-01-26
- **TASK-07**: Telegram Voice Support ✅ 2026-01-26
- **TASK-06**: Telegram Image Support ✅ 2026-01-26
- **TASK-05**: Bot Singleton Detection ✅ 2026-01-26
- **TASK-04**: Telegram UX Improvements ✅ 2026-01-26
- **TASK-03**: Git & PR Workflow ✅ 2026-01-26

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
- `internal/executor/runner.go` - Claude Code process spawner with stream-json parsing + slog logging
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
