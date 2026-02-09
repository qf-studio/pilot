# Pilot Development Navigator

**Navigator plans. Pilot executes.**

## ⚠️ WORKFLOW: Navigator + Pilot Pipeline

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

| ✅ Do | ❌ Don't |
|-------|----------|
| Use `/nav-task` for planning | Write code directly |
| Create issues with `pilot` label | Make commits manually |
| Review every PR before merging | Create PRs manually |
| Request changes on PR if needed | Approve without review |

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
| `.agent/system/PR-CHECKLIST.md` | **Before merging any PR** |
| `.agent/tasks/TASK-XX.md` | Active task details |
| `.agent/sops/*.md` | Before modifying integrations |
| `.agent/.context-markers/` | Resume after break |

## Current State

**Current Version:** v0.24.1 | **103 features working** | **4 unwired**

**Full implementation status:** `.agent/system/FEATURE-MATRIX.md`

### Key Components

| Component | Status | Notes |
|-----------|--------|-------|
| Task Execution | ✅ | Claude Code subprocess with Navigator |
| Telegram Bot | ✅ | Long-polling, voice, images, chat modes |
| GitHub Polling | ✅ | 30s interval, auto-picks `pilot` label |
| Alerts Engine | ✅ | Slack, Telegram, webhooks |
| Quality Gates | ✅ | Test/lint/build gates with retry |
| Task Dispatcher | ✅ | Per-project queue |
| Dashboard TUI | ✅ | Sparkline cards, muted palette, SQLite persistence, **epic-aware HISTORY (v0.22.1)** |
| Hot Upgrade | ✅ | Self-update via `pilot upgrade` or dashboard 'u' key, `syscall.Exec` restart |
| **Autopilot** | ✅ | CI monitor, auto-merge, feedback loop, **tag-only release (v0.24.1)** |
| **LLM Intent Judge** | ✅ | Intent classification in execution pipeline **(v0.24.0)** |
| **Rich PR Comments** | ✅ | Execution metrics (duration, tokens, cost) in PR comments **(v0.24.1)** |
| **AGENTS.md Support** | ✅ | LoadAgentsFile reads project AGENTS.md **(v0.24.1)** |
| **Self-Review** | ✅ | Auto code review before PR |
| **Auto Build Gate** | ✅ | Minimal build gate when none configured |
| **Effort Routing** | ✅ | Map task complexity to reasoning depth **(v0.20.0)** |
| **Release Pipeline** | ✅ | Tag-only → GoReleaser CI builds + uploads binaries **(v0.24.1)** |
| **Docs Site** | ✅ | Nextra v2 (pinned), GitLab sync, **auto-deploy via prod tag, OG metas + preview image (v0.24.1)** |
| **QuantFlow Landing** | ✅ | `/pilot` case study page on quantflow.studio **(2026-02-06)** |
| **CONTRIBUTING.md** | ✅ | Dev setup, code standards, BSL 1.1 note **(2026-02-06)** |

### Telegram Interaction Modes (v0.6.0)

| Mode | Trigger | Behavior |
|------|---------|----------|
| 💬 **Chat** | "What do you think about..." | Conversational response, no code changes |
| 🔍 **Questions** | "What files handle...?" | Quick read-only answers (90s timeout) |
| 🔬 **Research** | "Research how X works" | Deep analysis, output to chat + saves to `.agent/research/` |
| 📐 **Planning** | "Plan how to add X" | Creates plan with Execute/Cancel buttons |
| 🚀 **Tasks** | "Add a logout button" | Confirms → executes with PR |

**Default behavior**: Ambiguous messages now default to Chat mode instead of Task, preventing accidental PRs.

### Autopilot Environments

The `--autopilot` flag controls automation behavior, not project environments:

| Flag | CI Wait | Approval | Use Case |
|------|---------|----------|----------|
| `dev` | Skip | No | Fast iteration, trust the bot |
| `stage` | Yes | No | CI must pass, then auto-merge |
| `prod` | Yes | Yes | CI + human approval required |

```bash
# Examples
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

## Active Work

**Source of truth: GitHub Issues with `pilot` label**

```bash
# See Pilot's queue
gh issue list --label pilot --state open

# See what's in progress
gh issue list --label pilot-in-progress --state open

# See open PRs from Pilot
gh pr list --author "@me" --state open
```

### In Progress

_Queue empty - create issues with `pilot` label to add work._

### Backlog (Create issues as needed)

| Priority | Topic | Why |
|----------|-------|-----|
| ~~P2~~ | ~~Cost controls~~ | Wired in v0.21.3 (GH-539) |
| ~~P2~~ | ~~Approval workflows~~ | Fully wired: manager + Telegram/Slack/GitHub handlers + autopilot prod gate |
| 🟡 P2 | Docs v2: Nextra 4 migration | Attempted, failed — docs still on Nextra v2 |

**For accurate feature status, see:** `.agent/system/FEATURE-MATRIX.md`

---

## Completed (2026-02-09)

| Item | What |
|------|------|
| Docs fix | Pin Nextra v2 deps (~2.13.0, ~14.2.0) — caret range resolved to v4, broke build |
| Docs fix | Fix MDX list-in-JSX compile error in quickstart.mdx |
| Docs | Dashboard preview screenshot on homepage |
| Docs | OG metas: og:image, twitter:card summary_large_image, fix property= attrs |
| Docs | Banner updated to v0.24 |
| CI fix | Docs sync: decouple deploy tag from content diff, delete+recreate prod tag |
| Housekeeping | Closed GH-625, GH-626, stale docs PRs #628/#630/#631 |

## Completed (2026-02-07)

| Item | What |
|------|------|
| **v0.24.1** | Rich PR comments with execution metrics + fix autopilot release conflict (tag-only) |
| **v0.24.0** | Wire intent judge into execution pipeline (GH-624) |
| GH-626 | PR comments include duration, token count, cost, model used |
| GH-625 | LoadAgentsFile — read AGENTS.md from project directory |
| GH-624 | Intent judge wired into executor pipeline for task classification |
| **v0.23.3** | fix(executor): CommitSHA git fallback — recover SHA when output parsing misses it |
| **v0.23.2** | Docs: config reference (1511 lines), integrations pages, auto-deploy, community page |
| **v0.23.1** | fix(autopilot): wire sub-issue PR callback for epic execution (GH-588) |
| **v0.22.1** | Dashboard epic-aware HISTORY panel, docs pages (why-pilot, how-it-works, model-routing, navigator, approvals) |
| GH-620 | CountNewCommits() + fallback in runner.go — prevents "no changes" false failures |
| GH-616 | Integration docs: Linear, GitLab, Slack, Jira (680 lines, never committed) |
| GH-610 | Fix sync-docs.yml nested docker dir bug + integrations nav |
| GH-611 | Auto-trigger GitLab deploy via prod-{version} tag |
| GH-617 | Config reference, autopilot environments guide, internal ref cleanup |
| GH-618 | Community page with license summary |
| GH-588 | Epic sub-issue PRs now registered with autopilot controller for CI monitoring + auto-merge |
| GH-498 | Epic-aware HISTORY: CompletedTask extended, groupedHistory(), renderHistory() with progress bars |

## Completed (2026-02-06)

| Item | What |
|------|------|
| Docs site | Nextra v2 complete rewrite: homepage, why-pilot vision doc, quickstart guide |
| QuantFlow landing | `/pilot` case study page with 10 sections, added to case-studies-config |
| GitLab sync | GitHub Action syncs `docs/` → `quant-flow/pilot-docs` GitLab repo on merge |
| Docker deploy | Dockerfile + `.gitlab-ci.yml` for VPS deployment at `pilot.quantflow.studio` |
| CONTRIBUTING.md | Dev setup, code standards, PR process, BSL 1.1 contributor note |
| FUNDING.yml | GitHub Sponsors button enabled |

## Completed (2026-02-05)

| Item | What |
|------|------|
| **v0.20.0** | Default model → Opus 4.6, effort routing, dashboard card padding |
| **v0.19.2** | Dashboard: remove card gaps, rename TASKS → QUEUE |
| **v0.19.1** | Autopilot CI fix targets original branch via metadata (GH-489) |
| **v0.19.0** | Dashboard polish: muted colors, tighter card gap, sparkline baseline fix |
| **v0.18.1** | Release packaging fix: `make package` with COPYFILE_DISABLE |
| **v0.18.0** | Dashboard cards, data wiring, autopilot stale SHA fix |
| GH-489 | Embed branch metadata in CI fix issues, parse in poller |
| GH-471 | Dashboard: reduce card gap, fix sparkline flat-line, mute colors |
| GH-468 | Makefile: package tar.gz with COPYFILE_DISABLE + checksums |
| GH-465 | Close failed PR to unblock sequential poller |
| GH-464 | Fix CI failure from PR #463 (unused formatNumber) |
| GH-459 | Wire data loading, update handlers, replace renderMetrics |
| GH-458 | Mini-card builder + 3 card renderers |
| GH-457 | Always refresh HeadSHA from GitHub before CI check |

## Completed (2026-02-03)

| Item | What |
|------|------|
| **v0.13.2** | Config documentation (LLM classifier, rate limit) |
| **v0.13.1** | Dashboard SQLite persistence fix |
| **v0.13.0** | Major feature release |
| GH-358 | LLM-based intent classification (Claude Haiku) |
| GH-359 | GoReleaser + auto-changelog |
| GH-360 | Docs version sync script |
| GH-361 | MkDocs documentation site |
| GH-362 | Pre-commit verification instructions |
| GH-363 | Auto-enable build gate |
| GH-364 | Self-review phase before PR |
| GH-366 | Nextra documentation site |
| GH-369 | Hot reload on release update |
| GH-372 | Remove deprecated CLI flags |
| - | SQLite WAL mode for concurrent access |

## Completed (2026-02-01)

| Item | What |
|------|------|
| **v0.6.0** | Chat-like Telegram Communication |
| GH-290 | Add Research, Planning, Chat intent types |
| GH-291 | Add handleResearch() for deep analysis |
| GH-292 | Add handlePlanning() with Execute/Cancel buttons |
| GH-293 | Add handleChat() for conversational responses |
| GH-294 | Update greeting to show all interaction modes |
| GH-298 | Fix budget enforcer tests on 1st of month |

## Completed (2026-01-30)

| Item | What |
|------|------|
| **v0.4.2** | macOS upgrade fix (auto quarantine removal + signing) |
| **v0.4.1** | Autopilot PR scanning release |
| GH-257 | Autopilot: Scan existing PRs on startup |
| GH-249 | CI: Add iteration limit to ci-autofix workflow |
| **v0.4.0** | Asana, Decomposition & Research release |
| GH-248 | Warn when quality gates not configured |
| GH-247 | Remove dev environment CI skip |
| GH-246 | Add pre-merge CI verification |
| GH-245 | Telegram delivery channel for briefs |
| GH-244 | Wire brief scheduler to pilot start |
| **v0.3.3** | Homebrew formula fix release |
| GH-185 | Fix Homebrew formula validation error |
| GH-204 | Improve install.sh: Auto-configure PATH |
| GH-194 | Fix: Stop sequential processing on PR conflicts |

## Completed (2026-01-29)

| Item | What |
|------|------|
| **v0.3.2** | Autopilot superfeature release |
| GH-54 | Speed Optimization (complexity detection, model routing, timeout) |
| GH-198 | Wire autopilot controller into polling mode |
| GH-199 | Add Telegram notifications for autopilot events |
| GH-200 | Unit tests for autopilot components |
| GH-201 | Add dashboard panel for autopilot status |
| GH-203 | Fix install.sh URL (was 404) |

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

## Development Workflow

⚠️ **NEVER use local builds. Always release → upgrade.**

```bash
# Run tests
make test

# Format & lint
make fmt && make lint
```

## Release Workflow (Required for Every Change)

```bash
# 1. Build all platform binaries
make build-all

# 2. Create GitHub release
gh release create v0.X.Y \
  bin/pilot-darwin-amd64 \
  bin/pilot-darwin-arm64 \
  bin/pilot-linux-amd64 \
  bin/pilot-linux-arm64 \
  --title "v0.X.Y" \
  --notes "Release notes..."

# 3. Upgrade to new version
pilot upgrade
```

**Fresh Install:**
```bash
curl -fsSL https://raw.githubusercontent.com/alekspetrov/pilot/main/install.sh | bash
```

⚠️ **Known Issue (GH-204):** Install script doesn't auto-configure PATH. Users must add `~/.local/bin` to PATH or open new terminal.

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
- `--autopilot=ENV` - Enable autopilot mode: `dev`, `stage`, `prod`
- `--dashboard` - Launch TUI dashboard with live task monitoring
- `--telegram` - Enable Telegram polling
- `--github` - Enable GitHub polling
- `--daemon` - Run in background
- `--sequential` - Wait for PR merge before next issue (default)

### `pilot task`
- `--verbose` - Stream raw Claude Code JSON output
- `--alerts` - Enable alert engine for this task
- `--dry-run` - Show prompt without executing

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
