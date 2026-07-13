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

**Current Version:** v2.238.1 (daemon on local build `v2.238.1-11-g6e60eb4b` = origin/main incl. full hardening set; rides next 16:00 train) | full status in `.agent/system/FEATURE-MATRIX.md`

**Recent (July 3–12 2026):** **M7 COMPLETE — [#3423](https://github.com/qf-studio/pilot/issues/3423) CLOSED 2026-07-11.** All 10 adapters' poll/chat paths on studio-sdk; `internal/adapters/*` retired to accepted residual (path B, 4 live-caller github files, config TYPES/HELPER — gitlab precedent). GitHub adapter fully on SDK (4d.2e rollout 7/7 repos, 4d.5 webhook #4157, 4d.6 cleanup #4155) + final dead-code sweep ([TASK-397](tasks/archive/TASK-397-m7-close-out.md)). Release-cycle cutover COMPLETE (pilot repo `on_merge→on_schedule` daily 16:00 Berlin). Throughput Phase 1 + sub-issue ledger + reconciler/release fixes all live. (Detail below.)
- **July 11–12 — Dashboard grom-nav SHIPPED + LIVE-VERIFIED ([TASK-398](tasks/archive/TASK-398-dashboard-grom-panel-navigation.md)→[TASK-399](tasks/archive/TASK-399-dashboard-nav-wiring.md)):** spatial hjkl/arrow panel focus (grom `focusMove` port), enter=zoom/esc=return, zoomed all-items queue/autopilot/history lists + logs follow-viewport, git graph default-visible, fluid width, logs toggle `l`→`L`. Shipped in two halves: epic [#4199](https://github.com/qf-studio/pilot/issues/4199) collapsed to one sub-issue → PR #4201 scaffold-only (**mem-151 pitfall**: +840/−0 diff never touching `tui.go` passed CI green; parent auto-closed = false completion); wiring re-dispatched single-scope [#4203](https://github.com/qf-studio/pilot/issues/4203) with `Update`/`View` integration-test ACs → PR #4204 (`9a9f375a`, 14m, all 9 `TestNav_*` by exact name). Live-verified 07-12 on daemon local build. Fallout bug filed: [#4206](https://github.com/qf-studio/pilot/issues/4206) `use_sdk_poller` deprecation nag fires unconditionally when github enabled (config.go:884) — rescoped to full field deletion (M7 total, yaml parse lenient). mem-148: port-don't-promote grom internals + embed ported source in specs (executor can't read sibling repos).
- **July 11 — M7 milestone CLOSED ([TASK-397](tasks/archive/TASK-397-m7-close-out.md)):** final dead-code sweep dispatched to Pilot — W1 [#4189](https://github.com/qf-studio/pilot/issues/4189)/PR #4192 (delete dead `github/poller.go`+`project_board.go`, zero prod callers — slipped past the GH-4169 audit) + W2 [#4190](https://github.com/qf-studio/pilot/issues/4190) (children #4196 registry+jira-adapter, #4194/#4197 dormant `orchestrator.Process*IssueEvent`). #3423 close-out comment + CLOSED; docs truthfulness (`architecture.mdx` path-A/B callout, `github.mdx` polling-primary callout); TASK-368/397 archived. **Accepted residual (deliberate, not debt):** path B (`internal/pilot.Pilot`+`internal/orchestrator`, gateway webhook-only engine — sole path for Linear/Jira/GitLab/AzDO/Asana/Plane webhooks + GitHub webhook mode; no SDK webhook→IssueEvent bridge; retirement = separate future initiative), 4 live-caller github files, config TYPES/HELPER converters. **Future-audit notes:** telegram/slack outbound-notify still in-tree; linear live sub-issue creator `handlers.go:155`. Decisions/pitfall: mem-149 (retire adapters/keep path B), mem-150 (audit caller *liveness* not existence — GH-4169 misclassified `poller.go`).
- **July 10 — M7 Phase 4 finish + release-cycle cutover + defect sweep (daemon on local build `v2.236.9-13-g2d381fa4`):** **M7 Phase 4 code-complete** ([TASK-368](tasks/archive/TASK-368-m7-github-cutover-phase4.md)/[#3423](https://github.com/qf-studio/pilot/issues/3423)): 4d.2e rollout verified (`repos_configured=7 repos_started=7`, zero in-tree pollers), 4d.5 gateway webhook cut to studio-sdk (`OnPRReview` parity, fail-closed empty-secret preserved; #4154→PR #4157), 4d.6 cleanup (#4155→#4173/#4175/#4176/#4177/#4178 — board source→SDK `ProjectBoardSource`, `use_sdk_poller` deprecated+ignored, legacy handler + `spec_validator.go` deleted → single `ghissue.ValidateSpec`). **4d.6 scope-audit lesson**: parent spec over-listed dead files; Pilot grep-audited and *refused* to delete `poller.go`/`cleanup.go`/`merger.go`/`retry.go`/`issue_creator.go` (live callers: stale-label cleanup, sub-issue merge-wait, bot intake, `client.go`↔`retry.go`) → SOP `sops/integrations/github-poller-dead-code-cascade-gh4169.md`. Path B (`internal/pilot.Pilot`) + those kept files → accepted residual, milestone closed July 11 (see above). **Release cutover** (§ Release Cycles): pilot repo daily `on_schedule`, gated on #4150 `(#N)` squash titles + #4174 no-tags first release. **Defects filed & shipped through Pilot:** #4159 approval-callback observability (PR #4162) + #4164 release-aware ack card (split from #4159; PR #4166), #4160 Telegram brief Markdown-escape 400 (PR #4163), #4179 reconciler 404 stale-label loop, #4182/#4185 closed-issue re-dispatch + phantom running-card (PRs #4186/#4187/#4188). TASK-395/#4147 (reconciler false positives) merged; #3380 ops closed (human-led infra).
- **July 9 evening — Throughput acceleration program (TASK-393) + fallout fixes:** 5-lever plan (instrument → lanes → per-repo concurrency → repo primer → trust-tier auto-merge), roadmap `system/throughput-roadmap.md` (M0–M8, D1–D6). **Phase 1 SHIPPED in ~3h**: [#4127](https://github.com/qf-studio/pilot/issues/4127) auto-decomposed → 4 PRs — direct-path stage events, `waiting_ci` persistence, gate/research/retry ledger writes, `time_to_pr`/`queue_wait`/`approval_wait` histograms (**:9091**), grafterm breakdown panel. **Baseline started 20:11 UTC** (M0+M1 ✅). Delivery surfaced 3 defects, 2 already fixed same evening: sub-issue ledger gap ([TASK-394](tasks/archive/TASK-394-epic-subissue-execution-ledger.md)→#4140 ✅ PRs #4144/#4145), release-SHA loop ([TASK-396](tasks/archive/TASK-396-release-sha-propagation.md)→#4146 ✅ `877e08c3`), reconciler false positives ([TASK-395](tasks/archive/TASK-395-epic-reconcile-false-positives.md)→#4143, PR #4147 green, mem-100). New pitfall/decision memories mem-100–104; graph hygiene (2 junk table-separator decision nodes purged — `task_to_graph.py` upstream bug). (Pilot-repo `on_schedule` cutover completed July 10 — see July-10 bullet + § Release Cycles.)
- **July 9 — release-stage wedge INCIDENT + dashboard/metrics fixes (v2.236.2/.3):** `on_merge` auto-release silently **wedged in `releasing` without cutting a tag** on every `require_ci` repo (incl. pilot) — root cause `checkExternalMergeOrClose` (`controller.go:4306`) **lacked a `StageReleasing` guard** (GH-3994 regression): it drained releasing PRs at L4401 before `handleReleasing` fires, then `ScanRecentlyMergedPRs` re-adopted them in a loop. Fix [#4124](https://github.com/qf-studio/pilot/issues/4124)→PR #4125 (one-line guard + the test existing tests bypassed by calling `handleReleasing` directly). **Manual bootstrap tags v2.236.2/.3** cut to break the deadlock (the fix couldn't release itself — running daemon still had the wedge). **NOT restart-related** (marker's prior theory refuted — daemon alive ~170 ticks with PRs stuck). Pitfall **mem-093**. Also: dashboard history **2-row collapse** — `storeRefreshCmd` (`tui.go:876`) kept the raw-5-cap that #4117 only fixed on the startup path, full-replacing history each tick ([#4119](https://github.com/qf-studio/pilot/issues/4119)→#4120, **live-verified 5 distinct rows**); Grot analytics stats mislabeled — "success rate" tile bound to **deprecated `pilot_success_rate` (86.1%)** vs issue-level **78.9%**, and `pilot_prs_merged/failed` **ledger-scoped (54) vs all-time** cost/tokens ([#4121](https://github.com/qf-studio/pilot/issues/4121)→#4122 hydrates from `executions`); grot-config repoints (grot repo `examples/pilot.yaml`) + `pilot_ci_runs_total{result}` ask (pilot#4029) owned by Grom.
- **July 8 (M7 4d.2 fan-out, v2.236.0/.1):** `use_sdk_poller` now drives the default repo **+ every `projects[]` github repo** (was default-only); repo-keyed `githubPollerRegistry` fixes the sub-issue-skip/done/stale-label loops that only ranged the in-tree slice (epic children were duplicate-dispatched) — [#4110](https://github.com/qf-studio/pilot/issues/4110)/#4114 + #4115, studio-sdk **v0.30.0**. Root worktree drift lesson: a stale checkout behind origin is a fast-forward that **never ran** (fetch ≠ merge), not a divergence.
- **Dashboard grot redesign — [TASK-390 SHIPPED](tasks/archive/TASK-390-grot-dashboard-redesign.md), July 8, v2.234.0→v2.235.8:** whole TUI on `github.com/qf-studio/grot` v0.1.0 (go 1.24.2→1.25.0) — stat cards + panel chrome, pulsing daemon-liveness banner dot, queue border legend, 7-rung history segment meters + `StageInfo` (GH-4023 reducer preserved), autopilot per-PR lifecycle rows, glyph vocabulary, truthful history glyphs (#4071), `pr_title` persistence (#4080), Dockerfile go 1.25 (#4091, images had failed on every tag since v2.234.0). Loop milestone: #4067/#4072/#4074 Pilot-built, autopilot-merged, released with no human action.
- **July 8:** (1) **SDK-poller label drop fixed** — #4050 (HIGH, GH-201/727/920 regression from M7 4b: `handleGithubIssueEventSDK` dropped `Labels`/`State`/MemberID/AcceptanceCriteria/FromPR; every label gate no-op'd fleet-wide for 2 days; pitfall mem-089) → PR #4085. Proof: GH-3994 retry honored `no-decompose` → clean +269/−2 PR #4096. (2) **Metrics chain** (from #4029-comment forensics): #4068 multi-controller Prometheus aggregation (exporter read only the idle default controller — PR family flat 0; PR #4081) · #4069 dead `RecordPRConflicting` · #4070 success_rate semantics (per-attempt 64% vs ~100% issue-level; hydrator collapsed non-failures into failed) · #4093 PR-family hydration from `execution_events` (running). (3) **False-failure family closed**: #4052 epic→decomposer numbered-list feed · #4053 adopted-PR persist wedge · #4055 size floor excludes `.agent/**` · #4092 stale-recovery now checks branch-PR state before failing (5th recurrence, GH-4084). (4) **Security**: throwaway accounts posted fake "patch .zip" comments on fresh bugs (#4050/#4041) — architecturally inert (executor reads only issue title+body, `pilot` label needs write access); SOP `sops/security/malicious-patch-zip-comments.md`, learning mem-090; comments removed. (5) TASK-380/381 backlog cleared (PRs #4087/#4089).
- **July 6–7:** SDK-poller trial verified on real work (poll → dispatch → spec-guard → intent-judge veto/self-correct → SDK PR-create → CI incl. drift gate → autopilot merge → auto-release); two live incidents fixed same day through Pilot's own pipeline — **#3919** fail-loud poller token (v2.220.1) + **#3922** cross-repo PR-state collision (`repo` column, 4d.2 groundwork). SDK Logger gap closed cross-repo via label-as-trigger (studio-sdk#79 → **v0.29.0** → pilot#3921; pattern mem-087). **Release automation ([TASK-388 archived](tasks/archive/TASK-388-release-automation.md)):** publish modes + verification + human-merge tagging, config rolled out to all 8 repos. **Release cycles ([TASK-389](tasks/TASK-389-release-cycles.md), 5/6):** scope-triggered (epic/`scope:` label) + scheduled release trains, aggregated notes + LLM What's New; E #3994 (bug-only mode) open on human decision. Ledger forensics on "failed" shipped work filed **#4020** (executor duplicate-PR finalization + stage-strip masking) + **#4021** (auto-retry guard bypass + waiter race) — pitfall **mem-088**: verify `execution_events` + GitHub before trusting a ✗.
- **Runtime self-verification — [TASK-379](tasks/archive/TASK-379-runtime-self-verification.md) SHIPPED (V1–V8):** live auth probes in `doctor`/`/ready` + fail-loud degraded paths (V1–V3), 401-escalation + disabled-subsystem panel + config redaction (V4), execution-ledger consistency + `execution_events` + `pilot trace` + dashboard strip (V5/V6), shared Anthropic request builder retiring the #3700/#3703 400-class (V7), and a **scheduled synthetic end-to-end canary** (V8) on sandbox `qf-studio/pilot-canary-sandbox` — **validated live** (issue→daemon→PR→merge, `workflow_dispatch` run `28713784142`). Two first-run bugs caught & fixed on main (#3866 poll-step `gh --jq --arg`, #3869 spec-completeness header gate). Canary cron `disabled_manually` pending an auto-merge design decision (merge automation is project-specific). Retires the wiring-bug class from AUDIT-2026-05-25.
- **Defect burn-down — [TASK-382](tasks/archive/TASK-382-restart-epic-defect-burndown.md): 15/15 SHIPPED.** Final defect D6 ([#3789](https://github.com/qf-studio/pilot/issues/3789)) closed 2026-07-05 via [TASK-384](tasks/archive/TASK-384-gh3789-blockedby-inmemory-poll-gate.md) → [#3882](https://github.com/qf-studio/pilot/issues/3882) → PR #3883 (v2.214.1): blocker state resolved in-memory against the fetched candidate list — zero per-blocker API calls (the stress-deadlock root cause of 4 failed autonomous attempts). `auto_label_pilot` no-op ([#3710](https://github.com/qf-studio/pilot/issues/3710)) closed not-planned (vestigial field).
- **June 24–30 (historical, all archived):** M7 Phase 4a poll-path scaffolding closed on studio-sdk v0.25.0 (superseded by 4b/4d — see P1 row); conversational-bot docs page; Slack `/help` command routing fixed at the adapter seam after a false-green first fix ([TASK-372](tasks/archive/TASK-372-command-routing-safe-default.md), v2.194.x, mem-036); operational-intent live-queue answers ([TASK-371](tasks/archive/TASK-371-operational-intent-live-queue.md), v2.194.0); read-only-intent ghost-SHA fix + unified comms Handler factory (TASK-369/370, v2.192.x); cache-token persistence (v2.192.0, TASK-366/367). Detail: `tasks/archive/` + markers `2026-06-30_m7-sdk-gate-and-bot-docs-shipped.md`, `2026-06-25_slack-command-fix-shipped-v2.194.2.md`.

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
| P0 | **Release SHA propagation** ([TASK-396](tasks/archive/TASK-396-release-sha-propagation.md)) | Release pipeline fully wedged (2nd layer of mem-093): `handleReleasing`'s `HeadSHA←PostMergeSHA` copy-back is scope-gated (controller.go:2619, GH-3990) → plain squash-merged PRs always fail `guardReleaseSHAReachable` ("diverged") → `releasing→failed` re-adopt loop (#4139 18 attempts, #4144), **no organic tags** (v2.236.4 blocked; interim = manual tag, mem-020). Dormant since 06-10/#3559, unmasked by #4125. Fix: widen copy-back to `PostMergeSHA != ""` + tick-through tests. ✅ **SHIPPED 2026-07-09** — merged as `877e08c3` ([#4146](https://github.com/qf-studio/pilot/issues/4146)); releasing ✗-loop cleared. |
| P0 | **Epic sub-issue execution ledger** ([TASK-394](tasks/archive/TASK-394-epic-subissue-execution-ledger.md)) | Incident 2026-07-09 (epic GH-4127 → #4128–#4131): in-process sub-issue runs create no `executions` row → invisible to `IsTaskQueued` → poller's 5-min `retryGracePeriod` lease expires mid-epic → duplicate dispatch + `no_op` re-runs; execution-event FK failures (since 07-06); shipped work never credited `completed` → queue-depth sparkline/✓ counter starved. Fix: ledger row per sub-issue run + merged-PR pre-execute short-circuit. ✅ **SHIPPED 2026-07-09** — #4140 closed, PRs [#4144](https://github.com/qf-studio/pilot/pull/4144)/[#4145](https://github.com/qf-studio/pilot/pull/4145) merged, daemon live on it; verify FK-silence on next epic. |
| P1 | **Throughput acceleration** ([TASK-393](tasks/TASK-393-throughput-acceleration.md)) | **Phase 1 (instrumentation) ✅ SHIPPED 2026-07-09**: [#4127](https://github.com/qf-studio/pilot/issues/4127) auto-decomposed → #4128–#4131, 4 PRs merged same day (#4133/#4136/#4137/#4138) — direct-path stage events, `waiting_ci` persistence, quality/research/retry ledger writes, `time_to_pr`/`queue_wait`/`approval_wait` histograms, grafterm breakdown panel. **M3 baseline window OPEN 2026-07-13 09:38 UTC → ~07-20** (daemon on `6e60eb4b`; pre-window snapshot 93/104/31 — see roadmap M3 row). Remaining phases: (2) execution lanes on `Complexity`, (3) N-concurrent per repo (`ProjectWorker` pool, mem-101; GH-1312 shared-manager prereq, mem-102), (4) SHA-keyed repo primer, (5) risk-score trust tiers (mem-103). Delivery surfaced two defects: sub-issue ledger gap ([TASK-394](tasks/archive/TASK-394-epic-subissue-execution-ledger.md) → [#4140](https://github.com/qf-studio/pilot/issues/4140), ✅ shipped) + reconciler false positives ([TASK-395](tasks/archive/TASK-395-epic-reconcile-false-positives.md), ✅ merged 07-10 → [#4143](https://github.com/qf-studio/pilot/issues/4143), mem-100). **Roadmap: [`system/throughput-roadmap.md`](system/throughput-roadmap.md)** (M0–M8, gates, decision points D1–D6). |
| ✅ | **Dashboard grom-style panel navigation** ([TASK-398](tasks/archive/TASK-398-dashboard-grom-panel-navigation.md) → [TASK-399](tasks/archive/TASK-399-dashboard-nav-wiring.md)) | **SHIPPED 2026-07-11 in two halves.** Epic [#4199](https://github.com/qf-studio/pilot/issues/4199) collapsed to ONE sub-issue that shipped scaffold-only (PR #4201 `576b38ae`, v2.237.0 — helpers uncalled, mem-149 pitfall: +N/−0 diff never editing the target file passes CI trivially). Wiring re-dispatched single-scope with `Update`/`View` integration-test ACs → [#4203](https://github.com/qf-studio/pilot/issues/4203) → PR #4204 merged (`9a9f375a`, 14m): spatial hjkl focus, enter=zoom/esc=return, zoomed all-items queue/autopilot/history lists + logs follow-viewport, git graph default-visible, fluid width, logs toggle `l`→`L`. All 9 `TestNav_*` ACs delivered by exact name. **LIVE-VERIFIED 2026-07-12** (daemon on local build `v2.237.0-3-g9a9f375a`: hjkl focus walk, panels zoom open/close — user-confirmed). Rides the next release train (>v2.237.0). |
| P1 | **Epic reconcile shipped-check false positives** ([TASK-395](tasks/archive/TASK-395-epic-reconcile-false-positives.md)) | 🚀 [#4143](https://github.com/qf-studio/pilot/issues/4143) → PR [#4147](https://github.com/qf-studio/pilot/pull/4147) ✅ **MERGED 2026-07-10** (spec live-validated twice pre-merge — reproduced on #4140's close). GH-4127 incident: parent in its own text-search child set (self-amplifying), eventually-consistent `SearchPRsForIssue` → false "no merged PR" vetoes, escalation on already-closed parents, `pilot-needs-clarification` added but never removed. 4 fixes in `internal/autopilot/epic_reconcile.go`; land after [#4140](https://github.com/qf-studio/pilot/issues/4140). |
| ✅ | **Runtime self-verification** ([TASK-379](tasks/archive/TASK-379-runtime-self-verification.md)) | **SHIPPED — all 8 waves (V1–V8), 2026-07-04.** doctor/`/ready` live auth probes, fail-loud degraded paths, execution ledger + `execution_events` + `pilot trace`, shared Anthropic builder, and a **synthetic E2E canary validated live** (issue→daemon→PR→merge). **Auto-merge design call RESOLVED (2026-07-05):** the canary proved autopilot auto-merges on no-CI repos unaided; the only blocker was a pre-merge CI **grace-restart bug** (`verifyCIBeforeMerge` restarts the discovery grace on an already-resolved no-CI SHA → merge deadlock) — **fixed** ([#3873](https://github.com/qf-studio/pilot/issues/3873) → PR #3877 merged) + canary issue-body version-drift fixed ([#3874](https://github.com/qf-studio/pilot/issues/3874) → PR #3875). Canary cron ✅ **re-enabled 2026-07-13** (workflow 307188350, daily/6h) after TASK-403 metrics isolation (#4256). |
| ✅ | **Restart & epic-lifecycle defect burn-down** ([TASK-382](tasks/archive/TASK-382-restart-epic-defect-burndown.md)) | **15/15 SHIPPED 2026-07-05.** Defects from the July-3 autonomous shift; register with issue links + release versions in the task doc. Final defect D6 ([#3789](https://github.com/qf-studio/pilot/issues/3789)) closed via [TASK-384](tasks/archive/TASK-384-gh3789-blockedby-inmemory-poll-gate.md) → [#3882](https://github.com/qf-studio/pilot/issues/3882) → PR #3883 (v2.214.1): `hasPendingDependencies` resolves blockers in-memory against the fetched candidate list — zero per-blocker API calls (the stress-deadlock root cause of 4 failed attempts), sequential skip metrics added. First-attempt merge, +194 additions. |
| ~~P0~~ | **`@Pilot /help` creates a task on Slack** ([TASK-372](tasks/archive/TASK-372-command-routing-safe-default.md)) | ✅ **CLOSED 2026-06-25 + archived.** Two PRs: TASK-372 ([#3659](https://github.com/qf-studio/pilot/pull/3659), v2.194.1) added `IntentCommand` routing + safe `default:→clarify` in `comms` — necessary hardening but **did not fix the live bug** (the studio-sdk chat bridge splits `/`-commands into `Action:"command"` with empty `Text` one layer up, so the command never reached `comms`). Real fix [#3661](https://github.com/qf-studio/pilot/pull/3661) (`main @ f88d76de`) handles `Action=="command"` at the adapter seam (`slack/handler.go` + shared `sdkshim.MessageEventToIncomingMessage`). Live-verified: `/help`,`/status`,`/queue` route to command output, no stray tasks. Lesson: `pitfalls/bug_sdk_command_action_dropped.md` (mem-036) — test at the adapter→comms seam, not the inner layer in isolation. |
| ✅ | **Dashboard HISTORY progress fraction** ([TASK-383](tasks/archive/TASK-383-history-progress-fraction.md)) | **SHIPPED 2026-07-04** — [#3879](https://github.com/qf-studio/pilot/issues/3879) → PR [#3880](https://github.com/qf-studio/pilot/pull/3880) merged (v2.214.0). Variable-length `✓`-strip replaced with retry-proof `reached/7 stage` fraction (sage/steel/rose, no legend). HISTORY-only; ACTIVE rail untouched. Doc archived. |
| ~~P3~~ (ops) | **Host cache reclaim + GitLab registry retention** ([#3380](https://github.com/qf-studio/pilot/issues/3380)) | **CLOSED 2026-07-10** (human-led, needs infra access — not autopilotable). Steps remain in the issue body for the operator: reclaim stale `/data/quantflow/pilot_cache` + add GitLab Container Registry cleanup policy. Context: `.agent/system/docs-cache-and-lighthouse.md`. |
| P1 | Multi-tenant SaaS mode | Single-user CLI → hosted needs auth, isolation |
| P1 | Public launch prep | Landing page, onboarding, pricing, billing |
| P1 | Web dashboard polish | React UI functional but needs design pass |
| P1 | Fix `shouldTriggerRelease()` | Doesn't check `ResolvedEnv().Release` — only top-level config |
| ✅ P1 | **GH Projects board as work source + M7 SDK cutover** — Studio SDK roadmap | ✅ **M7 COMPLETE — [#3423](https://github.com/qf-studio/pilot/issues/3423) CLOSED 2026-07-11.** All 10 adapters' poll/chat paths consume studio-sdk; `internal/adapters/*` retired to accepted residual per the gitlab precedent. GitHub endgame (4b/4d.1/4d.3/4d.4 → 4d.2 fan-out v2.236.0 → 4d.2e rollout → 4d.5 webhook #4157 → 4d.6 cleanup) + final dead-code sweep ([TASK-397](tasks/archive/TASK-397-m7-close-out.md): W1 #4189/#4192 delete dead `github/poller.go`+`project_board.go`; W2 #4190 → #4196 registry+jira-adapter + #4194/#4197 dormant `orchestrator.Process*IssueEvent`) all merged. **Accepted residual (deliberate):** path B (`internal/pilot.Pilot`+`internal/orchestrator`, gateway webhook-only engine — sole path for Linear/Jira/GitLab/AzDO/Asana/Plane webhooks + GitHub webhook mode; retirement = separate future initiative) · 4 live-caller github files (`cleanup`/`merger`/`issue_creator`/`retry`) · config TYPES+HELPER converters. **Future-audit notes:** telegram/slack outbound-notify still in-tree; linear live sub-issue creator `handlers.go:155`. Board loop shipped/archived (TASK-317/319/354/355/356). SDK gate history: [TASK-385](tasks/archive/TASK-385-studio-sdk-v0260-github-surface.md); phase detail [TASK-368](tasks/archive/TASK-368-m7-github-cutover-phase4.md). |
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
