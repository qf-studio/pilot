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

## CRITICAL: Core Architecture Constraints

### 1. Navigator Integration (runner.go)

**NEVER remove Navigator integration from `internal/executor/runner.go`**

The `BuildPrompt()` function MUST invoke `/nav-loop` mode when `.agent/` exists. This is Pilot's core value proposition:

```go
// LocalMode takes priority — checked FIRST (GH-2103, bench val10)
if task.LocalMode {
    return r.buildLocalModePrompt(task)  // problem-solving prompt, no PR constraints
}

// Navigator-aware prompt structure for medium/complex tasks
if useNavigator {
    sb.WriteString("Use /nav-loop mode for this task.\n\n")  // <- NEVER REMOVE
    // ... PILOT EXECUTION MODE override for CLAUDE.md rules
}
```

**LocalMode priority (GH-2103)**: `task.LocalMode` MUST be checked before Navigator detection. Sandbox environments (bench, CI) may have `.agent/` directories that hijack the prompt to Navigator path. LocalMode = problem-solving prompt without PR workflow constraints.

**Incident 2026-01-26**: Navigator prefix was accidentally removed during "simplification" refactor. Pilot without Navigator = just another Claude Code wrapper with zero value.

### 2. Navigator Auto-Init (v0.33.16+)

Navigator is now auto-initialized for projects without `.agent/`. In `runner.go Execute()`:

```go
// Auto-init Navigator if configured and missing
if r.config.Navigator.AutoInit && !initialized {
    r.maybeInitNavigator(task.ProjectPath)  // Creates .agent/ from templates
}
```

**Key files**: `internal/executor/navigator.go`, `internal/executor/templates/`

Disable via config: `executor.navigator.auto_init: false`

---

## Quick Navigation

| Document | When to Read |
|----------|--------------|
| CLAUDE.md | Every session (auto-loaded) |
| This file | Every session (navigator index) |
| `.agent/system/FEATURE-MATRIX.md` | What's implemented vs not |
| `.agent/system/ARCHITECTURE.md` | System design, data flow |
| `.agent/system/PR-CHECKLIST.md` | Before merging PRs in `--autopilot=prod` mode |
| `.agent/tasks/TASK-XX.md` | Active task details |
| `.agent/sops/*.md` | Before modifying integrations |
| `.agent/sops/development/pilot-bench-real-binary.md` | Running real-binary bench on Daytona |
| `.agent/sops/daytona-bench-operations.md` | Daytona sandbox management + monitoring |
| `.agent/sops/benchmark-integrity-audit.md` | Pre-submission cheat-pattern audit for bench runs |
| `.agent/.context-markers/` | Resume after break |

## Current State

**Current Version:** v2.105.0 | **322 features working**

**Recent (v2.105.0, Apr 2026):** avionics TUI redesign + banner polish (v2.103.0–v2.103.1, GH-2454–2456, PR #2459); doctor bloat warning (GH-2462); CLAUDE.md doc maintenance rule (GH-2461).

**Full implementation status:** `.agent/system/FEATURE-MATRIX.md`

### Needs Verification

| Component | Issue |
|-----------|-------|
| Linear Webhooks | Needs gateway running |
| Jira Webhooks | Needs gateway running |
| Email Alerts | Implemented, untested |
| PagerDuty | Implemented, untested |
| Discord Intent Classification | [GH-2121](https://github.com/qf-studio/pilot/issues/2121) — all messages treated as tasks, needs Haiku classifier |
| Slack Regex Fallback | [GH-2122](https://github.com/qf-studio/pilot/issues/2122) — falls back to unreliable regex on LLM timeout |
| Dead Regex Classifier | [GH-2123](https://github.com/qf-studio/pilot/issues/2123) — remove `DetectIntent()` after 2121+2122 |

---

## Active Work

### TASK-25: Stale Recovery Queue Drain Fix (Planned)

**Problem**: `recoverStaleTasks()` marks queued tasks older than 5 min as failed. When `runner.Execute()` blocks for 10-20 min, queued tasks behind it get nuked even though the worker is actively processing.

**Fix**: Add `isWorkerProcessing()` helper and guard in `recoverStaleTasks()` — skip tasks for projects with active workers.

**Plan**: `.agent/tasks/TASK-25-stale-recovery-queue-drain-fix.md`

### Discord Intent Classification (GH-2121/2122/2123)

All I/O channels should use Haiku LLM classifier. Discord has none (every message = task). Slack falls back to unreliable regex on LLM timeout.
- **GH-2121**: Wire Haiku classifier into Discord handler
- **GH-2122**: Replace Slack's regex fallback with `IntentChat` default
- **GH-2123**: Remove dead `DetectIntent()` regex classifier (after 2121+2122)

### Backlog

| Priority | Topic | Why |
|----------|-------|-----|
| P1 | Multi-tenant SaaS mode | Single-user CLI → hosted needs auth, isolation |
| P1 | Public launch prep | Landing page, onboarding, pricing, billing |
| P1 | Web dashboard polish | React UI functional but needs design pass |
| P1 | Fix `shouldTriggerRelease()` | Doesn't check `ResolvedEnv().Release` — only top-level config |
| P2 | E2E test suite | No integration tests — reliability untested |
| P2 | Web dashboard auth | Token-based auth for remote access |
| P2 | Mobile-responsive dashboard | Primary use case is phone access |
| P3 | GitHub App auth | PAT → installable GitHub App |

---

Release history: see git log, GitHub releases, and `.agent/tasks/archive/`

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

### Adapters
- `internal/adapters/slack/notifier.go` - Slack notifications
- `internal/adapters/slack/socketmode.go` - Socket Mode client + Listen()

### Executor
- `internal/executor/runner.go` - Claude Code process spawner with stream-json parsing + slog logging
- `internal/executor/alerts.go` - AlertEventProcessor interface (avoids import cycles)
- `internal/executor/monitor.go` - Task state tracking

### Alerts
- `internal/alerts/engine.go` - Event processing, rule evaluation, cooldowns
- `internal/alerts/dispatcher.go` - Multi-channel alert dispatch

### Dashboard
- `internal/dashboard/tui.go` - Bubbletea TUI with token usage, cost, task history

### Memory
- `internal/memory/store.go` - SQLite storage
- `internal/memory/graph.go` - Knowledge graph

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
curl -fsSL https://raw.githubusercontent.com/qf-studio/pilot/main/install.sh | bash
```

## Configuration

Copy `configs/pilot.example.yaml` to `~/.pilot/config.yaml`.

Required environment variables:
- `LINEAR_API_KEY` - Linear API key
- `SLACK_BOT_TOKEN` - Slack bot token

## Documentation Loading Strategy

1. **Every session**: This file (~3k tokens)
2. **Feature work**: Task doc (3k tokens)
3. **Architecture changes**: System doc (5k tokens)
4. **Integration work**: Relevant adapter code

Total: ~12k tokens vs 50k+ loading everything.
