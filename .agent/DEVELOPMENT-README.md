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

**Current Version:** v2.207.5 | full status in `.agent/system/FEATURE-MATRIX.md`

**Recent (June 27–30 2026):** **Conversational-bot docs site page · M7 advanced to the SDK gate (studio-sdk v0.25.0, Phase 4a closed out, studio-sdk#71 spec'd) — SHIPPED.** (Earlier June 16–25 detail below.)
- **Nav + docs site + M7 SDK gate (June 27–30):** Conversational bot module now **documented on the docs/ Nextra site** — new `features/conversational-bot.mdx` + config/Slack/model-routing/index cross-links (PR [#3709](https://github.com/qf-studio/pilot/pull/3709)). Filed `auto_label_pilot` no-op cleanup ([#3710](https://github.com/qf-studio/pilot/issues/3710), `pilot`-labeled — field is never read; `pilot` label hardcoded at `issue_intake.go:53-62`). **M7 github cutover advanced to the SDK gate:** `studio-sdk` bumped **v0.24.0→v0.25.0** + Phase 4a closed out (PR [#3711](https://github.com/qf-studio/pilot/pull/3711)); re-verified file-by-file that v0.25.0 still lacks the github board layer / 5 client methods / poller-option surface — gate moved to **v0.26.0**, spec'd as [studio-sdk#71](https://github.com/qf-studio/studio-sdk/issues/71). Next: implement #71 upstream, then Pilot 4b–4d. Marker `2026-06-30_m7-sdk-gate-and-bot-docs-shipped.md`.
- **Slack `/help`-creates-a-task fix (PRs [#3659](https://github.com/qf-studio/pilot/pull/3659)+[#3661](https://github.com/qf-studio/pilot/pull/3661), June 25):** `@Pilot /help` on Slack created a code-**task** instead of printing help. Shipped in two PRs. TASK-372 (#3659, v2.194.1) added an `IntentCommand` dispatch case + safe `default:→clarify` in `comms` — useful hardening, but **did not fix the live bug**: the studio-sdk chat bridge splits `/`-commands into `core.MessageEvent{Action:"command", Text:""}` **one layer up**, so the command never reached `comms` as `/help` (binary confirmed to carry the fix via `vcs.revision`; bug still reproduced). Real fix #3661 (`main @ f88d76de`) handles `Action=="command"` at the **adapter seam** (`slack/handler.go` + shared `sdkshim.MessageEventToIncomingMessage`), reconstructing `Command+Args` into `Text` so `comms.detectIntent → IntentCommand → CommandHandler` finally fires. TASK-372 merged green only because its tests fed `comms` already-`/`-prefixed text and never crossed the adapter→comms boundary (the verification gap). Live-verified 2026-06-25: `/help`,`/status`,`/queue` route to command output, no stray tasks. ([TASK-372 archived](tasks/archive/TASK-372-command-routing-safe-default.md); lesson `pitfalls/bug_sdk_command_action_dropped.md`/mem-036). Fix on `main`, daemon-live; release tag pending.
- **Operational-intent live-queue answers (v2.194.0, June 24–25):** natural-language ops queries ("what's in the queue?", "anything running?", "queue status") now answer **inline from the daemon's store** (`GetActiveExecutions` + `GetQueuedTasks`) in <1s — no Claude Code exec, no tokens. New `intent.IntentOperational` category detected *before* the `IsClearQuestion` fast-path (the ordering trap); store-backed `handleOperational`; shared `formatQueueSummary` reused by `/queue`, `/status`, and the NL path; patterns tightly scoped so "where is the queue implemented?" still routes to `IntentQuestion` ([TASK-371](tasks/archive/TASK-371-operational-intent-live-queue.md), issue #3648 → decomposed into GH-3649/GH-3650). ⚠️ **Shipped in two halves:** the intent layer merged alone in **v2.193.0** while the handler PR failed on a daemon queue-race (`task GH-3650 already queued or running`), so v2.193.0 briefly mis-routed ops queries to the dispatch switch `default:` ("treat as task") — the exact failure mode TASK-370 had just fixed. Re-dispatched; handler landed in **v2.194.0** (`62abeac7`), closing the regression. Archived.
- **Comms intent reliability (v2.192.1, June 24):** read-only intents (`question`/`research`/`chat`) no longer fail the ghost-SHA commit guard in `UseWorktree` configs — `LocalMode`/`CreatePR:false` now set on read-only handlers ([TASK-369](tasks/archive/TASK-369-readonly-intents-ghost-sha-guard.md), PR [#3643](https://github.com/qf-studio/pilot/pull/3643)). Intent wiring unified across all adapters via a shared `comms.Handler` factory — Slack now gets the same LLMClassifier/ConvStore/RateLimit wiring as Telegram, closing per-adapter drift ([TASK-370](tasks/archive/TASK-370-unified-comms-handler-factory.md), PR [#3646](https://github.com/qf-studio/pilot/pull/3646)). Classifier is now *wireable* but still off by default (`adapters.*.llm_classifier: null`). Both archived.
- **M7 Phase 4a — github→studio-sdk poll-path scaffolding (MANUAL, PR [#3638](https://github.com/qf-studio/pilot/pull/3638)):** additive + **dormant** (default-off `adapters.github.use_sdk_poller`, NOT in `adapterPollerRegistrations()`) — `orchestrator.ProcessGithubIssueEvent`, the `sdkshim.ResolveRepoForEvent` github branch, `cmd/pilot/poller_github.go`, `handleGithubIssueEventSDK`. A 6-agent surface map verdict: **full retirement of `internal/adapters/github` is NOT possible at studio-sdk v0.24.0** — SDK lacks the ProjectV2 board layer, 5 Pilot-only client methods (`CompareStatus`/`GetAuthenticatedUser`/`SearchOpenPRsForIssue`/`FindOpenPRByBranch`/`ExecuteGraphQLTolerant`), and the poller-option surface; autopilot is hard-typed to the concrete `*github.Client`. Phases 4b–4d **re-confirmed blocked at studio-sdk v0.25.0** (github surface byte-identical to v0.24.0) → gate now **v0.26.0**, spec'd as [studio-sdk#71](https://github.com/qf-studio/studio-sdk/issues/71). **Phase 4a is now COMPLETE** — `go.mod` on v0.25.0, full suite green (PR [#3711](https://github.com/qf-studio/pilot/pull/3711)). Adversarially reviewed (verdict SHIP). [TASK-368](tasks/TASK-368-m7-github-cutover-phase4.md), tracking [#3423](https://github.com/qf-studio/pilot/issues/3423).
- **Cache-token persistence (v2.192.0, [#3567](https://github.com/qf-studio/pilot/issues/3567)):** `executions` now persists `tokens_cache_read`/`tokens_cache_write` (migration + INSERT + metrics SUM + TOKENS-card cached/uncached split). The populate step — the completion `UPDATE` in `SaveExecutionMetrics` — failed twice as GH-3616 before landing via [#3634](https://github.com/qf-studio/pilot/pull/3634); stuck epics #3632/#3636 closed. TASK-366/367 archived.

**SHIPPED — Conversational Bot Module (P1–P5), live-verified end-to-end 2026-06-26 (v2.200.2):** fast direct-LLM chat/Q&A/issue-intake by extending `comms.Handler`; disabled ⇒ identical executor fallback. **Demo loop proven on real artifacts:** a Slack sentence — "create an issue to add a /ping health endpoint" → bot-drafted issue [#3705](https://github.com/qf-studio/pilot/issues/3705) → daemon executed → **PR [#3706](https://github.com/qf-studio/pilot/pull/3706) merged** (+34/-0, CI green). Runbook: `.agent/sops/demos/bot-demo-runbook.md`.
- **Phases:** P1+P2 `internal/llm.Client` + `comms.Responder` + fast `handleChat`/`handleGreeting` ([#3665](https://github.com/qf-studio/pilot/issues/3665)) · P3 `Responder.Answer()` bounded retrieval, executor fallback when too broad ([#3671](https://github.com/qf-studio/pilot/issues/3671)) · P4 `Responder.DraftIssue()` + `handleIssueIntake` → `pilot`-labeled issue via `comms.IssueCreator` ([#3691](https://github.com/qf-studio/pilot/issues/3691)) · P5 persona across Chat/Answer/DraftIssue + `bot.voice` scaffold + commented `bot:` example ([#3673](https://github.com/qf-studio/pilot/issues/3673)). Pattern mem-038.
- **Runtime fixes — the bot did NOT work until these (found in live bring-up, not CI):**
  - **v2.200.1 ([#3700](https://github.com/qf-studio/pilot/pull/3700)):** `internal/llm.Client` omitted required `max_tokens` and sent non-standard `output_config{effort}` → every call HTTP 400 → "Sorry, I couldn't process that." Pitfall **mem-039**.
  - **v2.200.2 ([#3703](https://github.com/qf-studio/pilot/pull/3703)):** `internal/intent/classifier.go` had the **same** `output_config` 400 (so the Haiku classifier silently fell back to regex) **and** a prompt that routed "draw/outline/show" → code TASK. Removed `output_config`; added a **deliverable test** (answer/diagram → question/chat, change-code → task, file-ticket → issue_intake). **Haiku is now the NL router; regex only guards `/`-commands.** Pitfall **mem-041**.
- **Operational gotchas (config, not code) — pitfall mem-042:** intake targets **`adapters.github.repo`** (single hardwired `NewIssueCreator` entry), NOT the active/default project — `/switch` doesn't move it; and the per-context active project **resets to `default_project` on restart**. Point `default_project` + `adapters.github.repo` at the same repo (+ `project_board.enabled:false` for label polling) or Q&A/intake hit the wrong repo. Re-verify after every restart.
- Architecture: `comms.Handler` shared chokepoint; disabling bot is zero-regression. Pattern `patterns/pattern_fast_conversational_path_vs_executor.md`.

**Open caveat (since v2.149.4):** `gateway.Config.LinearWebhookPublicKey` still has no YAML decode in `cmd/pilot/main.go` — Ed25519 verification is gated behind a field nothing can set (TASK-295 follow-up; backlog below).

**Earlier (v2.179.0–v2.187.1, June 9–16 2026):** `pilot project add` gh wizard (TASK-282) · board-GraphQL partial-data tolerance (`ExecuteGraphQLTolerant`) · TASK-322 security audit CLOSED · decomposition-integrity waves 1+2 · hot-upgrade self-verify on boot · executor SHA-harvest fix · `safeGo` panic-recovery sweep · board-orphan defense-in-depth · ancestor-tag release dedup. Detail in `git log` + `.agent/tasks/archive/`.

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
| ~~P0~~ | **`@Pilot /help` creates a task on Slack** ([TASK-372](tasks/archive/TASK-372-command-routing-safe-default.md)) | ✅ **CLOSED 2026-06-25 + archived.** Two PRs: TASK-372 ([#3659](https://github.com/qf-studio/pilot/pull/3659), v2.194.1) added `IntentCommand` routing + safe `default:→clarify` in `comms` — necessary hardening but **did not fix the live bug** (the studio-sdk chat bridge splits `/`-commands into `Action:"command"` with empty `Text` one layer up, so the command never reached `comms`). Real fix [#3661](https://github.com/qf-studio/pilot/pull/3661) (`main @ f88d76de`) handles `Action=="command"` at the adapter seam (`slack/handler.go` + shared `sdkshim.MessageEventToIncomingMessage`). Live-verified: `/help`,`/status`,`/queue` route to command output, no stray tasks. Lesson: `pitfalls/bug_sdk_command_action_dropped.md` (mem-036) — test at the adapter→comms seam, not the inner layer in isolation. |
| P1 | Multi-tenant SaaS mode | Single-user CLI → hosted needs auth, isolation |
| P1 | Public launch prep | Landing page, onboarding, pricing, billing |
| P1 | Web dashboard polish | React UI functional but needs design pass |
| P1 | Fix `shouldTriggerRelease()` | Doesn't check `ResolvedEnv().Release` — only top-level config |
| P1 | **GH Projects board as work source** — Studio SDK roadmap | ✅ Read path (TASK-317) + full board-driven lifecycle loop (TASK-319, LIVE-verified) + daemon-loop hardening (TASK-356, v2.166.7–9) + board-orphan defense-in-depth ([TASK-354](tasks/archive/TASK-354-board-orphan-in-progress.md), v2.180.0) all **shipped & archived**. Remaining: **M7 SDK cutover** — 9/10 adapter poll/chat paths consume studio-sdk; **github is last + can only partially cut over** (Phase 4a poll-path scaffolding SHIPPED dormant via [#3638](https://github.com/qf-studio/pilot/pull/3638), **closed out on studio-sdk v0.25.0** via [#3711](https://github.com/qf-studio/pilot/pull/3711); 4b–4d **re-confirmed blocked at v0.25.0** — board layer, 5 client methods, poller-option surface all absent → gate **v0.26.0**, spec'd as [studio-sdk#71](https://github.com/qf-studio/studio-sdk/issues/71)). **Next: implement #71 upstream, cut SDK v0.26.0, then Pilot 4b–4d.** Human-led, [TASK-368](tasks/TASK-368-m7-github-cutover-phase4.md) / [#3423](https://github.com/qf-studio/pilot/issues/3423). Board-loop tail [TASK-355](tasks/archive/TASK-355-board-sourced-noop-false-positive.md): ✅ shipped v2.186.2, live-verified + archived. |
| P1 | **Decomposition integrity residue** | ✅ **CLOSED 2026-06-12 + archived.** Waves 1+2 shipped (v2.183.0/v2.186.0); [TASK-364](tasks/archive/TASK-364-decomposition-integrity-residual-holes.md) holes 4+5 shipped v2.186.4–6 (Pilot-built); wave-2 epic checklist **live-PASSED** on GH-3582 ([TASK-361](tasks/archive/TASK-361-autopilot-decomposition-integrity.md)); standalone-split successor [#3597](https://github.com/qf-studio/pilot/issues/3597) fixed via MANUAL PR [#3598](https://github.com/qf-studio/pilot/pull/3598), released **v2.186.7**, daemon live on it since 2026-06-12 14:00 — **`no-decompose` labeling workaround retired**. Bookkeeping noise (`no_op` rows for sibling-merged children, row-SHA mismatches) recorded in TASK-361 — revisit only if audit-trail confusion recurs. |
| P1 | **Daemon finalization hardening** — Shapes A/B/C closure | Surfaced as #1 in `pilot-known-bugs` after studio-sdk run (~70% of #28–#56 needed manual finalize-recovery). Three failure shapes (stall-before-push, retry-race vs human recovery PR, late-duplicate-PR) trace to one structural defect (epic vs direct path divergent error contracts in `runner.go`) + two boundary bugs (`notifyExternalClose`, missing `InvalidateCompletion` on retry-ready). 🟢 **ALL 5 layers SHIPPED:** 2a #3417→v2.166.13, 2b #3418→v2.166.14, 3a #3419, 3b #3420→PR #3438, **Layer 1 (MANUAL) #3441→v2.166.16** (merged 2026-06-04, stage daemon restarted). Live Shape A/B/C verification deferred to next SDK batch. [TASK-359](tasks/TASK-359-daemon-finalization-hardening.md). |
| P1 | `safeGo()` panic-recovery sweep | ✅ **SHIPPED v2.186.3 + archived** — all 35 bare `internal/` goroutines wrapped, `pilot_panics_total{component}` live ([TASK-292](tasks/archive/TASK-292-safego-panic-recovery-sweep.md), PR #3575). |
| P1 | TASK-295 follow-up: wire `linear.webhook_public_key` YAML → `gateway.Config.LinearWebhookPublicKey` | Without this glue in `cmd/pilot/main.go`, the v2.149.4 Ed25519 verification is gated behind a config field that has no decode path. Small (≤30 LOC); blocks the security improvement from being active. |
| P2 | E2E test suite | No integration tests — reliability untested |
| P2 | Web dashboard auth | Token-based auth for remote access |
| P2 | Mobile-responsive dashboard | Primary use case is phone access |
| ~~P2~~ | ~~Persist cache token counts + TOKENS card~~ | ✅ **SHIPPED v2.192.0** ([#3567](https://github.com/qf-studio/pilot/issues/3567)) — `tokens_cache_read`/`write` persisted + TOKENS-card split; populate fix #3634 after GH-3616 failed twice. TASK-366/367 archived. |
| P3 | GitHub App auth | PAT → installable GitHub App |
| ~~P3~~ | ~~`pilot project add` gh wizard~~ | ✅ **SHIPPED v2.187.1** ([#3017](https://github.com/qf-studio/pilot/issues/3017) → PR [#3612](https://github.com/qf-studio/pilot/pull/3612)) — TTY wizard, repo picker, token seed, `--no-wizard`. TASK-282 archived. |
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
