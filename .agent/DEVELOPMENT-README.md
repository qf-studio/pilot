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

Disable via config: `executor.navigator.auto_init: false`

---

## Quick Navigation

| Document | When to Read |
|----------|--------------|
| CLAUDE.md | Every session (auto-loaded) |
| This file | Every session (navigator index) |
| `.agent/system/FEATURE-MATRIX.md` | What's implemented vs not |
| `.agent/system/ARCHITECTURE.md` | System design, data flow |
| `.agent/system/PR-CHECKLIST.md` | Before merging PRs in `--env=prod` mode |
| `.agent/tasks/TASK-XX.md` | Active task details |
| `.agent/sops/*.md` | Before modifying integrations |
| `.agent/.context-markers/` | Resume after break |

## Current State

**Current Version:** v2.155.0 | **323 features working**

**Recent (v2.149.4, May 25 2026):** Wave 1 hardening sweep + Linear webhook signature verification (from `.agent/audits/AUDIT-2026-05-25.md`).
- `fix(quality)`: Default `quality.parallel` → `false`. Eliminates the shared `~/.cache/go-build` / `~/.cache/golangci-lint` race that produced 11 spurious gate failures in 3h at the 2026-05-21 workshop. Opt-back-in via explicit `quality.parallel: true`. New SOP: `.agent/sops/quality/parallel-gate-cache-race.md`. (TASK-289 / #3057)
- `fix(config)`: `~/.pilot/config.yaml` now writes mode `0600` (was `0644`) + parent dir `0700`. Existing `0644` configs tightened on next save via explicit `os.Chmod` (since `os.WriteFile` leaves existing-file modes alone). The file holds GitHub PAT, Linear API key, Slack bot token, Anthropic key — was world-readable on shared workstations. (TASK-290 / #3058)
- `fix(autopilot)`: `getMainBranchSHA` reads `c.config.ResolvedEnv().Branch` instead of the hardcoded `"main"` literal. Repos defaulting to `develop` / `master` / `trunk` now get correct post-merge CI monitoring; previously releases could fire before the real default branch's CI completed. (TASK-291 / #3059)
- `fix(linear)`: New `VerifyLinearSignature` primitive (Ed25519 over raw request body) + gateway wiring. `handleLinearWebhook` rejects 401 on bad signatures when a public key is configured. **Caveat:** `gateway.Config.LinearWebhookPublicKey` has no YAML decode in `cmd/pilot/main.go` yet — verification is gated behind a field nothing can set. Follow-up in backlog. (TASK-295 / #3060)
- `fix(security)`: `scripts/check-secret-patterns.sh` broadened from `--include='*_test.go'` (~50 files) to all tracked files (1086) with an explicit allowlist for the 4 educational files (CLAUDE.md / CONTRIBUTING.md / testutil/tokens.go / TASK-41 postmortem). Plus: GitHub-side `secret_scanning` + `push_protection` enabled on the repo (was disabled). (TASK-299 / #3062)
- `chore(ci)`: Deleted dead `.github/workflows/ci-autofix.yml` + the `notify-failure` job in `ci.yml` that fed it. The workflow had been emitting phantom 0-job "failure" run records on every push for 4+ days, generating alert-email noise. (#3063)

**Previous (v2.149.0, May 21 2026):**
- `feat(adapters/github)`: **Repo-allowlist guardrail — Phase B (adapter)** — `CreatePilotIssue` now takes an `IssueAllowlist` parameter and refuses unconfigured repos via `validateIssueRepo`. `IssueAllowlist` defined locally in `internal/adapters/github` to avoid the executor→github import cycle; `cmd/pilot/repo_allowlist.go::configRepoAllowlist` satisfies both interfaces transparently. (#3047, follow-up to GH-3027)
- `docs(sops)`: Subprocess OOM tuning runbook at `.agent/sops/subprocess-oom-tuning.md` — companion to the v2.148.0 RSS telemetry. (#3048)

**Full implementation status:** `.agent/system/FEATURE-MATRIX.md`

### Autopilot Environments (v1.59.0+)

The `--env` flag selects a deployment pipeline:

| Flag | CI Wait | Approval | Post-Merge | Use Case |
|------|---------|----------|------------|----------|
| `dev` | Skip | No | none | Fast iteration, trust the bot |
| `stage` | Yes | No | none | CI must pass, then auto-merge |
| `prod` | Yes | Yes | tag | CI + human approval required |

```bash
pilot start --env=stage --telegram --github  # Balanced (recommended)
```

---

## Active Work

**Source of truth: GitHub Issues with `pilot` label**

```bash
gh issue list --label pilot --state open
gh issue list --label pilot-in-progress --state open
gh pr list --state open
```

### Backlog

| Priority | Topic | Why |
|----------|-------|-----|
| P1 | Multi-tenant SaaS mode | Single-user CLI → hosted needs auth, isolation |
| P1 | Public launch prep | Landing page, onboarding, pricing, billing |
| P1 | Web dashboard polish | React UI functional but needs design pass |
| P1 | Fix `shouldTriggerRelease()` | Doesn't check `ResolvedEnv().Release` — only top-level config |
| P1 | **Wave 2 (from 2026-05-25 audit)** — 3 S-sized refactors ready to ship | `safeGo()` panic-recovery sweep [TASK-292](tasks/TASK-292-safego-panic-recovery-sweep.md) · Poller skip-reason counters [TASK-293](tasks/TASK-293-poller-skip-reason-counters.md) · Centralize `WithRetry` in github `doRequest` [TASK-294](tasks/TASK-294-github-retry-in-dorequest.md). Merge order: 293 before 292 (both touch poller.go); 294 independent. Resumption marker: `.agent/.context-markers/2026-05-25-wave1-and-task295-shipped.md`. |
| P1 | TASK-295 follow-up: wire `linear.webhook_public_key` YAML → `gateway.Config.LinearWebhookPublicKey` | Without this glue in `cmd/pilot/main.go`, the v2.149.4 Ed25519 verification is gated behind a config field that has no decode path. Small (≤30 LOC); blocks the security improvement from being active. |
| P2 | **Wave 3 (from 2026-05-25 audit)** — 3 M-sized refactors | `IsTaskShipped` predicate + cross-site invariant test [TASK-296](tasks/TASK-296-istaskshipped-predicate.md) · Docs drift sweep + `{CURRENT_VERSION}` interpolation [TASK-297](tasks/TASK-297-docs-drift-sweep.md) · Consolidate 7 `*_processed` SQLite tables into `adapter_processed` [TASK-298](tasks/TASK-298-consolidate-processed-tables.md). Merge order: 296 before 298 (both touch `state_store.go`). |
| P2 | TASK-288 (poller dispatch false-positive) — keep open until Wave 3 closes | Split across [TASK-296](tasks/TASK-296-istaskshipped-predicate.md) (steps 1+3) and [TASK-298](tasks/TASK-298-consolidate-processed-tables.md) (step 2). Close manually via `gh issue close` with links to both PRs. [TASK-288](tasks/TASK-288-poller-dispatch-false-positive-fix.md) |
| P2 | Pick one release pipeline: `make release` vs goreleaser-on-tag | They raced on v2.149.4 — `make release` uploaded 5 assets first, goreleaser hit `422 already_exists` on the same names, exited 1. Release succeeded (10 total assets across both); only the goreleaser workflow showed FAILED. Decide: keep goreleaser (just `git tag && git push`), or keep `make release` (delete `.github/workflows/release.yml`), or make them idempotent. Also: Makefile `release` target multi-line `NOTES=` still breaks the shell parse — same bug as v2.146.7 cut. |
| P2 | E2E test suite | No integration tests — reliability untested |
| P2 | Web dashboard auth | Token-based auth for remote access |
| P2 | Mobile-responsive dashboard | Primary use case is phone access |
| P2 | Scope TUI dashboard to single project | Today `-p` scopes execution only; metrics/sparklines mix all projects — [TASK-284](tasks/TASK-284-dashboard-project-scope.md) |
| P3 | GitHub App auth | PAT → installable GitHub App |
| P3 | Add `project_path` to `eval_tasks` + scope eval panel | Follow-up to TASK-284; lets eval/bench panel scope per-project instead of `[global]` label — [TASK-285](tasks/TASK-285-eval-tasks-project-path.md) (blocked by TASK-284) |
| P3 | `pilot project add` gh wizard | Interactive repo picker + token seed from `gh auth` — [TASK-282](tasks/TASK-282-project-add-gh-wizard.md) → [#3017](https://github.com/qf-studio/pilot/issues/3017) (not yet `pilot`-labeled) |
| P3 | Audit §3 Wave 4+ candidates | Not in Top 10 / not yet decomposed: `RecordAPIError` wiring beyond github · `AlertTypeOOMKilled` · multi-gate scanner phase discipline · subprocess migration end-to-end validation · `autopilot` adapter coupling refactor · SQL `withTx` helper · generic `Poller[T]` extraction · `Releaser` frozen-at-startup fix. Source: `.agent/audits/AUDIT-2026-05-25.md` §3. |

---

Release history: see `git log`, GitHub releases, and `.agent/tasks/archive/`.

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

### Memory / Testing
- `internal/memory/store.go` - SQLite storage
- `internal/memory/graph.go` - Knowledge graph
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

Key per-adapter env vars:
- `GITHUB_TOKEN` - GitHub polling + PR creation
- `LINEAR_API_KEY` - Linear webhook adapter
- `SLACK_BOT_TOKEN` - Slack Socket Mode adapter
- `TELEGRAM_BOT_TOKEN` - Telegram adapter

## CLI Flags

### `pilot start`
- `--env=ENV` - Enable autopilot mode: `dev`, `stage`, `prod`
- `--dashboard` - Launch TUI dashboard with live task monitoring
- `--telegram` - Enable Telegram polling
- `--github` - Enable GitHub polling
- `--slack` - Enable Slack Socket Mode
- `--daemon` - Run in background
- `--sequential` - Wait for PR merge before next issue (default)

## Documentation Loading Strategy

1. **Every session**: This file
2. **Feature work**: Task doc in `.agent/tasks/`
3. **Architecture changes**: `.agent/system/ARCHITECTURE.md`
4. **Integration work**: Relevant adapter code
