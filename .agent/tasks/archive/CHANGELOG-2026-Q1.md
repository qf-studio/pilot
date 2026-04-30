# Pilot Changelog — 2026 Q1 (Feb–Mar)

Archived from `.agent/DEVELOPMENT-README.md` on 2026-04-30.
For context see: git log, GitHub releases, or `.agent/tasks/archive/`.

---

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
