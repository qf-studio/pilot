# Pilot Feature Matrix

**Last Updated:** 2026-03-04 (v2.44.0)

## Legend

| Symbol | Meaning |
|--------|---------|
| ✅ | Fully implemented and working |
| ⚠️ | Implemented but not wired to CLI |
| 🚧 | Partial implementation |
| ❌ | Not implemented |

---

## Core Execution

| Feature | Status | Package | CLI Command | Config Key | Notes |
|---------|--------|---------|-------------|------------|-------|
| Task execution | ✅ | executor | `pilot task` | - | Claude Code subprocess |
| Branch creation | ✅ | executor | `--no-branch` disables | - | Auto `pilot/TASK-XXX` |
| PR creation | ✅ | executor | `--create-pr` | - | Via `gh pr create` |
| Progress display | ✅ | executor | - | - | Lipgloss visual bar |
| Navigator detection | ✅ | executor | - | - | Auto-prefix if `.agent/` exists |
| AGENTS.md loading | ✅ | executor | - | - | LoadAgentsFile reads project AGENTS.md (v0.24.1) |
| Dry run mode | ✅ | executor | `--dry-run` | - | Show prompt only |
| Verbose output | ✅ | executor | `--verbose` | - | Stream raw JSON |
| Task dispatcher | ✅ | executor | - | - | Per-project queue (GH-46) |
| Sequential execution | ✅ | executor | `--sequential` | `orchestrator.execution.mode` | Wait for PR merge before next issue |
| Self-review | ✅ | executor | - | - | Auto code review before PR push (v0.13.0) |
| Auto build gate | ✅ | executor | - | - | Minimal build gate when none configured (v0.13.0) |
| Epic decomposition | ✅ | executor | - | `decompose.enabled` | PlanEpic + CreateSubIssues for complex tasks (v0.20.2) |
| Epic scope guard | ✅ | executor | - | - | Consolidate single-package epics to prevent conflict cascade (v1.0.11) |
| Haiku subtask parser | ✅ | executor | - | - | Structured extraction via Haiku API, regex fallback (v0.21.0) |
| Self-review alignment | ✅ | executor | - | - | Verify files in issue title were actually modified (v0.33.14) |
| Nav-loop mode | ✅ | executor | - | - | Structured autonomous execution with NAVIGATOR_STATUS (v0.33.15) |
| Navigator auto-init | ✅ | executor | - | `executor.navigator.auto_init` | Auto-creates .agent/ on first task execution (v0.33.16) |
| Preflight checks | ✅ | executor | - | - | Claude available, git clean, git repo validation (v0.48.0) |
| Smart retry | ✅ | executor | - | - | Error-type-specific retry with exponential backoff (v0.51.0) |
| Acceptance criteria | ✅ | executor | - | - | Extract from issue body, include in prompts (v0.51.0) |
| Worktree isolation | ✅ | executor | - | `executor.use_worktree` | Execute in git worktree, allows uncommitted changes (v0.53.2) |
| Signal parser v2 | ✅ | executor | - | - | JSON pilot-signal blocks with validation (v0.56.0) |
| Backend-aware preflight | ✅ | executor | - | `executor.backend` | Preflight CLI check matches configured backend (claude/opencode/qwen) (v1.39.0) |
| Session resume | ✅ | executor | `--resume` | - | Self-review context continuation, ~40% token savings (v1.1.0, GH-1265) |
| PR context resume | ✅ | executor | `--from-pr` | - | CI fix session context with auto-fallback (v1.2.0, GH-1267) |
| Structured output | ✅ | executor | `--json-schema` | - | Classifiers + post-execution summary (v1.3.0, GH-1264) |
| Claude Code hooks | ✅ | executor | - | `executor.hooks` | Stop/PreToolUse/PostToolUse inline quality gates (v1.3.0) |
| Claude Code hooks v2 | ✅ | executor | - | - | Matcher-based hook format for CC 2.1.42+ (v1.14.0, GH-1366) |
| Claude Code hooks v3 | ✅ | executor | - | - | Regex matcher string, Stop hooks no matcher field (v1.50.0) |
| Stale hook cleanup | ✅ | executor | - | - | Cleanup on startup regardless of hooks config (v2.10.1, GH-1749) |
| Pre-push lint gate | ✅ | executor | - | - | Run golangci-lint before creating PRs (v1.15.0, GH-1376) |
| Navigator context bridge | ✅ | executor | - | - | Load project context (key files, components) into execution prompt (v1.18.0, GH-1387) |
| Navigator docs auto-update | ✅ | executor | - | - | Auto-update feature matrix + knowledge capture post-execution (v1.19.0, GH-1388) |
| No-decompose defense | ✅ | executor | - | - | `detectEpic` checks `no-decompose` label as defense-in-depth (v1.57.0, GH-1568) |
| Incremental lint | ✅ | executor | - | - | `golangci-lint --new-from-rev` prevents unrelated lint blocking PRs (v1.57.0, GH-1569) |
| Decompose for retry | ✅ | executor | - | `retry.decompose_on_kill` | Retry-with-decomposition on signal:killed (v2.10.0, GH-1729) |
| LLM classifier gate fix | ✅ | executor | - | - | Word count gate conditional on classifier type (v2.10.0, GH-1728) |
| Execution mode auto-switch | ✅ | executor | - | - | Scope-based auto parallel/sequential via union-find (v2.25.0) |
| Pattern compliance check | ✅ | executor | - | - | Self-review validates learned patterns from memory (v2.43.0, GH-1941) |
| Self-review pattern extraction | ✅ | executor | - | - | Extract new patterns from self-review results and store (v2.44.0, GH-1955) |
| Case-insensitive label matching | ✅ | executor | - | - | `Pilot` and `pilot` labels treated identically (v0.33.3) |
| Commit SHA git fallback | ✅ | executor | - | - | Recover SHA via git log when output parsing misses it (v0.23.3) |
| Branch switch hard fail | ✅ | executor | - | - | Abort execution on git checkout failure (v0.34.0) |
| Sub-issue PR callback | ✅ | executor | - | - | Wire sub-issue PRs back to autopilot controller chain (v0.23.1, GH-588) |
| Error classification engine | ✅ | executor | - | - | parseClaudeCodeError() routes rate_limit/api_error/timeout for retry (v0.48.0, GH-917) |
| Retry on label removal | ✅ | executor | - | - | Allow retry when pilot-failed label is manually removed (v0.33.2) |
| Code simplification pipeline | ✅ | executor | - | - | simplify.go integrated into execution pipeline for code quality (v0.61.0, GH-995) |
| Context markers | ✅ | executor | - | - | markers.go for context save points before risky operations (v0.61.0) |
| Worktree push fix | ✅ | executor | - | - | Fix git push from worktree "no such file or directory" error (v1.16.0, GH-1389) |

## Intelligence

| Feature | Status | Package | CLI Command | Config Key | Notes |
|---------|--------|---------|-------------|------------|-------|
| Complexity detection | ✅ | executor | - | - | Haiku LLM classifier: trivial/simple/medium/complex/epic (v0.30.0) |
| Model routing | ✅ | executor | - | - | Haiku (trivial), Opus 4.6 (complex), Sonnet 4.6 (simple/medium) (v0.20.0) |
| Effort routing | ✅ | executor | - | - | Map complexity to Claude thinking depth (v0.20.0) |
| LLM intent classification | ✅ | adapters/telegram | - | - | Pattern-based intent detection for Telegram messages |
| Intent judge (pipeline) | ✅ | executor | - | - | Wired into execution pipeline for task classification (v0.24.0) |
| Research subagents | ✅ | executor | - | - | Haiku-powered parallel codebase exploration |
| Drift detection | ✅ | executor | - | - | Collaboration alignment monitor with re-anchoring (v0.61.0) |
| Workflow enforcement | ✅ | executor | - | - | Embedded autonomous execution instructions (v0.61.0) |
| Sonnet 4.6 model routing | ✅ | executor | - | - | Default simple/medium tasks to Sonnet 4.6, 40% cheaper than Opus (v1.40.0, GH-1488) |
| LLM word count conditional gate | ✅ | executor | - | - | Word count threshold only applied in heuristic-only mode (v2.10.0, GH-1728) |
| Model ID codebase update | ✅ | executor | - | - | Update all stale claude-sonnet-4-5 → claude-sonnet-4-6 across defaults + tests (v1.40.1, GH-1490) |

## Input Adapters

| Feature | Status | Package | CLI Command | Config Key | Notes |
|---------|--------|---------|-------------|------------|-------|
| Telegram bot | ✅ | adapters/telegram | `pilot start --telegram` | `adapters.telegram` | Long-polling mode |
| Telegram voice | ✅ | transcription | - | `adapters.telegram.transcription` | OpenAI Whisper |
| Telegram images | ✅ | adapters/telegram | - | - | Vision support |
| Telegram chat mode | ✅ | adapters/telegram | - | - | Conversational responses (v0.6.0) |
| Telegram research | ✅ | adapters/telegram | - | - | Deep analysis to chat (v0.6.0) |
| Telegram planning | ✅ | adapters/telegram | - | - | Plan with Execute/Cancel (v0.6.0) |
| GitHub polling | ✅ | adapters/github | `pilot start --github` | `adapters.github.polling` | 30s interval |
| GitHub run issue | ✅ | adapters/github | `pilot github run` | `adapters.github` | Manual trigger |
| GitLab polling | ✅ | adapters/gitlab | `pilot start --gitlab` | `adapters.gitlab` | Full adapter with webhook support |
| Azure DevOps | ✅ | adapters/azuredevops | `pilot start --azuredevops` | `adapters.azuredevops` | Full adapter with webhook support |
| Linear webhooks | ✅ | adapters/linear | - | `adapters.linear` | Wired in pilot.go, gateway route + handler registered |
| Linear sub-issue creation | ✅ | adapters/linear | - | `adapters.linear` | CreateIssue GraphQL mutation for epic decomposition (v1.27.0) |
| Jira webhooks | ✅ | adapters/jira | - | `adapters.jira` | Wired in pilot.go, gateway route + handler + orchestrator |
| Slack Socket Mode | ✅ | adapters/slack | `pilot start --slack` | `adapters.slack.app_token` | Listen() with auto-reconnect, wired in main.go (v0.29.0) |
| Parallel GitHub polling | ✅ | adapters/github | - | `orchestrator.max_concurrent` | Goroutines + semaphore for concurrent issue processing (v0.26.1) |
| Multi-repo polling | ✅ | adapters/github | - | `projects[].github` | Poll issues from all projects with GitHub config (v0.54.0) |
| Asana adapter | ✅ | adapters/asana | `pilot start --asana` | `adapters.asana` | Task polling, state transitions on success (v0.4.x) |
| Plane.so adapter | ✅ | adapters/plane | `pilot start --plane` | `adapters.plane` | REST client, polling, webhooks, HMAC-SHA256 (v2.25.0) |
| Discord adapter | ✅ | adapters/discord | `pilot start --discord` | `adapters.discord` | Gateway WebSocket, bot commands, progress embeds (v2.25.0) |
| Linear ProcessedStore | ✅ | adapters/linear | - | - | Persistent dedup across restarts (v1.11.0, GH-1351) |
| Linear parallel execution | ✅ | adapters/linear | - | - | Goroutines + semaphore for concurrent processing (v1.11.0, GH-1355) |
| Linear orphan recovery | ✅ | adapters/linear | - | - | Recover pilot-in-progress issues on restart (v1.11.0, GH-1357) |
| Non-GitHub ProcessedStore | ✅ | adapters | - | - | Jira, Asana, AzureDevOps persistent dedup (v1.12.0, GH-1357-1359) |
| Non-GitHub parallel exec | ✅ | adapters | - | - | Parallel polling for Jira, Asana, AzureDevOps (v1.12.0) |
| Linear OnPRCreated | ✅ | adapters/linear | - | - | Wire Linear PRs to autopilot for CI monitor + auto-merge (v1.13.0, GH-1361) |
| Jira/Asana autopilot wire | ✅ | adapters | - | - | OnPRCreated + HeadSHA/BranchName for Jira + Asana (v1.19.0, GH-1397) |
| GitHub Projects V2 Board | ✅ | adapters/github | - | `adapters.github.project_board` | GraphQL board sync: Review/Done/Failed columns (v2.30.0, PR #1863) |
| Common Adapter Registry | ✅ | adapters | - | - | Unified Adapter interface, generic ProcessedStore table (v2.30.0, PR #1845) |
| Linear workspace mode | ✅ | adapters/linear | - | `adapters.linear.projects` | Project-scoped routing via project_ids mapping for multi-project setups |

## Output/Notifications

| Feature | Status | Package | CLI Command | Config Key | Notes |
|---------|--------|---------|-------------|------------|-------|
| Slack notifications | ✅ | adapters/slack | - | `adapters.slack` | Task updates |
| Telegram replies | ✅ | adapters/telegram | - | - | Auto in telegram mode |
| GitHub comments | ✅ | adapters/github | - | - | PR/issue updates |
| Rich PR comments | ✅ | main | - | - | Execution metrics (duration, tokens, cost, model) in PR comments (v0.24.1) |
| Outbound webhooks | ✅ | webhooks | `pilot webhooks` | `webhooks` | Dispatches task.started/completed/failed/progress events |
| Adapter state transitions | ✅ | adapters | - | - | Move Linear/Jira/Asana issues to Done on success (v1.19.0, GH-1396) |
| Environment context in notifications | ✅ | main | - | - | Env name included in Slack/Telegram PR notifications (v1.60.2, GH-1643) |
| Messenger refactor | ✅ | adapters | - | - | Shared Handler with TelegramMessenger/SlackMessenger (v2.25.0) |
| GitHub Review status transition | ✅ | adapters/github | - | - | Move issue to Review column on PR creation (v2.30.0, PR #1872) |
| Discord progress embeds | ✅ | adapters/discord | - | - | Rich Discord embed messages for task start/progress/complete (v2.25.0) |

## Alerts & Monitoring

| Feature | Status | Package | CLI Command | Config Key | Notes |
|---------|--------|---------|-------------|------------|-------|
| Alert engine | ✅ | alerts | `pilot task --alerts` | `alerts.enabled` | Event-based |
| Slack alerts | ✅ | alerts | - | `alerts.channels[].type=slack` | - |
| Telegram alerts | ✅ | alerts | - | `alerts.channels[].type=telegram` | - |
| Email alerts | ✅ | alerts | - | `alerts.channels[].type=email` | SMTP sender + wired to dispatcher |
| Webhook alerts | ✅ | alerts | - | `alerts.channels[].type=webhook` | - |
| PagerDuty alerts | ✅ | alerts | - | `alerts.channels[].type=pagerduty` | Wired to dispatcher, HTTP-verified tests |
| Custom rules | ✅ | alerts | - | `alerts.rules[]` | Configurable conditions |
| Cooldown periods | ✅ | alerts | - | `alerts.defaults.cooldown` | Avoid spam |
| PagerDuty escalation | ✅ | alerts | - | - | Auto-escalate after 3 retries (v0.38.0, GH-848) |
| Deadlock detector | ✅ | autopilot | - | - | Alert after 1h with no progress on PR (v0.38.0, GH-849) |
| Rate limit detection | ✅ | executor | - | - | Detect GitHub API rate limits, pause + resume at reset time (v0.34.0) |

## Quality Gates

| Feature | Status | Package | CLI Command | Config Key | Notes |
|---------|--------|---------|-------------|------------|-------|
| Quality gate runner | ✅ | quality | - | `quality.enabled` | Pre-completion checks |
| Test gates | ✅ | quality | - | `quality.gates[].type=test` | Run test commands |
| Lint gates | ✅ | quality | - | `quality.gates[].type=lint` | Run lint commands |
| Build gates | ✅ | quality | - | `quality.gates[].type=build` | Compile check |
| Retry on failure | ✅ | quality | - | `quality.max_retries` | Auto-retry with feedback |

## Memory & Learning

| Feature | Status | Package | CLI Command | Config Key | Notes |
|---------|--------|---------|-------------|------------|-------|
| Execution history | ✅ | memory | - | `memory.path` | SQLite store |
| Lifetime metrics | ✅ | memory | - | - | Token/cost/task counts persist across restarts (v0.21.2) |
| Cross-project patterns | ✅ | memory | `pilot patterns` | - | Pattern learning |
| Pattern search | ✅ | memory | `pilot patterns search` | - | Keyword search |
| Pattern stats | ✅ | memory | `pilot patterns stats` | - | Usage analytics |
| Knowledge graph | ✅ | memory | - | - | Internal only |
| Knowledge store | ✅ | memory | - | - | Experiential memory with confidence tracking (v0.61.0) |
| Profile manager | ✅ | memory | - | - | User preferences + correction learning (v0.61.0) |
| Learning loop wiring | ✅ | executor | - | - | Runner fields + setters for learning loop & pattern context (GH-1811) |
| SQLite auto-recovery | ✅ | memory | - | - | SetMaxOpenConns(1) + withRetry() exponential backoff (v1.5.2, GH-1284) |
| Pattern learning from reviews | ✅ | memory | - | - | LearnFromReview() in feedback.go, confidence boost (v2.25.0, PR #1824) |
| Anti-pattern filter | ✅ | memory | - | - | Fix anti-pattern injection filter bug in query.go (v2.43.0, PR #1948) |
| Pattern DB indexes | ✅ | memory | - | - | Indexes on cross_patterns updated_at and title for perf (v2.43.0, PR #1953) |
| Self-review pattern extractor | ✅ | memory | - | - | ExtractFromSelfReview method in pattern extractor (v2.44.0, GH-1954) |
| Execution milestones store | ✅ | memory | - | - | Milestone events stored per execution for dashboard/API (v1.55.0, GH-1600) |
| Pattern injection into prompts | ✅ | executor | - | - | Inject learned patterns into execution prompts on retry (v2.25.0, PR #1820) |

## Dashboard

| Feature | Status | Package | CLI Command | Config Key | Notes |
|---------|--------|---------|-------------|------------|-------|
| TUI dashboard | ✅ | dashboard | `--dashboard` | - | Bubbletea terminal UI |
| Token metrics card | ✅ | dashboard | - | - | Sparkline + lifetime totals (v0.18.0) |
| Cost metrics card | ✅ | dashboard | - | - | Sparkline + cost/task (v0.18.0) |
| Queue metrics card | ✅ | dashboard | - | - | Current queue depth, succeeded/failed (v0.21.2) |
| Autopilot panel | ✅ | dashboard | - | - | Live PR lifecycle status |
| Task history | ✅ | dashboard | - | - | Recent 5 completed tasks |
| Hot upgrade key | ✅ | dashboard | `u` key | - | In-place upgrade from dashboard |
| SQLite persistence | ✅ | dashboard | - | - | Metrics survive restarts (v0.21.2) |
| Queue state panel | ✅ | dashboard | - | - | 5-state: done/running/queued/pending/failed with shimmer (v0.63.0) |
| Git graph panel | ✅ | dashboard | `g` key | - | Live git graph: 3-state toggle, auto-refresh 15s, auto-prune, scrollable (v1.40.2) |
| Dashboard API | ✅ | gateway | - | - | REST endpoints: /api/v1/tasks, /api/v1/autopilot, /api/v1/history (v1.55.0, GH-1599) |
| Web dashboard | ✅ | gateway | - | - | Embedded React frontend at /dashboard with SSE log streaming (v1.56.0, GH-1609) |
| Desktop app (Wails) | ✅ | desktop | - | - | Wails v2 desktop app with React dashboard, macOS builds (v1.53.1) |
| GoReleaser desktop artifact | ✅ | ci | - | - | Separate GH Actions workflow, macOS universal binary on release (v1.54.0, GH-1614) |
| Dashboard git graph sizes | ✅ | dashboard | `g` key | - | Small/medium/large/hidden modes, auto-size by terminal width (v2.35.0, PR #1900) |
| Dashboard responsive layout | ✅ | dashboard | - | - | Stacked layout on narrow terminals, full-width panels (v2.38.0, PR #1913) |
| History dedup | ✅ | desktop | - | - | Deduplicates execution records per issue, success takes priority (v1.62.0, GH-1663) |
| WebSocket log streaming | ✅ | gateway | - | - | Real-time execution logs via WebSocket to web dashboard (v1.56.0, GH-1613) |
| Epic-aware HISTORY panel | ✅ | dashboard | - | - | HISTORY panel shows epic decomposition info + sub-issue counts (v0.22.1) |

## Replay & Debug

| Feature | Status | Package | CLI Command | Config Key | Notes |
|---------|--------|---------|-------------|------------|-------|
| Execution recording | ✅ | replay | - | - | Auto-saved |
| List recordings | ✅ | replay | `pilot replay list` | - | Filter by project/status |
| Show recording | ✅ | replay | `pilot replay show` | - | Metadata view |
| Interactive replay | ✅ | replay | `pilot replay play` | - | TUI viewer |
| Analyze recording | ✅ | replay | `pilot replay analyze` | - | Token/phase breakdown |
| Export recording | ✅ | replay | `pilot replay export` | - | HTML/JSON/Markdown |

## Reports & Briefs

| Feature | Status | Package | CLI Command | Config Key | Notes |
|---------|--------|---------|-------------|------------|-------|
| Daily briefs | ✅ | briefs | `pilot brief` | `orchestrator.daily_brief` | Scheduled |
| Weekly briefs | ✅ | briefs | `pilot brief --weekly` | - | Manual trigger |
| Slack delivery | ✅ | briefs | - | `orchestrator.daily_brief.channels` | - |
| Metrics summary | ✅ | briefs | - | `orchestrator.daily_brief.content.include_metrics` | - |

## Cost Controls

| Feature | Status | Package | CLI Command | Config Key | Notes |
|---------|--------|---------|-------------|------------|-------|
| Budget tracking | ✅ | budget | `pilot budget` | `budget` | View daily/monthly usage via memory store |
| Daily/monthly limits | ✅ | budget | `pilot task --budget` | `budget.daily_limit` | Enforcer blocks tasks when exceeded |
| Per-task limits | ✅ | budget | - | `budget.per_task` | TaskLimiter wired to executor in main.go (v0.24.1) |
| Budget in polling mode | ✅ | budget | - | - | Enforcer checks budget before picking issues in GitHub/Linear pollers |
| Alerts on overspend | ✅ | alerts | - | `alerts.rules[].type=budget` | Enforcer fires alert callbacks at thresholds |

## Team Management

| Feature | Status | Package | CLI Command | Config Key | Notes |
|---------|--------|---------|-------------|------------|-------|
| Team CRUD | ✅ | teams | `pilot team` | `teams` | Wired to Pilot struct + `--team` flag (GH-633) |
| Permissions | ✅ | teams | `--team` | `team.enabled` | Pre-execution RBAC check in Runner (GH-634) |
| Project mapping | ✅ | teams | `--team-member` | `team.member_email` | Project access validation in poller + CLI (GH-635) |

## Infrastructure

| Feature | Status | Package | CLI Command | Config Key | Notes |
|---------|--------|---------|-------------|------------|-------|
| Cloudflare tunnel | ✅ | tunnel | `pilot start --tunnel` | `tunnel` | Auto-start tunnel, prints webhook URLs |
| Gateway HTTP | ✅ | gateway | `pilot start` | `gateway` | Internal server, wired in main.go |
| Gateway WebSocket | ✅ | gateway | - | - | Session management active in gateway |
| Health checks | ✅ | health | `pilot doctor` | - | System validation, 32 unit tests |
| OpenCode backend | ✅ | executor | `--backend opencode` | `executor.backend` | HTTP/SSE alternative to Claude Code |
| K8s health probes | ✅ | gateway | - | - | `/ready` and `/live` endpoints for Kubernetes (v0.37.0) |
| Prometheus metrics | ✅ | gateway | - | - | `/metrics` endpoint in Prometheus text format (v0.37.0) |
| JSON structured logging | ✅ | - | - | `logging.format` | Optional JSON log output mode (v0.38.0) |
| Qwen Code backend | ✅ | executor | `--backend qwen` | `executor.backend` | Alibaba Qwen Code CLI with stream-json (v1.9.0, GH-1314) |
| Docker support | ✅ | - | - | - | Dockerfile + deployment guide (v1.46.0) |
| Helm chart | ✅ | - | - | - | Kubernetes Helm chart for production deployment (v1.46.0) |
| PowerShell installer | ✅ | install | - | - | Windows PowerShell install script (`install.ps1`) |
| Gateway in polling mode | ✅ | gateway | - | - | HTTP server starts in background during polling for desktop/web (v1.62.0, GH-1662) |
| Gateway budget nil fix | ✅ | gateway | - | - | Fix nil dereference when budget disabled in gateway mode (v2.43.0, GH-1935) |
| Gateway learning loop | ✅ | gateway | - | - | Learning system init in gateway mode, mirrors polling mode (v2.43.0, GH-1935) |
| Wiring verification tests | ✅ | testing | - | - | Wiring harness + completeness checks for all adapters (v2.39.0, PR #1931) |
| Adapter registry completeness test | ✅ | testing | - | - | Test that all registered adapters satisfy interface (v2.40.0, PR #1932) |
| Runner accessor methods | ✅ | executor | - | - | Has* introspection methods for test + wiring verification (v2.39.0, PR #1930) |
| Docs version sync CI | ✅ | ci | - | - | Workflow closes previous version-sync PRs before creating new (v2.38.11) |
| GoReleaser CI builds | ✅ | ci | - | - | Binary builds + uploads on release tag (v0.24.1) |
| install.sh | ✅ | install | - | - | curl-pipe installer for Linux/macOS (v0.3.x) |
| Homebrew formula | ✅ | install | `brew install` | - | Homebrew tap formula for macOS (v0.3.x) |
| Integration tests (patterns) | ✅ | testing | - | - | Integration tests for self-review pattern accumulation (v2.44.0, GH-1956) |
| Handler refactoring | ✅ | adapters | - | - | handleIssueGeneric() consolidates 5 adapter flows (v2.30.0, PR #1856) |
| Config validation preflight | ✅ | executor | - | - | Validate config fields on startup before accepting issues (v0.48.0) |
| GitLab docs sync CI | ✅ | ci | - | - | GitHub Action syncs docs/ to GitLab repo on merge to main (v0.23.2) |
| Poller registration refactor | ✅ | main | - | - | Extract poller registration pattern from main.go (v2.30.0, PR #1857) |
| Secrets check | ✅ | - | `make check-secrets` | - | Scan test files for realistic secret patterns before push |

## Approval Workflows

| Feature | Status | Package | CLI Command | Config Key | Notes |
|---------|--------|---------|-------------|------------|-------|
| Approval engine | ✅ | approval | `--env=prod` | `approval` | Wired to autopilot controller |
| Slack approval | ✅ | approval | - | `adapters.slack.approval` | Interactive messages, registered in main.go |
| Telegram approval | ✅ | approval | - | - | Inline keyboards, registered in main.go |
| Rule-based triggers | ✅ | approval | - | `approval.rules[]` | RuleEvaluator with 4 matchers wired into Manager (GH-636) |

## Autopilot (v0.19.1+)

| Feature | Status | Package | CLI Command | Config Key | Notes |
|---------|--------|---------|-------------|------------|-------|
| Autopilot controller | ✅ | autopilot | `--env=ENV` | - | Orchestrates PR lifecycle |
| CI monitoring | ✅ | autopilot | - | - | Polls check status with HeadSHA refresh (v0.18.0) |
| Auto-merge | ✅ | autopilot | - | - | Merges after CI/approval |
| Feedback loop | ✅ | autopilot | - | - | Creates fix issues for CI failures |
| CI fix on original branch | ✅ | autopilot | - | - | `autopilot-meta` comment embeds branch (v0.19.1) |
| PR scanning on startup | ✅ | autopilot | - | - | Resumes tracking existing PRs |
| Telegram notifications | ✅ | autopilot | - | - | PR status updates |
| Dashboard panel | ✅ | dashboard | `--dashboard` | - | Live autopilot status |
| Environment gates | ✅ | autopilot | - | - | dev/stage/prod behavior |
| Tag-only release | ✅ | autopilot | - | - | CreateTag() → GoReleaser handles full release (v0.24.1) |
| SQLite state persistence | ✅ | autopilot | - | - | Crash recovery for PR states, processed issues (v0.30.0) |
| Merge conflict detection | ✅ | autopilot | - | - | Detect conflicts before CI wait (v0.30.0) |
| Per-PR circuit breaker | ✅ | autopilot | - | - | Independent failure tracking per PR (v0.34.0) |
| Stale label cleanup | ✅ | adapters/github | - | - | Clean pilot-failed labels, allow retry (v0.34.0) |
| GitHub API retry | ✅ | adapters/github | - | - | Exponential backoff, Retry-After header respect (v0.34.0) |
| CI auto-discovery | ✅ | autopilot | - | - | Auto-detect check names from GitHub API (v0.41.0) |
| Stagnation monitor | ✅ | executor | - | - | State hash tracking, escalation: warn → pause → abort (v0.56.0) |
| URL-encode branch names | ✅ | adapters/github | - | - | `url.PathEscape(branch)` in DeleteBranch/GetBranch — fixes 404 on slash branches (v1.28.0) |
| Branch cleanup on PR close | ✅ | autopilot | - | - | Delete remote branches on PR close/fail, not just merge (v1.35.0) |
| Desktop app release | ✅ | ci | - | - | Separate GH Actions workflow builds Wails macOS universal binary, uploads to release (v1.41.0) |
| Jira/Asana autopilot | ✅ | autopilot | - | - | OnPRCreated wired for Jira + Asana adapters (v1.19.0, GH-1397) |
| CI error logs in fix issues | ✅ | autopilot | - | - | Embed CI error output in generated fix issues (v1.58.0, GH-1566) |
| Branch lineage circuit breaker | ✅ | autopilot | - | - | Circuit breaker keyed by branch lineage, not PR ID (v1.58.0, GH-1567) |
| Environment config | ✅ | autopilot | - | `environments` | EnvironmentConfig + ResolvedEnv(), no hardcoded env checks (v1.59.0, GH-1640) |
| Post-merge deployer | ✅ | autopilot | - | `environments.*.deploy` | Webhook and branch-push deployment triggers after merge (v1.60.0, GH-1641) |
| CLI `--env` flag | ✅ | main | `--env=stage` | - | Renamed from `--autopilot`, updated onboarding + config (v1.60.1, GH-1642) |
| Prod auto-approve safety | ✅ | autopilot | - | - | Block auto-merge when pre_merge approval disabled in prod (v1.61.0) |
| Auto-rebase on conflict | ✅ | autopilot | - | - | GitHub UpdatePullRequestBranch API before close-and-retry (v2.25.0) |
| CI fix dependencies | ✅ | autopilot | - | - | `Depends on: #N` annotations in generated fix issues (v2.25.0) |
| Board sync on merge | ✅ | autopilot | - | - | Move issue to Done column on PR merge (v2.30.0, PR #1864) |
| Label cleanup on retry | ✅ | adapters/github | - | - | Remove `pilot-failed` on successful retry — accurate metrics (v1.8.1, GH-1302) |
| Autopilot CI optimization | ✅ | autopilot | - | - | Cached GetPR, API failure escalation, dynamic 10s/60s poll interval (v1.8.5, GH-1304) |
| Stale branch detection | ✅ | autopilot | - | - | Detect and clean stale remote branches before execution (v0.48.0) |

## Epic Management

| Feature | Status | Package | CLI Command | Config Key | Notes |
|---------|--------|---------|-------------|------------|-------|
| Epic decomposition engine | ✅ | executor | - | `decompose.enabled` | Detects complex tasks, plans + creates sub-issues (v0.20.2) |
| Haiku-powered subtask extraction | ✅ | executor | - | - | LLM structured extraction, regex fallback (v0.21.0) |
| Epic scope consolidation | ✅ | executor | - | - | Single-package epics consolidated → one task, no conflict cascade (v1.0.11) |
| Sub-issue PR wiring | ✅ | executor | - | - | Sub-issue PR callbacks chain back to autopilot controller (v0.23.1) |
| Linear sub-issue creation | ✅ | adapters/linear | - | `adapters.linear` | CreateIssue GraphQL mutation for decomposed epics (v1.27.0) |
| Decompose on retry | ✅ | executor | - | `retry.decompose_on_kill` | Retry via decomposition when task killed (signal:killed) (v2.10.0, GH-1729) |

## Self-Management

| Feature | Status | Package | CLI Command | Config Key | Notes |
|---------|--------|---------|-------------|------------|-------|
| Version check | ✅ | upgrade | `pilot version` | - | Shows current |
| Auto-upgrade | ✅ | upgrade | `pilot upgrade` | - | Downloads latest |
| Hot upgrade | ✅ | upgrade | `u` key in dashboard | - | Graceful drain + restart, no orphaned tasks (v0.18.0, v0.63.0) |
| Config init | ✅ | config | `pilot init` | - | Creates default |
| Setup wizard | ✅ | main | `pilot setup` | - | Interactive config |
| Shell completion | ✅ | main | `pilot completion` | - | bash/zsh/fish |
| Zip archive support | ✅ | upgrade | - | - | Windows self-upgrade handles .zip archives |
| Pipeline hardening | ✅ | executor | - | - | 4 correctness checks: constants, parity, coverage, dropped features (v1.10.0, GH-1321) |
| Pre-commit hooks | ✅ | - | `make install-hooks` | - | Git hooks for secret scanning + lint |
| Qwen Code bug fixes | ✅ | executor | `--backend qwen` | - | 5x pricing correction, CLI version check, session_not_found handling (v1.9.2, GH-1316) |

---

## Feature Summary

| Category | ✅ Working | ⚠️ Implemented | 🚧 Partial | ❌ Missing |
|----------|-----------|----------------|-----------|-----------|
| Core Execution | 50 | 0 | 0 | 0 |
| Intelligence | 11 | 0 | 0 | 0 |
| Input Adapters | 29 | 0 | 0 | 0 |
| Output/Notifications | 10 | 0 | 0 | 0 |
| Alerts & Monitoring | 11 | 0 | 0 | 0 |
| Quality Gates | 5 | 0 | 0 | 0 |
| Memory & Learning | 16 | 0 | 0 | 0 |
| Dashboard | 19 | 0 | 0 | 0 |
| Replay & Debug | 6 | 0 | 0 | 0 |
| Reports & Briefs | 4 | 0 | 0 | 0 |
| Cost Controls | 5 | 0 | 0 | 0 |
| Team Management | 3 | 0 | 0 | 0 |
| Infrastructure | 28 | 0 | 0 | 0 |
| Approval Workflows | 4 | 0 | 0 | 0 |
| Autopilot | 33 | 0 | 0 | 0 |
| Epic Management | 6 | 0 | 0 | 0 |
| Self-Management | 10 | 0 | 0 | 0 |
| **Total** | **250** | **0** | **0** | **0** |

---

## Usage Patterns

### Minimal Setup (Task Execution Only)
```yaml
# ~/.pilot/config.yaml
projects:
  - name: my-project
    path: ~/code/my-project
    navigator: true
```
```bash
pilot task "Add user authentication"
```

### Telegram Bot Mode
```yaml
adapters:
  telegram:
    enabled: true
    bot_token: "your-bot-token"
    transcription:
      provider: openai
      openai_key: "your-openai-key"
```
```bash
pilot start --telegram --project ~/code/my-project
```

### GitHub Polling Mode
```yaml
adapters:
  github:
    enabled: true
    repo: "owner/repo"
    polling:
      enabled: true
      interval: 30s
      label: "pilot"
```
```bash
# Start with GitHub polling, picks up issues labeled "pilot"
pilot start --github
# Or combine with Telegram
pilot start --telegram --github
```

### Autopilot Mode (v1.59.0+)
```bash
# Fast iteration - auto-merge without CI
pilot start --env=dev --telegram --github

# Balanced - wait for CI, then auto-merge
pilot start --env=stage --telegram --github --dashboard

# Production - CI + manual approval required
pilot start --env=prod --telegram --github --dashboard
```

### Full Production Setup
```yaml
gateway:
  host: "0.0.0.0"
  port: 9090

adapters:
  telegram: { enabled: true, bot_token: "..." }
  github: { enabled: true, repo: "...", polling: { enabled: true } }
  slack: { enabled: true, bot_token: "..." }

alerts:
  enabled: true
  channels:
    - name: slack-ops
      type: slack
      slack: { channel: "#pilot-alerts" }
  rules:
    - name: task-failed
      type: task_failed
      channels: [slack-ops]

quality:
  enabled: true
  gates:
    - name: tests
      type: test
      command: "make test"
    - name: lint
      type: lint
      command: "make lint"
```
