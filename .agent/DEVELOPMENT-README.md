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
| Let merged work ride the 16:00 CET release train | Cut ad-hoc releases (incidents only — see Release Cycles) |

### Release Cycles (workflow decision, 2026-07-09 — mem-104)

Work is organized in **cycles** (Linear-style), layered ON TOP of the
Navigator + Pilot pipeline above — planning/dispatch/review/merge are
unchanged; cycles govern **scope and release cadence** only:

1. **Ideate & research** — as before (`/nav-task`, navigator-research agents).
2. **Plan the cycle** — pick the updates that ship this cycle; the cycle
   **ends before the release train**, so scope what can merge by then.
3. **Execute & collect** — dispatch to Pilot; merged PRs **accumulate on
   `main` unreleased**. Merged-but-unreleased is the NORMAL state, not an
   incident (do not "fix" it — see mem-093 for what an actual release wedge
   looks like).
4. **Release** — the scheduled train tags at **16:00 Europe/Berlin**. The
   pilot repo is **daily** (`schedule: "0 16 * * *"`); the other project
   repos are Mon–Fri (`0 16 * * 1-5`). Config in `~/.pilot/config.yaml`.

**The one exception**: incidents. A production-impacting fix does NOT wait
for the train — release ASAP (out-of-band tag is safe; the releaser reads
its baseline live from git tags, mem-093).

**Cutover COMPLETE (2026-07-10)**: pilot repo flipped `on_merge → on_schedule`
after two prerequisites landed — #4150 (append ` (#N)` to squash titles so
`resolveTrainMemberPRs` can resolve members; without it `on_schedule` skips
every tick with "no resolvable member PRs") and #4174 (no-tags-repo first
release). Verified live: scheduler runs `0 16 * * *`, next_run correct, no
release cut on restart. Watch item: the train still skips a repo whose
squash commits predate #4150, or a repo with zero tags.

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

**Current Version:** box runs **`Pilot v2.251.1`** (self-upgraded via exec — board "uptime" spans upgrades; carries #4647 CIConfigMismatch + #4651/#4652 is_canary wire). Self-upgrade path proven since v2.251.0 (07-31, first fully autonomous train). **Daemon runs on AWS** (`i-0e0c1ca34e7b561f9`, TASK-409; ops via `pilot-aws` skill; NO local daemon; binary `/var/lib/pilot/bin/pilot`, `/usr/local/bin/pilot` symlinks to it, rollback at `pilot.prev`) — **approvals OFF since 07-20** (auto-merge on green CI; size-gated PRs still park `awaiting_approval`) — **GH-4391 rate-budget client LIVE** | full status in `.agent/system/FEATURE-MATRIX.md`

**PRIORITY (founder directive 2026-07-26 — supersedes 07-17):** **SaaS/platform UNPARKED — TASK-405 is active work again.** The 07-17 ordering (pointer delivery → pilot reliability → SaaS parked) held while the dispatch-reliability chain was open; that chain closed with v2.246.0 on 07-25. Pointer and pilot reliability remain live tracks but no longer gate S-milestone dispatch. Memory: `founder-priority-pointer-first-saas-parked` (superseded).

**Recent (July 29 – Aug 3 2026; detail lives in `system/saas-roadmap.md` v9.1–v9.8, `tasks/archive/`, and git log — do not re-grow this block, replace it):**
- **08-03: executor-reliability cluster (TASK-437) — 9 issues from one incident chain.** RCA of the GH-4648/4649 duplicate-PR race (nav-research, 91 tool calls): every admission guard is scoped to its *own* task_id ledger or branch, so a decomposed parent whose child died re-implemented the spec itself while the child's retry outlived it. Dispatched: **#4677** coordinator-resume (single-PR re-file of #4655) · **#4656** issue-state revalidation at pickup/PR-create · **#4657 ✅ merged** conflict-close · **#4668** heartbeat liveness · **#4669** dead intent judge · **#4670/#4671** executor gh scoping · **#4678** cancel verb · **#4679** compare-before-close. Two trigger defects found: heartbeat SIGKILLs healthy runs during long local tool execution (`make test` >5m — claude-code's stream is silent by design), and **the intent judge has been 100% dead since the 07-16 cutover** (4,321 `context_deadline` kills hidden by fail-open). Cleanup: #4655's 7 fragmented children + 4 PRs closed after it decomposed for want of a `no-decompose` marker. Memories: `pilot_issue_missing_no_decompose_fragments_single_fix`, `pilot_stalled_status_is_retry_not_cancel`, `commit_message_hyphenated_close_keyword_autocloses_issues`.
- **08-01/08-03: S4 board track — wave 1 merged, wave 2 dispatched.** Wave 1: console#85→PR#87 (C1 data model) · console#86→PR#88 (C2 reconciler) · ui#40→PR#42 (kanban v1) · ui#41→PR#43 (drawer/feed/verbs); TASK-432–435 archived. Wave 2 chained 08-03: **console#89 C7 board API → #90 C3 ingest → #91 C4 outbound** (TASK-438/439/440). **studio-sdk sync contract verified live** (`v0.31.2`): `core.SyncCapable` implemented by all three connectors; provider asymmetries folded into the specs (Linear has no retry + uses `description` not `body` + hard-errors on unknown patch keys; priority Linear-only; `gte` watermark needs caller dedupe; `Deleted` never set; Jira comment idem-scan unpaginated). Canary-metrics hygiene shipped (#4648/#4650), **v2.251.1** live. Roadmap **v9.8**.
- **07-31: first autonomous self-upgrade (v2.251.0) · drift-defects #6–#8 shipped+SOP-verified same day · release trains unblocked · AWS cost audit.** Release-train incident RCA'd (TASK-431 archived): global `required_checks` naming checks that never post → permanent CIPending, auth 11 days unreleased; ops fix released **auth v0.68.0 + sdk v0.31.2**; durable fix #4646→PR#4647 merged (terminal `CIConfigMismatch`, startup lint, stale-panel reconcile) — in v2.251.1. Cost audit: CDK fleet estate untagged in cost view + 2nd NAT GW vs v1 design → infra#26→**PR#27 merged** (app-scope `Environment=fleet`, NAT→1); **operator `cdk deploy` pending**. Pitfall `cdk-untagged-estate-invisible-to-cost-view`. Roadmap **v9.6**.
- **07-30: v2.250.0 manual cutover · spec-guard epic #4624 shipped (strike-loop class dead) · real-stack-verify SOP adopted (founder nod)** — drift-class scoreboard 8-for-8 caught by operator real-stack verify, zero by fixture-only daemon gates · #4643/#4644 carrier-timeout park fix · #4265 closed (TASK-407 proof met). Roadmap **v9.5**.
- **07-29: S3 backend wave 10/10 merged (AM) · local stack LIVE (night) · overnight lead-watch shipped the S3 product punch list** (6 issues ~2.5h) — dashboard renders on the real local stack, browser-verified. Roadmap **v9.1–v9.5**.
- **Earlier (compressed):** 07-27/28 S2 EXIT MET · graph-drift prevention program · v2.247.0 self-shipped · 07-26 SaaS UNPARKED + hosted merge leg fixed (mem-160) · approvals-off 07-20 · rate-budget client #4495 · TASK-407 atomic-claim track proven · S6-lite AWS cutover 07-16 (TASK-409) · #4393 split-brain recovery. Detail: git log + `tasks/archive/`.

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

## 🚀 Pilot Cloud SaaS Program (TASK-405) — ACTIVE

Building the hosted Pilot SaaS using this daemon to build it (Pilot ships its own SaaS via `pilot`-labeled issues).

- **Plan of record + live status**: [`system/saas-roadmap.md`](system/saas-roadmap.md) (v9.8) — S0 ✅ · S1 ✅ · S2 ✅ (exit met 07-27) · H1–H12 ✅ · R-track ✅ · S6-lite ✅ · **S3 BUILT** (exit gated on founder staging inputs → operator deploy per infra PR#25) · **S4-early board track merged 08-01** (C1/C2 + kanban UI ×2; C3/C4/C5/C7 not yet cut)
- **Program doc**: [`tasks/TASK-405-pilot-saas-platform.md`](tasks/TASK-405-pilot-saas-platform.md)
- **Design**: [`system/saas-architecture.md`](system/saas-architecture.md) · [`saas-kanban-sync-design.md`](system/saas-kanban-sync-design.md) · [`saas-fleet-design.md`](system/saas-fleet-design.md) · [`saas-asset-research.md`](system/saas-asset-research.md)
- **New repos** (created 2026-07-14, in `~/.pilot/config.yaml`): `qf-studio/pilot-console` (Go control plane) · `pilot-console-ui` (Vue3/Vite/Bun SPA) · `pilot-cloud-infra` (Go CDK) — each has its own `CLAUDE.md`
- **Latest handoff marker**: `.agent/.context-markers/2026-08-03_s4-board-merged-nav-docs.md`
- **Systemic**: TASK-407 atomic dispatch-admission claim — **proven + archived 2026-07-30** ([`tasks/archive/`](tasks/archive/TASK-407-dispatch-admission-claim.md); #4265 closed, `duplicate-pr` green since 07-24). TASK-406 shipped → archived.
- **Ops SOP**: [`sops/operations/safe-daemon-restart.md`](sops/operations/safe-daemon-restart.md) — restart is the operator's action; never relaunch the `--dashboard` daemon from an assistant shell (no single-instance lock yet)
- **Quality SOP**: [`sops/quality/real-stack-verify-gates-ui-merges.md`](sops/quality/real-stack-verify-gates-ui-merges.md) — ADOPTED 2026-07-30: UI-surface merges aren't DONE until operator-verified on the live local stack (daemon gates are fixture-only; 5 drift defects in one night prove it)
- **Incident**: [`system/incident-duplicate-cifix-2026-07-14.md`](system/incident-duplicate-cifix-2026-07-14.md) — the Hardening-track root cause

## Active Work

**Source of truth: GitHub Issues with `pilot` label**

```bash
gh issue list --label pilot --state open
gh issue list --label pilot-in-progress --state open
gh pr list --state open
```

### Backlog

Shipped items live in `git log` + `tasks/archive/` — this table holds **open work only**.
Do not append completed rows here.

| Priority | Topic | Why |
|----------|-------|-----|
| **P0** | **Executor reliability cluster** ([TASK-437](tasks/TASK-437-duplicate-execution-race-prevention.md)) | 8 open issues from one 07-31 incident chain. Guards are all scoped to their own task_id/branch, so scope delivered by a sibling is invisible: **#4677** decomposed-parent must resume coordinator (single-PR re-file of #4655) · **#4656** revalidate issue state at pickup + before PR create · **#4668** heartbeat SIGKILLs healthy runs during long local tool execution (`make test` >5m) · **#4669** intent judge 100% dead since the 07-16 cutover, 4,321 `context_deadline` kills hidden by fail-open · **#4670/#4671** scope executor `gh` powers (prompt rule + audit; PATH-interposed guard shim) · **#4678** no cancel verb — `stalled` reads as retry-me and spins generations · **#4679** compare-before-close so #4657 can't discard unlanded work. #4657 ✅ merged. |
| **P1** | **Pilot SaaS platform** ([TASK-405](tasks/TASK-405-pilot-saas-platform.md)) | S3 **built**, exit gated on founder inputs (Stripe test keys/price/webhook secret · console + sending domains · ACM DNS) → operator staging deploy → S3 exit test. S4 board: wave 1 merged (C1/C2 + kanban UI), wave 2 dispatched 08-03 ([TASK-438](tasks/TASK-438-console-c7-board-api.md) console#89 C7 API → [TASK-439](tasks/TASK-439-console-c3-ingest-workers.md) #90 C3 ingest → [TASK-440](tasks/TASK-440-console-c4-outbound-op-worker.md) #91 C4 outbound). Uncut: C5 status-map seeding, C6 rate budgeter, C8/C9. System docs: [`saas-architecture.md`](system/saas-architecture.md) · [`saas-kanban-sync-design.md`](system/saas-kanban-sync-design.md) · [`saas-fleet-design.md`](system/saas-fleet-design.md). Roadmap: [`saas-roadmap.md`](system/saas-roadmap.md) v9.8. |
| **P1** | **Throughput acceleration** ([TASK-393](tasks/TASK-393-throughput-acceleration.md)) | Phase 1 (instrumentation) ✅ shipped 07-09. **M3 baseline window closed ~07-20 — histograms never harvested; phases 2–5 remain gated on that analysis.** Remaining: (2) execution lanes on `Complexity`, (3) N-concurrent per repo (`ProjectWorker` pool — note this is also the sole serialization point, see mem-101/102), (4) SHA-keyed repo primer, (5) risk-score trust tiers. Roadmap: [`throughput-roadmap.md`](system/throughput-roadmap.md) (M0–M8, D1–D6). |
| **P1** | **Execution lifecycle chokepoint** ([TASK-404](tasks/TASK-404-execution-lifecycle-chokepoint.md)) | B1 shipped (#4243 — `ExecutionLifecycle` Begin/Transition/Finish + typed status vocabulary). Remaining legs open; #4678's cancel verb lands on this seam. |
| P1 | Wire `linear.webhook_public_key` YAML → `gateway.Config.LinearWebhookPublicKey` | TASK-295 follow-up. Ed25519 webhook verification has shipped since v2.149.4 but is gated behind a config field with **no decode path in `cmd/pilot/main.go`** — the security improvement is inert. Small (≤30 LOC). |
| P1 | Fix `shouldTriggerRelease()` | Doesn't check `ResolvedEnv().Release` — only top-level config. |
| P1 | Web dashboard polish | React UI functional but needs a design pass. |
| P2 | E2E test suite | No integration tests — reliability untested. |
| P2 | Web dashboard auth | Token-based auth for remote access. |
| P2 | Mobile-responsive dashboard | Primary use case is phone access. |
| P3 | GitHub App auth | PAT → installable GitHub App. |
| P3 | Audit §3 Wave 4+ candidates | Not yet decomposed: `RecordAPIError` wiring beyond github · `AlertTypeOOMKilled` · multi-gate scanner phase discipline · subprocess migration end-to-end validation · `autopilot` adapter coupling refactor · SQL `withTx` helper · generic `Poller[T]` extraction · `Releaser` frozen-at-startup fix. Source: `.agent/audits/AUDIT-2026-05-25.md` §3. |

**Operator-parked (not autopilotable):** `cdk deploy` of infra PR#27 (Environment tag + NAT→1; brief egress blip, time around the canary tenant) · branch protection on `qf-studio/pilot` main (TASK-405 founder decision 7 — main is currently unprotected) · infra#2 Golden AMI v2 (**stuck: `aws-infrastructure-pilot` is not in the box config, so the poller can never see it** — onboard the repo or move the issue to `pilot-cloud-infra`) · console#45 (`pilot-spec-incomplete`/`blocked` since 07-24 — needs rewriting into an implementable spec).

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

**Default: release then upgrade — don't run ad-hoc local builds.**

```bash
make test
make fmt && make lint
```

**Cycle-gated exception (2026-07-10):** to run merged-but-unreleased `main`
on the daemon *without* cutting a release (release cycles hold work for the
16:00 train), build from a **detached worktree at `origin/main`** and install
to the daemon's path — NOT the root, NOT `make install` (~/go/bin), NOT brew:

```bash
git worktree add --detach /tmp/pilot-build origin/main
cd /tmp/pilot-build && make build          # bin/pilot, version stamped from git describe
cp -p ~/.local/bin/pilot ~/.local/bin/pilot.bak-<rev>   # rollback
cp bin/pilot ~/.local/bin/pilot            # daemon runs ~/.local/bin/pilot (mem: binary path)
git worktree remove --force /tmp/pilot-build
# restart daemon in the zellij `pilot` pane: pilot start --dashboard --github --telegram --tunnel --replace
```

Config is external (`~/.pilot/config.yaml`) — the new binary shares it
unchanged. Building never releases (release = tag push only). Verify the
running binary with `go version -m ~/.local/bin/pilot | grep -E 'main.version|vcs'`.

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
