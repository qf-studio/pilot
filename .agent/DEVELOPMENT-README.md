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
// Navigator-aware prompt structure for medium/complex tasks
if useNavigator {
    sb.WriteString("Use /nav-loop mode for this task.\n\n")  // <- NEVER REMOVE
    // ... PILOT EXECUTION MODE override for CLAUDE.md rules
}
```

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
| `.agent/.context-markers/` | Resume after break |

## Current State

**Current Version:** v1.27.0 | **156 features working**

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
| Dashboard TUI | Done | Sparkline cards, muted palette, SQLite persistence, epic-aware HISTORY, state-aware QUEUE |
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
| Slack Socket Mode | Done | Full inbound handler wired into main.go lifecycle (v0.33.13) |
| Self-Review Alignment | Done | Verifies files in issue title were actually modified (v0.33.14) |
| Nav-Loop Execution | Done | Explicit /nav-loop mode for structured autonomous work (v0.33.15) |
| Navigator Auto-Init | Done | Auto-creates .agent/ on first task execution (v0.33.16) |
| Stale Label Cleanup | Done | Cleans `pilot-failed` labels, allows retry (v0.34.0) |
| Per-PR Circuit Breaker | Done | Independent failure tracking per PR (v0.34.0) |
| GitHub API Retry | Done | Exponential backoff on transient failures (v0.34.0) |
| Branch Switch Hard Fail | Done | Abort execution on checkout failure (v0.34.0) |
| CI Auto-Discovery | Done | Auto-detect CI check names from GitHub API (v0.41.0) |
| K8s Health Probes | Done | `/ready` and `/live` endpoints for Kubernetes (v0.37.0) |
| Prometheus Metrics | Done | `/metrics` endpoint for observability (v0.37.0) |
| JSON Structured Logging | Done | Optional JSON log output mode (v0.38.0) |
| Smart Retry | Done | Error-type-specific retry with backoff (v0.51.0) |
| Acceptance Criteria | Done | Extract and include in prompts (v0.51.0) |
| Worktree Isolation | Done | Execute in git worktree, allows uncommitted changes (v0.53.2) |
| Multi-Repo Polling | Done | Poll issues from all projects with GitHub config (v0.54.0) |
| Signal Parser v2 | Done | JSON pilot-signal blocks with validation (v0.56.0) |
| Stagnation Monitor | Done | State hash tracking, escalation: warn → pause → abort (v0.56.0) |
| Queue State Dashboard | Done | 5-state QUEUE panel: done/running/queued/pending/failed with shimmer animation |
| Epic Scope Guard | Done | Prevent serial conflict cascade — consolidate single-package epics (v1.0.11) |
| Session Resume | Done | `--resume` for self-review context continuation, ~40% token savings (v1.1.0) |
| PR Context Resume | Done | `--from-pr` for CI fix session context with auto-fallback (v1.2.0) |
| Structured Output | Done | `--json-schema` for classifiers + post-execution summary (v1.3.0) |
| Claude Code Hooks | Done | Stop/PreToolUse/PostToolUse hooks for inline quality gates (v1.3.0) |
| Label Cleanup on Retry | Done | Remove `pilot-failed` on successful retry — accurate metrics (v1.8.1) |
| Autopilot Optimization | Done | Cached GetPR, API failure escalation, dynamic CI poll interval (v1.8.5) |
| Qwen Code Backend | Done | Third executor backend — Alibaba Qwen Code CLI with stream-json (v1.9.0) |
| Qwen Bug Fixes | Done | Pricing correction, version check, session_not_found, resume fallback (v1.9.2) |
| Pipeline Hardening | Done | External correctness checks: constants, parity, coverage, dropped features (v1.10.0) |
| Linear ProcessedStore | Done | Persistent dedup for Linear issues across restarts (v1.11.0) |
| Linear Parallel Execution | Done | Goroutines + semaphore for Linear poller (v1.11.0) |
| Linear Orphan Recovery | Done | Recover pilot-in-progress issues on restart (v1.11.0) |
| Non-GitHub ProcessedStore | Done | Jira, Asana, AzureDevOps persistent dedup (v1.12.0) |
| Non-GitHub Parallel Exec | Done | Parallel polling for Jira, Asana, AzureDevOps (v1.12.0) |
| Linear OnPRCreated | Done | Wire Linear PRs to autopilot for CI monitor + auto-merge (v1.13.0) |
| Claude Code Hooks v2 | Done | Migrate to matcher-based hook format for CC 2.1.42+ (v1.14.0) |
| Pre-Push Lint Gate | Done | Run golangci-lint before creating PRs (v1.15.0) |
| Worktree Push Fix | Done | Fix git push from worktree "no such file or directory" (v1.16.0) |
| Auto-Delete Branches | Done | Auto-delete remote branches after PR merge (v1.17.0) |
| Navigator Context Bridge | Done | Load project context (key files, components) into execution prompt (v1.18.0) |
| Navigator Docs Update | Done | Auto-update feature matrix + knowledge capture post-execution (v1.19.0) |
| Adapter State Transitions | Done | Transition Linear/Jira/Asana issues to Done on success (v1.19.0) |
| Jira/Asana Autopilot Wire | Done | Wire OnPRCreated for Jira + Asana, add HeadSHA/BranchName (v1.19.0) |

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

### Stability Plan ✅ Complete (11/11)

**Full plan: `.agent/tasks/STABILITY-PLAN.md`**

Goal: Raise autonomous reliability from 3/10 to 8/10. **Achieved: 8/10**

| Phase | Items | PRs |
|-------|-------|-----|
| Phase 1 | Stale label cleanup, per-PR breaker, API retry, branch fail, rate limit | #844, #841, #843, #842, #32 |
| Phase 2 | Conflict detection, auto-rebase, sequential sub-issues | #740, GH-742/743 |
| Phase 3 | SQLite state, LLM classifier, metrics | #737, #739 |

### Slack Socket Mode (v0.33.13 — DONE)

| Issue | What | Status |
|-------|------|--------|
| GH-644 | Extract shared intent package | Queued |
| GH-650 | Slack handler with 5 interaction modes | Done (PR #831) |
| GH-651 | Slack MemberResolver RBAC | Queued |
| GH-652 | Wire Slack into pilot.go + main.go | **Done (v0.33.13)** — `--slack` flag works |

**Usage**: `pilot start --slack` enables Socket Mode. Requires `app_token` in config.

### Backlog

| Priority | Topic | Why |
|----------|-------|-----|
| P2 | Docs v2: Nextra 4 migration | Attempted, failed — docs still on Nextra v2 |
| P3 | Docs refresh for 107 features | Teams RBAC, approval rules not documented |

---

## Completed Log

### 2026-02-17

| Item | What |
|------|------|
| **v1.27.0** | Harden GH-1388: dedup modifiedFiles, case-insensitive feat( check, robust table insertion (no anchor dependency) |
| **v1.27.0** | Use build version in UpdateFeatureMatrix instead of hardcoded v1.0.0 — Version field on BackendConfig |
| **v1.19.0** | Adapter state transitions: Linear `UpdateIssueState`, Jira `TransitionIssueTo`, Asana `CompleteTask` on success (GH-1396) |
| **v1.19.0** | Autopilot wiring: OnPRCreated for Jira + Asana, HeadSHA/BranchName in result types (GH-1397) |
| **v1.19.0** | Navigator post-execution docs update: feature matrix, knowledge capture, context markers (GH-1388) |
| Bug fix | APP-55 Linear retry — unblocked from processed store, PR created on aso-generator |

### 2026-02-16

| Item | What |
|------|------|
| **v1.18.0** | Navigator context bridge: load project context (key files, components, structure) into execution prompt (GH-1387) |
| **v1.17.0** | Auto-delete remote branches after PR merge (GH-1383) |
| **v1.16.0** | Fix git push from worktree "no such file or directory" (GH-1389) |
| **v1.15.0** | Pre-push lint gate: run golangci-lint before creating PRs (GH-1376) |
| **v1.14.0** | Claude Code hooks v2: migrate to matcher-based format for CC 2.1.42+ (GH-1366) |
| **v1.13.0** | Wire Linear PRs to autopilot controller for CI monitoring + auto-merge (GH-1361) |
| **v1.12.0** | Non-GitHub adapter parity: ProcessedStore + parallel exec for Jira, Asana, AzureDevOps (GH-1357-1359) |
| **v1.11.0** | Linear adapter parity: ProcessedStore, parallel execution, orphan recovery (GH-1351, GH-1355, GH-1357) |
| Cleanup | Removed MkDocs integration — unused, replaced by Nextra (GH-1385) |
| Diagnostics | APP-55 failure analysis: identified missing adapter state transitions |

### 2026-02-15

| Item | What |
|------|------|
| **v1.10.0** | Pipeline hardening: 4 external correctness checks — constants sanity, cross-file parity, coverage delta, dropped features (GH-1321) |
| **v1.9.2** | Qwen Code bug fixes: pricing 5x correction, CLI version check, session_not_found, --resume fallback (GH-1316) |
| **v1.9.0** | Qwen Code backend engine — third executor backend (GH-1314) |
| Docs | Multi-backend documentation page: Claude Code, Qwen Code, OpenCode (GH-1324) |

### 2026-02-14

| Item | What |
|------|------|
| **v1.8.5** | Autopilot optimization: cached GetPR, API failure escalation, dynamic CI poll interval (GH-1304) |
| **v1.8.1** | Remove `pilot-failed` label on successful retry — fixes inflated failure metrics (GH-1302) |
| **v1.8.0** | Docs: configuration reference + Navigator cross-reference for SDK features (GH-1289) |
| **v1.7.0** | Docs: example config updated with new fields (GH-1289 sub-issue) |
| **v1.6.0** | Docs: tunnel setup guide + GitHub API rate limiting guide (GH-1290, GH-1291) |
| **v1.5.2** | SQLite auto-recovery: `SetMaxOpenConns(1)` + `withRetry()` backoff (GH-1284) |
| **v1.5.1** | `parseAutopilotPR()` test + configured command in `getPostExecutionSummary()` (GH-1280, GH-1281) |
| **v1.3.0** | Structured output (`--json-schema`) + Claude Code hooks system (GH-1264, GH-1266) |
| **v1.2.0** | PR context resume (`--from-pr`) for CI fix session continuity (GH-1267) |
| **v1.1.0** | Session resume (`--resume`) for self-review token savings ~40% (GH-1265) |
| **v1.0.11** | Epic scope guard — prevent serial conflict cascade (GH-1265) |
| Diagnostics | Full v1.0.11→v1.5.0 architecture review, SQLite BUSY root cause analysis |
| Cleanup | Closed 8 stuck sub-issues, 21 stale dual-labeled issues identified |

### 2026-02-13

| Item | What |
|------|------|
| Queue states | State-aware QUEUE panel: ✓done ●running ◌queued ·pending ✗failed with shimmer animation, fixed monitor state transitions |
| **v0.63.0** | Fix monotonic progress — dashboard no longer jumps backwards (90%→85%→95%) |
| **v0.61.0** | Pricing fix ($5/$25 not $15/$75), LLM effort classifier, knowledge store, drift detection, simplify.go, workflow enforcement |
| **v0.60.0** | Preflight skip `git_clean` when worktree enabled (GH-1002) |
| Nav port | TASK-01 scaffolding complete (8/8 files), wiring pending (GH-1026) |
| Cleanup | Closed 4 stuck `pilot-failed` issues, resolved serial conflict cascade |

### 2026-02-12

| Item | What |
|------|------|
| **v0.51.0** | Smart retry by error type (rate_limit, api_error, timeout) + acceptance criteria in prompts |
| **v0.48.0** | Phase 1 reliability: config validation, stale branch detection, preflight checks, error classification |
| **v0.41.0** | CI auto-discovery — detect check names from GitHub API, no manual config needed |
| **v0.40.0** | Controller wiring for CI auto-discovery |
| **v0.39.0** | CIChecksConfig struct, example config updates |
| **v0.38.0** | JSON structured logging, PagerDuty escalation, deadlock detector |
| **v0.37.0** | K8s health probes (`/ready`, `/live`), Prometheus `/metrics`, config fix |
| Bug fixes | Hot upgrade doesn't reload config, stale `pilot-in-progress` labels, dependency ordering |

### 2026-02-11

| Item | What |
|------|------|
| **v0.33.16** | Navigator auto-init — creates `.agent/` automatically on first task execution |
| **v0.33.15** | Explicit `/nav-loop` mode for structured autonomous execution with NAVIGATOR_STATUS |
| **v0.33.14** | Self-review alignment check — verifies files in issue title were actually modified |
| **v0.33.13** | Slack Socket Mode wired into main.go — `--slack` flag now works |
| **v0.33.3** | Case-insensitive label matching — `Pilot` and `pilot` now work the same |
| **v0.33.2** | Allow retry when `pilot-failed` label is removed (poller no longer marks failed as processed) |
| Issue cleanup | Closed 9 `pilot-done` issues, 2 stale CI fix issues |
| Reliability | 4 fixes addressing incomplete wiring pattern (GH-652 lesson learned) |
| **v0.34.0** | Stability Plan complete (11/11) — 4 final features merged |
| **v0.34.1** | Stale `pilot-failed` cleanup (PR #844), per-PR circuit breaker (PR #841), API retry (PR #843), branch hard fail (PR #842) |
| Stability | Target reliability 8/10 achieved — Pilot can run 24h+ unattended |

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

### QUEUE Panel States

The dashboard QUEUE panel shows 5 distinct task states with unique visual indicators:

| State | Icon | Bar Style | Meta | Color |
|-------|------|-----------|------|-------|
| done | `✓` | Solid green `██████████████` | `done` | Sage green `#7ec699` |
| running | `●` | Standard `███████░░░░░░░` | `50%` | Steel blue `#7eb8da` (pulses) |
| queued | `◌` | Shimmer `░▒▓▒░` animated | `queue` | Mid gray `#8b949e` |
| pending | `·` | Empty spaces | `wait` | Slate `#3d4450` |
| failed | `✗` | Frozen at fail point | `fail` | Dusty rose `#d48a8a` |

**State transitions:**
- `monitor.Register()` → pending (poller picks up issue)
- `monitor.Queue()` → queued (dispatcher accepts task)
- `monitor.Start()` → running (runner.Execute actually begins)
- `monitor.Complete()` → done (PR created)
- `monitor.Fail()` → failed

Items sorted by state priority: done → running → queued → pending → failed.
Queued items have staggered shimmer animation (offset per queue position).

**Key files:** `internal/dashboard/tui.go` (rendering), `internal/executor/monitor.go` (state machine), `cmd/pilot/main.go` (state wiring)

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
