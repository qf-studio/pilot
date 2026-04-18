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

**Current Version:** v2.99.1 | **319 features working**

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
| URL-Encode Branch Names | Done | `url.PathEscape(branch)` in DeleteBranch/GetBranch — fixes 404 on slash branches (v1.28.0) |
| Branch Cleanup on PR Close | Done | Delete remote branches on PR close/fail, not just merge (v1.35.0) |
| Backend-Aware Preflight | Done | Preflight CLI check matches configured backend (claude/opencode/qwen) (v1.39.0) |
| Sonnet 4.6 Model Routing | Done | Default simple/medium tasks to Sonnet 4.6 (near-Opus quality, 40% cheaper) (v1.40.0) |
| Sonnet 4.6 Codebase Update | Done | Update all stale model IDs to Sonnet 4.6 / Opus 4.6 across defaults, wizard, tests (v1.40.1) |
| Docker / Helm Chart | Done | Dockerfile, Helm chart, deployment guide in docs (v1.46.0) |
| Claude Code Hooks v3 | Done | Matcher as regex string, Stop hooks no matcher, dedup cleanup (v1.50.0) |
| Dashboard Git Graph | Done | Live git graph panel in TUI with branch visualization (v1.53.0) |
| Desktop App (Wails) | Done | Wails v2 desktop app with React dashboard, macOS builds (v1.53.1) |
| Dashboard API | Done | REST endpoints: `/api/v1/tasks`, `/api/v1/autopilot`, `/api/v1/history` (v1.55.0) |
| Web Dashboard | Done | Embedded React frontend at `/dashboard` with SSE/WebSocket streaming (v1.56.0) |
| No-Decompose Defense | Done | `detectEpic` checks `no-decompose` label as defense-in-depth (v1.57.0) |
| Incremental Lint | Done | `golangci-lint --new-from-rev` prevents unrelated lint blocking PRs (v1.57.0) |
| Environment Config | Done | `EnvironmentConfig` + `ResolvedEnv()` replaces hardcoded env checks (v1.59.0) |
| Post-Merge Deployer | Done | Webhook and branch-push deployment triggers after merge (v1.60.0) |
| CLI `--env` Flag | Done | Renamed `--autopilot` to `--env`, updated onboarding + config (v1.60.1) |
| Prod Auto-Approve Safety | Done | Block auto-approve in prod when pre_merge approval disabled (v1.61.0) |
| Gateway in Polling Mode | Done | HTTP server starts in background during polling mode for desktop/web (v1.62.0) |
| History Dedup | Done | Desktop app deduplicates execution records per issue (v1.62.0) |
| Desktop Native Titlebar | Done | macOS `TitleBarDefault()`, simplified two-column layout (v1.62.0) |
| Pattern Learning | Done | Learn from PR reviews, inject patterns into prompts (v2.25.0) |
| Execution Mode Auto | Done | Scope-based auto-switching between sequential/parallel (v2.25.0) |
| Auto-Rebase on Conflict | Done | GitHub Update Branch API before close-and-retry (v2.25.0) |
| CI Fix Dependencies | Done | `Depends on: #N` annotations in fix issues (v2.25.0) |
| Messenger Refactor | Done | Extracted TelegramMessenger/SlackMessenger, shared Handler (v2.25.0) |
| Plane.so Adapter | Done | REST client, polling, webhooks, HMAC-SHA256, state transitions (v2.25.0) |
| Discord Adapter | Done | Gateway WebSocket, reconnection, rate limits, mention strip, Author.Bot filter (v2.80.0) |
| GitHub Projects V2 Board | Done | GraphQL board sync: Review/Done/Failed columns (v2.30.0) |
| Common Adapter Registry | Done | Unified Adapter interface, generic ProcessedStore (v2.30.0) |
| Handler Refactoring | Done | `handleIssueGeneric()` consolidates 5 adapter flows (v2.30.0) |
| Windows Build Fix | Done | Removed SQLite WAL artifacts breaking checkout (v2.34.0) |
| Dashboard Git Graph Sizes | Done | Small/medium/large/hidden modes, auto-size by terminal width (v2.35.0) |
| Dashboard Responsive | Done | Stacked layout on narrow terminals, full-width panels (v2.38.0) |
| Docs Version Sync CI | Done | Workflow closes previous version-sync PRs (v2.38.11) |
| PR Reviewer Auto-Assign | Done | Reviewers config for auto-assigning PR reviewers (v2.77.0) |
| `pilot init` Command | Done | Project scaffolding — config, hooks, Navigator setup (v2.77.0) |
| LocalMode Prompt Priority | Done | `BuildPrompt` checks `task.LocalMode` FIRST before Navigator (v2.78.0) |
| Configurable HeartbeatTimeout | Done | `executor.heartbeat_timeout` in config.yaml, default 15m (v2.78.0) |
| Success Recovery on Timeout | Done | Recover success when Claude Code times out after completing work (v2.78.0) |
| Skip Nav Auto-Init in LocalMode | Done | No `.agent/` creation in bench/CI sandboxes (v2.78.0) |
| OOM/SIGKILL Handling | Done | Classify exit 137/139 as OOM, skip retry on unrecoverable errors (v2.79.0) |
| Skip Quality Gates in LocalMode | Done | Quality gates bypass in bench/CI execution (v2.79.0) |
| Discord Bot Self-Loop Fix | Done | `Author.Bot` field filter, single handler via poller registry (v2.79.5-v2.79.6) |
| Discord Production Hardening | Done | Reconnection, rate limits, mention strip, sync.Once, per-task progress (v2.80.0) |

### Telegram Interaction Modes (v0.6.0)

| Mode | Trigger | Behavior |
|------|---------|----------|
| Chat | "What do you think about..." | Conversational response, no code changes |
| Questions | "What files handle...?" | Quick read-only answers (90s timeout) |
| Research | "Research how X works" | Deep analysis, output to chat + saves to `.agent/research/` |
| Planning | "Plan how to add X" | Creates plan with Execute/Cancel buttons |
| Tasks | "Add a logout button" | Confirms, executes with PR |

**Default behavior**: Ambiguous messages now default to Chat mode instead of Task, preventing accidental PRs.

### Autopilot Environments (v1.59.0+)

The `--env` flag (renamed from `--autopilot` in v1.60.1) selects a deployment pipeline:

| Flag | CI Wait | Approval | Post-Merge | Use Case |
|------|---------|----------|------------|----------|
| `dev` | Skip | No | none | Fast iteration, trust the bot |
| `stage` | Yes | No | none | CI must pass, then auto-merge |
| `prod` | Yes | Yes | tag | CI + human approval required |

Custom environments supported via `EnvironmentConfig` in config YAML.

```bash
pilot start --env=dev --telegram --github    # YOLO mode
pilot start --env=stage --telegram --github  # Balanced (recommended)
pilot start --env=prod --telegram --github   # Safe, manual approval
```

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

**Problem**: `recoverStaleTasks()` marks queued tasks older than 5 min as failed. When `runner.Execute()` blocks for 10-20 min (GLM-5.1, complex tasks), queued tasks behind it get nuked even though the worker is actively processing.

**Fix**: Add `isWorkerProcessing()` helper and guard in `recoverStaleTasks()` — skip tasks for projects with active workers (`processing == true`).

**Plan**: `.agent/tasks/TASK-25-stale-recovery-queue-drain-fix.md`

**Changes**:
- `internal/executor/dispatcher.go` — `isWorkerProcessing()` + guard (~16 lines)
- `internal/executor/dispatcher_test.go` — 2 new tests (~85 lines)

### Config Update: Opus 4.7 High Effort (2026-04-17)

Changed from GLM-5.1 back to Claude Opus 4.7 with high effort mode:

| Config | Before | After |
|--------|--------|-------|
| Pilot `default_model` | `glm-5.1` | `claude-opus-4-7` |
| Pilot `model_routing.complex` | `claude-opus-4-6` | `claude-opus-4-7` |
| Claude Code `ANTHROPIC_MODEL` | `glm-5.1` | `claude-opus-4-7` |
| Claude Code `effortLevel` | `high` | `high` |

**Note**: Z.AI API base URL still configured — ensure provider supports `claude-opus-4-7`.

### Terminal-Bench Benchmark (feat/pilot-bench-real)

Full 89-task run in progress on Daytona. Benchmarking real Pilot Go binary vs stock Claude Code (58%).
- **Validation**: 3/3 (100%) on val10 — break-filter, chess, gcode all pass
- **Branch**: `feat/pilot-bench-real` — bench workspace, not for merge to main
- **Findings → main**: GH-2103 (LocalMode priority), GH-2104 (configurable heartbeat) — both merged
- **Docs**: `pilot-bench/README.md`, `pilot-bench/WORKLOG.md`, `.agent/sops/development/pilot-bench-real-binary.md`

**Source of truth: GitHub Issues with `pilot` label**

```bash
gh issue list --label pilot --state open
gh issue list --label pilot-in-progress --state open
gh pr list --state open
```

### Discord Intent Classification (GH-2121/2122/2123)

All I/O channels should use Haiku LLM classifier. Discord has none (every message = task). Slack falls back to unreliable regex on LLM timeout.
- **GH-2121**: Wire Haiku classifier into Discord handler
- **GH-2122**: Replace Slack's regex fallback with `IntentChat` default
- **GH-2123**: Remove dead `DetectIntent()` regex classifier (after 2121+2122)

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

### Docs Refresh ✅ Complete (8/8)

Nextra 4 migration (PR #1409) + 8 docs pages covering all 156 features:

| Issue | Title | PR | Status |
|-------|-------|----|--------|
| GH-1411 | Epic Decomposition guide | #1422 | Merged (via retry GH-1420) |
| GH-1412 | Claude Code Hooks guide | #1419 | Merged |
| GH-1413 | Multi-Repo Polling guide | #1428 | Merged (retry) |
| GH-1414 | Signal Parser reference | #1423 | Merged |
| GH-1415 | Execution Backends SDK features | #1424 | Merged |
| GH-1416 | Stagnation Monitor section | #1426 | Merged |
| GH-1417 | Navigator Auto-Init section | #1425 | Merged |
| GH-1418 | Homepage + config reference | #1427 | Merged |

**Docs site**: 60+ pages, Nextra 4, 316 features documented. Discord, Plane, board sync, git graph, auto-rebase, review learning sections added (v2.53.0).

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
| ~~P3~~ | ~~Learn from PR reviews~~ | ✅ Done (v2.25.0) — `LearnFromReview()` in memory/feedback.go |

---

## Completed Log

### 2026-04-18

| Item | What |
|------|------|
| **v2.99.1** | `fix(executor)`: self-review `--resume` fallback mirrors Qwen logic + `sanitizeFilename` strips path separators (GH-2377, PR #2378) |
| **v2.99.0** | `refactor(autopilot)`: remove dead `prod-X.Y.Z` tag auto-push — `sync-docs.yml` already handles GitLab deploy (GH-2374, PR #2375 reverts #2370) |
| **v2.98.1 / v2.98.0** | Executor env injection of `api_base_url`/`default_model`/`api_auth_token` into Claude Code subprocess (GH-2287/GH-2371); `default_branch` honored (GH-2286); conventional-commit rewrite suggestion after 2nd reject; docs version sync unblocked manually via [PR #2373](https://github.com/qf-studio/pilot/pull/2373) (GitHub Actions lacks PR-create permission at repo level) |
| **Jira search migration** | GH-2289 deprecated `/rest/api/2|3/search` → `/rest/api/3/search/jql` (PR #2376, ghost-abandoned on first retry then recovered via escape-hatch dispatch) |
| **Known gotcha** | `docs-version-sync.yml` can't auto-PR after release — `GITHUB_TOKEN` lacks PR-create permission (repo Settings toggle required) |
| **Reverted premise** | [PR #2370](https://github.com/qf-studio/pilot/pull/2370) shipped on a wrong assumption: `prod-X.Y.Z` GitHub tags trigger nothing — deploy lives in `sync-docs.yml`'s tag-push to GitLab. v2.99.0 reverts. |

### 2026-04 (between 2026-03-13 and 2026-04-17)

| Cluster | Highlights |
|---------|------------|
| **Autopilot hardening** | `ScanExistingPRs` no longer clobbers `RestoreState` on startup; idempotent merge-completion notification (dedup re-entry); skip `pilot-retry-ready` when `pilot-done` already set; periodic external-merge scan (GH-2251); cross-repo PR release-op fix (GH-2243); `notifyExternalClose` now closes parent (GH-2198); strip `GH-XXXX` prefix from squash titles for conventional-commit detection (GH-2312); post success comment on merge close (GH-2297) |
| **Executor / non-Anthropic providers** | `default_model` + `api_base_url` for Z.AI/GLM/OpenRouter/LiteLLM (GH-2287, TASK-24); fix bare `NewRunner()` callsites ignoring executor config (GH-2286); cache-aware token tracking + cost estimation (GH-2164); 1M context window control + updated budget defaults (GH-2163); OOM/SIGKILL handling for Claude Code subprocess on large Navigator contexts (GH-2324); gate PR creation on conventional-commit title (GH-2325); signal executor mode + classify no-diff (GH-2328); persist backend stderr + final message on failure |
| **Dispatcher / poller** | `hasLiveWorker` guard on running-task reaper (GH-2331); persist `Task.Labels` across queue round-trip (GH-2326); delete orphan rows for already-completed tasks; check execution store before re-dispatch (GH-2242); skip retry of closed issues with stale `pilot-failed` (GH-2252); auto-retry stuck `pilot-failed` (GH-2176); clear stale `pilot-in-progress` on startup (GH-2301); retry grace period + TaskChecker (GH-2201); resolve stale-threshold defaults treating 0 as unset (GH-2226) |
| **Epic / sub-issues** | Validate sub-issue titles — reject LLM analysis-style titles (decomposition); native sub-issue GraphQL linking (GH-2210/2211/2212); merge-wait wired via `SubIssueMergeWaitFn` (GH-2178/2179); worktree isolation for sub-issues (GH-2178); sub-issues branch from real repo (GH-2177); `gh pr create --head` to bypass dirty worktree check |
| **Dashboard** | Adapter poller tasks visible in gateway+dashboard mode (GH-2291); refresh history/metrics from DB every 5s (GH-2248) and stop wiping sparklines (GH-2280); gate stdout prints on dashboard mode (GH-2333); zero-poller startup warning (GH-2307) |
| **Adapters** | GitLab: string type for `MergeRequest.detailed_merge_status` (#2295), MR creation fixes (#2271), parallel `OnIssue` with result (#2236); GitHub: adapter-specific PR/MR number extraction (GH-2293); auto-retry issues with `pilot-retry-ready` (GH-2276); Discord greeting-prefix misclassification (#2162); broken Discord invite link (#2231); GitHub issue templates (GH-2298) |
| **Release / packaging** | GoReleaser `brews` section for Homebrew auto-publish (GH-2188); migrate tap to `qf-studio/homebrew-pilot` (#1); LLM-generated GitHub release notes summary (GH-2173); `pilot doctor` GitHub config validation (GH-2304/2306); install script jq-based version parsing (GH-2260) |
| **Bench / Harbor** | Self-verification harness for `PilotAgent` (#2238); remove oracle test access (GH-2237 — Harbor compliance); AWS orchestrator signal handling (GH-2267); always regenerate stale task manifest (GH-2269) |
| **Alerts** | `task_stuck` Slack flood fix — wire progress events, per-rule cooldown, orphan cleanup (GH-2204) |
| **Docs / project** | Repo rename `alekspetrov` → `qf-studio` across non-Go files (GH-2196), llms.txt URLs (GH-2192), brew tap refs (GH-2189); navbar version-sync workflow trigger fix via `workflow_dispatch` (GH-2206); Terminal-Bench #1 ranking + flow diagram fixes; epic merge-wait docs (GH-2183); Navigator-only rules scoped to interactive sessions (#2327) |

### 2026-03-13

| Item | What |
|------|------|
| **v2.80.0** | Discord production hardening: reconnection, rate limits, mention strip, per-task progress, sync.Once (PR #2119 via GH-2117) |
| **v2.79.5-v2.79.6** | Discord bot self-loop fix (Author.Bot field) + duplicate handler removal (direct-to-main) |
| **v2.79.0** | OOM/SIGKILL handling (exit 137/139), skip quality gates in LocalMode (PRs #2113, #2114) |
| **v2.78.0** | LocalMode prompt priority, configurable HeartbeatTimeout, success recovery on timeout, skip nav auto-init (PRs #2105, #2106, #2109, #2110) |
| **v2.77.0** | PR reviewer auto-assign, `pilot init` project scaffolding (PRs #2101, #2102) |
| **Docs** | Version strings synced to v2.76.0, evaluation system page, epic auto-close docs, PR review feedback section (PRs #2094-#2097) |
| **Discord live test** | First real-world Discord integration — found and fixed 15 bugs across 3 releases |
| **TASK-10** | Completed — Discord handler wired and tested |
| **TASK-12** | Completed — Discord production hardening (GH-2118 closed, PR #2119 merged) |
| **Open issues** | GH-2121 (Discord intent), GH-2122 (Slack regex fallback), GH-2123 (remove dead classifier) |

### 2026-02-28

| Item | What |
|------|------|
| **v2.38.11** | Docs website update: version strings, feature counts, 4 new content sections |
| **Docs: version strings** | Updated v1.8.1 → v2.38.11 in quickstart + installation, navbar badge (GH-1918) |
| **Docs: dashboard git graph** | Added Git Graph section, `g` shortcut, responsive layout docs (GH-1919) |
| **Docs: autopilot auto-rebase** | Added Conflict Resolution section + CI fix dependency annotations (GH-1920) |
| **Docs: review learning** | Added Review Learning section to memory page (GH-1921) |
| **CI fix** | `docs-version-sync.yml` now closes previous version-sync PRs before creating new one |
| **Cleanup** | Closed 7 stale version-sync PRs (#1901-#1917), deleted orphan branches |
| **Project files** | Updated CLAUDE.md + DEVELOPMENT-README.md to v2.38.11, added Discord/Plane/board sync to status |

### 2026-02-25–27

| Item | What |
|------|------|
| **v2.25.0–v2.33.0** | Discord adapter, Plane adapter, GitHub Projects V2 board sync, pattern learning, auto-rebase, handler refactoring, common adapter registry |
| **v2.34.0** | Windows build fix (SQLite WAL artifacts), dashboard project path fix |
| **v2.35.0–v2.38.11** | Dashboard git graph size variants, responsive layout, auto-sizing, narrow terminal fixes |

### 2026-02-20

| Item | What |
|------|------|
| **v1.62.0** | Release with gateway fix, desktop polish, history dedup |
| **Gateway in polling mode** | Start HTTP server in background during polling mode — desktop app detects daemon via `/health` (GH-1662, PR #1664) |
| **History dedup** | Desktop app deduplicates execution records per issue, success takes priority (GH-1663, PR #1665) |
| **Desktop native titlebar** | `TitleBarHiddenInset()` → `TitleBarDefault()` — fixes traffic light overlap |
| **Desktop layout** | Simplified two-column flex layout, equal columns, metrics in left column |
| **Desktop panel spacing** | Unified `gap-3` + `px-2` across Queue/History/Logs, `whitespace-nowrap` on issue IDs |
| **Desktop panel sizing** | Autopilot + History `shrink-0`, Logs `flex-1` fills remaining space |
| **Desktop logo** | Removed 3-space indent from ASCII PILOT logo |
| **Desktop metrics** | Removed extra padding from MetricsCards to align with panels below |
| **Env config** | Configured `environments.stage` with auto-release, post-merge tag, CI required |
| **CLI migration** | `--autopilot=stage` → `--env stage`, `--auto-release` → YAML config |
| **v1.61.0** | Prod auto-approve safety (PR #1628), env redesign v1.59.0–v1.60.2 (PRs #1644, #1651, #1652, #1655) |
| **Desktop TUI parity** | Pilot executed GH-1657/1658/1660/1661 — redesigned desktop frontend layout + components |

### 2026-02-19

| Item | What |
|------|------|
| **v1.61.0** | Prod auto-approve safety: block auto-merge when `pre_merge` approval disabled in prod (PR #1628 by @dastanko) |
| **v1.60.2** | Environment context in notifications and dashboard (GH-1643) |
| **v1.60.1** | Rename `--autopilot` to `--env`, update onboarding + config surface (GH-1642) |
| **v1.60.0** | Post-merge deployer: webhook and branch-push deployment triggers (GH-1641) |
| **v1.59.0** | EnvironmentConfig + ResolvedEnv() — replaces all hardcoded env checks (GH-1640) |
| **v1.58.0** | CI error logs in fix issues + circuit breaker keyed by branch lineage (GH-1566, GH-1567) |
| **v1.57.0** | No-decompose label defense-in-depth + incremental lint (GH-1568, GH-1569) |
| **v1.56.0** | WebSocket log streaming for web dashboard (GH-1613) |
| **v1.55.0** | Dashboard API endpoints + execution milestones log store (GH-1599, GH-1600, GH-1601) |
| **v1.54.0** | GoReleaser desktop app artifact for macOS (GH-1614) |
| **v1.53.1** | Desktop app browser CSS adaptation + HTTP data provider (GH-1610, GH-1611) |
| **v1.53.0** | Embed React frontend at `/dashboard` + gateway wiring (GH-1609, GH-1612) |

### 2026-02-18

| Item | What |
|------|------|
| **v1.40.1** | Update all stale model IDs: `claude-sonnet-4-5-20250929` → `claude-sonnet-4-6`, `claude-opus-4-5` → `claude-opus-4-6` across config defaults, wizard, onboarding, and ~30 test refs (GH-1490) |
| **v1.40.0** | Sonnet 4.6 model routing: default simple/medium → `claude-sonnet-4-6` (40% cheaper than Opus, near-Opus quality). Updated defaults, example config, tests (GH-1488) |
| **Docs** | Updated 9 docs pages with Sonnet 4.6 / Opus 4.6 model references — model-routing, configuration, architecture, execution-backends, prerequisites, troubleshooting, replay, dashboard, why-pilot (GH-1492) |

### 2026-02-17

| Item | What |
|------|------|
| **v1.39.0** | Backend-aware preflight checks: `PreflightOptions.BackendType` matches configured backend (claude/opencode/qwen) instead of hardcoding `claude` (GH-1483 — @kegesch contribution) |
| **v1.35.0** | Delete branches on PR close/fail: `removePR()` in autopilot controller now calls `DeleteBranch()` for all PR removal paths, not just merged PRs |
| **v1.28.0** | URL-encode branch names: `DeleteBranch()` and `GetBranch()` use `url.PathEscape(branch)` — fixes silent 404 on branch names with slashes (GH-1383 fix) |
| **Docs refresh** | 8 issues (GH-1411–1418) all merged — epic decomp, hooks, multi-repo, signal parser, SDK features, stagnation, auto-init, config ref. 60 pages total. |
| **Nextra 4** | Docs site migrated from Nextra 2 to Nextra 4 (App Router) — PR #1409, GH-1407 closed |
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
curl -fsSL https://raw.githubusercontent.com/qf-studio/pilot/main/install.sh | bash
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
