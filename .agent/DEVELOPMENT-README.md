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
| 2. Execute | `"dispatch TASK-XX to Pilot"` (auto-invokes `nav-pilot`, v6.16.0+) — or raw `gh issue create --label pilot` | Hand off to Pilot for execution |
| 3. Review | `gh pr view <n>` | Check Pilot's PR |
| 4. Ship | `gh pr merge <n>` | Merge when approved |

### Quick Commands

```bash
# Plan a feature (Navigator does the thinking)
/nav-task "Add rate limiting to API endpoints"

# Hand off to Pilot — preferred: nav-pilot skill (Navigator v6.16.0+)
#   "dispatch TASK-XX to Pilot"          # auto-resolves doc → gh issue from H1 + --body-file
# Raw equivalent (when bypassing the skill):
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

**Current Version:** v2.187.0 | full status in `.agent/system/FEATURE-MATRIX.md`

**Recent (v2.186.2–v2.186.9, June 10–15 2026):** **TASK-322 security audit CLOSED + decomposition-integrity wave 2 + hot-upgrade verification + executor SHA-harvest fix + retry-machinery hardening — all SHIPPED.**
- **v2.186.9 — TASK-322 audit CLOSED + flaky-test de-flake (June 15):** Wave 4's 13 lows re-audited against `main` (all 10 actionable survived; none pre-fixed by Waves 2–3) and shipped manually via nav-loop in one worktree — PR [#3603](https://github.com/qf-studio/pilot/pull/3603) (`cc30c4df`): leak evictions (`recordedMerges`/`discoveryStart`/`retryTracker`-TTL), error-handling hygiene (`CleanupOrphanedWorktrees` return-signature, hook-tmp cleanup, `%w`-wrap, discord-heartbeat), the subagent `--model` argv bug, GraphQL multi-error aggregation, and `internal/transcription` 0→3 test files. **Closes the entire TASK-322 audit** (3 crit · 14 high · 17 med · 10 low); [TASK-357](tasks/archive/TASK-357-wave4-lows-reaudit.md) archived + roadmap closed (docs PR [#3604](https://github.com/qf-studio/pilot/pull/3604)). One carry-over → TASK-319 board track (board-GraphQL partial-data tolerance — only error-aggregation shipped). Separately, the recurring `TestSafeGo_IncrementsCounter` CI flake (global `panicCounter` cross-test leak) was specced + **dispatched to Pilot via nav-pilot ([#3605](https://github.com/qf-studio/pilot/issues/3605)) → Pilot built & merged PR [#3606](https://github.com/qf-studio/pilot/pull/3606) (`5124359c`)** — full Navigator→Pilot loop, test-only fix ([TASK-365](tasks/archive/TASK-365-flaky-safego-counter-test.md)).
- **v2.186.8 — hot-upgrade silent-failure fix ([#3600](https://github.com/qf-studio/pilot/issues/3600), MANUAL PR [#3601](https://github.com/qf-studio/pilot/pull/3601), merged `259f2a93`):** two-phase upgrade state (`awaiting_restart`/`restart_failed`; hot path never writes `completed` pre-exec, CLI semantics untouched); exec failures persisted to `upgrade-state.json`; boot reconciliation (`ReconcileBootState`) verifies running version vs `state.NewVersion` as ground truth — match promotes+cleans, mismatch logs ERROR + sticky "! UPGRADE FAILED" TUI panel; dashboard mode no longer discards slog — redirected to rotating `~/.pilot/logs/daemon.log` (`logging.dashboard_log`, `"off"` = legacy discard). Live-verified in isolated HOME (all 3 scenarios) + artifact-verified at the tag. From v2.186.8 on, hot upgrades self-verify on boot.
- **v2.186.4–v2.186.6 — TASK-364 holes closed by Pilot itself (June 11–12):** hole 4 sanitizer (`sanitizeSubtaskDescription`) via the #3582 epic (#3590/#3591, regex tightened in #3595 after conflict self-heal + Telegram approval); hole 5 (recovered-children rehydration + empty-description refusal) via #3593 — a clean single-PR run under `no-decompose` that **live-verified TASK-320/355** (execution row: `completed` + real worker SHA matching the PR — #3224 closed, both tasks archived) and the wave-2 epic checklist (**held**: no premature close, no false supersession, conflict self-heal worked). The #3582 run also exposed the next decomposer bug — splits explicitly-standalone single-package tasks ([#3597](https://github.com/qf-studio/pilot/issues/3597)) — **fixed 2026-06-12** via MANUAL PR [#3598](https://github.com/qf-studio/pilot/pull/3598) (merged `3aadbbd7`): standalone prose ("must NOT be decomposed", "is a standalone task", "as a single PR") now gates like the `no-decompose` label at every gate, and `isSinglePackageScope` ignores context-cited parent-description paths. Released in **v2.186.7** (artifact-verified: gate strings present in the published darwin-arm64 binary); **daemon restarted onto v2.186.7 2026-06-12 14:00 (lsof-verified image) — labeling workaround RETIRED.** The restart had to be manual: the dashboard hot upgrade installed v2.186.7 but its exec step silently failed — [#3600](https://github.com/qf-studio/pilot/issues/3600), **fixed in v2.186.8** (see above).
- **v2.186.3 — safeGo sweep + retry hardening (June 11, PRs [#3575](https://github.com/qf-studio/pilot/pull/3575) + [#3580](https://github.com/qf-studio/pilot/pull/3580), both MANUAL-merged):** TASK-292 shipped — `logging.SafeGo` panic recovery on all 35 bare `internal/` goroutines + `pilot_panics_total{component}` (recovered from the failed #3573 run's retry-worker branch). Retry machinery: quality/intent retries now run in the worktree (GH-3577 — closes the structurally-unable-to-pass retry loop that burned #3573), gate feedback is failure-aware instead of head-truncated (GH-3578), `createSubIssuesViaGitHub` honors dryRun and tests can no longer live-fire gh/claude (GH-3579 — **the GH-201 "OAuth loop" ghosts were the test suite**, not a teammate PAT; mem-035). Also fixed the pre-existing main lint break (7 dead `cmd/pilot/handlers.go` functions).
- **v2.186.0–v2.186.1 — decomposition wave 2 + release-stage hardening (TASK-361/363, June 10):** decomposed parents close only via the count-verified path; `MaxReleasingAttempts` cap + `guardReleaseSHAReachable` (closes the phantom-v2.181.0 vector). Detail in archived [TASK-361](tasks/archive/TASK-361-autopilot-decomposition-integrity.md).
- **v2.186.2 — executor SHA-harvest fix (PR [#3571](https://github.com/qf-studio/pilot/pull/3571), MANUAL):** **one bug was both TASK-320 B2 and TASK-355** — `getPostExecutionSummary` had no `cmd.Dir`, so its `git log` ran in the daemon's CWD and reported the wrong repo's HEAD as the commit SHA; the ghost guard then destroyed real worker commits (4/4 false no-ops on #3569/#3570, transcript-proven) or recorded wrong-repo `completed` SHAs. Fix: worktree git harvest BEFORE the LLM summary + `cmd.Dir = executionPath`. Also: truthful `EscalationReason` in approval-misconfig reporting (#3569) and size floor 200→500 (#3570). Pitfall `mem-034`.
- **Ops:** pre-merge approvals ENABLED in `~/.pilot/config.yaml` (Telegram, approver 283716179, 24h default-reject) — oversized PRs now request approval instead of dead-ending `StageFailed`. ~~Teammate OAuth-feeder suspicion~~ RESOLVED: the GH-201 feeder was the test suite (mem-035), killed in v2.186.3; no teammate purge or PAT rotation needed.

**Previous (v2.180.0, June 9 2026):** **TASK-354 board-orphan defense-in-depth — SHIPPED.** Spec-guard now writes `boardStatuses.Failed` so a re-dispatched In-Progress card moves to Blocked (PR [#3511](https://github.com/qf-studio/pilot/pull/3511)); no-op orphan path was already covered by TASK-320/321/341, label-orphan by #3495. TASK-360 closed same day — `(none)`-status cards are the board's own disabled workflow #6, not a Pilot bug (`learn_verify_write_callsite_before_fix`). Plans in `.agent/tasks/archive/`.

**Earlier (v2.179.0, June 9 2026):** **Ancestor-tag release dedup — SHIPPED** (PR [#3506](https://github.com/qf-studio/pilot/pull/3506)): `tagCoveringCommit()` exact-match OR ancestor-of-recent-tag (10-tag window; the v2.186.1 exhaustive lookup now backstops it). Pitfall `mem-029` `bug_phantom_version_blocks_upgrade` recorded.

**Open caveat (since v2.149.4):** `gateway.Config.LinearWebhookPublicKey` still has no YAML decode in `cmd/pilot/main.go` — Ed25519 verification is gated behind a field nothing can set (TASK-295 follow-up; backlog below).

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
| P1 | **GH Projects board as work source** — Studio SDK roadmap | ✅ Read path (TASK-317) + full board-driven lifecycle loop (TASK-319, LIVE-verified) + daemon-loop hardening (TASK-356, v2.166.7–9) + board-orphan defense-in-depth ([TASK-354](tasks/archive/TASK-354-board-orphan-in-progress.md), v2.180.0) all **shipped & archived**. Remaining: SDK connector extraction — 9/10 done after the studio-sdk run (plane/gitlab/azuredevops/github/linear/jira/asana/telegram/slack); next chat-bridge design + final 1 ([TASK-318](tasks/archive/TASK-318-sdk-m1-plane-extraction.md)). Board-loop tail [TASK-355](tasks/archive/TASK-355-board-sourced-noop-false-positive.md): ✅ fix shipped v2.186.2, **live-verified 2026-06-12 + archived** (real worker SHA in execution row, #3224 closed). |
| P1 | **Decomposition integrity residue** | ✅ **CLOSED 2026-06-12 + archived.** Waves 1+2 shipped (v2.183.0/v2.186.0); [TASK-364](tasks/archive/TASK-364-decomposition-integrity-residual-holes.md) holes 4+5 shipped v2.186.4–6 (Pilot-built); wave-2 epic checklist **live-PASSED** on GH-3582 ([TASK-361](tasks/archive/TASK-361-autopilot-decomposition-integrity.md)); standalone-split successor [#3597](https://github.com/qf-studio/pilot/issues/3597) fixed via MANUAL PR [#3598](https://github.com/qf-studio/pilot/pull/3598), released **v2.186.7**, daemon live on it since 2026-06-12 14:00 — **`no-decompose` labeling workaround retired**. Bookkeeping noise (`no_op` rows for sibling-merged children, row-SHA mismatches) recorded in TASK-361 — revisit only if audit-trail confusion recurs. |
| P1 | **Daemon finalization hardening** — Shapes A/B/C closure | Surfaced as #1 in `pilot-known-bugs` after studio-sdk run (~70% of #28–#56 needed manual finalize-recovery). Three failure shapes (stall-before-push, retry-race vs human recovery PR, late-duplicate-PR) trace to one structural defect (epic vs direct path divergent error contracts in `runner.go`) + two boundary bugs (`notifyExternalClose`, missing `InvalidateCompletion` on retry-ready). 🟢 **ALL 5 layers SHIPPED:** 2a #3417→v2.166.13, 2b #3418→v2.166.14, 3a #3419, 3b #3420→PR #3438, **Layer 1 (MANUAL) #3441→v2.166.16** (merged 2026-06-04, stage daemon restarted). Live Shape A/B/C verification deferred to next SDK batch. [TASK-359](tasks/TASK-359-daemon-finalization-hardening.md). |
| P1 | `safeGo()` panic-recovery sweep | ✅ **SHIPPED v2.186.3 + archived** — all 35 bare `internal/` goroutines wrapped, `pilot_panics_total{component}` live ([TASK-292](tasks/archive/TASK-292-safego-panic-recovery-sweep.md), PR #3575). |
| P1 | TASK-295 follow-up: wire `linear.webhook_public_key` YAML → `gateway.Config.LinearWebhookPublicKey` | Without this glue in `cmd/pilot/main.go`, the v2.149.4 Ed25519 verification is gated behind a config field that has no decode path. Small (≤30 LOC); blocks the security improvement from being active. |
| P2 | E2E test suite | No integration tests — reliability untested |
| P2 | Web dashboard auth | Token-based auth for remote access |
| P2 | Mobile-responsive dashboard | Primary use case is phone access |
| P2 | Persist cache token counts + TOKENS card | `tokens_input` stores only uncached input; cache counts (the bulk of throughput) not persisted — [#3567](https://github.com/qf-studio/pilot/issues/3567) (backlog, unlabeled) |
| P3 | GitHub App auth | PAT → installable GitHub App |
| P3 | `pilot project add` gh wizard | Interactive repo picker + token seed from `gh auth` — [TASK-282](tasks/TASK-282-project-add-gh-wizard.md) → [#3017](https://github.com/qf-studio/pilot/issues/3017) (not yet `pilot`-labeled) |
| P3 | Audit §3 Wave 4+ candidates | Not in Top 10 / not yet decomposed: `RecordAPIError` wiring beyond github · `AlertTypeOOMKilled` · multi-gate scanner phase discipline · subprocess migration end-to-end validation · `autopilot` adapter coupling refactor · SQL `withTx` helper · generic `Poller[T]` extraction · `Releaser` frozen-at-startup fix. Source: `.agent/audits/AUDIT-2026-05-25.md` §3. |

> **Shipped (was Wave 2/3, now archived):** TASK-293 poller counters · TASK-294 `WithRetry` in `doRequest` · TASK-296 `IsTaskShipped` · TASK-297/gh-3099 docs drift · TASK-298 consolidate `*_processed` (incl. TASK-288 Steps 1+2) · TASK-314/316 release scanner. Plans in `.agent/tasks/archive/`.
> **Shipped (June 1):** release pipeline de-raced — `make release` is now tag-only, goreleaser is sole publisher (#3377), closing the P2 `make release` vs goreleaser collision · TASK-309 releasing-stage B3/B4 defense-in-depth (#3375, closes #3188) · TASK-353 flaky-CI fix (#3374).
> **Shipped (June 2):** TASK-358 dashboard "failed" count classification — outcome classifier + idempotent backfill + ANSI-safe card render (#3401/#3404/#3407) → **v2.166.10–11**; live DB 784→234. TASK-356 daemon-loop fixes — epic-decompose work-loss (#3383), board write-back for externally-merged PRs (#3391) + decoupled from on_merge release (#3395) → **v2.166.7–9**. TASK-322 Wave 3 mediums (TASK-343/350/351) + Waves 0–1 (TASK-323–334) archived. Only **Wave 4 lows** remain (TASK-357, gated ~June 15).
> **Shipped (June 9):** Ancestor-tag release dedup — `handleReleasing` now treats a HeadSHA that's an ancestor of a recent tag as covered (#3506) → **v2.179.0**, with nav-docs pitfall `mem-029` `bug_phantom_version_blocks_upgrade` recorded (#3508). TASK-354 board-orphan defense-in-depth — `applySpecGuard()` writes `boardStatuses.Failed` so a re-dispatched In-Progress card transitions to Blocked instead of stranding (#3511) → **v2.180.0**; live audit confirmed 0 current orphans on the Studio SDK board, no-op path already covered by TASK-320/321/341, label-orphan covered by #3495. TASK-360 (5 (none)-status cards seen during the audit) **resolved + archived same day** — root cause is GitHub Project workflow #6 disabled on the board, not a Pilot bug; lesson `learn_verify_write_callsite_before_fix` captured.
> **Shipped (June 10–11):** **TASK-284** TUI dashboard project scoping (#3523, hand-merged) + `--dashboard-scope` flag (#3543/#3544) → v2.182.0–v2.185.x · **TASK-285** eval `project_path` (#3539 + #3561) · **TASK-361 wave 1** decomposition counting guards (#3527) → **v2.183.0** · **TASK-362** child-PR base pin (#3548) → v2.185.1 · **TASK-361 wave 2** verified closes / evidence-based supersession / junk-child guards (#3565) → **v2.186.0** · **TASK-363** release-stage hardening + recovered TASK-362 reachability guard (#3559, Pilot-built) → **v2.186.1** · **TASK-320 B2 / TASK-355 root cause** — daemon-CWD SHA harvest (#3571) + truthful escalation reasons (#3569) + size floor 500 (#3570) → **v2.186.2**, pitfall `mem-034`. TASK-284/285/362/363 + wave-2 plan archived. Incident log: GH-3513 / GH-3535 / GH-3532 in [TASK-361](tasks/TASK-361-autopilot-decomposition-integrity.md) + [TASK-364](tasks/TASK-364-decomposition-integrity-residual-holes.md).

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
