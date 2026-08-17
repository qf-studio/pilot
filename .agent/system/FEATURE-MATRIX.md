# Pilot Feature Matrix

**Last Updated:** 2026-08-17 (v2.259.1 + PR#4865)

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
| OOM smart-retry | ✅ | executor | - | `executor.retry.oom_killed` | Retry OOM-killed subprocess once after 10s (GH-3028, v2.147.0) |
| RSS telemetry | ✅ | executor | - | `executor.subprocess_limits` | Peak/final RSS sampled per execution; stored in DB + shown in history (GH-3028, v2.147.0) |
| Subprocess memory cap | ✅ | executor | - | `executor.subprocess_limits.enabled` | cgroup v2 `memory.max` leaf on Linux + cooperative `NODE_OPTIONS=--max-old-space-size` everywhere; degrades to telemetry-only if cgroup v2 undelegated (default off; tune after RSS baseline). Replaces RLIMIT_AS, which broke 100% of Linux executor subprocesses (GH-3028, GH-4401) |
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
| Per-repo workflow.yaml | ✅ | executor/workflow | - | - | `.pilot/workflow.yaml` overrides max_turns, reasoning_effort, policy, appends prompt (TASK-304, GH-3203) |
| Workflow lifecycle hooks | ✅ | executor/workflow | - | - | `after_create`, `before_run`, `after_run`, `before_remove` bash scripts in `.pilot/workflow.yaml` (TASK-305, GH-3203) |
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
| Acceptance criteria in self-review | ✅ | executor | - | - | Verify acceptance criteria during self-review prompt (v2.48.0, PR #1976) |
| Errcheck lint guidance | ✅ | executor | - | - | Add errcheck lint rules for generated test code (v2.25.0, PR #1802) |
| Scope utilities extraction | ✅ | executor | - | - | Extract directory-scope utilities into scope.go (v2.25.0, PR #1807) |
| Scope-overlap guard | ✅ | executor | - | - | Scope-overlap guard in parallel dispatch prevents file conflicts (v2.25.0, PR #1808) |
| Default sequential mode | ✅ | executor | - | - | Default execution mode is sequential, not parallel (v2.25.0, PR #1804) |
| Token limit check accessor | ✅ | executor | - | - | HasTokenLimitCheck accessor for wiring verification (v2.42.0, PR #1937) |

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
| CI error pattern extraction | ✅ | memory | - | - | Enhance CI error pattern extraction and categorization (v2.51.0, PR #1980) |
| CI-specific error matchers | ✅ | memory | - | - | Add CI-specific error matchers to PatternExtractor (v2.47.0, PR #1973) |
| CI pattern confidence boost | ✅ | memory | - | - | Confidence boosting for recurring CI patterns (v2.48.0, PR #1975) |
| CI log learning pipeline | ✅ | autopilot | - | - | Wire CI log learning into autopilot controller and feedback loop (v2.50.0, PR #1977) |
| Expanded pattern extractors (11 categories) | ✅ | memory | - | - | Added API design, concurrency, config wiring, test patterns, performance, security matchers (v2.54.0, GH-1989) |

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
| GitHub Projects V2 Board Source | ✅ | adapters/github | - | `adapters.github.project_board.source_enabled` | Pull work FROM a board column (FindIssuesFromProject); opt-in via source_enabled/source_status (GH-3228) |
| Common Adapter Registry | ✅ | adapters | - | - | Unified Adapter interface, generic ProcessedStore table (v2.30.0, PR #1845) |
| Linear workspace mode | ✅ | adapters/linear | - | `adapters.linear.projects` | Project-scoped routing via project_ids mapping for multi-project setups |
| Plane.so state transitions | ✅ | adapters/plane | - | - | State transitions and PR comments on Plane.so issues (v2.25.0, PR #1843) |
| Plane.so webhooks | ✅ | adapters/plane | - | - | Webhook handler with HMAC-SHA256 signature verification (v2.25.0, PR #1842) |
| Plane.so ProcessedStore | ✅ | adapters/plane | - | - | Persistent dedup for Plane.so in autopilot StateStore (v2.25.0, PR #1839) |
| Discord adapter wiring | ✅ | main | `--discord` | `adapters.discord` | Wire Discord poller, config, and CLI flag in main.go (v2.30.0, PR #1882) |
| Asana CompleteTask callback | ✅ | adapters/asana | - | - | Wire Asana CompleteTask on successful PR creation (v2.10.0, PR #1720) |
| Telegram memory store | ✅ | adapters/telegram | - | - | Wire memory store to Telegram HandlerConfig (v2.25.0, PR #1754) |
| Web chat API | ✅ | adapters/web, gateway | - | `adapters.chat` | Operator chat HTTP transport for console dashboard: `POST /api/v1/chat/messages` + `GET /api/v1/chat/conversations/{id}/events`, backed by comms.Handler via a per-conversation seq-numbered in-memory event buffer (500-event cap, 1h expiry); dispatches on the daemon context so tasks survive request cancellation; rejects approve/reject-shaped callbacks (approvals route through `/api/v1/approvals/{requestId}/decision` instead) (GH-4835) |

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
| Comms Messenger interface | ✅ | comms | - | - | Unified Messenger interface with shared helpers (v2.25.0, PR #1770) |
| Comms shared Handler | ✅ | comms | - | - | Shared Handler with HandleMessage, intent dispatch, task lifecycle (v2.25.0, PR #1790) |
| TelegramMessenger | ✅ | comms | - | - | TelegramMessenger implementing comms.Messenger (v2.25.0, PR #1791) |
| SlackMessenger | ✅ | comms | - | - | SlackMessenger implementing comms.Messenger (v2.25.0, PR #1780) |
| Telegram Transport layer | ✅ | adapters/telegram | - | - | Transport layer extraction, handler shrunk to ~200 lines (v2.25.0, PR #1777) |
| Comms shared types | ✅ | comms | - | - | ProjectSource, RateLimiter shared types (v2.25.0, PR #1766) |
| Comms intent consolidation | ✅ | comms | - | - | Conversation store + LLM classifier consolidated into intent package (v2.25.0, PR #1789) |
| Comms main.go wiring | ✅ | main | - | - | Updated main.go wiring for unified comms.Handler (v2.25.0, PR #1775) |
| Comms BuildHandler factory | ✅ | comms | - | `llm_classifier` under slack/discord | Single assembly point for comms.HandlerConfig; all 5 adapter call sites route through it; Slack/Discord/gateway reach classifier parity with Telegram (v2.193.0, PR #3645) |
| Bot direct-LLM answer primitive | ✅ | internal/llm | - | `bot.api_key` | `llm.Client.Answer()` — direct Anthropic API, no executor spawn (~1-2s) (v2.194.2, GH-3665) |
| Bot Responder + fast chat | ✅ | comms | - | `bot.enabled`, `bot.model` | `comms.Responder`, `handleGreeting`/`handleChat` fast path; executor fallback when disabled (v2.194.2, GH-3665) |
| Bot grounded Q&A retrieval | ✅ | comms | - | `bot.retrieval` | `Responder.Answer()` + bounded file retrieval; falls back to executor for too-broad questions (v2.194.2, GH-3671) |
| Bot conversational issue intake | ✅ | comms | - | `bot.enabled`, GitHub adapter | `Responder.DraftIssue()` + `handleIssueIntake`; creates GitHub issue with `pilot` label (v2.194.2, GH-3691) |
| Bot persona + voice scaffold | ✅ | comms, config | - | `bot.persona`, `bot.voice.enabled` | Persona in Chat/Answer/DraftIssue; VoiceText seam in HandleMessage; fully commented example config (v2.194.2, GH-3673) |

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
| Lane-starvation detector | ✅ | autopilot/alerts | - | `alerts.rules[].condition.lane_starvation_poll_cycles` | Alert when a project lane has open non-blocked pilot-labeled issues but 0 queued/running executions for N consecutive poll cycles (default 3, cooldown 30m) — catches wedged/stalled issues that never produce a PR or execution row for the other health checks to watch (v2.241.2, GH-4454) |
| Self-close marker + escalateAndHold (rung foundation) | ✅ | autopilot | - | - | `markSelfClosed`/`consumeSelfClosedMarker` (10m TTL) let the external-close poll path tell autopilot's own PR closes apart from real human rejections, skipping the GH-3818/D10 reclassify+branch-delete for stamped closes; `escalateAndHold` is a reusable give-up helper (StageFailed + pilot-needs-human + caller labels + PR comment + alert, no close/branch-delete/re-execution) for future conflict/CI-recovery rungs to call (v2.241.3, GH-4458) |
| Needs-manual-rebase re-adoption | ✅ | autopilot | - | - | `escalateAndHold`'s needs-manual-rebase hold (StageFailed) previously required a fully manual `gh pr merge` even after an operator pushed a fix — the poll loop treated StageFailed as terminal and never looked again. `reAdoptHeldRebasePR` runs every `processAllPRs` tick before `ProcessPR`: for a PR with `RebaseHoldActive=true` whose GitHub head SHA no longer matches the stored `HeadSHA`, it re-enters the pipeline at `StageWaitingCI` (fresh CI on the new head), preserving `MergeAttempts`/`RebaseAttempts` and posting a re-adoption comment; capped at `maxReadoptAttempts=2` via `PRState.ReadoptCount` (persisted) so a repeatedly-conflicting branch can't ping-pong forever — it stays parked past the cap. `RebaseHoldActive`/`ReadoptCount` are both persisted (`autopilot_pr_state.rebase_hold_active`/`readopt_count`) so the flag and budget survive a daemon restart. The external-merge scan remains the fallback for PRs merged by hand. Fixes a 5x-in-one-wave recurrence on 2026-07-29 (pilot-console PRs #67/#68/#70/#74/#75, all requiring manual operator rebase + merge) (v2.249.0, GH-4610) |
| Alert dispatch metrics | ✅ | alerts/gateway | - | - | alerts_fired_total, alert_delivery_total, alert_events_dropped_total, alert_queue_depth on /metrics (TASK-332) |
| Running-process version observability | ✅ | gateway + health | `pilot doctor` | - | Hot restarts (`syscall.Exec`, GH-3600) preserve the PID and don't touch the disk binary, so `ps` uptime, disk `pilot version`, and the pre-fix doctor staleness check were all blind to whether a hot restart actually took effect — 3 operator misdiagnoses 08-08→08-13. `pilot_build_info{version,commit} 1` gauge on `/metrics` (`Server.SetVersion`, commit parsed from the git-describe version string via `commitFromVersion`); `/health` and `/api/v1/status` return the real running version instead of a hardcoded `"0.1.0"`; `pilot doctor`'s `self-upgrade.running-vs-disk` check fetches the local daemon's `/health` and explicitly reports a running≠disk mismatch ("hot restart pending or failed"), degrading gracefully when no daemon is reachable locally (TASK-476, GH-4864) |
| Dead-man tracker (reusable liveness primitive) | ✅ | alerts | - | - | `alerts.DeadManTracker`/`Engine.RegisterDeadManTracker` generalizes the intent-judge streak counter (GH-4669/GH-4685) into a reusable primitive: counts attempts and successes separately from failures (a subsystem wired to nothing produces zero of all three, not just zero failures — memory `poller-labels-removed-log-means-never-applied`), fires its registered `AlertType` exactly once at a configurable consecutive-failure threshold, resets on success. `internal/executor` relays attempt/success/failure across the import-cycle boundary via `AlertEventTypeDeadMan{Attempt,Success,Failure}` + `EngineAdapter.ProcessEvent`. Migrated the intent judge onto it unchanged (`WithDeadManEventType` keeps routing through the original `AlertTypeIntentJudgeFailureStreak`/`handleIntentJudgeFailureStreak` pair), and registered two more seams from the same silent-death incident class: the post-GH-4692 label-lifecycle notifier (`notifyTaskStartedSDK`, GH-4687: 19 days dead) and post-run self-review (`runSelfReview`, GH-4702: months dead) — both new `AlertTypeLabelLifecycleFailureStreak`/`AlertTypeSelfReviewFailureStreak` rules (TASK-441 L2, GH-4709) |
| Post-task invariant tripwire sweep | ✅ | executor/alerts | - | - | `runFinishTripwireSweep` (`internal/executor/finish_tripwires.go`) runs as an optional post-`Persist` hook in `ExecutionLifecycle.Finish` on every terminal transition (nil-safe, `Transition`'s non-terminal path never triggers it): (a) root-clean — `git status --porcelain` on `task.ProjectPath`; (b) label lifecycle — an adapter-dispatched execution recorded at least one execution event; (c) decomposed children all terminal — no orphaned non-terminal child at parent finish; (d) worktree pruned + no commits-without-PR (epic-discard pitfall). Log-and-alert only — a sweep panic/error is recovered and never blocks or fails the Finish path; each check feeds its own `alerts.DeadManTracker` (`finish_tripwire_{root_clean,label_lifecycle,children_terminal,worktree}`, new `AlertTypeFinishTripwireFailureStreak`). Byproduct fix: `runPollingMode` (the actual `pilot start --telegram --github` entrypoint in `cmd/pilot/main.go`) never called `runner.SetAlertProcessor`, so all executor-side alert relays — this sweep and every prior TASK-441 L2 dead-man tracker — were silently dead in production polling mode; now wired there and in `internal/pilot/pilot.go`'s `initAlerts` (TASK-441 L5, GH-4716) |
| Rate limit detection | ✅ | executor | - | - | Detect GitHub API rate limits, pause + resume at reset time (v0.34.0) |
| GitHub token fallback + live validation | ✅ | cmd/pilot, health | `pilot doctor` | `adapters.github.token` | config → `GITHUB_TOKEN` env → `gh auth token` fallback; authenticated startup check logs ERROR + fires `config_error` alert on a dead/expired token (GH-3718) |
| GitHub App installation-token auth | ✅ | adapters/github, cmd/pilot, executor | - | `adapters.github.app` | Opt-in `{app_id, installation_id, private_key_path}` block, validated eagerly at `Config.Validate()` (partial block errors naming the missing field). `github.TokenSource` (`apptoken.go`) mints an installation token via a hand-rolled RS256 JWT (no new go.mod dep) → `POST /app/installations/{id}/access_tokens`, caches it, and refreshes proactively 5min before the ~1h expiry. Sits ahead of the config-token/`GITHUB_TOKEN`/gh-CLI chain in `resolveGitHubToken`; on mint failure logs loudly and falls through to that chain rather than failing the caller. The same minted token authenticates pilot-worktree git push/fetch via a `GIT_ASKPASS` helper (`internal/executor/git_credentials.go`) that only ever passes the token through a child process's `PILOT_GIT_TOKEN` env var — never argv or a log line — installed once at daemon startup via `executor.SetGitCredentialProvider`. Kills the single-OAuth-grant SPOF and moves the daemon off the shared per-user 5000/hr rate pool onto a per-installation one (see `.agent/sops/config/github-token-architecture.md`). Known scope boundaries left for follow-up: `gh` CLI subprocess calls (PR creation, issue comments) still ride ambient `GITHUB_TOKEN`/gh-CLI login, not this token (GH-4743). Daemon-lifetime studio-sdk clients now hot-rotate too: `newGitHubSDKClient` (`cmd/pilot/main.go`) resolves per request via `githubSDK.NewClientWithTokenFunc`, and the SDK poller's adapter is injected with one shared instance via `githubSDK.WithAdapterClient` so the Poller/MergeWaiter/board sync all inherit it (TASK-461 Leg 2, GH-4824, sdk PR#108/PR#110) |

## Quality Gates

| Feature | Status | Package | CLI Command | Config Key | Notes |
|---------|--------|---------|-------------|------------|-------|
| Quality gate runner | ✅ | quality | - | `quality.enabled` | Pre-completion checks |
| Test gates | ✅ | quality | - | `quality.gates[].type=test` | Run test commands |
| Lint gates | ✅ | quality | - | `quality.gates[].type=lint` | Run lint commands |
| Build gates | ✅ | quality | - | `quality.gates[].type=build` | Compile check |
| Retry on failure | ✅ | quality | - | `quality.max_retries` | Auto-retry with feedback |
| Knowledge-graph drift local prevention | ✅ | scripts | `python3 scripts/check-graph.py --fix` | - | Pre-commit (blocks commits staging `.agent/knowledge/` paths) and pre-push gate (`[5/6] Knowledge Graph`) now run `check-graph.py` locally, so drift is caught before push instead of first surfacing on CI's Knowledge Graph Drift Gate. `--fix` auto-repairs class-2 findings only (unindexed memory files — stub node generated from the file's frontmatter `name`/`type`/`description`); broken links and dangling edges still fail and need human judgment. No-`--fix` behavior (used by CI) is unchanged (GH-4574, follow-up to the 88fad61c red-main incident) |

## Memory & Learning

| Feature | Status | Package | CLI Command | Config Key | Notes |
|---------|--------|---------|-------------|------------|-------|
| Execution history | ✅ | memory | - | `memory.path` | SQLite store |
| Lifetime metrics | ✅ | memory | - | - | Token/cost/task counts persist across restarts (v0.21.2) |
| PR-family counter hydration | ✅ | autopilot/memory | - | - | `pilot_prs_merged_total`/`pilot_prs_failed_total` are pure session counters (start at 0 every boot, live-increment only) as of GH-4511 — hydrating them directly from the store's lifetime baseline caused Prometheus counter-reset artifacts (a hydrated value landing below the pre-restart live value made `increase()` replay the entire baseline as fabricated activity; observed live: 1236 reported vs 3 true merges in a 3h window). The lifetime baseline now lands on separate gauges, `pilot_prs_merged_lifetime`/`pilot_prs_failed_lifetime`, hydrated at daemon start all-time from the `executions` table (`Store.GetLifetimePRCountersFromExecutions`), not the `execution_events` ledger — the ledger only goes back to its TASK-379/GH-3844 introduction and undercounted these two counters ~20x against every other lifetime counter (GH-4121, follow-up to GH-4093/PR #4043). Deduped by `task_id` (a retried task counts once). GH-4511 also closed a merge-persist miss: `ScanRecentlyMergedPRsWithWindow`'s `SelfHealExecutionByPRURL` fallback heal now runs unconditionally (previously gated on `issueNum == 0`), and `healAndBackfillRows` backfills a still-empty `pr_url` on already-`completed` rows — both were gaps where a live-counted merge's `executions` row never satisfied the lifetime query's filters, permanently desyncing the gauge from the session counter across a restart. `pilot_pr_time_to_merge_seconds` still hydrates from the ledger (`GetLifetimePRTimeToMerge`) since it needs the pr_created→merged timestamp delta, which the executions table doesn't carry. Lands on the same designated-owner `Metrics` as other `HydrateFromStore` baselines (GH-4068's aggregate), so no double-count with per-controller live recording. Reset-on-restart by design (no durable per-event source): `pilot_prs_conflicting_total`, `pilot_circuit_breaker_trips_total`, `pilot_api_errors_total`, `pilot_label_cleanups_total`, `pilot_approval_persist_misses_total`, poller skip/dispatch/deferred counters, `pilot_panics_total` |
| CI pass/fail counter | ✅ | autopilot/gateway | - | - | `pilot_ci_runs_total{result="pass"\|"fail"}` — true CI verdict counter, distinct from the `pilot_prs_failed_total` proxy (which also folds in approval rejections, merge escalations, and size-guard failures). Incremented once per distinct CI verdict: `result="pass"` at the `StageCIPassed` transition in `handleWaitingCI`, `result="fail"` at each terminal exit of `handleCIFailed` — a multi-iteration CI-fix cascade on one PR counts as several distinct verdicts, not one. CI pass rate = `sum(pilot_ci_runs_total{result="pass"}) / sum(pilot_ci_runs_total)`. Hydrated at daemon start from the `execution_events` ledger (`Store.GetLifetimeCIRunCounters`) — unlike PR merged/failed, CI verdicts have no pre-ledger source in `executions`, so history before the ledger's TASK-379/GH-3844 introduction is not recoverable (GH-4134) |
| Queue-depth gauge refresh (headless) | ✅ | cmd/pilot | - | - | `pilot_queue_depth` used to be refreshed only by the interactive TUI dashboard's 2s loop (sole call site of `autopilot.RefreshQueueDepth`), so any headless daemon (`pilot start --telegram --github`, no `--dashboard`) exported a gauge frozen at whatever value was last set — silently lying to fleet/tenant observability (GH-4512). Fixed by `startQueueDepthRefresh` (`cmd/pilot/main.go`): a `logging.SafeGo` ticker (30s, one `store.CountQueuedTasks` COUNT query per tick) plus an immediate synchronous refresh at boot, gated on the same `!noGateway && cfg.Gateway != nil` condition that starts `/metrics`, cancelled cleanly via the daemon's `ctx`. Coexists with the dashboard's 2s refresh (both are idempotent `SetQueueDepth` writers) |
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
| Learning system init wiring | ✅ | main | - | - | Initialize and wire learning system in main.go with config (v2.25.0, PR #1818) |
| Execution outcome recording | ✅ | executor | - | - | Record execution outcomes for pattern learning (v2.25.0, PR #1817) |
| Learning system fields | ✅ | executor | - | - | Learning system fields and setters on Runner (v2.25.0, PR #1815) |
| Review learning wiring | ✅ | autopilot | - | - | Wire review learning into handleMerged and webhook handler (v2.25.0, PR #1826) |
| PR review comments API | ✅ | adapters/github | - | - | GetPullRequestComments for line-level review feedback (v2.25.0, PR #1825) |
| CI log pattern learning | ✅ | autopilot | - | - | Wire CI log learning into autopilot feedback loop (v2.50.0, PR #1977) |
| Staticcheck S1011 fix | ✅ | memory | - | - | Replace loop with append for staticcheck compliance (v2.46.1, PR #1971) |
| Execution stage ledger (data layer) | ✅ | memory | - | - | `execution_events` table + Stage enum, `InsertExecutionEvent`/`ListExecutionEvents`/`ListExecutionsForTask`; no consumers wired yet (TASK-379 C3, GH-3844, v2.208.0) |
| Memory-doc deletion hard veto | ✅ | executor | - | - | `GitOperations.EnforceMemoryDocDeletionGuard`, wired into all 3 finalize/PR paths (`finalizeEpicBranchPR`, inline `executeWithOptions`, `finalizeDecomposedParentPR`): blocks push when a `.agent/knowledge/memories/**.md` deletion was graph-indexed on **baseBranch** (not just HEAD, so a commit that deletes the doc and its graph node together is still caught), unless the task explicitly names memory/knowledge-graph files. Also fixed `StripUnindexedMemoryDocs`/`RestoreDeletedIndexedMemoryDocs` to match indexed docs by slug/concept_index fallback and check every path field (not just the first present one), closing the false-negative that let an indexed doc through as "unindexed" (TASK-410 class, GH-4484/GH-4489/PR #4495, GH-4496) |
| gh-guard shim | ✅ | executor/ghguard | `pilot gh-guard` (internal) | `executor.claude_code.gh_guard` | Preventive half of the GH-4649 containment pair (detective half GH-4670 is the post-run side-effect audit). A `gh` shim script is prepended onto every Claude Code subprocess's PATH; it re-execs `pilot gh-guard -- <argv>`, which classifies the call against a data-driven allowlist (`internal/executor/ghguard.Classify`) — reads and the session's own PR/issue always allowed, issue/PR lifecycle mutations, cross-issue/cross-repo targeting, label mutations, and whole command families (`release`/`repo`/`secret`/`variable`/`workflow`) always denied — before exec-ing the real `gh` (resolved once at daemon start). Fails closed for mutations (a Deny never execs, regardless of whether the real `gh` was resolvable) and fails open for reads (falls back to a PATH search excluding the shim's own dir if `PILOT_GH_REAL` is unset). Denials are journaled per-execution (JSONL) and, back on the runner side, turned into an `execution_events` row (`StageGhGuardDenied`) plus an alert-engine warning (`AlertEventTypeGhGuardDenied`) so operators have one place to look regardless of which half of the containment pair caught the bad call. Default on; escape hatch via `gh_guard: false` (GH-4671) |

## Dashboard

| Feature | Status | Package | CLI Command | Config Key | Notes |
|---------|--------|---------|-------------|------------|-------|
| TUI dashboard | ✅ | dashboard | `--dashboard` | - | Bubbletea terminal UI on the grot design system (`github.com/qf-studio/grot` `pkg/tui/render`+`theme.Pilot`): rounded cards, border legends, stat cards w/ braille trends, glyph vocabulary, one-line banner w/ daemon liveness dot; logs panel flexes to terminal bottom (content-sized in stacked-graph mode) (TASK-390, v2.234.x) |
| Token metrics card | ✅ | dashboard | - | - | Stacked braille trend: dim-accent cached mass + bright-accent fresh cap, daily `tokens_cache_read/write` from `GetDailyMetrics`; detail line doubles as color key (TASK-390, v2.234.0) |
| Cost metrics card | ✅ | dashboard | - | - | Uniform dim-sage braille trend + cost/task (TASK-390, v2.234.0) |
| Queue metrics card | ✅ | dashboard | - | - | Current queue depth; stacked trend sage succeeded / rose failed per day matching ✓/✗ detail colors (TASK-390, v2.234.0) |
| Autopilot panel | ✅ | dashboard | - | - | One row per active PR: glyph + 5-cell lifecycle meter (ci→rebase→merge→tag→release) + stage label + age; `↳ ⟲ retry N/M · error` detail only when failures exist; `┤ ● N prs ├` border legend; `pr_title` persisted so rows survive restarts (branch-name fallback) (TASK-390, v2.235.8) |
| Task history | ✅ | dashboard | - | - | Recent 5 executions with truthful status glyphs — `executions.status` passes through (`·` skipped, `○` no_op, `●` running, …); terminal non-ladder outcomes own the row label + muted meter (TASK-390, v2.235.1) |
| Execution stage strip | ✅ | dashboard | - | - | Pipeline progress on HISTORY rows as a 7-rung segment meter (`■■■■□□□` + dim stage label; sage/rose/accent by outcome, dim track when no events), fed from `execution_events` via `buildStageInfo` — fixed ladder position, not raw event count, so retries don't inflate it; cached at hydrate/refresh time not per render (GH-3849, v2.208.0; fraction TASK-383; meter TASK-390) |
| HISTORY archaeology heal | ✅ | dashboard, memory | - | - | One-shot heal at `hydrateFromStore`: `Store.HealFrozenHistoryLadders` backfills the terminal `execution_events` row on any pre-H4 `status='completed'` row whose ladder is frozen at a non-terminal rung — unlike `SelfHealExecutionAfterMerge`/`SelfHealExecutionByPRURL`, not bounded by `merged_pr_scan_window`. `declined-preflight` (zero-event decline) added to `mutedOutcomes`, and `stageInfoForExecution` now labels/mutes a muted outcome even with zero events instead of rendering blank (GH-4368, v2.240.1) |
| Hot upgrade key | ✅ | dashboard | `u` key | - | In-place upgrade from dashboard |
| SQLite persistence | ✅ | dashboard | - | - | Metrics survive restarts (v0.21.2) |
| Queue state panel | ✅ | dashboard | - | - | 5-state: done/running/queued/pending/failed with shimmer (v0.63.0) |
| Queue card store reconciliation | ✅ | executor | - | - | `Monitor.ReconcileWithStore` runs on the dashboard's 2s refresh tick (polling mode): pulls the `executions.status` for every in-memory running/queued/pending card and force-terminates it (completed/failed/cancelled/**no_op**/stalled) if the DB row already terminated — fixes cards stuck at running/100% after a no-commit failure or externally closed PR that never called back into `Monitor` (GH-4490). Subtask 2: the dispatcher's own terminal-outcome branch (`ProjectWorker.processQueue`) now also drives `Monitor.NoOp`/`Monitor.Fail` directly off the classified `outcome.Status` (no_op vs failed) the moment a "no new commit produced" run finishes, instead of relying solely on the periodic backstop — new `StatusNoOp` card state renders distinctly (not "failed", not "pending") in the QUEUE panel. Subtask 3: `TaskMonitor` gained a `Fail(taskID, errorMsg)` method, and `Controller.notifyExternalClose` now calls it the moment autopilot observes a PR closed without merging — by then the card is usually already `StatusCompleted` (the execution that opened the PR already ran `Complete()`), which sits outside `ReconcileWithStore`'s Running/Queued/Pending candidate set, so the periodic backstop alone would never flip a "done" card back to failed. |
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
| Update notification | ✅ | dashboard | - | - | Show update notification independently of banner toggle (v1.46.0) |
| Banner gap fix | ✅ | dashboard | - | - | Remove top gap when banner hidden, align metrics with git graph (v1.46.0) |
| Desktop native titlebar | ✅ | desktop | - | - | macOS TitleBarDefault, simplified two-column layout (v1.62.0, GH-1661) |
| Desktop panel spacing | ✅ | desktop | - | - | Consistent spacing, nowrap issue IDs, flex logs panel (v1.62.0) |
| Desktop TUI parity | ✅ | desktop | - | - | Redesign frontend layout to match TUI dashboard (v1.62.0, GH-1658) |
| Avionics redesign: splash screen | ✅ | dashboard | `--no-splash` | - | Boot splash with 4-lamp animation at 200ms cadence, key/CI bypass (v2.103.0, GH-2455) |
| Avionics redesign: banner frame | ✅ | dashboard | `b` toggle | - | Bordered banner frame with version, env, model stack, adapter dots, uptime, live UTC clock (v2.103.0, GH-2455) |
| Avionics redesign: autopilot rail | ✅ | dashboard | - | - | Autopilot panel shows STATE/PR/AGE + CI/MERGE/RETRY gauges + pipeline rail; idle collapses (v2.103.0, GH-2455) |

## Replay & Debug

| Feature | Status | Package | CLI Command | Config Key | Notes |
|---------|--------|---------|-------------|------------|-------|
| Execution recording | ✅ | replay | - | - | Auto-saved |
| List recordings | ✅ | replay | `pilot replay list` | - | Filter by project/status |
| Show recording | ✅ | replay | `pilot replay show` | - | Metadata view |
| Interactive replay | ✅ | replay | `pilot replay play` | - | TUI viewer |
| Analyze recording | ✅ | replay | `pilot replay analyze` | - | Token/phase breakdown |
| Export recording | ✅ | replay | `pilot replay export` | - | HTML/JSON/Markdown |
| Execution stage trace | ✅ | cmd/pilot | `pilot trace <task-id>` | - | Renders `execution_events` timeline per execution (newest first, UTC timestamps + inter-stage durations); consumes TASK-379 C3 data layer (GH-3848) |

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
| Agent doc size check | ✅ | health | `pilot doctor` | - | Warns >500 lines, errors >1000 lines per .agent/*.md (GH-2462) |
| OpenCode backend | ✅ | executor | `--backend opencode` | `executor.backend` | HTTP/SSE alternative to Claude Code |
| OpenAI-compatible direct backend | ✅ | executor | `type: openai-api` | `executor.openai` | Direct /v1/chat/completions for OpenAI, OpenRouter, Groq, Synthetic, vLLM, Ollama (v2.105.0, GH-2382) |
| K8s health probes | ✅ | gateway | - | - | `/ready` and `/live` endpoints for Kubernetes (v0.37.0) |
| Prometheus metrics | ✅ | gateway | - | - | `/metrics` endpoint in Prometheus text format (v0.37.0) |
| Prometheus counter baselines on restart | ✅ | autopilot/memory | - | - | `HydrateFromStore` restores lifetime per-(model,direction/result) token/cost/execution counters from `executions` table before `/metrics` starts serving, so `pilot_tokens_consumed_total`/`pilot_execution_cost_usd_total`/`pilot_executions_total`/`pilot_success_rate` survive daemon restarts instead of resetting to zero (GH-4041) |
| Rolling-window headline cost/success | ✅ | memory/dashboard/gateway/autopilot | - | `dashboard.stats_window_days` | `Store.GetWindowedStats` (canary-excluded, `created_at >= since`) backs a windowed (default 30d) view of cost/delivery/success that replaces the old all-time headline numbers on the TUI cost + task cards (`~$X.XX/issue · 30d` detail, 9-way ✓/✗ breakdown), the gateway dashboard JSON's new `window{days,totalCostUsd,costPerDeliveredUsd,issuesAttempted,issuesDelivered,deliveryRate,attemptSuccessRate}` object, and 4 new gauges — `pilot_window_cost_usd{window="30d"}`, `pilot_window_cost_per_delivered_usd{window="30d"}`, `pilot_window_delivery_rate{window="30d"}`, `pilot_window_attempt_success_rate{window="30d"}` — seeded at boot (`autopilot.HydrateWindowStats`) and refreshed on a 5-minute ticker (`autopilot.StartWindowStatsRefresher`) rather than per-scrape, since the aggregate query is too expensive to run on every `/metrics` hit. `GetLifetimeTokens` also gained the canary filter it was missing (population hygiene) (GH-4735) |
| Approvals HTTP API | ✅ | gateway | - | - | `GET /api/v1/approvals` lists pending approvals (same expiry predicate as channel rehydration, reused via `LoadPendingApprovals`; joined to execution/PR context via `GetExecutionByApprovalRequestID`, project-scoped via `dashboardProjectPath` like sibling dashboard routes) and `POST /api/v1/approvals/{requestId}/decision` records a decision through the `approval.DecisionRecorder` seam (`Server.SetDecisionRecorder`, wired to the same `*approval.Manager` as Telegram/Slack in both gateway-mode and polling-mode `cmd/pilot/main.go` paths — not a direct store write, so in-memory pending-map cleanup and persistence match a channel decision exactly). 404 unknown/already-decided requestId, 400 bad decision value/missing `by`, same bearer auth as sibling `/api/v1/` routes (C14 pilot leg, GH-4748) |
| Execution-events HTTP API | ✅ | gateway | - | - | `GET /api/v1/executions/{id}/events` returns the `occurred_at`-ASC stage timeline (`{stage,occurredAt,detail}`) for one execution via `ListExecutionEvents`; 404 unknown/cross-project id. `GET /api/v1/tasks/{taskId}/events?project=<path>` resolves the NEWEST execution for that (task, project) via `ListExecutionsForTask` (matches the pilot-console C8 join's pick-newest rule) and returns the same array wrapped in an `{executionId,status,events}` envelope; 404 when the task has no executions. `stage` is served as an opaque string (31-value vocabulary and growing — never enumerated in gateway code). Project scoping: when `dashboardProjectPath` is set it's authoritative and the task route's `project` query param is ignored; the query param only matters for unscoped/daemon deployments, closing the GH-4352 task_id-collision leak. No redaction/scrubbing helper exists on the gateway/dashboard/memory path today — `detail` is served verbatim; a scrubber is a separate S4 leg. No pagination (execution event counts are bounded) (S4 pilot leg, GH-4749) |
| External-merge metrics | ✅ | autopilot | - | - | Record PRsMerged/IssuesProcessed/PRTimeToMerge for externally-merged PRs via scanner (v2.147.0, GH-2981) |
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
| Release asset completeness gate | ✅ | ci | - | - | `scripts/verify-release-assets.sh` runs after the GoReleaser step in `release.yml` and asserts every expected asset (`pilot-{linux,darwin}-{amd64,arm64}.tar.gz`, `pilot-windows-amd64.zip`, `checksums.txt`) exists on the tag's release, retrying to absorb listing lag, failing the train loudly on a miss (GH-4523) |
| install.sh | ✅ | install | - | - | curl-pipe installer for Linux/macOS (v0.3.x) |
| Homebrew formula | ✅ | install | `brew install` | - | Homebrew tap formula for macOS (v0.3.x) |
| Integration tests (patterns) | ✅ | testing | - | - | Integration tests for self-review pattern accumulation (v2.44.0, GH-1956) |
| Handler refactoring | ✅ | adapters | - | - | handleIssueGeneric() consolidates 5 adapter flows (v2.30.0, PR #1856) |
| Config validation preflight | ✅ | executor | - | - | Validate config fields on startup before accepting issues (v0.48.0) |
| GitLab docs sync CI | ✅ | ci | - | - | GitHub Action syncs docs/ to GitLab repo on merge to main (v0.23.2) |
| Poller registration refactor | ✅ | main | - | - | Extract poller registration pattern from main.go (v2.30.0, PR #1857) |
| Repick-storm backoff | ✅ | main (handler_common.go) + executor (dispatcher.go) + memory (repick_backoff table) | - | - | Per-issue exponential backoff (capped at 30s×32) + independent `HasTerminalCompletion` re-check at the shared dispatch chokepoint — throttles the poller's label-removed retry heuristic re-admitting a completed-but-open issue every poll tick; storm rate on `pilot_poller_skipped_total{reason="repick_storm_backoff"}` (GH-4376). Cooldown state mirrors to the store's `repick_backoff` table via `*Dispatcher.{RepickBackoffState,SetRepickBackoffState,ClearRepickBackoffState}` so a daemon restart or shadow-DB split-brain no longer resets it to zero mid-storm (GH-4394 subtask 1/5). `Dispatcher.beginWithGenerationRetry`'s own terminal-claim re-pick (`dispatch re-pick: prior claim was terminal but task is not done`) now gates on and grows the SAME store row directly — previously this was the only production QueueTask caller's internal retry path and it always returned a valid execID, which handleIssueGeneric's blanket `recordSuccess` treated as a fresh dispatch and wiped the backoff the re-pick had just armed (the actual GH-85 mechanism: 5 repicks/15min, no growth). `Dispatcher.ExecutionGeneration` + a generation>0 check in handler_common.go now stop that wipe (GH-4394 subtask 2/5). Subtask 3/5 investigated and ruled out the remaining canary-sandbox hypothesis — `task.IsCanary`/`ProjectConfig.Canary` never short-circuits this gate, the backoff key is stable ProjectPath+TaskID regardless of canary status (regression-pinned by `TestDispatcher_BeginWithGenerationRetry_ThrottlesCanaryProjectSameAsRegular`). Subtask 4/5 audited every "sub-issue paths" entry point named in the issue: `nextRetryGeneration` (the only generation+1 decider anywhere in the codebase) lives solely in dispatcher.go and is reached only through `beginWithGenerationRetry` — already backoff-gated by subtasks 2/3 — so epic.go's sub-issue loop (which claims generation 0 directly via `ExecutionLifecycle.Begin`, never generation>0) cannot itself produce an unthrottled repick: a repicked epic re-discovering an already-failed child collides with that child's terminal generation-0 claim and loses (`ErrClaimLost`) without re-invoking the backend (regression-pinned by `TestExecuteSubIssues_RepickDoesNotBypassBackoff`). Also pinned cmd/pilot's and executor's independently-duplicated `repickBackoffKey` formats to the identical literal so the two packages can't silently diverge onto different store rows (`TestRepickBackoffKey_FormatMatchesDispatcherPackage` / `TestRepickBackoffKey_FormatMatchesCmdPilotPackage`). Subtask 5/5 closes the loop: exponential backoff alone never stops a doomed task, it only slows the interval to ~16 min forever — `beginWithGenerationRetry` now hard-stops at `dispatcherRepickHardCap` (5) consecutive repicks, marks the claimed execution `stalled` via `UpdateExecutionStatus`, and raises one `AlertEventTypeTaskFailed` alert (via a new `Runner.EmitAlertEvent` export) instead of granting another generation — idempotent, so a task sitting past the cap doesn't re-alert on every subsequent backoff-window expiry (regression-pinned by `TestDispatcher_BeginWithGenerationRetry_HardCapStallsInsteadOfRetrying` / `TestDispatcher_BeginWithGenerationRetry_HardCapIsIdempotent`). GH-4394 fully closed. GH-4502: the hard cap above miscounted stall-watchdog kills (execution status `stalled`, a healthy session killed mid-turn, not broken code) as if they were genuine failures — 4 consecutive stall-kills wedged a healthy task at `dispatcherRepickHardCap` (incident: pilot-console GH-24), requiring manual re-arm. `priorClaimWasStalled` now carves stall-kills out of the shared `consecutive_drops` counter into their own persisted `stall_drops` column (same `repick_backoff` row/key, via `Store.{Get,Set}StallDropCount`) with its own higher cap `dispatcherStallRepickCap` (8, not unlimited — a complex-lane task can stall on every generation until the separate silent-turn root cause ships, so an uncapped bypass would retry forever) — trip escalates via the same hold path but with a distinct alert reason (`stall_repick_cap_stalled` vs `repick_hard_cap_stalled`) so an operator isn't misled into thinking the code is broken. Reusing `stalled` as both the watchdog's genuine-kill status AND the dispatcher's own escalate-and-hold marker required two subtleties: the hard-cap check must run before `priorClaimWasStalled` is consulted (else a hard-cap-escalated execution's own "stalled" status would be misread as a fresh stall-kill on the next tick, defeating the cap), and idempotency in the shared `escalateStalledTask` helper matches on exact reason text, not just status (since a first-ever stall-cap trip's own triggering condition IS `Status=="stalled"`). Regression-pinned by `TestDispatcher_BeginWithGenerationRetry_StallDropsDoNotCountTowardHardCap` / `_StallCapEscalatesWithDistinctReason` / `_OperatorCancelAndStallCarveOutsAreIndependent` / `_MixedStallAndFailedDropsHardCapAtFive` |
| Secrets check | ✅ | - | `make check-secrets` | - | Scan test files for realistic secret patterns before push |
| Windows hot upgrade | ✅ | upgrade | - | - | Allow dashboard hot upgrade on Windows without restart (v1.46.0) |
| Windows forward slashes | ✅ | navigator | - | - | Use forward slashes for embed.FS on Windows (v1.46.0) |
| Nextra 4 migration | ✅ | docs | - | - | Docs site migrated from Nextra 2 to Nextra 4 App Router (v1.27.0, PR #1409) |
| Docs navbar branding | ✅ | docs | - | - | PILOT logo, version badge, and nav links in navbar (v2.10.0) |
| Docs GitLab deploy tags | ✅ | ci | - | - | Unique deploy tags to trigger GitLab pipelines (v2.10.0) |
| Desktop CI artifact naming | ✅ | ci | - | - | Rename desktop artifacts to Pilot-Desktop-* prefix (v1.54.0) |
| Desktop CI resilience | ✅ | ci | - | - | Delete-asset before upload, checkout step in desktop release (v1.54.0) |
| GraphQL client method | ✅ | adapters/github | - | - | ExecuteGraphQL method on GitHub client (v2.30.0, PR #1860) |
| Project board config types | ✅ | adapters/github | - | - | ProjectBoardConfig types and NodeID on Issue struct (v2.30.0, PR #1858) |
| Project board example config | ✅ | config | - | - | project_board example in pilot.example.yaml (v2.30.0, PR #1859) |
| Epic DependsOn annotations | ✅ | executor | - | - | Wire DependsOn annotations into sub-issue creation (v2.25.0, PR #1800) |
| Docs Discord/Plane pages | ✅ | docs | - | - | Discord and Plane.so integration documentation pages (v2.38.0) |
| Docs board sync page | ✅ | docs | - | - | GitHub Projects V2 Board Sync documentation (v2.38.0) |
| Docs CLI/homepage update | ✅ | docs | - | - | Update CLI commands and homepage for v2.25 (v2.38.0) |
| Docs architecture update | ✅ | docs | - | - | Update architecture page with new adapters (v2.38.0) |
| Nightly ledger backup to S3 | ✅ | scripts/box | - | - | Daemon-independent `pilot-backup-s3.sh` (VACUUM INTO snapshot + knowledge JSON, SSE-KMS upload, head-object verify) + `pilot-backup.timer`/`.service` (03:30 UTC) + restore SOP; repo-tracked, operator installs on box (GH-4465) |
| Shadow-ledger startup guard | ✅ | memory + main | `pilot start` | - | `memory.NewStoreGuarded` refuses to open a brand-new/empty state directory when a home-relative `~/.pilot/last_known_good.json` marker shows this daemon previously ran with a non-empty ledger at a different resolved path — returns typed `*memory.ErrSplitBrainLedger`, which `runPollingMode` treats as fatal (unlike an ordinary store-open error, which degrades gracefully). Startup also logs the symlink-resolved absolute DB path in the first lines of `daemon.log` (`logMemoryStartupBanner`/`resolveMemoryDBPath`) so a shadow-path open is visible immediately instead of only in hindsight. Closes the code-hardening half of the 2026-07-16 cutover incident (GH-4393; ops recovery + shim already done, see TASK-409) |
| Gateway bearer auth enforced in production | ✅ | gateway/config/pilot/main | `pilot start` | `auth.type`, `auth.token` | PR#4752 added `Authenticator.Middleware` and a gateway-package test proving it 401s — but neither production construction site (`internal/pilot/pilot.go` gateway mode, `cmd/pilot/main.go` polling mode) ever called `gateway.NewServerWithAuth` with a real token; both always called `gateway.NewServer`, so the deployed daemon served all of `/api/v1/` (including the pre-merge approval decision route) with zero auth regardless of config — mitigated only by the default loopback bind. `Config.GatewayAuthConfig()` is now the single gate both sites call: returns non-nil only for an explicitly configured `auth.type: api-token` with a non-empty `auth.token` (the DefaultConfig `claude-code` seed and an empty token both resolve to nil, reproducing prior fully-open behavior exactly). `auth.token` supports `${ENV_VAR}` expansion like every other config field, so a hosted deployment can set `token: "${PILOT_GATEWAY_TOKEN}"` to match the bearer token pilot-console's proxy already sends (`internal/proxy/proxy.go:336`) — confirming/adding that block to the console's tenant config render is a console-repo follow-up, not done here. Composed test starting from `*config.Config` (not a hand-built `AuthConfig`) proves 401-without-bearer and 200-with-bearer on both a read route and the decision route (GH-4784) |

## Approval Workflows

| Feature | Status | Package | CLI Command | Config Key | Notes |
|---------|--------|---------|-------------|------------|-------|
| Approval engine | ✅ | approval | `--env=prod` | `approval` | Wired to autopilot controller |
| Slack approval | ✅ | approval | - | `adapters.slack.approval` | Interactive messages, registered in main.go; Socket Mode clicks routed via `slack.ApprovalCallbackHandler` in `adapters/slack/handler.go` (GH-4431, HTTP-webhook-only wiring was the sole path before). `SlackHandler.resolveChannel` is channel-first (GH-4808): a configured `adapters.slack.approval.channel` always wins over `Approvers[0]`, which is now only a DM fallback when no channel is configured — before this, any request with an approver silently landed in an unwatched Pilot-bot DM even with a channel configured (incident: PR#4806's ask sat unseen ~50 min). When the destination is a channel and an approver is set, the message `<@U…>`-mentions them; the "async approval request submitted" log line (`internal/approval/manager.go`) now also carries the resolved `destination` field via the handler's optional `ResolvedDestination` method, so the send target is visible from `daemon.log` alone |
| Telegram approval | ✅ | approval | - | - | Inline keyboards, registered in main.go |
| Rule-based triggers | ✅ | approval | - | `approval.rules[]` | RuleEvaluator with 4 matchers wired into Manager (GH-636) |
| Non-blocking async approval | ✅ | autopilot | - | `approval.async_dispatch` | handleAwaitApproval tick-handler; PR-A stall no longer blocks PR-B (GH-2685) |
| Approval persistence in executions | ✅ | autopilot/memory | - | - | approval_request_id + decision written to executions table (GH-2712) |

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
| GitHub 403 secondary rate-limit retry | ✅ | adapters/github | - | - | 403 secondary rate-limits retried; Retry-After/X-RateLimit-Reset headers honored via RateLimitError (TASK-330, v2.162.8) |
| CI auto-discovery | ✅ | autopilot | - | - | Auto-detect check names from GitHub API (v0.41.0) |
| Stagnation monitor | ✅ | executor | - | - | State hash tracking, escalation: warn → pause → abort (v0.56.0) |
| URL-encode branch names | ✅ | adapters/github | - | - | `url.PathEscape(branch)` in DeleteBranch/GetBranch — fixes 404 on slash branches (v1.28.0) |
| Branch cleanup on PR close | ✅ | autopilot | - | - | Delete remote branches on PR close/fail, not just merge (v1.35.0) |
| Desktop app release | ✅ | ci | - | - | Separate GH Actions workflow builds Wails macOS universal binary, uploads to release (v1.41.0) |
| Jira/Asana autopilot | ✅ | autopilot | - | - | OnPRCreated wired for Jira + Asana adapters (v1.19.0, GH-1397) |
| CI error logs in fix issues | ✅ | autopilot | - | - | Embed CI error output in generated fix issues (v1.58.0, GH-1566) |
| Branch lineage circuit breaker | ✅ | autopilot | - | - | Circuit breaker keyed by branch lineage, not PR ID (v1.58.0, GH-1567) |
| Environment config | ✅ | autopilot | - | `environments` | EnvironmentConfig + ResolvedEnv(), no hardcoded env checks (v1.59.0, GH-1640) |
| `default_environment` actually read | ✅ | autopilot | - | `default_environment` | `ResolvedEnv()` now checks `DefaultEnvironment` against the `environments` map (between the runtime `--env` check and the legacy `Environment` fallback) and errors if it's set but unresolvable, instead of silently falling through to stage. Previously the field was read nowhere (AUDIT-2026-05-25 P2). Hot-path callers use the new `ResolvedEnvOrDefault()` wrapper, which logs and falls back to stage on error. Unresolvable-name error now names the available environment keys (sorted) per GH-4544's acceptance criteria; covered by a table-driven test in `types_test.go` for all 5 resolution cases (GH-4545/GH-4549, v2.246.0) |
| `default_environment` validation | ✅ | autopilot | - | `default_environment` | `autopilot.Config.Validate()` rejects an unknown `default_environment` at startup, listing the available environment keys (built-in dev/stage/prod + any custom `environments` entries), instead of silently falling back. Wired into `config.Config.Validate()` so it runs on every `Load()` (GH-4546) |
| Post-merge deployer | ✅ | autopilot | - | `environments.*.deploy` | Webhook and branch-push deployment triggers after merge (v1.60.0, GH-1641) |
| CLI `--env` flag | ✅ | main | `--env=stage` | - | Renamed from `--autopilot`, updated onboarding + config (v1.60.1, GH-1642) |
| Prod auto-approve safety | ✅ | autopilot | - | - | Block auto-merge when pre_merge approval disabled in prod (v1.61.0) |
| Auto-rebase on conflict | ✅ | autopilot | - | - | GitHub UpdatePullRequestBranch API before close-and-retry (v2.25.0) |
| CI fix dependencies | ✅ | autopilot | - | - | `Depends on: #N` annotations in generated fix issues (v2.25.0) |
| Board sync on merge | ✅ | autopilot | - | - | Move issue to Done column on PR merge (v2.30.0, PR #1864) |
| Label cleanup on retry | ✅ | adapters/github | - | - | Remove `pilot-failed` on successful retry — accurate metrics (v1.8.1, GH-1302) |
| Autopilot CI optimization | ✅ | autopilot | - | - | Cached GetPR, API failure escalation, dynamic 10s/60s poll interval (v1.8.5, GH-1304) |
| Stale branch detection | ✅ | autopilot | - | - | Detect and clean stale remote branches before execution (v0.48.0) |
| Auto-close issues on merge | ✅ | autopilot | - | - | Close GitHub issues after successful execution and merge (v1.62.0, PR #1636) |
| shouldTriggerRelease fix | ✅ | autopilot | - | - | Check ResolvedEnv().Release instead of top-level config only (v2.25.0, PR #1752) |
| Board sync IssueNodeID | ✅ | autopilot | - | - | Wire board sync into controller with IssueNodeID in PRState (v2.30.0, PR #1873) |
| Board sync owner guard | ✅ | adapters/github | - | - | Guard owner extraction in board sync construction (v2.30.0, PR #1871) |
| Board sync tests | ✅ | adapters/github | - | - | ProjectBoardSync and ExecuteGraphQL unit tests (v2.30.0, PR #1865) |
| CI context wiring | ✅ | autopilot | - | - | Wire proper CI context from autopilot controller (v2.52.0, PR #1981) |
| PR stage execution-event audit trail | ✅ | autopilot | - | - | `Controller` writes `execution_events` rows (ci_passed/ci_failed/awaiting_approval/merged/released/failed) via `memory.Store.InsertExecutionEvent`; survives PR-state-row cleanup after merge since it keys off `executions.id` (GH-3847). `checkExternalMergeOrClose`'s external-merge finalizer — the path an operator's manual `gh pr merge`/GitHub-UI merge of a size-held PR takes — now writes the same terminal `merged` event before draining the PR from tracking; previously it only finalized GitHub-side artifacts (issue close, branch delete, tracking removal) and left the journal frozen at whatever stage it was in (e.g. `awaiting_approval`), which the dashboard history strip then rendered forever, even across a restart (v2.259.2, GH-4869) |
| Release train tick retry | ✅ | autopilot | - | - | `scheduleReleaseTickWithRetry` retries a failed `on_schedule` tick (rate limit/5xx/network) every 15-30m for up to 6h past the scheduled time before giving up, instead of forfeiting the whole day the way the 2026-07-18 403 did; exhausted retries fire a `release_tick_failed` alert via the alerts engine (GH-4476, v2.243.0) |
| Shared GitHub rate-limit budget + poller reserve | ✅ | ghbudget + autopilot | - | `orchestrator.autopilot.startup_merged_pr_scan_window`, `.rate_limit_floor_pct`, `.scan_stagger_interval` | `ghbudget.RoundTripper` installed over `http.DefaultTransport` before any GitHub client is constructed: tracks `X-RateLimit-Remaining/Limit` on every response and transparently caches GET responses by ETag so a repeat scan of an unchanged resource costs a free 304. Conditional-GET caching is scoped to `req.URL.Host == "api.github.com"` only (`Observe` stays host-agnostic) and the cache map is dropped in full whenever the Tracker's tracked `ResetAt` rolls over, bounding it to one rate-limit window's worth of entries instead of growing forever from unique per-SHA check-run URLs (GH-4498, follow-up to GH-4391/#4495). `ghbudget.Tracker` gates `PriorityBackground` callers (merged-PR scans, orphan-PR sweeps) below a floor (default 15%) while `PriorityCritical` callers (pollers, active-PR CI watches) are never gated. Startup catch-up window is configurable (default 72h, down from a hardcoded 720h) and shrinks further on restart via a per-repo cursor persisted in `StateStore` metadata (`ScanRecentlyMergedPRsAtStartup`). Per-repo startup scans are staggered (jittered, serialized via `autopilot.StaggerRepoScans`) instead of bursting all repos at boot. Floor engagement increments `pilot_rate_limit_floor_engaged_total` and logs one WARN per engagement episode. Closes the 2026-07-16 incident: 11 repos' startup rescans burned the entire shared per user rate budget in under an hour and 403'd every issue poller for 67+ minutes (GH-4391) |
| CI infra-outage classification + auto-retry | ✅ | autopilot | - | - | `classifyPRFailure`/`classifyCheckFailure` (`failure_class.go`) classify a failed PR's scoped checks as `infra` only on a conservative unambiguous signature set (action-download 429, golangci-lint-run 5xx, runner shutdown signal, lost-communication-with-server) — any real `.go:N:N:` annotation, mixed signal, or empty/unrecognized log defaults to `code` (fail-safe). `handleCIFailed` classifies before the iteration/size guards and fix-issue machinery; an `infra` verdict with retry budget remaining (`maxInfraRerunBudget = 2`, scoped per HeadSHA via `PRState.InfraRerunCount`/`InfraRerunSHA`, persisted through `StateStore` so a daemon restart cannot grant a fresh budget on a stuck SHA) calls `RerunFailedJobs` once per unique owning workflow run and re-enters `StageWaitingCI`, skipping the fix-issue path entirely. Budget exhaustion falls through to the normal fix-issue path with an `(infra retries exhausted (2/2))` note. `PRFailureClasses` and `CIRuns["infra_retry"/"infra_fail"]` metrics track classification and verdict separately. Prevents a repeat of the GH-4526 incident, where a transient action-download rate limit was misdiagnosed as a code failure and closed a good PR (GH-4533, sub-issue of GH-4531). Extended with a distinct `infra_billing` classification (`FailureClass.IsInfra()` covers both) for GitHub Actions' jobs-never-started shape — org billing (payment failure/spending limit) refuses to even start a job, so there are zero job logs to search: `isJobsNeverStartedInfra` matches the check run's own `Output.Summary/Text` against a billing-refusal phrase or (when `StepLogClient` is wired) a known-zero jobs-API step count, with a real `.go:N:N:` annotation always winning first as proof the job actually ran. Handled identically to generic infra (auto-retry, no PR close, no fix issue) but fires a dedicated `ci_billing_refusal` alert, deduped once per repo per outage window (`alertedBillingRefusal`, reset by the next `handleCIPassed`) rather than once per PR. Fixes the 2026-07-28 incident where `pilot-canary-sandbox` PR #106 was wrongly closed and a wasteful fix issue (#107) was spawned for a PR whose content was never actually tested (GH-4591) |
| required_checks name-mismatch detection | ✅ | autopilot | - | `required_checks`/`ci_checks.required` | `checkRequiredChecks` now distinguishes a genuinely-pending required check from one whose name can never appear: once every discovered check-run on the SHA has reached `completed` and a required name still hasn't shown up, it returns the new terminal `CIConfigMismatch` status (instead of `CIPending` forever) and logs a WARN naming the missing vs. discovered check names — gated so any still-executing run on the SHA keeps the old pending behavior. `handleWaitingCI`/`handlePostMergeCI` route `CIConfigMismatch` straight to `StageFailed` (not `StageCIFailed`, to avoid spawning a misdiagnosed CI-fix issue for a config error with no code to fix); the post-merge scope-release carrier path threads the missing/discovered names into `handleScopeReleaseFailure`'s reason and into `parkScopeReleaseAfterTimeouts`'s park message, replacing the old blanket "this repo likely has no post-merge CI configured" guess with the actual discovered check names when CI is in fact running. `Controller.Start` also runs a one-shot best-effort `lintRequiredChecksMismatch` startup probe against the latest main-branch SHA so a stuck-allowlist repo WARNs at boot instead of only being discovered mid-incident. `RestoreState` now reconciles (deletes) stale `autopilot_pr_state` rows for scopes already terminal (`failed`/`done`) instead of leaving them to linger in the dashboard's non-released panel. Fixes the 18-scope auth-service/studio-sdk stuck-release incident, where both repos inherited a global `required_checks: [test, lint]` tuned for a different repo and neither ever posted both exact names — the GH-4643 no-workflow probe couldn't catch it since CI genuinely was configured, just under different names (GH-4646, follow-up to GH-4307/GH-4478/GH-4643) |
| Cross-PR platform-outage breaker (part 1) | ✅ | autopilot | - | `orchestrator.autopilot.platform_breaker.enabled`, `.min_correlated_prs`, `.correlation_window`, `.quiet_period` | `PlatformBreaker` (`platform_breaker.go`) correlates CI-failure observations across PRs — and repos — to see a platform-wide outage the existing per-PR circuit breaker cannot by construction (deliberately scoped to one PR at a time). Follows the `ghbudget.Tracker` shape: one process-wide instance shared by every controller via `WithPlatformBreaker` (constructed once in `cmd/pilot/main.go`, same pattern as `WithRateBudget`), `Observe(pr, repo, class)` feeds it, exactly-one log per state transition. Opens when `min_correlated_prs` (default 3) distinct `owner/repo#N` PRs observe an infra-or-unknown-class (`FailureClass.IsInfra()`/`FailureClassUnknown`) CI failure within `correlation_window` (default 15m); closes via simple time-based recovery after `quiet_period` (default 20m) elapses with no further infra/unknown-class failure, evaluated lazily on each `Observe` call. While open, `handleCIFailed` suppresses every irreversible action (PR close, fix-issue creation, escalate-and-hold) for every PR regardless of that PR's own classification — the auto-retry path (`maybeRetryInfraFailure`, non-destructive) is never gated — leaving `prState.Stage` untouched so the PR is re-examined on a later tick. Fires exactly one alert on open and one on close (`platform_breaker_open`/`platform_breaker_close`) with the correlated PR list, deduped for free via the breaker's own mutex rather than a per-controller flag. Metrics: `pilot_platform_breaker_open` gauge + `pilot_platform_breaker_trips_total` counter. Disabled by default (`PlatformBreaker` config nil); a nil `*PlatformBreaker` is a byte-identical no-op. Part 1 of 2 (TASK-458) — responds to the 2026-08-06 GitHub Actions outage, where the daemon acted on false CI signals for ~50 minutes (closing a correct PR, spawning junk fix issues, burning retries) until a human stopped it; part 2 adds the external githubstatus.com probe, admission pause, and held-PR re-drive (GH-4791/GH-4792) |
| Cross-PR platform-outage breaker (part 2 — probe, admission pause, held-PR re-drive) | ✅ | autopilot + executor | - | `orchestrator.autopilot.platform_breaker.probe_interval`, `.pause_admission` | Builds on part 1's correlation/suppression. (1) `ProbeGitHubStatus` (`platform_breaker_probe.go`) fires once, synchronously, on the `JustOpened` transition only: GETs githubstatus.com's official component-scoped API (`components.json` for the `Actions` component, `incidents/unresolved.json` for an unresolved incident naming Actions), 5s timeout via an injectable `platformStatusHTTPGet` var, any failure/non-200/unparseable-JSON/missing-component degrades to `unknown` rather than erroring. Purely advisory — enriches the open alert's `probe_verdict` metadata, structurally cannot veto or delay the breaker's own correlation decision (probe call happens strictly after `Observe` already decided). (2) `Dispatcher.PauseAdmissionFor`/`ResumeAdmissionFor(owner string)` (`internal/executor`) replace the old zero-arg pause with an owner-reference-counted set — admission stays paused as long as *any* owner holds it, so the platform breaker (`platform-breaker` owner) and the GH-4683 self-upgrade drain (`self-upgrade` owner) can't fight over one shared bool; enforced at `ProjectWorker.processQueue`. `Controller.SetAdmissionPauser` takes a narrow `AdmissionPauser` interface (avoids an `autopilot`→`executor` import cycle; `*executor.Dispatcher` satisfies it structurally) and calls it synchronously inside `alertPlatformBreakerTransition` on `JustOpened`/`JustClosed`. Config-gated via `pause_admission` (`*bool`, default true). (3) `handleMerging` holds an already-CI-green PR at `StageMerging` without merging while the breaker is open (CI signal is untrustworthy platform-wide during an incident) — non-terminal, doesn't increment `MergeAttempts`, retried automatically once closed. (4) `handleCIFailed`'s suppression branch now parks the PR at `StageFailed` with a new `BreakerHoldActive` flag (mirrors GH-4610's `RebaseHoldActive`/`reAdoptHeldRebasePR` shape) instead of leaving it at `StageCIFailed` for indefinite per-tick reprocessing (which would keep refreshing the breaker's own quiet-period clock). Since `StageFailed` PRs have nothing PR-specific to poll for, `Controller.ReDriveBreakerHeldPRs` — driven by a new `startPlatformBreakerMonitor` background ticker in `cmd/pilot/main.go` (interval = `probe_interval`, default 5m) rather than per-PR polling — re-enters every `BreakerHoldActive` PR into `StageWaitingCI`, clears the hold flag, posts a re-adoption PR comment, and increments a capped `BreakerReadoptCount` (`maxBreakerReadoptAttempts`) the first tick after `PlatformBreaker.EvaluateClose()` (the standalone equivalent of `Observe`'s time-based close check, needed because nothing else calls `Observe` during a quiet spell with no CI activity anywhere to trigger it) reports `JustClosed`; a PR at the cap stays parked for a human. The monitor's close alert is fired directly/once/fleet-wide (not through the per-controller `alertPlatformBreakerTransition`) to preserve the "exactly one alert per transition" guarantee across N shared controllers. Part 2 of 2 (TASK-458) (GH-4792) |
| Irreversible-action inventory + `Verdict` contract (Phase 1) | ✅ | autopilot | - | - | `.agent/system/irreversible-actions.md` inventories every irreversible/operator-costly call site in the daemon (PR close, branch delete, fix-issue spawn, retry-budget burn, merge, ledger cancel/supersede writes, `escalateAndHold`) with reversibility tier, blast radius, current evidence shape, and whether its signal is authoritative or decorative under a project's `required_checks` allowlist. `Verdict` (`failure_class.go`) is the typed evidence-carrying contract those sites migrate to in later phases: unexported `class`/`evidence`/`source`/`scope` fields, constructor-only (`NewVerdict`, `NewUnknownVerdict`) — `NewVerdict` silently downgrades any requested class to `FailureClassUnknown` whenever `evidence` is empty, so an evidence-free destructive verdict cannot be constructed (no boolean `hasEvidence` flag, no confidence score). Phase 1 of TASK-459; changes no behaviour — no call site is migrated yet (Phase 2 gates `handleCIFailed`, Phase 3 covers executor/dispatcher/poller, Phase 4 adds the `check-destructive-calls.sh` grep gate). Responds to the 2026-08-06/07 incident cluster where a correct PR was closed three times during a GitHub Actions outage (#4765/#4768/#4770), a resurrected PR was re-closed within 90 seconds on superseded check-runs (#4781), and junk fix-issues were spawned (#4766/#4769/#4775) — all instances of the same root pattern: an irreversible action authorized on the same confidence threshold as a log line (GH-4796) |
| Non-default-base merge guard | ✅ | autopilot | - | - | `handleMerging` refuses to auto-merge a PR whose base isn't the repo's resolved default branch (`resolveMainBranchName()`) — parks it (`parkForBaseMismatch`, `autopilot/parked-awaiting-approval` label) with a one-time PR comment + escalation alert instead. `ProcessPR` refreshes `PRState.TargetBranch` from the already-fetched `ghPR.Base.Ref` unconditionally every tick (GH-4909, was previously only written when empty) — a stale cached target could otherwise either wedge a retargeted-to-main PR parked forever, or let a PR retargeted away from main sail through the guard on a stale "main" reading. If `TargetBranch` is still empty (legacy/restored row) it re-reads the PR rather than failing open on an unknown base; an unresolvable base (empty target + failed re-read) now fires a one-time escalation alert (GH-4909) instead of silently holding with only a per-tick log line. "Delivered" requires default-branch landing at three independent sites: the `isPilotPR` merged-PR scanner, `checkExternalMergeOrClose`'s finalize, and — belt-and-braces (GH-4909) — `handleMerging`'s own post-merge finalize block, which re-verifies the actually-landed base via a fresh `GetPullRequest` call (guards the narrow race where GitHub's merge endpoint lands on whatever base is current at merge time, not whatever was last cached) before closing the issue/labeling `pilot-done`/self-healing/moving the board; a non-default landing skips all of that and alerts instead, leaving the issue retryable. `checkExternalMergeOrClose` gained a `StageMerged` guard (it runs every poll tick before `ProcessPR`, and previously double-fired the external-merge issue-close/comment path the tick after `handleMerging`'s own finalize) plus a `MergeNotificationPosted` guard on its completion comment. `parkForBaseMismatch`'s one-time side effects are keyed off `EscalationReason` matching, not just the shared `Parked` bool (GH-4909) — a PR already parked for an unrelated GH-4596 approval misconfig that later hits a base mismatch still gets its own alert/comment instead of silently inheriting the earlier park. All four `DeleteBranch` call sites route through `safeDeleteBranch`, which checks `ListPullRequests` and refuses to delete a branch that is currently the base of another open PR (alerts once per branch, fail-closed on a check error). Root incident (2026-08-15): a ui PR stacked on a `pilot/GH-*` branch was squash-merged into that stack branch instead of main, closed its issue as delivered, and the stack branch was later deleted during unrelated cleanup — permanently orphaning the merged content with no trace on main and a self-sealing retry (GH-4872, fast-followed by GH-4909 after PR#4908 review) |

## Epic Management

| Feature | Status | Package | CLI Command | Config Key | Notes |
|---------|--------|---------|-------------|------------|-------|
| Epic decomposition engine | ✅ | executor | - | `decompose.enabled` | Detects complex tasks, plans + creates sub-issues (v0.20.2) |
| Haiku-powered subtask extraction | ✅ | executor | - | - | LLM structured extraction, regex fallback (v0.21.0) |
| Epic scope consolidation | ✅ | executor | - | - | Single-package epics consolidated → one task, no conflict cascade (v1.0.11) |
| Sub-issue PR wiring | ✅ | executor | - | - | Sub-issue PR callbacks chain back to autopilot controller (v0.23.1) |
| Linear sub-issue creation | ✅ | adapters/linear | - | `adapters.linear` | CreateIssue GraphQL mutation for decomposed epics (v1.27.0) |
| Decompose on retry | ✅ | executor | - | `retry.decompose_on_kill` | Retry via decomposition when task killed (signal:killed) (v2.10.0, GH-1729) |
| Conventional sub-issue titles | ✅ | executor | - | - | CC-format enforced on subtask titles: re-prompt → Approach B fallback → creation guard (GH-2494) |
| Sub-issue dedup guard | ✅ | executor | - | - | CreateSubIssues skips if open children referencing parent already exist (GH-2494) |
| createPilotIssue chokepoint | ✅ | adapters/github, autopilot | - | - | All Pilot-internal issue creation validated through CreatePilotIssue CC gate (GH-2494) |
| Epic-lifecycle synthetic canary | ✅ | .github/workflows, scripts | `workflow_dispatch` (scenario `epic-lifecycle`) | - | GH Actions scenario files a deliberately decomposable epic on the sandbox and asserts 5 invariants (≥2 children, no duplicate merged PR per child, parent auto-close, no stale `pilot-needs-clarification`, no cascade beyond direct children) via `canary-poll.sh --assert n-children-merged` (TASK-403 A3, GH-4242) |

## Test Coverage

| Feature | Status | Package | CLI Command | Config Key | Notes |
|---------|--------|---------|-------------|------------|-------|
| Linear notifier tests | ✅ | adapters/linear | - | - | Test coverage for Linear notifier (v2.10.0, PR #1726) |
| Jira notifier tests | ✅ | adapters/jira | - | - | Test coverage for Jira notifier (v2.10.0, PR #1730) |
| Asana notifier tests | ✅ | adapters/asana | - | - | Test coverage for Asana notifier (v2.10.0, PR #1727) |
| Slack Socket Mode tests | ✅ | adapters/slack | - | - | Test coverage for Slack Socket Mode and Telegram handlers (v2.10.0, PR #1721) |
| Alerts test lint fixes | ✅ | alerts | - | - | Fix lint errors in alerts test files (v2.10.0, PR #1722) |
| Planning timeout config | ✅ | config | - | `planning_timeout` | Add planning_timeout config field (v2.10.0, PR #1741) |
| SA5011 lint fix | ✅ | adapters | - | - | Add return after t.Fatal to satisfy SA5011 across all adapters (v1.46.0) |
| Duplicate test decl fix | ✅ | upgrade | - | - | Remove duplicate test declarations causing lint failure (v2.10.0, PR #1713) |
| CI pattern integration test | ✅ | testing | - | - | Integration test for CI pattern confidence boosting (v2.48.0, PR #1975) |

## Self-Management

| Feature | Status | Package | CLI Command | Config Key | Notes |
|---------|--------|---------|-------------|------------|-------|
| Version check | ✅ | upgrade | `pilot version` | - | Shows current |
| Auto-upgrade | ✅ | upgrade | `pilot upgrade` | - | Downloads latest |
| Hot upgrade | ✅ | upgrade | `u` key in dashboard | - | Graceful drain + restart, no orphaned tasks (v0.18.0, v0.63.0) |
| Config init | ✅ | config | `pilot init` | - | Creates default |
| Setup wizard | ✅ | main | `pilot setup` | - | Interactive config |
| Project add wizard | ✅ | cli | `pilot project add` (no flags) | - | gh CLI auth, repo picker, token seeding; `--no-wizard` for CI (GH-3017, v2.187.1) |
| Shell completion | ✅ | main | `pilot completion` | - | bash/zsh/fish |
| Zip archive support | ✅ | upgrade | - | - | Windows self-upgrade handles .zip archives |
| Self-upgrade writability preflight | ✅ | upgrade | - | - | Upgrade() probes binary dir before downloading; typed ErrBinaryNotWritable (dir+uid+hint); ERROR log + service_unhealthy alert on auto-upgrade failure; download progress logging throttled to ≥1s/10% steps (GH-4468) |
| Pipeline hardening | ✅ | executor | - | - | 4 correctness checks: constants, parity, coverage, dropped features (v1.10.0, GH-1321) |
| Pre-commit hooks | ✅ | - | `make install-hooks` | - | Git hooks for secret scanning + lint |
| Qwen Code bug fixes | ✅ | executor | `--backend qwen` | - | 5x pricing correction, CLI version check, session_not_found handling (v1.9.2, GH-1316) |

---

## Feature Summary

| Category | ✅ Working | ⚠️ Implemented | 🚧 Partial | ❌ Missing |
|----------|-----------|----------------|-----------|-----------|
| Core Execution | 56 | 0 | 0 | 0 |
| Intelligence | 15 | 0 | 0 | 0 |
| Input Adapters | 35 | 0 | 0 | 0 |
| Output/Notifications | 18 | 0 | 0 | 0 |
| Alerts & Monitoring | 13 | 0 | 0 | 0 |
| Quality Gates | 5 | 0 | 0 | 0 |
| Memory & Learning | 23 | 0 | 0 | 0 |
| Dashboard | 24 | 0 | 0 | 0 |
| Replay & Debug | 6 | 0 | 0 | 0 |
| Reports & Briefs | 4 | 0 | 0 | 0 |
| Cost Controls | 5 | 0 | 0 | 0 |
| Team Management | 3 | 0 | 0 | 0 |
| Infrastructure | 43 | 0 | 0 | 0 |
| Approval Workflows | 4 | 0 | 0 | 0 |
| Autopilot | 39 | 0 | 0 | 0 |
| Epic Management | 6 | 0 | 0 | 0 |
| Test Coverage | 9 | 0 | 0 | 0 |
| Self-Management | 10 | 0 | 0 | 0 |
| **Total** | **316** | **0** | **0** | **0** |

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

---

## Recent Additions (v2.149–v2.151)

> For full history run: `git log --oneline --grep="^feat" v2.149.0..HEAD`
> GitHub Releases: `gh release list`

| Feature | Version | Package | Notes |
|---------|---------|---------|-------|
| Issue-level success rate + rate_limited exclusion | v2.235.0+ | autopilot + memory + gateway | `pilot_issue_level_success_rate` / `pilot_issues_shipped_total` / `pilot_issues_attempted_total` dedupe by `task_id` across retries; `pilot_success_rate` now excludes `rate_limited` from the denominator; hydrator no longer folds declined/no_op/stalled/infra/skipped into `failed` (TASK-392 / GH-4070) |
| Poller skip-by-reason counters | v2.150.0 | adapters/github | `pilot_poller_skipped/dispatched/deferred` Prometheus counters (TASK-293 / GH-3064) |
| `WithRetry` centralized in `doRequest` | v2.150.0 | adapters/github | All GitHub client methods now get retry; `RecordAPIError` wired (TASK-294 / GH-3065) |
| Linear webhook Ed25519 verification | v2.149.4 | gateway + adapters/linear | `VerifyLinearSignature`; YAML wiring added in v2.151.0 (TASK-295 / GH-3060, GH-3066) |
| `quality.parallel` defaults to `false` | v2.149.4 | executor | Eliminates shared build-cache race (TASK-289 / GH-3057) |
| Config file mode 0600 | v2.149.4 | config | `~/.pilot/config.yaml` world-readable fixed (TASK-290 / GH-3058) |
| Branch-aware post-CI monitoring | v2.149.4 | autopilot | Uses `ResolvedEnv().Branch` instead of hardcoded `main` (TASK-291 / GH-3059) |
| Repo allowlist Phase B | v2.149.0 | adapters/github | `CreatePilotIssue` validates against allowlist (GH-3047) |
| `safeGo()` panic-recovery wrapper | v2.150.x | executor + adapters | All goroutine spawns wrapped (TASK-292) |
| `IsTaskShipped` predicate | v2.151.x | autopilot | Cross-site invariant preventing double-dispatch (TASK-296 / GH-3091) |
| Ghost-SHA guard | v2.151.x | executor | Fail-closed when commit_sha already on base branch (TASK-300 / GH-3099) |
| Merge→done race window closed | v2.163.0 | autopilot + adapters/github | `Controller.SetOnIssueDone` fires `MarkProcessed` on all pollers at PR-merge, preventing phantom re-dispatch during label propagation lag (TASK-321 PR-4 / GH-3271) |
| Decomposed-child base-branch pinning | v2.184.x | executor | Resolve `BaseBranch` from main-repo git context before worktree creation; decomposed children never PR against a sibling branch (GH-3540) |
| GitHub-poller auth-failure escalation | v2.207.x | adapters/github + health | Consecutive 401/non-ratelimit-403 fetch errors escalate to ERROR log + alert at threshold (`WithAlertProcessor`/`WithTokenSource`); `pilot doctor` disabled-subsystems panel; `pilot config show` secret redaction + `--reveal` (TASK-379 V4 / GH-3839) |
| Executor prompt memory injection | v2.219.x | executor | `BuildPrompt` appends a "Known pitfalls from project memory" block ranked via `graphrecall.RecallRelevant`, gated on `MemoryInjection.Enabled`/non-LocalMode/Navigator/recall hits, capped at `min(MaxMemories,5)` entries and ~1500 chars (TASK-387 / GH-3909) |
