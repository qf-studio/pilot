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

**Current Version:** v2.186.3 | full status in `.agent/system/FEATURE-MATRIX.md`

**Recent (v2.179.0, June 9 2026):** **Autopilot ancestor-tag release dedup — SHIPPED.** `handleReleasing` dedup'd releases by **exact-SHA** only (`GetTagForSHA(HeadSHA)`), so a PR whose `HeadSHA` was an **ancestor** of an existing release tag's commit (already shipped inside a later squash-merge, or a tag on a descendant) slipped through and cut a **redundant, lower-content release** — the v2.178.0 incident. Fix: `tagCoveringCommit()` + a new `github.Client.CompareStatus()` treat exact-match **or** ancestor-of-recent-tag (`base...head` == `ahead`/`identical`) as already-released → the PR drains from `activePRs` without a new tag; table-driven test (exact / ancestor / diverged). #3506. **Released as v2.179.0, skipping a *phantom* `v2.178.0`:** the deleted spurious tag had left the daemon binary reporting `2.178.0`, which **blocked `pilot upgrade`** (it looked newer than the real Latest v2.177.0) — cutting v2.179.0 cleared the deadlock; daemon upgraded + restarted on the fix. Pitfalls: `bug_manual_merge_spurious_release` (`mem-028`, now marked fixed) + new `bug_phantom_version_blocks_upgrade` (`mem-029`).

**Previous (v2.177.0, June 9 2026):** **M7 chat-contract wrap-up + daemon self-heal — SHIPPED & live-verified.** Closed the M7 SDK batch (Azure/Linear/Jira/Asana/GitLab/Discord) and hardened the two defects that had stranded Telegram #3470 for an hour. **TASK-354** — periodic stranded-issue sweep: the poller now clears orphaned `pilot-in-progress` *mid-session* via an in-flight-gated 10-min ticker (no daemon restart needed; `recoverOrphanedIssues` was startup-only). #3495; **live-verified** (added the label to a throwaway issue → swept within the interval, terminal label preserved, no execution). **TASK-320 Layer B2** — decomposed-task no-op tolerance: a no-commit subtask (analysis/verify/already-present) no longer aborts the whole task; only a task that delivers *zero* commits fails, after one escalated retry with the evidence-backed directive. #3497. **Telegram #3470** landed as SDK chat-contract conformance on the handler (#3494) + an **opt-in** `telegram.sdk_bridge` flag (#3498, default off; full `commands.go` cutover soak-gated per the issue's Scope-OUT). **Incident:** hand-merging the four PRs + one manual `v2.177.0` tag made the autopilot reconciler adopt #3494's `pilot/` branch and cut a **spurious v2.178.0** (ancestor of v2.177.0, wrongly "Latest") — deleted, `version.ts` reverted (#3502); v2.177.0 is the correct Latest. Pitfall `bug_manual_merge_spurious_release` (`mem-028`): don't hand-merge `pilot/GH-*` PRs while the daemon runs.

**Earlier (v2.166.10–11, June 2 2026):** **TASK-358 dashboard "failed" count fixed — SHIPPED & live-verified.** The QUEUE card showed `✗ 784 failed`, wildly inflated. Root cause: `dispatcher.go` collapsed *every* `result.Success==false` outcome (declined / no-op / stalled / budget / infra / rate-limit / skipped) into `status='failed'`, and `GetLifetimeTaskCounts` summed `status='failed'`. Fix: a `TerminalStatus()` classifier (ordered error-signature table) writes distinct statuses; an idempotent `reclassifyLegacyOutcomes()` backfill runs in `migrate()`; heal-on-merge scope widened to the non-failure statuses. **Live DB: 784 → 234 genuine failures** (infra 305 · no-op 120 · skipped 81 · rate-limited 34 · stalled 10; conservation verified). A v2.166.11 follow-up (#3407) fixed a TUI render bug where the wide breakdown suffix overflowed the mini-card and `truncateVisual` (not ANSI-aware) blanked the failed line. PRs #3401/#3404/#3407. Pitfall `pitfall_dashboard_failed_count_conflation` (`mem-024`); learning `learn_restart_vs_rebuild_stale_binary` (`mem-025`). Plan archived: `.agent/tasks/archive/TASK-358-dashboard-failed-count-classification.md`.

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
| P1 | **GH Projects board as work source** — Studio SDK roadmap | ✅ Read path (TASK-317) + full board-driven lifecycle loop (TASK-319, LIVE-verified) + daemon-loop hardening (TASK-356, v2.166.7–9) all **shipped & archived**. Remaining: SDK connector extraction — 9/10 done after the studio-sdk run (plane/gitlab/azuredevops/github/linear/jira/asana/telegram/slack); next chat-bridge design + final 1 ([TASK-318](tasks/TASK-318-sdk-m1-plane-extraction.md)). Open board-loop tails: [TASK-354](tasks/TASK-354-board-orphan-in-progress.md) (orphan In-Progress — **label-sweep shipped #3495/v2.177.0**; board-column terminal transition still open) · [TASK-355](tasks/TASK-355-board-sourced-noop-false-positive.md) (no-op false-positive). |
| P1 | **Daemon finalization hardening** — Shapes A/B/C closure | Surfaced as #1 in `pilot-known-bugs` after studio-sdk run (~70% of #28–#56 needed manual finalize-recovery). Three failure shapes (stall-before-push, retry-race vs human recovery PR, late-duplicate-PR) trace to one structural defect (epic vs direct path divergent error contracts in `runner.go`) + two boundary bugs (`notifyExternalClose`, missing `InvalidateCompletion` on retry-ready). 🟢 **ALL 5 layers SHIPPED:** 2a #3417→v2.166.13, 2b #3418→v2.166.14, 3a #3419, 3b #3420→PR #3438, **Layer 1 (MANUAL) #3441→v2.166.16** (merged 2026-06-04, stage daemon restarted). Live Shape A/B/C verification deferred to next SDK batch. [TASK-359](tasks/TASK-359-daemon-finalization-hardening.md). |
| P1 | `safeGo()` panic-recovery sweep | Last open Wave 2 refactor — 73 bare `go func()` in `internal/` lack recover(). [TASK-292](tasks/TASK-292-safego-panic-recovery-sweep.md) |
| P1 | TASK-295 follow-up: wire `linear.webhook_public_key` YAML → `gateway.Config.LinearWebhookPublicKey` | Without this glue in `cmd/pilot/main.go`, the v2.149.4 Ed25519 verification is gated behind a config field that has no decode path. Small (≤30 LOC); blocks the security improvement from being active. |
| P2 | E2E test suite | No integration tests — reliability untested |
| P2 | Web dashboard auth | Token-based auth for remote access |
| P2 | Mobile-responsive dashboard | Primary use case is phone access |
| P2 | Scope TUI dashboard to single project | Today `-p` scopes execution only; metrics/sparklines mix all projects — [TASK-284](tasks/TASK-284-dashboard-project-scope.md) |
| P3 | GitHub App auth | PAT → installable GitHub App |
| P3 | Add `project_path` to `eval_tasks` + scope eval panel | Follow-up to TASK-284; lets eval/bench panel scope per-project instead of `[global]` label — [TASK-285](tasks/TASK-285-eval-tasks-project-path.md) (blocked by TASK-284) |
| P3 | `pilot project add` gh wizard | Interactive repo picker + token seed from `gh auth` — [TASK-282](tasks/TASK-282-project-add-gh-wizard.md) → [#3017](https://github.com/qf-studio/pilot/issues/3017) (not yet `pilot`-labeled) |
| P3 | Audit §3 Wave 4+ candidates | Not in Top 10 / not yet decomposed: `RecordAPIError` wiring beyond github · `AlertTypeOOMKilled` · multi-gate scanner phase discipline · subprocess migration end-to-end validation · `autopilot` adapter coupling refactor · SQL `withTx` helper · generic `Poller[T]` extraction · `Releaser` frozen-at-startup fix. Source: `.agent/audits/AUDIT-2026-05-25.md` §3. |

> **Shipped (was Wave 2/3, now archived):** TASK-293 poller counters · TASK-294 `WithRetry` in `doRequest` · TASK-296 `IsTaskShipped` · TASK-297/gh-3099 docs drift · TASK-298 consolidate `*_processed` (incl. TASK-288 Steps 1+2) · TASK-314/316 release scanner. Plans in `.agent/tasks/archive/`.
> **Shipped (June 1):** release pipeline de-raced — `make release` is now tag-only, goreleaser is sole publisher (#3377), closing the P2 `make release` vs goreleaser collision · TASK-309 releasing-stage B3/B4 defense-in-depth (#3375, closes #3188) · TASK-353 flaky-CI fix (#3374).
> **Shipped (June 2):** TASK-358 dashboard "failed" count classification — outcome classifier + idempotent backfill + ANSI-safe card render (#3401/#3404/#3407) → **v2.166.10–11**; live DB 784→234. TASK-356 daemon-loop fixes — epic-decompose work-loss (#3383), board write-back for externally-merged PRs (#3391) + decoupled from on_merge release (#3395) → **v2.166.7–9**. TASK-322 Wave 3 mediums (TASK-343/350/351) + Waves 0–1 (TASK-323–334) archived. Only **Wave 4 lows** remain (TASK-357, gated ~June 15).
> **Shipped (June 9 → v2.177.0):** M7 SDK chat batch closed (Azure/Linear/Jira/Asana/GitLab/Discord). **TASK-354** periodic stranded-issue sweep — in-flight-gated poller ticker clears orphaned `pilot-in-progress` mid-session (#3495, live-verified; archived). **TASK-320 Layer B2** decomposed-task no-op tolerance + escalated retry (#3497, archived). **Telegram #3470** — SDK chat-contract conformance (#3494) + opt-in `telegram.sdk_bridge` (#3498; full cutover soak-gated). Pitfall `mem-028` (manual-merge → spurious release). **Follow-up shipped (v2.179.0, #3506):** `handleReleasing` ancestor-tag dedup; phantom-version upgrade-deadlock pitfall `mem-029`.

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
