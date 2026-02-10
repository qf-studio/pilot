# Pilot Development Navigator

**Navigator plans. Pilot executes.**

## WORKFLOW: Navigator + Pilot Pipeline

**This session uses Navigator for planning, Pilot for execution.**

### The Pipeline

```
┌─────────────────┐                          ┌─────────────────┐
│   /nav-task     │  ───── plan ──────────►  │  GitHub Issue   │
│   (Navigator)   │       --label pilot      │  (with pilot)   │
└─────────────────┘                          └────────┬────────┘
        ▲                                             │
        │                                             ▼
        │ iterate                            ┌─────────────────┐
        │ if needed                          │   Pilot Bot     │
        │                                    │   (executes)    │
┌───────┴─────────┐                          └────────┬────────┘
│   Review PR     │  ◄──── creates PR ───────────────┘
│   Merge/Request │
└─────────────────┘
```

### Workflow Steps

| Step | Command | Action |
|------|---------|--------|
| 1. Plan | `/nav-task "feature description"` | Design solution, create implementation plan |
| 2. Execute | `gh issue create --label pilot` | Hand off to Pilot for execution |
| 3. Review | `gh pr view <n>` | Check Pilot's PR |
| 4. Ship | `gh pr merge <n>` | Merge when approved |

### Quick Commands

```bash
# Plan a feature (Navigator does the thinking)
/nav-task "Add rate limiting to API endpoints"

# Hand off to Pilot (creates issue from plan)
gh issue create --title "Add rate limiting" --label pilot --body "..."

# Check Pilot's queue
gh issue list --label pilot --state open

# Review PR
gh pr view <number>

# Merge when ready
gh pr merge <number>
```

### Rules

| Do | Don't |
|----|-------|
| Use `/nav-task` for planning | Write code directly |
| Create issues with `pilot` label | Make commits manually |
| Review every PR before merging | Create PRs manually |
| Request changes on PR if needed | Approve without review |

---

## CRITICAL: Core Architecture Constraint

**NEVER remove Navigator integration from `internal/executor/runner.go`**

The `BuildPrompt()` function MUST include `"Start my Navigator session"` prefix when `.agent/` exists. This is Pilot's core value proposition:

```go
// Check if project has Navigator initialized
agentDir := filepath.Join(task.ProjectPath, ".agent")
if _, err := os.Stat(agentDir); err == nil {
    sb.WriteString("Start my Navigator session.\n\n")  // <- NEVER REMOVE
}
```

**Incident 2026-01-26**: This was accidentally removed during "simplification" refactor. Pilot without Navigator = just another Claude Code wrapper with zero value.

---

## Quick Navigation

| Document | When to Read |
|----------|--------------|
| CLAUDE.md | Every session (auto-loaded) |
| This file | Every session (navigator index) |
| `.agent/system/FEATURE-MATRIX.md` | What's implemented vs not |
| `.agent/system/ARCHITECTURE.md` | System design, data flow |
| `.agent/system/PR-CHECKLIST.md` | Before merging any PR |
| `.agent/tasks/TASK-XX.md` | Active task details |
| `.agent/sops/*.md` | Before modifying integrations |
| `.agent/.context-markers/` | Resume after break |

## Current State

**Current Version:** v0.30.1 | **107 features working** | **0 unwired**

**Full implementation status:** `.agent/system/FEATURE-MATRIX.md`

### Key Components

| Component | Status | Notes |
|-----------|--------|-------|
| Task Execution | Done | Claude Code subprocess with Navigator |
| Telegram Bot | Done | Long-polling, voice, images, chat modes |
| GitHub Polling | Done | 30s interval, auto-picks `pilot` label, parallel execution (v0.26.1+) |
| Alerts Engine | Done | Slack, Telegram, Email, Webhook, PagerDuty (all wired v0.26.1) |
| Slack Notifications | Done | Task lifecycle + alerts to #engineering (v0.26.1) |
| Slack Socket Mode | Done | OpenConnection, Listen with auto-reconnect, event parsing (v0.29.0) |
| Quality Gates | Done | Test/lint/build gates with retry |
| Task Dispatcher | Done | Per-project queue |
| Dashboard TUI | Done | Sparkline cards, muted palette, SQLite persistence, epic-aware HISTORY |
| Hot Upgrade | Done | Self-update via `pilot upgrade` or dashboard 'u' key |
| Autopilot | Done | CI monitor, auto-merge, feedback loop, tag-only release, SQLite state (v0.30.0) |
| Conflict Detection | Done | Detect merge conflicts before CI wait (v0.30.0) |
| LLM Complexity | Done | Haiku-based task complexity classifier (v0.30.0) |
| LLM Intent Judge | Done | Intent classification in execution pipeline (v0.24.0) |
| Rich PR Comments | Done | Execution metrics (duration, tokens, cost) in PR comments |
| Self-Review | Done | Auto code review before PR |
| Effort Routing | Done | Map task complexity to reasoning depth (v0.20.0) |
| Release Pipeline | Done | Tag-only, GoReleaser CI builds + uploads binaries |
| Docs Site | Done | Nextra v2 (pinned), GitLab sync, auto-deploy via prod tag |
| Email Alerts | Done | SMTP sender with TLS, configurable templates (v0.25.0) |
| PagerDuty Alerts | Done | Events API v2 integration (v0.25.0) |
| Jira Webhooks | Done | Inbound webhook handler (v0.25.0) |
| Outbound Webhooks | Done | Configurable HTTP webhooks with HMAC signing (v0.25.0) |

### Telegram Interaction Modes (v0.6.0)

| Mode | Trigger | Behavior |
|------|---------|----------|
| Chat | "What do you think about..." | Conversational response, no code changes |
| Questions | "What files handle...?" | Quick read-only answers (90s timeout) |
| Research | "Research how X works" | Deep analysis, output to chat + saves to `.agent/research/` |
| Planning | "Plan how to add X" | Creates plan with Execute/Cancel buttons |
| Tasks | "Add a logout button" | Confirms, executes with PR |

**Default behavior**: Ambiguous messages now default to Chat mode instead of Task, preventing accidental PRs.

### Autopilot Environments

The `--autopilot` flag controls automation behavior, not project environments:

| Flag | CI Wait | Approval | Use Case |
|------|---------|----------|----------|
| `dev` | Skip | No | Fast iteration, trust the bot |
| `stage` | Yes | No | CI must pass, then auto-merge |
| `prod` | Yes | Yes | CI + human approval required |

```bash
pilot start --autopilot=dev --telegram --github    # YOLO mode
pilot start --autopilot=stage --telegram --github  # Balanced (recommended)
pilot start --autopilot=prod --telegram --github   # Safe, manual approval
```

### Needs Verification

| Component | Issue |
|-----------|-------|
| Linear Webhooks | Needs gateway running |
| Jira Webhooks | Needs gateway running |
| Email Alerts | Implemented, untested |
| PagerDuty | Implemented, untested |

---

## Active Work

**Source of truth: GitHub Issues with `pilot` label**

```bash
gh issue list --label pilot --state open
gh issue list --label pilot-in-progress --state open
gh pr list --state open
```

### Stability Plan (Priority)

**Full plan: `.agent/tasks/STABILITY-PLAN.md`**

Goal: Raise autonomous reliability from 3/10 to 8/10. Three phases:

**Phase 1 — Stop the Bleeding (P0):**
| Issue | Fix | Key File |
|-------|-----|----------|
| GH-718 | Clean stale `pilot-failed` labels + clear processed map | `cleanup.go`, `poller.go` |
| GH-719 | Per-PR circuit breaker (not global) | `controller.go` |
| GH-720 | Retry with backoff on GitHub API calls | new `retry.go` |
| GH-721 | Hard fail on branch switch failure | `runner.go` |
| GH-722 | Detect rate limits, retry with backoff | `runner.go`, `main.go` |

**Phase 2 — Prevent Cascading Failures (P1):**
| Issue | Fix | Key File |
|-------|-----|----------|
| GH-723 | Sequential sub-issue execution (merge-then-next) | `epic.go` |
| GH-724 | Detect merge conflicts before CI wait | `controller.go` |
| GH-725 | Auto-rebase PRs on simple conflicts | `controller.go` |

**Phase 3 — Resilience (P2):**
| Issue | Fix | Key File |
|-------|-----|----------|
| GH-726 | Persist autopilot state to SQLite | new `state_store.go` |
| GH-727 | LLM complexity classifier (supersedes GH-665) | `decomposer.go` |
| GH-728 | Failure metrics + alerting dashboard | new `metrics.go` |

### Slack Socket Mode (Remaining)

| Issue | What | Status |
|-------|------|--------|
| GH-644 | Extract shared intent package | Queued |
| GH-650 | Slack handler with 5 interaction modes | Blocked by 644 |
| GH-651 | Slack MemberResolver RBAC | Blocked by 650 |
| GH-652 | Wire Slack into pilot.go + main.go | Blocked by all above |

### Backlog

| Priority | Topic | Why |
|----------|-------|-----|
| P2 | Docs v2: Nextra 4 migration | Attempted, failed — docs still on Nextra v2 |
| P3 | Docs refresh for 107 features | Teams RBAC, approval rules not documented |

---

## Completed Log

### 2026-02-10

| Item | What |
|------|------|
| **v0.30.1** | Fix undefined RawSocketEvent build error |
| **v0.30.0** | SQLite state persistence (GH-726), LLM complexity classifier (GH-727), merge conflict detection (GH-724) |
| **v0.29.0** | Socket Mode Listen() with auto-reconnect on SocketModeClient |
| **v0.28.0** | `--slack` CLI flag, app_token validation, Socket Mode handler tests |
| **v0.27.0** | Parallel execution, Socket Mode core (OpenConnection, events, handler), config fields |
| Dashboard | Human-readable autopilot labels, ASCII indicators instead of emojis |
| Model | Reverted default from Opus 4.6 to Opus 4.5 |
| PR cleanup | Merged #733, #737, #739, #740; closed 4 conflicting PRs |
| Issue cleanup | Closed decomposition artifacts (GH-763-768) |

### 2026-02-09

| Item | What |
|------|------|
| **Slack connected** | Bot verified, 5 notification samples sent to #engineering, config updated |
| **v0.26.1** | Wire Email/Webhook/PagerDuty alert channels into all 3 dispatcher blocks |
| **Parallel execution** | Fixed `checkForNewIssues()` — was synchronous, now goroutines + semaphore |
| **Stability plan** | 11 issues (GH-718-728) across 3 phases for reliability 3/10 to 8/10 |
| **v0.26.0** | Teams RBAC, rule-based approvals, 107/107 features |
| **v0.25.0** | Email + PagerDuty alerts, Jira webhooks, outbound webhooks, tunnel flag, 32 health tests |
| Docs fixes | Pin Nextra v2 deps, fix MDX compile error, OG metas, deploy tag decoupling |

### 2026-02-07

| Item | What |
|------|------|
| **v0.24.1** | Rich PR comments with execution metrics + fix autopilot release conflict (tag-only) |
| **v0.24.0** | Wire intent judge into execution pipeline (GH-624) |
| **v0.23.3** | CommitSHA git fallback — recover SHA when output parsing misses it |
| **v0.23.2** | Docs: config reference (1511 lines), integrations pages, auto-deploy, community page |
| **v0.23.1** | Wire sub-issue PR callback for epic execution (GH-588) |
| **v0.22.1** | Dashboard epic-aware HISTORY panel |

### 2026-02-06

| Item | What |
|------|------|
| Docs site | Nextra v2 complete rewrite: homepage, why-pilot vision doc, quickstart guide |
| QuantFlow landing | `/pilot` case study page, added to case-studies-config |
| GitLab sync | GitHub Action syncs `docs/` to `quant-flow/pilot-docs` GitLab repo on merge |
| CONTRIBUTING.md | Dev setup, code standards, PR process, BSL 1.1 note |

### 2026-02-05

| Item | What |
|------|------|
| **v0.20.0** | Default model to Opus 4.6, effort routing, dashboard card padding |
| **v0.19.x** | Dashboard polish, autopilot CI fix targets original branch, release packaging fix |
| **v0.18.0** | Dashboard cards, data wiring, autopilot stale SHA fix |

### 2026-02-03 and earlier

| Item | What |
|------|------|
| **v0.13.x** | LLM intent classification, GoReleaser, self-review, hot reload, SQLite WAL |
| **v0.6.0** | Chat-like Telegram Communication (5 interaction modes) |
| **v0.4.x** | Autopilot PR scanning, macOS upgrade fix, Asana + decomposition |
| **v0.3.x** | Autopilot superfeature, Homebrew formula, install.sh fixes |

Full archive: `.agent/tasks/archive/`

---

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
- `internal/adapters/slack/socketmode.go` - Socket Mode client + Listen()
- `internal/adapters/slack/events.go` - Event types + envelope parsing

### Executor
- `internal/executor/runner.go` - Claude Code process spawner with stream-json parsing + slog logging
- `internal/executor/alerts.go` - AlertEventProcessor interface (avoids import cycles)
- `internal/executor/progress.go` - Visual progress bar display (lipgloss)
- `internal/executor/monitor.go` - Task state tracking

### Alerts
- `internal/alerts/engine.go` - Event processing, rule evaluation, cooldowns
- `internal/alerts/dispatcher.go` - Multi-channel alert dispatch
- `internal/alerts/channels.go` - Slack, Telegram, Email, Webhook, PagerDuty
- `internal/alerts/adapter.go` - EngineAdapter bridges executor to alerts engine

### Dashboard
- `internal/dashboard/tui.go` - Bubbletea TUI with token usage, cost, task history

### Memory
- `internal/memory/store.go` - SQLite storage
- `internal/memory/graph.go` - Knowledge graph
- `internal/memory/patterns.go` - Global pattern store

### Testing
- `internal/testutil/tokens.go` - Safe fake tokens for all test files

## Development Workflow

**NEVER use local builds. Always release then upgrade.**

```bash
make test
make fmt && make lint
```

## Release Workflow

```bash
# Tag-only: GoReleaser CI handles the rest
git tag v0.X.Y && git push origin v0.X.Y

# Upgrade to new version
pilot upgrade
```

**Fresh Install:**
```bash
curl -fsSL https://raw.githubusercontent.com/alekspetrov/pilot/main/install.sh | bash
```

**Known Issue (GH-204):** Install script doesn't auto-configure PATH. Users must add `~/.local/bin` to PATH or open new terminal.

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
- Phases: Starting, Exploring, Implementing, Testing, Committing, Completed
- Alerts: Task lifecycle events emitted via `AlertEventProcessor` interface

### Slack
- Notifications: Task started, progress, completed, failed
- Handler: `internal/adapters/slack/notifier.go`
- Socket Mode: `internal/adapters/slack/socketmode.go` — Listen() with auto-reconnect

## CLI Flags

### `pilot start`
- `--autopilot=ENV` - Enable autopilot mode: `dev`, `stage`, `prod`
- `--dashboard` - Launch TUI dashboard with live task monitoring
- `--telegram` - Enable Telegram polling
- `--github` - Enable GitHub polling
- `--slack` - Enable Slack Socket Mode
- `--daemon` - Run in background
- `--sequential` - Wait for PR merge before next issue (default)

### `pilot task`
- `--verbose` - Stream raw Claude Code JSON output
- `--alerts` - Enable alert engine for this task
- `--dry-run` - Show prompt without executing

## Progress Display

`pilot task` shows real-time visual progress:

```
Executing task with Claude Code...

   Implementing   [============........] 60%  TASK-34473  45s

   [14:35:15] Claude Code initialized
   [14:35:18] Analyzing codebase...
   [14:35:25] Creating App.tsx
   [14:35:40] Installing dependencies...
   [14:35:55] Committing changes...

---
Task completed successfully!
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
| Research | `PHASE: -> RESEARCH` | 25% |
| Implement | `PHASE: -> IMPL` | 50% |
| Verify | `PHASE: -> VERIFY` | 80% |
| Checkpoint | `.agent/.context-markers/` write | 88% |
| Completing | `EXIT_SIGNAL: true` | 92% |
| Complete | `LOOP COMPLETE` / `TASK MODE COMPLETE` | 95% |

Navigator status blocks provide real progress via `Progress: N%` field.

### Execution Report

After task completion, `pilot task` displays a structured execution report:

```
---
EXECUTION REPORT
---
Task:       GH-47
Status:     Success
Duration:   3m 42s
Branch:     pilot/GH-47
Commit:     a1b2c3d
PR:         #48

Navigator: Active
   Mode:    nav-task

Phases:
  Research     45s   (20%)
  Implement    2m    (54%)
  Verify       57s   (26%)

Files Changed:
  M runner.go
  A quality.go
  M TASK-20.md

Tokens:
  Input:    45k
  Output:   12k
  Cost:     ~$0.82
  Model:    claude-opus-4-6
---
```

Navigator detection shown at start:
- `Navigator: detected (.agent/ exists)` if Navigator initialized
- `Navigator: not found (running raw Claude Code)` otherwise

## Documentation Loading Strategy

1. **Every session**: This file (2k tokens)
2. **Feature work**: Task doc (3k tokens)
3. **Architecture changes**: System doc (5k tokens)
4. **Integration work**: Relevant adapter code

Total: ~12k tokens vs 50k+ loading everything.
