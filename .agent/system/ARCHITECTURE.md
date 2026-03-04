# Pilot Architecture

**Last Updated:** 2026-03-04 (v2.56.0 - Multi-backend, dashboard, environment pipelines)

## System Overview

Pilot is a Go-based autonomous AI development platform that:
- Receives tickets from GitHub, Linear, Jira, Asana, GitLab, AzureDevOps, Slack, Telegram, Discord, Plane
- Plans and executes implementation using Claude Code, Qwen Code, or OpenCode
- Creates branches, commits, and PRs with optional self-review
- Monitors CI, auto-merges, and deploys via environment-specific pipelines
- Learns patterns from PR reviews and applies them to future tasks
- Provides TUI dashboard and web/desktop dashboards for monitoring

```
                     ┌─────────────────────────────────────────┐
                     │              CLI (cmd/pilot)            │
                     │ start | task | telegram | github | ...  │
                     └─────────────────┬───────────────────────┘
                                       │
         ┌─────────────────────────────┴─────────────────────────────┐
         │                                                           │
         ▼                                                           ▼
┌─────────────────────────┐                          ┌──────────────────────┐
│   Polling Mode          │                          │   Gateway Mode       │
│  (daemon background)    │                          │  (HTTP + WebSocket)  │
│                         │                          │                      │
│  • GitHub poller        │                          │  • Inbound webhooks  │
│  • Linear/Jira/Asana    │                          │  • Web dashboard     │
│  • GitLab/AzureDevOps   │                          │  • Desktop app API   │
│  • Telegram polling     │                          │  • Slack Socket Mode │
│  • Dashboard TUI        │                          │  • Discord WebSocket │
└─────────┬───────────────┘                          └──────────┬───────────┘
          │                                                     │
          └──────────────────────┬──────────────────────────────┘
                                 │
                    ┌────────────▼───────────┐
                    │    Task Dispatcher     │
                    │  (per-project queue)   │
                    └────────────┬───────────┘
                                 │
        ┌────────────────────────┼────────────────────────┐
        │                        │                        │
        ▼                        ▼                        ▼
┌─────────────────┐  ┌──────────────────┐  ┌──────────────────┐
│    Executor     │  │     Memory       │  │  Pattern Engine  │
│ • Claude/Qwen   │  │ SQLite + Graph   │  │ Learn from PRs   │
│ • Runner + Git  │  │ Task History     │  │ PR review inject │
│ • Quality Gates │  │ Cross-project    │  │                  │
│ • Autopilot CI  │  │ Patterns         │  │                  │
│ • Self-review   │  └──────────────────┘  └──────────────────┘
└─────────────────┘
```

## Data Flow

### Task Execution Flow (Primary)

```
   Webhook/Polling              GitHub Issue / Telegram / Linear / etc
        │                                    │
        └────────────┬───────────────────────┘
                     │
                     ▼
          ┌──────────────────────┐
          │   Issue Handler      │
          │ (common logic for    │
          │  all adapters)       │
          └────────┬─────────────┘
                   │
                   ▼
          ┌──────────────────────┐
          │   Task Dispatcher    │
          │ (per-project queue + │
          │  parallel execution) │
          └────────┬─────────────┘
                   │
                   ▼
          ┌──────────────────────┐
          │  Runner.Execute()    │
          │ (subprocess + stream │
          │  -json parsing)      │
          └────────┬─────────────┘
                   │
    ┌──────────────┼──────────────┐
    │              │              │
    ▼              ▼              ▼
 Git Ops    Claude Code    Progress/Alerts
 (branch,   (stream-json    (lipgloss,
  commit,    events)         slog)
  PR)
```

### Autopilot CI Pipeline

```
┌───────────────────────────────────────────────────────────────┐
│ PR Created                                                    │
│ ├─ WaitingCI: Poll GitHub for CI checks                      │
│ ├─ CIPassed: All checks passed                               │
│ │  └─ Self-review: Optional code review before merge         │
│ ├─ Merging: Rebase/squash and merge branch                   │
│ ├─ Merged: Branch deleted, monitoring ends                   │
│ ├─ PostMergeCI: Optional CI after merge (tag/deploy)         │
│ └─ (Merge conflict: Auto-rebase via GitHub API)              │
└───────────────────────────────────────────────────────────────┘
```

### Environment Config System (v1.59.0+)

```
┌─────────────────────────────────────────┐
│  Autopilot Environment Config           │
├─────────────────────────────────────────┤
│ EnvironmentConfig per environment:      │
│  • dev:    Skip CI, no approval         │
│  • stage:  Wait for CI, auto-merge      │
│  • prod:   Wait for CI, require approval│
│  • custom: User-defined via YAML        │
├─────────────────────────────────────────┤
│ Post-merge actions:                     │
│  • Webhook trigger                      │
│  • Branch push deployment               │
│  • Tag-based release                    │
└─────────────────────────────────────────┘
```

## Multi-Executor Backend System (v1.9.0+)

Pilot supports multiple AI execution backends:

| Backend | Status | Model Family | Use Case | Notes |
|---------|--------|--------------|----------|-------|
| Claude Code | Primary | Claude 3.x | Default, best for complex tasks | v1.0+ |
| Qwen Code | Supported | Qwen 2.5 | Cost-sensitive, simpler tasks | v1.9.0+ |
| OpenCode | Future | Alibaba CodeStudio | On-device inference | Placeholder |

**Backend Selection:**
- Config-driven: `executor.backend: claude|qwen|opencode`
- Model selection: Route Haiku (trivial) → Sonnet 4.6 (medium) → Opus 4.6 (complex)
- Preflight validation: Backend CLI check (v1.39.0)

## Package Architecture

### Core Packages (Wired in main.go)

| Package | Purpose | Key Files | Version Added |
|---------|---------|-----------|----------------|
| `pilot` | Top-level orchestration | `pilot.go` | v0.1 |
| `executor` | Claude/Qwen process management | `runner.go`, `git.go`, `backends.go` | v0.1 |
| `config` | YAML configuration loading | `config.go`, `schema.go` | v0.1 |
| `memory` | SQLite + knowledge graph | `store.go`, `graph.go`, `patterns.go` | v0.1 |
| `logging` | Structured slog logging | `logger.go` | v0.1 |
| `alerts` | Event-based alerting | `engine.go`, `dispatcher.go` | v0.1 |
| `quality` | Quality gates (test/lint) | `executor.go`, `gates.go` | v0.1 |
| `dashboard` | Bubbletea TUI | `tui.go` | v0.1 |
| `gateway` | HTTP + WebSocket server | `server.go`, `router.go` | v0.1 |
| `autopilot` | CI monitor, auto-merge, deploy | `controller.go`, `auto_merger.go` | v0.3 |
| `briefs` | Daily/weekly summaries | `generator.go` | v0.1 |
| `replay` | Execution recording viewer | `player.go` | v0.1 |
| `upgrade` | Self-update mechanism | `upgrader.go` | v0.1 |

### Adapter Packages (v2.30.0+: Common Registry)

| Package | Purpose | Status | Added |
|---------|---------|--------|-------|
| `adapters/github` | GitHub Issues + PR ops | Polling + webhook | v0.1 |
| `adapters/linear` | Linear workspace | Webhook + ProcessedStore | v1.11.0 |
| `adapters/jira` | Jira instance | Webhook + ProcessedStore | v1.12.0 |
| `adapters/asana` | Asana workspace | Webhook + ProcessedStore | v1.12.0 |
| `adapters/gitlab` | GitLab instance | REST + webhook | v1.12.0 |
| `adapters/azuredevops` | Azure DevOps | REST + webhook | v1.12.0 |
| `adapters/slack` | Slack bot | Socket Mode + notifications | v0.1 |
| `adapters/telegram` | Telegram bot | Long-polling + voice | v0.1 |
| `adapters/discord` | Discord bot | Gateway WebSocket | v2.25.0 |
| `adapters/plane` | Plane.so | REST + webhooks | v2.25.0 |

**Common Adapter Registry (v2.30.0):**
- Unified `Adapter` interface (ProcessedStore, state transitions)
- Generic `handleIssueGeneric()` consolidates 5 adapter flows
- State transitions: `UpdateIssueState()`, `TransitionIssueTo()`, `CompleteTask()`

### Supporting Packages

| Package | Purpose | Status |
|---------|---------|--------|
| `approval` | Human-in-the-loop gates | Implemented, optional |
| `budget` | Cost controls + rate limiting | Implemented, CLI command |
| `teams` | RBAC + rule-based approvals | Implemented |
| `tunnel` | Cloudflare tunnel integration | Implemented |
| `webhooks` | Outbound webhook triggers | Implemented |
| `health` | K8s health probes | Implemented |
| `testutil` | Safe test token constants | Test-only |

## Dashboard Systems

### TUI Dashboard (Bubbletea, v0.1+)

Real-time monitoring with sparkline cards, git graph visualization, and state-aware queue:

| Panel | Features | Updates |
|-------|----------|---------|
| **Queue** | Task lifecycle visualization, 5 states (done/running/queued/pending/failed) | Per-event |
| **History** | Epic-aware task history, execution metrics | Per-task |
| **Logs** | Real-time Claude Code output streaming | Per-event |
| **Autopilot** | PR status, CI checks, merge progress | Per-check |
| **Git Graph** | Live branch visualization with 4 size modes | Per-branch |
| **Metrics** | Token usage, cost tracking, uptime | Per-interval |

**States (v2.13.0+):**
- `done` ✓ (sage green)
- `running` ● (steel blue, pulses)
- `queued` ◌ (mid gray, shimmer)
- `pending` · (slate)
- `failed` ✗ (dusty rose)

### Web Dashboard (React, v1.56.0+)

Full-featured monitoring at `http://localhost:9090/dashboard`:

| Feature | Tech | Version |
|---------|------|---------|
| **Tasks** | React hooks + SSE | v1.55.0 |
| **Autopilot** | Real-time CI status | v1.55.0 |
| **History** | Pagination + filtering | v1.62.0 |
| **WebSocket** | Log streaming | v1.56.0 |
| **API** | REST endpoints `/api/v1/*` | v1.55.0 |

### Desktop App (Wails v2, v1.53.0+)

Native macOS/Windows app with React frontend:

| Feature | Version |
|---------|---------|
| Git graph panel | v1.53.0 |
| WebSocket log streaming | v1.56.0 |
| HTTP data provider | v1.53.1 |
| Native titlebar | v1.62.0 |
| Responsive layout | v2.38.0 |

## Worktree Isolation + Epic Interaction

**Worktree Isolation (v0.53-v2.56)**: Execute tasks in isolated git worktrees, preventing conflicts with user's uncommitted changes.

| Version | Feature | Issue |
|---------|---------|-------|
| v0.53.2 | Initial worktree isolation | GH-936 |
| v0.56.0 | Epic + worktree integration | GH-945 |
| v0.57.3 | Crash recovery, orphan cleanup | GH-962 |
| v1.0.11 | Serial conflict cascade prevention | GH-1265 |
| v2.53.0 | Merged PR guard in poller | GH-1855 |

**Epic Decomposition Guard (v1.0.11):**
```go
// Prevent serial conflict cascade
isSinglePackageScope() {
    // Detects when all subtasks target same directory
    // Consolidates into single task instead of creating separate issues
}
```

**Key files:** `internal/executor/worktree.go`, `epic.go`, `runner.go`

### Epic Execution Flow

```
┌──────────────────────────────────────────────────┐
│  Epic Detected (>5 phases, structural signals)   │
├──────────────────────────────────────────────────┤
│  1. Check `no-decompose` label (v1.57.0)         │
│  2. Check for single-package scope (v1.0.11)     │
│  3. Create worktree with unique path              │
│  4. Copy .agent/ (Navigator preservation)         │
│  5. Plan decomposition in worktree                │
│  6. Create sub-issues via GitHub API              │
│  7. Execute sub-issues SEQUENTIALLY               │
│     └─ allowWorktree=false (no nesting)           │
│  8. Cleanup worktree (deferred)                   │
└──────────────────────────────────────────────────┘
```

## Pattern Learning System (v2.25.0+)

Learn from PR reviews and inject patterns into future prompts:

```
┌────────────────────────────────────────┐
│  PR Review Analysis                    │
├────────────────────────────────────────┤
│  1. Extract comments from review       │
│  2. Classify as pattern or anti-pattern│
│  3. Calculate confidence score         │
│  4. Store in memory.cross_patterns     │
│  5. Inject top patterns into prompts   │
│     for similar future tasks           │
└────────────────────────────────────────┘
```

**Files:** `internal/memory/feedback.go`, `LearnFromReview()` integration in autopilot

### CI Error Pattern Learning (v2.49.0+)

Extract and learn from CI failures with categorized error patterns:

```
┌──────────────────────────────────────┐
│  CI Failure Detection                │
├──────────────────────────────────────┤
│  1. Capture CI check logs            │
│  2. Extract error patterns by type:  │
│     • Compilation errors             │
│     • Test failures                  │
│     • Linter violations              │
│     • Build failures                 │
│  3. Tag with source:ci + category    │
│  4. Store as anti-patterns (0.5 conf)│
│  5. Boost confidence on recurrence   │
│  6. Inject into retry prompts        │
└──────────────────────────────────────┘
```

**Features:**
- Pattern categorization: compilation, test, lint, build
- Automatic confidence boosting on pattern recurrence
- Context-aware: tracks check names and CI framework
- Integration with retry system: injects CI patterns into follow-up prompts

**Files:** `internal/memory/extractor.go` (pattern extraction), `internal/memory/feedback.go` (learning loop)

## Self-Review System (v0.33.14+)

Optional automated code review before PR merge:

| Phase | Action | Version |
|-------|--------|---------|
| Execution | Create PR without merge | v0.8 |
| Self-Review | Analyze code + comment | v0.8 |
| Alignment Check | Verify modified files vs issue title | v0.33.14 |
| AC Verification | Extract + verify acceptance criteria | v2.49.0 |
| Auto-Approval | Approve if quality gates pass | v0.61 |

## Auto-Rebase on Conflict (v2.25.0+)

Automatically resolve merge conflicts:

```
┌──────────────────────────────────────┐
│  Merge Conflict Detected              │
├──────────────────────────────────────┤
│  1. GitHub UpdateBranch API           │
│  2. Rebase branch against main        │
│  3. Retry merge                       │
│  4. Create CI fix issue if still fails│
│     (Depends on: #N annotation)       │
└──────────────────────────────────────┘
```

## GitHub Projects V2 Board Sync (v2.30.0+)

Automatic GraphQL board sync with 3-column layout:

```
┌─────────────┬─────────────┬──────────────┐
│ Backlog     │ Review      │ Done         │
│ (open)      │ (in PR)     │ (merged)     │
│             │ (in progress)              │
└─────────────┴─────────────┴──────────────┘
```

**Features:**
- Lazy ID resolution (org-first discovery)
- Concurrent issue moves
- Custom field updates
- Key files: `internal/autopilot/board_sync.go`

## Key Integration Points

### Claude Code Integration

```go
// internal/executor/runner.go - Stream-JSON parsing
cmd := exec.Command("claude",
    "-p", prompt,
    "--output-format", "stream-json",
    "--dangerously-skip-permissions",
)
// Parses: system, assistant, tool_use, tool_result, result events
```

### Navigator Integration

Pilot activates `/nav-loop` mode when `.agent/` exists (v0.33.15+):

```go
if useNavigator {
    sb.WriteString("Use /nav-loop mode for this task.\n\n")
}
```

**Navigator context bridge (v1.18.0):**
- Load key files, components, structure into prompt
- Post-execution docs update: feature matrix, knowledge capture

### Hooks System (v1.3.0+)

Claude Code inline quality gates via JSON hooks (v1.50.0 format):

```json
{
  "PreToolUse": [
    {
      "matcher": "Bash",
      "hooks": [{"type": "command", "command": "..."}]
    }
  ]
}
```

**Key files:** `internal/executor/hooks.go` (generation + merging)

### Alerts Integration

Event-based multi-channel dispatch:

```go
// internal/executor/alerts.go
type AlertEventProcessor interface {
    ProcessEvent(event alerts.Event)
}

// Emits: TaskStarted, TaskProgress, TaskCompleted, TaskFailed
```

## Configuration Structure

```yaml
# ~/.pilot/config.yaml
gateway:
  host: "127.0.0.1"
  port: 9090

executor:
  backend: "claude"  # or qwen
  use_worktree: true
  navigator:
    auto_init: true

adapters:
  github:
    enabled: true
    polling:
      interval: 30s
      label: "pilot"
  linear:
    enabled: false
    api_key: "..."
  slack:
    enabled: false
    app_token: "..."

autopilot:
  enabled: true

environments:
  dev:
    ci_required: false
    approval_required: false
  stage:
    ci_required: true
    approval_required: false
  prod:
    ci_required: true
    approval_required: true
    post_merge:
      action: "tag"

memory:
  path: "~/.pilot/memory.db"

alerts:
  enabled: true
  channels: ["slack", "telegram"]
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
    pr_url TEXT,
    model TEXT,
    tokens_input INTEGER,
    tokens_output INTEGER
);

-- Cross-project patterns (learning system)
CREATE TABLE cross_patterns (
    id TEXT PRIMARY KEY,
    title TEXT,
    description TEXT,
    type TEXT,  -- "pattern" or "anti_pattern"
    scope TEXT,
    confidence REAL,
    occurrences INTEGER,
    is_anti_pattern BOOLEAN,
    first_seen DATETIME,
    last_seen DATETIME
);

-- Task queue (per-project dispatch)
CREATE TABLE task_queue (
    id TEXT PRIMARY KEY,
    project_path TEXT,
    task_json TEXT,
    status TEXT,
    created_at DATETIME,
    started_at DATETIME,
    completed_at DATETIME
);

-- Processed store (dedup across restarts)
CREATE TABLE processed (
    id TEXT PRIMARY KEY,
    adapter TEXT,
    external_id TEXT,
    processed_at DATETIME,
    UNIQUE(adapter, external_id)
);

-- Execution milestones (dashboard log)
CREATE TABLE milestones (
    id TEXT PRIMARY KEY,
    execution_id TEXT,
    phase TEXT,
    timestamp DATETIME,
    duration_ms INTEGER,
    metadata TEXT
);

-- GitHub Projects V2 board state
CREATE TABLE board_state (
    id TEXT PRIMARY KEY,
    project_number INTEGER,
    issue_number INTEGER,
    column_name TEXT,
    updated_at DATETIME
);
```

## Test Coverage

| Package | Test Files | Status |
|---------|-----------|--------|
| adapters/github | 5 | ✅ |
| adapters/slack | 2 | ✅ |
| adapters/telegram | 7 | ✅ |
| adapters/jira | 3 | ✅ |
| adapters/linear | 3 | ✅ |
| adapters/asana | 2 | ✅ |
| adapters/gitlab | 1 | ✅ |
| adapters/azuredevops | 1 | ✅ |
| adapters/discord | 2 | ✅ |
| adapters/plane | 2 | ✅ |
| alerts | 4 | ✅ |
| approval | 2 | ✅ |
| autopilot | 8 | ✅ |
| briefs | 4 | ✅ |
| budget | 2 | ✅ |
| config | 1 | ✅ |
| executor | 20 | ✅ |
| gateway | 4 | ✅ |
| logging | 2 | ✅ |
| memory | 8 | ✅ |
| quality | 3 | ✅ |
| replay | 4 | ✅ |
| teams | 1 | ✅ |
| tunnel | 6 | ✅ |
| upgrade | 1 | ✅ |
| webhooks | 1 | ✅ |

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

# Release (tag-only)
git tag v2.X.Y && git push origin v2.X.Y  # GoReleaser CI handles rest
```

**Binary versioning:** `v2.X.Y` (semantic)

## Security Considerations

1. **Tokens in tests**: Use `internal/testutil/tokens.go` for fake tokens
2. **API keys**: Environment variables or config file (`~/.pilot/config.yaml`)
3. **Sandbox mode**: Claude Code runs with `--dangerously-skip-permissions` (trusted context)
4. **Webhook secrets**: HMAC validation for incoming webhooks (SHA256)
5. **Database**: SQLite with WAL mode, connection pooling (`SetMaxOpenConns(1)`)

## Key Execution Modes

| Mode | Trigger | Behavior |
|------|---------|----------|
| Sequential | Default or many file changes | One issue at a time |
| Parallel | Few file changes, different scopes | Multiple issues concurrently |
| Epic | >5 phases detected | Decompose into sub-issues |
| Worktree | `use_worktree: true` | Isolated execution environment |

**Execution mode selection (v2.25.0):** Scope-based auto-switching via union-find algorithm

## Version History

| Version | Key Milestone | Date |
|---------|---------------|------|
| v0.1 | Initial release | 2025-12 |
| v0.53.2 | Worktree isolation | 2026-01 |
| v0.57.5 | Previous arch doc | 2026-02-13 |
| v1.0.0 | v1.0 stabilization | 2026-02-14 |
| v1.59.0 | Environment config | 2026-02-19 |
| v1.62.0 | Gateway + desktop app | 2026-02-20 |
| v2.25.0 | Pattern learning, auto-rebase, Discord | 2026-02-25 |
| v2.30.0 | Common adapter registry, board sync | 2026-02-26 |
| v2.53.0 | Merged PR guard, CI error patterns | 2026-02-28 |
| v2.56.0 | Current (this doc) | 2026-03-04 |

## Appendix: Full Package Audit

**Last Audit:** 2026-03-04

| Package | Exists | Imported | Wired | Tests | Status |
|---------|--------|----------|-------|-------|--------|
| adapters/github | ✅ | ✅ | ✅ | ✅ | ✅ |
| adapters/jira | ✅ | ✅ | ✅ | ✅ | ✅ |
| adapters/linear | ✅ | ✅ | ✅ | ✅ | ✅ |
| adapters/slack | ✅ | ✅ | ✅ | ✅ | ✅ |
| adapters/telegram | ✅ | ✅ | ✅ | ✅ | ✅ |
| adapters/asana | ✅ | ✅ | ✅ | ✅ | ✅ |
| adapters/gitlab | ✅ | ✅ | ✅ | ✅ | ✅ |
| adapters/azuredevops | ✅ | ✅ | ✅ | ✅ | ✅ |
| adapters/discord | ✅ | ✅ | ✅ | ✅ | ✅ |
| adapters/plane | ✅ | ✅ | ✅ | ✅ | ✅ |
| alerts | ✅ | ✅ | ✅ | ✅ | ✅ |
| approval | ✅ | ✅ | ✅ | ✅ | ✅ |
| autopilot | ✅ | ✅ | ✅ | ✅ | ✅ |
| banner | ✅ | ✅ | ✅ | ❌ | ✅ |
| briefs | ✅ | ✅ | ✅ | ✅ | ✅ |
| budget | ✅ | ✅ | ✅ | ✅ | ✅ |
| config | ✅ | ✅ | ✅ | ✅ | ✅ |
| dashboard | ✅ | ✅ | ✅ | ❌ | ✅ |
| executor | ✅ | ✅ | ✅ | ✅ | ✅ |
| gateway | ✅ | ✅ | ✅ | ✅ | ✅ |
| health | ✅ | ✅ | ✅ | ❌ | ✅ |
| logging | ✅ | ✅ | ✅ | ✅ | ✅ |
| memory | ✅ | ✅ | ✅ | ✅ | ✅ |
| orchestrator | ✅ | ✅ | ✅ | ✅ | ✅ |
| pilot | ✅ | ✅ | ✅ | ❌ | ✅ |
| quality | ✅ | ✅ | ✅ | ✅ | ✅ |
| replay | ✅ | ✅ | ✅ | ✅ | ✅ |
| teams | ✅ | ✅ | ✅ | ✅ | ✅ |
| testutil | ✅ | ✅ | ❌ | ❌ | ✅ |
| transcription | ✅ | ✅ | ❌ | ❌ | ✅ |
| tunnel | ✅ | ✅ | ✅ | ✅ | ✅ |
| upgrade | ✅ | ✅ | ✅ | ✅ | ✅ |
| webhooks | ✅ | ✅ | ✅ | ✅ | ✅ |

**Summary:**
- 34 packages total
- 100% exist and are imported
- 100% wired in main.go
- 85% have test files
- 100% of tested packages pass

---

## Critical Integration Constraints

### 1. Navigator Integration (DO NOT REMOVE)

`BuildPrompt()` in `internal/executor/runner.go` MUST invoke `/nav-loop` mode when `.agent/` exists. This is Pilot's core value proposition.

**Incident 2026-01-26**: Accidental removal during refactor. Pilot without Navigator = just another Claude Code wrapper.

### 2. Git Worktree Isolation

Worktree isolation prevents conflicts when user has uncommitted changes. **DO NOT remove `use_worktree` config option.**

### 3. Serial Conflict Cascade Prevention (v1.0.11)

`isSinglePackageScope()` in `epic.go` detects when all planned subtasks target the same directory. When detected, epic is consolidated into a single task instead of creating separate issues.

**Why?** Each sub-issue branches from stale `main`, creates conflicts when they all modify the same files.

---

**For questions, refer to DEVELOPMENT-README.md completed log and task files in `.agent/tasks/`**
