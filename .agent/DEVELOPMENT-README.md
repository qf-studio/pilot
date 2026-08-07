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

**Current Version:** box runs **`Pilot v2.254.0-7-g8c8b79eb`** (08-06 11:06Z — consented manual rebuild+restart from main to activate the GH-4738 gauge fix + GH-4740 label-strip; `pilot_window_*` verified live 93.3%/$3.71; tonight's 16:00 train cuts v2.255.0 from same-line main and self-upgrades over it, harmless). 08-04 milestone: **first fully unattended drain→upgrade→re-exec cycle** (v2.253.0; #4683 admission-pause drain; exec preserves PID/etime — verify via `/proc/<pid>/exe`, never board uptime). **Daemon runs on AWS** (`i-0e0c1ca34e7b561f9`, TASK-409; ops via `pilot-aws` skill; NO local daemon; binary `/var/lib/pilot/bin/pilot`, `/usr/local/bin/pilot` symlinks to it, rollback at `pilot.prev`) — **approvals OFF since 07-20** (auto-merge on green CI; size-gated PRs still park `awaiting_approval`) — **GH-4391 rate-budget client LIVE** | full status in `.agent/system/FEATURE-MATRIX.md`

**PRIORITY (founder directive 2026-07-26 — supersedes 07-17):** **SaaS/platform UNPARKED — TASK-405 is active work again.** The 07-17 ordering (pointer delivery → pilot reliability → SaaS parked) held while the dispatch-reliability chain was open; that chain closed with v2.246.0 on 07-25. Pointer and pilot reliability remain live tracks but no longer gate S-milestone dispatch. Memory: `founder-priority-pointer-first-saas-parked` (superseded).

**Recent (Aug 1 – Aug 6 2026; detail lives in `system/saas-roadmap.md`, `tasks/archive/`, and git log — do not re-grow this block, replace it):**
- **08-06: gauges LIVE via consented manual rebuild · fix-and-follow-up day (7 PRs merged) · S4 wave 4 dispatched.** GH-4738 root cause: `AggregateMetrics.Snapshot()` never copied the 5 `Window*` fields and every real deployment reads `/metrics` through the aggregate → PR#4739 (verbatim take from first `WindowDays>0` source + `<=0` clamp + component-logger routing + composed-wiring regression test). GH-4740: failed executions left `pilot-in-progress` → poller never re-picked (console#98 stuck 1.5h) → PR#4741 (strip in `ExecutionLifecycle.Persist`, all non-deliverable terminals). Queue idle at 11:06Z → **rebuild+restart to `v2.254.0-7-g8c8b79eb`** → `pilot_window_*` live: **30d delivery 93.3% · $3.71/delivered · $2023.80 window spend** (beats the 08-05 estimate of 81%). Also merged: ui PR#49 createInTracker→dispatch verb · console PR#106 dup-canonical 400 · pilot PR#4744 brief-metrics population fix · PR#4745 GitHub App installation-token auth (+ cutover prereqs filed: #4746→PR#4750 gh-CLI env routing MERGED · #4747 SDK TokenSource in flight). **S4 wave 4** research-grounded then dispatched: **close verb DROPPED** (already satisfied by C7+C4 generic status path) · 22→31 stage-vocab drift corrected in roadmap · pilot#4748 C14-pilot (approval read + first gateway POST on `DecisionRecorder`) · pilot#4749 execution-events endpoint · console#107 C15→**PR#108 MERGED** (org labels + alert rules). C14-console + timeline legs deliberately gated on #4748/#4749 **merging** (chain gates release on execution-done, the C9 lesson). Grom example moved to window gauges + `max()` label-churn fix (grot repo, local commits). task_stuck suppression deferred: GH-4416 already skips Queued phase — needs a live capture to pin the real path.
- **08-05 pm: S4 wave 3 + UI wave — 5 of 6 legs MERGED; C8 blocked on review.** C5 statusmap PR#99 (23 min dispatch→PR) · C6 budgeter PR#100 · ui#44 httpAdapter un-stub PR#46 · ui#45 statusmap editor PR#47 (autopilot-merged) · pilot#4735 **TASK-448 → PR#4736 merged 12:31Z, rides the 16:00 train** (headline metrics → rolling 30d window: `GetWindowedStats` single canary-excluded population, TUI `~$X/issue · 30d` cards, dashboard JSON `window{}`, 4 `pilot_window_*` gauges on 5-min ticker, `stats_window_days` knob, `GetLifetimeTokens` canary fix; ledger truth: lifetime $0.80/79.9% were era-blended — honest 30d ≈ $3.66/delivered issue, 81% delivery). **console#97 C8 → PR#101 auto-merged BEFORE the Navigator review landed** (approvals-off + green CI outran the review — pipeline gap: a review comment does not gate auto-merge; the two blockers are on console main → **fix issue console#102 filed**: hoist writers-nil 503 above card mutation · route `CreateIssue` through the C6 budgeter · 3 nits). **#98 C9 released at 14:16Z and running** — the wave chain gate is execution-done, not PR-merged. **16:00 train GREEN**: v2.254.0 published 14:09Z (first fully-green run incl. tap push since 08-01 — token rotation proven), box self-upgraded 14:13Z (verified `/proc/exe`). **New bug found on box: `pilot_window_*` gauges serve `window="0d"`/0** — seed + 5-min refresher never populate the exporter's Metrics instance while adjacent lifetime hydration works; TUI 30d card + JSON `window{}` unaffected; config-decode ruled out (local repro: `Load` → 30) → **pilot#4738 filed** with full evidence. **Token incident resolved**: daemon rides the box gh OAuth token (`gho_`, no expiry) — every PAT unused since the 07-16 cutover; SOP `sops/config/github-token-architecture.md` rewritten; `HOMEBREW_TAP_GITHUB_TOKEN` + `CANARY_GH_TOKEN` rotated, both canaries green, #4673 closed. Durable follow-up (unfiled): GitHub App installation token kills the OAuth SPOF + shared 5000/hr pool.
- **08-05 am: TASK-441 CODE COMPLETE — all 8 legs + 5 adapter fixes merged in 2 days; seam code LIVE on box via manual rebuild.** L6 #4733→PR#4734 (2-method `IssueLabeler`, autopilot-only) closed the set; L8 PR#4732 refreshed `.agent/system/ARCHITECTURE.md` (Pilot's audit corrected table count to **30 live tables/3 stores**, not "34"; convergence doc superseded). Frozen-contract grep-diff clean across all 13 PRs. **Sole remainder: operator kill-drill** (`sops/operations/task441-kill-drill.md`, UNGATED since the 08:33Z rebuild) → then archive TASK-441. Also 08-05: **org upgraded to GitHub Team** (API pool UNCHANGED at 5000/hr — verified; branch protection on private repos now available; `system/project/project_github_org_plan.md`).
- **08-04: first unattended self-upgrade (v2.253.0) · TASK-437 CLOSED (all 9) · TASK-441 launched + legs 1–5,7 merged same day.** 08-03 incident chain fully remediated (#4656/#4685/#4692/#4695/#4686/#4699/#4698/#4700 · gh-guard PR#4704 · runSelfReview-root #4706 + `backendExecute` chokepoint #4707). TASK-441 highlights: `make check-mocks` PR#4711 · `alerts.DeadManTracker` PR#4712 · notify-audit PR#4713 (verdict: no GH-4692-class bug in the 6 non-GitHub adapters — SDK pollers label internally; 5 comment-gap fixes #4723/4725/4728/4729/4730, Jira's closed its dead `Transitions` config) · `LivenessPolicy` PR#4722 · `Finish` tripwire sweep PR#4724. Memories: `runselfreview-runs-in-repo-root-phantom-reimplementation`, `localmode-tasks-never-get-worktree-qdocs-in-root`, `poller-labels-removed-log-means-never-applied`, `config-env-expansion-eats-dollar-vars-in-commands`.
- **08-03/08-04: v2.252.0 shipped via MANUAL upgrade** — self-upgrade drain deadlocked on a saturated queue (waits on queued+running while admission continues; filed+fixed as #4683, proven by the 08-04 autonomous cycle). **S4 board wave 2 MERGED** (console#89 C7 API→PR#92 · #90 C3 ingest→PR#93 · #91 C4 outbound→PR#94; TASK-438/439/440 done). **infra#2 Golden AMI v2 merged** (gh CLI + pilot binary baked, throwaway-instance bake test; repo onboarded to box config with per-project quality gates; **operator bake run pending**). Dashboard `superseded` status filed #4701→shipped.
- **Earlier (compressed):** 08-01/08-03 S4 wave 1 merged (C1/C2, kanban v1, drawer/feed) + studio-sdk contract verified · 07-31 first autonomous train v2.251.0 + release-train RCA (TASK-431) + AWS cost audit (infra PR#27 merged, `cdk deploy` pending) · 07-30 spec-guard epic + real-stack-verify SOP · 07-29 S3 backend 10/10 + local stack LIVE · 07-27/28 S2 EXIT MET · 07-26 SaaS UNPARKED · 07-20 approvals off · 07-16 S6-lite AWS cutover (TASK-409). Detail: git log + `tasks/archive/`.

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

- **Plan of record + live status**: [`system/saas-roadmap.md`](system/saas-roadmap.md) (v9.9) — S0 ✅ · S1 ✅ · S2 ✅ (exit met 07-27) · H1–H12 ✅ · R-track ✅ · S6-lite ✅ · **S3 BUILT** (exit gated on founder staging inputs → operator deploy per infra PR#25) · **S4 board: waves 1+2 merged** (C1/C2/C7/C3/C4 + kanban UI) · **wave 3 + UI wave COMPLETE 08-05** (C5 · C6 · C8+fixes · C9 · ui#44/45 · TASK-448 metrics+PR#4739/4741 fixes) · **wave 4 in flight 08-06** (C15 PR#108 ✅ · pilot#4748 C14-pilot + #4749 events endpoint queued · C14-console + timeline legs gated on those merging · close verb dropped as already-built)
- **Program doc**: [`tasks/TASK-405-pilot-saas-platform.md`](tasks/TASK-405-pilot-saas-platform.md)
- **Design**: [`system/saas-architecture.md`](system/saas-architecture.md) · [`saas-kanban-sync-design.md`](system/saas-kanban-sync-design.md) · [`saas-fleet-design.md`](system/saas-fleet-design.md) · [`saas-asset-research.md`](system/saas-asset-research.md)
- **New repos** (created 2026-07-14, in `~/.pilot/config.yaml`): `qf-studio/pilot-console` (Go control plane) · `pilot-console-ui` (Vue3/Vite/Bun SPA) · `pilot-cloud-infra` (Go CDK) — each has its own `CLAUDE.md`
- **Latest handoff marker**: `.agent/.context-markers/2026-08-06_wave4-inflight-gauges-live-compact-ready.md`
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
| **P0** | **Contract hardening tune-up** ([TASK-441](tasks/TASK-441-contract-hardening-tune-up.md)) | 8-leg program vs the "wiring bug wearing a green test suite" class (judge dead 17d, labels never applied since cutover, self-review in repo root, drifted silence floors). Legs: ban argument-discarding mocks · reusable dead-man tracker in alerts engine · non-GitHub handler notify audit · one `LivenessPolicy` · tripwire sweep on `Finish` · narrow `IssueLabeler` iface · gh-guard (=#4671) · ARCHITECTURE.md refresh (doc is 10 weeks stale, 4 of 6 documented tables don't exist). External contract freeze list embedded — SaaS consumers must not break. **ALL 8 LEGS ✅ MERGED (PR#4711/4712/4713/4722/4724/4704/4732/4734) + 5 adapter notify fixes ✅ · frozen-contract grep-diff clean · sole remainder: operator kill-drill per `sops/operations/task441-kill-drill.md` once first post-08-04 train lands (box still v2.253.0) → archive.** |
| **P1** | **Pilot SaaS platform** ([TASK-405](tasks/TASK-405-pilot-saas-platform.md)) | S3 **built**, exit gated on founder inputs (Stripe test keys/price/webhook secret · console + sending domains · ACM DNS) → operator staging deploy → S3 exit test. S4 board: **waves 1+2 MERGED** (C1/C2/C7/C3/C4 + kanban UI — console PRs #87/#88/#92/#93/#94; TASK-432–435, 438–440 done). **Wave 3 rolling 08-05, 3 of 6 legs merged same-day**: console#95 C5 (PR#99) + #96 C6 (PR#100) + ui#44 un-stub (PR#46) all **MERGED** (TASK-442/443/446 archived) → running: #97 (C8 dispatch verb, [TASK-444](tasks/TASK-444-console-c8-dispatch-verb.md)) → #98 (C9 metrics, [TASK-445](tasks/TASK-445-console-c9-sync-metrics.md)) · ui#45 (status-map editor, [TASK-447](tasks/TASK-447-ui-statusmap-editor.md)). **Metrics 30d-windowing dispatched**: pilot#4735 ([TASK-448](tasks/TASK-448-metrics-30d-window.md)) — headline cost/success move to a rolling 30-day window (operator decision 08-05; lifetime $0.80/79.9% were era-blended; honest 30d: ~$3.66/delivered issue, 81% delivery). System docs: [`saas-architecture.md`](system/saas-architecture.md) · [`saas-kanban-sync-design.md`](system/saas-kanban-sync-design.md) · [`saas-fleet-design.md`](system/saas-fleet-design.md). Roadmap: [`saas-roadmap.md`](system/saas-roadmap.md) v9.9. |
| **P0** | **Irreversible-action audit** ([TASK-459](tasks/TASK-459-irreversible-action-audit.md)) | **Phase 1 dispatched 08-07 → #4796** (inventory + typed `Verdict` contract, `no-decompose`); Phases 2→3→4 gated on it, Phase 5 (false-success class) needs a scope call. The false-signal class: the daemon takes irreversible actions (close PR, delete branch, spawn fix issue, burn retry) on inferred failure, applying the same confidence threshold as a log line. Three root patterns — absence-of-evidence read as failure · failure inferred from side-effects instead of recorded status · one fact implemented twice and drifting. 08-06/07 cost: a correct PR closed 3× during the GH Actions outage (~$10.65 discarded), a resurrected PR re-closed in 90s on superseded check-runs, 3 junk fix-issues, 1 false operator alert. Instances fixed/in flight: #4787 · #4790 · #4791/#4792 · #4794 — this task makes the invariant global and enforced (typed `Verdict` + grep gate + SOP). |
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
