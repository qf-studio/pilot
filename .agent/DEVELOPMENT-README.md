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

**Current Version:** box runs **v2.268.0-18-gf28e48c2** — MAIN TIP, founder-ordered rebuild+restart 08-24 22:35Z (all ~22 of 08-24's merged fixes LIVE: sequential mode w/ sdk **v0.38.0** pin, base-presence body-edit self-heal #5199, gate hardening #5193, dashboard cards #5196; `pilot_build_info` metric-verified). The 08-25 14:00Z train cuts **v2.269.0** and self-upgrades over this — same content, expected. ⚠️ Known noise: `circuit_breaker_trip` critical re-fires to Slack every 30m off a standing counter with NO live trip — **#5209** (pilot-labeled) makes it edge-triggered. **Box auth: 1-year `CLAUDE_CODE_OAUTH_TOKEN` in `start-pilot.sh`** since 08-23 (the 8h-refresh `.credentials.json` path is dead — installed after the 08-23 OAuth outage; signature + two-part cap re-arm recipe = mem-171/174). ⚠️ laptop `pilot` binary freezes at cutover versions — `pilot upgrade` locally before trusting local `--help`. Prior: v2.263.0 (08-19 train; sdk v0.35.2 pin PR#5007). Prior: v2.262.0 (installed 08-18 late evening — founder-confirmed), corrective operator tag at `b58b659c` after the 08-18 train defect (recovery fired 15min BEFORE the 14:00Z tick cutting v2.261.0 at 13:45Z, then the 15:12Z restart never recovered the genuinely-missed tick → **pilot#4982** filed with log evidence). v2.262.0 carries: sdk **v0.35.1** pin (Jira Cloud ADF poller fix — **#4917 CLOSED** against this version), releaser tags-authoritative baseline (#4953→PR#4973), dispatch label-unwind (#4961→PR#4971), ghguard pflag parser (#4963→PR#4975, APPROVE-w-notes) + GH_REPO/GH_HOST env guard (#4968→PR#4981), no_op decline contract (#4964→PR#4977), comms signal-strip consolidation (#4967→PR#4980), Linear UUID label filter (#4965→PR#4978), squash-prefix stripping (#4966→PR#4979), and the golangci-lint **skip-cache** CI fix (`049456d5` — self-poisoning cache killed 4 green PRs; pitfall `golangci-action-cache-self-poisoning-sa5011`). Jira adapter now LIVE against `quantflowstudio.atlassian.net` (project KAN, label `pilot`, 60s poll; token on box config only). Earlier: **`v2.259.2`** (08-13 14:19Z hot restart — includes PR#4865 running-version observability + PR#4867 dead-man rules fix, both drill/wire-validated same day). ⚠️ **The "disk≠process mismatch" signature is RETIRED — it was the same misdiagnosis ×3** (memory `hot-restart-preserves-pid-uptime-false-mismatch`): the restart leg is `syscall.Exec`, the PID is preserved, so board `uptime` (ps etime) survives hot restarts and proves nothing. **Running-version surface LIVE** ([pilot#4864](https://github.com/qf-studio/pilot/issues/4864)/PR#4865): `pilot_build_info` metric · real /health version · doctor 3-way check; board `ver` repointed at the metric 08-13 (`(disk!)` suffix = daemon unreachable, disk fallback). **`platform_breaker` ENABLED in production since that restart** (config: `orchestrator.autopilot.platform_breaker.enabled: true` — NOT top-level `autopilot:`, which binds to nothing; backup `config.yaml.bak-20260808-breaker`; armed-state startup log = #4814). **Daemon runs on AWS** (`i-0e0c1ca34e7b561f9`, TASK-409; ops via `pilot-aws` skill; NO local daemon; binary `/var/lib/pilot/bin/pilot`, rollback at `pilot.prev`) — **approvals OFF since 07-20** (auto-merge on green CI; size-floor/scope-drift escalations park `awaiting_approval`, asks route channel-first per #4810, live since the rebuild) — **GH-4391 rate-budget client LIVE** | full status in `.agent/system/FEATURE-MATRIX.md`

**PRIORITY (founder directive 2026-07-26 — supersedes 07-17):** **SaaS/platform UNPARKED — TASK-405 is active work again.** The 07-17 ordering (pointer delivery → pilot reliability → SaaS parked) held while the dispatch-reliability chain was open; that chain closed with v2.246.0 on 07-25. Pointer and pilot reliability remain live tracks but no longer gate S-milestone dispatch. Memory: `founder-priority-pointer-first-saas-parked` (superseded).

**Recent (Aug 11 – Aug 24 2026; detail lives in `system/saas-roadmap.md`, `system/approval-architecture-roadmap.md`, `tasks/archive/`, and git log — do not re-grow this block, replace it):**
- **08-24 (day, ~22 PRs merged+reviewed): SECOND EXTERNAL CONTRIBUTOR (`d3rowy`) — 4/4 reports verified+delivered · 2 epics · 8 review-driven fixes · 2 incidents · backlog drained to one founder-blocked issue.** Vetted (blank profile ≠ scam — all 4 claims line-exact; zero links/attachments): #5149 approval-auth + #5150/#5152 spec-guard re-authored in-house → epics **#5153** (11 legs: Telegram+Slack `isAuthorizedApprover` refuse-before-mutate, allowlist wiring, refusal UX, tests) + **#5154** (spec-guard contract fix — fail-open was DEAD at the caller · docs) — all 14 PRs APPROVE; **#5151 fixed by d3rowy's own merged code** (sdk#137 → **studio-sdk v0.38.0** cut manually + pilot PR#5207; note: issues-search returns PRs — sdk#137 sat unreviewed a day). Review-driven follow-ups all shipped same day: #5185 (600s -race CI timeouts → 20m budget) · #5189 (Socket-Mode ephemeral refusal; wedged on a cross-repo backtick path → cancel+label-cycle) · #5190/#5197 (docs precision + formatCompact B tier) · #5191/#5198 (extraFixesKeyword prose-promotion tightened ×2) · **#5193 (base-presence gate: body re-read per tick + bounded holds — wedge class self-healing post-v2.269.0)** · #5192/TASK-484 dashboard cards (risk-reviewed spec w/ grom truncation budgets; all guardrails held) · #5203 **GitLab docs-deploy recurrence #4**: #5134's own cleanup-registry job shipped invalid YAML (colon-space→mapping; every pipeline 08-22→08-24 zero-jobs-failed, site pinned 2.266.0, GitHub green) + stale Docker Hub creds on runner host — hot-fixed pilot-docs, fresh tag deployed 2.268.0, source fix + CI guard merged (SOP addendum: GraphQL failureReason first) · #5087/#5062/#5063 backlog cleared (config-verify off hot path — proven live same day; mock-gate QualityChecker; git-config-watch tripwire for the core.bare writer, + 4th-occurrence datapoint & SIGTERM negative). 3 phantom autopilot-fix issues closed (#5168/#5175/#5183 — GH-4526 discrimination still the durable gap). Only open: **#5008 (founder scoping)**. Markers: `2026-08-24_d3rowy-triage-four-issues.md`.
- **08-24 (morning): PR#5121 maintainer takeover → MERGED, #4888 closed — topic-threading arc (#4887→#4888→#5130) fully shipped.** lkshrk idle 2 days → founder-approved takeover: rebase, sdkshim `ThreadID` fix, bridge+`thread_ts` wire tests; 7 gates green. Ops learnings in marker (worktree VCS-stamping; fork workflow-file pushes need SSH).
- **08-23 (PM): tripwire root-caused + durable fix shipped; queue drained clean.** Recurring critical breaker alerts 08-17→08-23 traced to ONE stale untracked `q-<epoch>.md` query scratch in `.agent/tasks/` tripping `finish_tripwire_root_clean` every finish-sweep (NOT the self-review-in-root pitfall) — removed on box; durable fix #5145 → **wedged itself** (backtick example paths → base-presence hold; paths cached on execution row, body-edit can't clear → close+refile is the recovery, mem-175) → **#5147 → PR#5148 merged** (anchored gitignore + regression test). Same day: morning docs/CI queue (GH-5139/5137/5136/5138 → PR#5140–5143) all merged; #5130 voice/photo topic → PR#5146 merged. Knowledge writes mem-171–175. Marker: `2026-08-23_pm-queue-drained-tripwire-knowledge.md`.
- **08-22→23 (AM): `--gitlab` shipped same-day · docs drift audit · GitLab space incident #3 · glob wedge + OAuth outage both root-caused.** External user report → #5122→PR#5123 (flag never existed, docs promised it since 07-03) in v2.266.0. Audit found the class systemic: 4 fabricated command sections (~35 phantom flags) + phantom config keys → #5131/#5141/#5143 all merged. GitLab registry+prod-disk incident #3 resolved (build cache 224GB = true killer; cleanup policy enabled; `docker builder prune` leg PR#5142); docs site had silently served July 3 build 7 weeks. Base-presence glob wedge fixed (#5133→PR#5135, in v2.267.0). OAuth expiry wedged the daemon (401 only visible in stream recordings; all queued tasks burned to repick cap) → 1-yr token + two-part re-arm. Markers: `2026-08-23_gitlab-flag-drift-audit-oauth-outage.md`.
- **08-19 (evening/night): docs read chain COMPLETE + founder-account E2E demo PASS + findings loop closed same evening.** (1) **TASK-466 read leg DONE**: ui#108 → **PR#109 merged + reviewed APPROVE-w-notes** (all 5 checklist items; `html:false` escape-tested; verdict as comment — GitHub blocks formal approve on own-account PRs); fast-follows ui#110 → PR#111 merged. (2) **Founder-account product-path E2E PASS**: credentials via console UI → Provision button → fresh box ~100s → connection repos synced product-path (PR#178 proven live) → ship-test-js#7 picked <60s → `average()` → **PR#8 auto-merged, issue closed** ($0.12 window). Local rig unblocked mid-run: console container flipped to **ssm secrets driver + AWS creds** (untracked override; postgres/SSM split-brain wedged credentials + broke `/docs` proxy auth). (3) **4 findings filed, 3 Pilot-fixed same evening**: console#189 (credential convergence + store-presence honesty) → PR#192 · console#190 (default-branch fallback + WARN spam) → PR#191 · ui#112 (v-applied label lies vs drift badge) → PR#113 · **pilot#5008 UNLABELED — founder scoping (hosted retry path; ship-test-js#6 kept open as repro)**. **Post-merge reviews DONE 08-20** (verdicts on the PRs): ui PR#111 APPROVE · console PR#191 APPROVE-w-notes (root cause genuine — startReconciler's hardcoded-SSM credReader vs driver-aware handlers; watch item: one repo's fallback persisted under ssm posture live) · console PR#192 APPROVE-w-notes (strongest of the batch; ui consumption gap → **ui#114** filed) · **ui PR#113 APPROVE-w-DEFECTS — FALSE DELIVERY, fourth TASK-460 incident**: trusted the ui types.ts docblock over the server DTO (`specVersion` = TARGET gen, handlers.go:141) and the incident evidence; only the v0 edge shipped, tests now enshrine the wrong semantics. Supersession chain: **console#193 → PR#194 merged + reviewed APPROVE** (raw-body-pinned `appliedSpecVersion`; doc-rot corrected at source) · **ui#114 → PR#116 merged + reviewed APPROVE-w-notes** (explicit-false-only warning semantics; notes: double-fetch, banner staleness, ConnectionDetailView badge residual) · **ui#115 → PR#117 merged + reviewed APPROVE 08-20** — supersession genuinely complete: renders from `appliedSpecVersion`, incident fixture honest (target 2/applied 1 → "v1 applied"), doc-rot corrected at all four sites (note: field typed required — pre-#193 consoles render "none applied" during deploy skew). **GH-112 chain CLOSED.** TASK-460 evidence row added (new leg candidate: doc-vs-wire verification). Local stack refreshed 08-20 ~10:30Z for operator incident-fix verify (console container rebuilt on main incl. PR#191/#192/#194 · ui checkout at PR#117; preview seeds: `VITE_MOCK_SEED=wiped_secret`). Demo verdict recorded: controlled-demo READY; launch gated on payment processor · staging deploy · pen test · branch protection. Marker: `2026-08-19_e2e-demo-pass-findings.md`.
- **08-19 (morning→afternoon, condensed): three review/dispatch waves in one day.** Morning: post-merge batch ×7 (verdicts on PRs; **#4978 Linear UUID = functional no-op caught** → #4985; #4982 narrative corrected — defect #2 never existed; KAN-6 close-leg gap → #4987 + sdk#121). Midday: whole batch executed + reviewed ×7; **FALSE DELIVERY caught: PR#4992's Jira done leg = dead code** (pitfall `merged-feature-dead-callback-not-bridged-onprcreated`, third TASK-460 incident) → #4999 + sdk#123/#124; TASK-479/481 closed+archived. Afternoon: reachability chain cleared same-day, all 5 reviewed (**#5000 = Pilot's own spawner fix** · PR#5001 ADFText · sdk PR#125 OnPRCreated all-adapters (+sdk#127 filed) · sdk PR#126 statusCategory · PR#5002 idempotency) — **KAN-6 code-complete**; trains cut clean 14:00Z (pilot v2.263.0 · sdk v0.35.2) → pin bump #5006→PR#5007 merged+reviewed. Detail: `2026-08-19_postmerge-review-batch.md` + git log.
- **08-18 (evening): S3 EXIT TEST PASSED + 2 incidents found/fixed + full dispatch batch shipped + v2.262.0.** (1) **S3 exit met** (founder's local-first definition): 3 fresh tenants signup→credentials→provision→box→PR→auto-merge concurrently (fleet VPC, AMI `ami-01ed3bb9600200ce4`, ~$0.06/exec); fixture repos `pilot-s3exit-t1/2/3` kept; estate fully torn down incl. 25-day canary + demo box, `/tenants/*` SSM empty. Gaps → **console#177** (connection repos never reach config spec; `consolectl add-repo` was the bridge) + **pilot#4961** (poller labels in-progress pre-claim; dropped claim wedges issue — hit live ×2). (2) **Lint-cache incident**: golangci action cache self-poisons (green run saves → next restore = phantom SA5011s in files not in the diff; second wave hit DIFFERENT files), killed 4 green PRs, spawned 6 garbage fix-issues → `skip-cache: true` direct-to-main, all spawns closed, flake-recovery recipe extended (branch-ref restore from headRefOid). (3) **Train defect**: boot recovery cut v2.261.0 at 13:45Z — before its own 14:00Z tick — then the post-tick restart never recovered → **#4982** filed; operator cut **v2.262.0** (installed on box). (4) **lkshrk window closed → TASK-479/480/481 + D5 dispatched as #4963–#4968 — ALL merged same evening** (PR#4975/#4977–#4981; #4975 reviewed APPROVE-w-notes: spec's G5–G7 `-p`-assumption was wrong, implementation corrected it). Note: first gens of #4965/#4966 hit stale-diff-base intent-judge vetoes (08-17 #4922 class) — recurred, watch it. Markers: `2026-08-18_s3-exit-three-tenant-pass.md` (evening) · `2026-08-18_jira-cloud-e2e-day-close.md` (day).
- **08-18 (day): one external Jira bug → 7 defects fixed across 3 repos + Jira Cloud e2e LIVE-VALIDATED.** Morning: founder redefined S3 (memory `no-stripe-local-first-s3-testing` — no Stripe/Montenegro, no domain, local-first; infra PR#25 deploy deferred); local console stack refreshed (:8090 + :5173). #4917 (external, MattiaFailla) root-caused → #4929 epic — which **mis-decomposed into 8 children** (bare `no-decompose` token unmatched; pitfall memory updated ×2) surfacing a defect zoo, ALL fixed same day by Pilot: #4938 bare-token phrase (PR#4947) · #4944 closed-child-fails-run (PR#4949) · #4946 flaky `TestNewController_LogsResolvedReleasePolicy` killed green PR#4943 → flake recovery recipe (restore branch ref → reopen → rerun → merge) · #4927 PR-CI lint blind spot (PR#4928, <1h). Then **live-fire validation against a real Jira Cloud site falsified the fix**: the poller runs the **SDK client** (`cmd/pilot/poller_jira.go` → studio-sdk), not `internal/adapters/jira` — reporter's exact error reproduced on the patched binary (pitfall `jira-two-parallel-clients-poller-is-sdk`). Port sdk#119 → PR#120 Pilot-fixed in ~40min; sdk train then cut **v0.34.2 BELOW existing v0.35.0** (releaser baseline ignores tags it didn't create → #4953 filed+queued; corrective founder tag **v0.35.1**) → pin bump #4952 → PR#4954 → box rebuild → **JIRA-KAN-6 picked up, parsed (rich ADF), executed 56s/$0.21 → PR#4955** — full tracker-to-PR chain proven; every code change in it Pilot-authored. Also: #4265 closed (stale), #4932-class supersede gate = `pilot-superseded` label, root repo survived a `core.bare=true` flip from a SIGTERM-killed pre-push gate. Marker: `2026-08-18_jira-cloud-e2e-day-close.md`.
- **Earlier (compressed):** 08-17 evening recovery sweep + handleMerged-dead-code discovery → eval metrics live (#4919/#4922 chain, v2.260.1; memory `handlemerged-shadowed-dead-by-external-merge-detector`) · 08-17 late FIRST EXTERNAL CONTRIBUTOR lkshrk: 19 issues + 15 PRs reviewed, 10 merged same day, spawned TASK-479/480/481 (all since shipped; marker `2026-08-17_lkshrk-pr-batch-review.md`) · 08-16→17 TASK-405 un-patched ship test ✅ + estate (FleetVpc, golden AMI, GH-4872 chain, design-conformance program — marker `2026-08-17_lkshrk-pr-batch-review.md` + git log) · 08-15 TASK-478 overnight build-out (six size-held PRs, morning playbook) · 08-14 rail design→12/17 legs merged same day (+pilot#4869) · 08-12 PR#4846 incident closed + 3-generation same-day fix cascade (memory `incidents-always-first`) · 08-11 design sprint ×4 + C16/C17 shipped e2e · 08-06→08-08 GH-Actions outage → recovery → hardening wave (TASK-458 breaker enabled in prod; detail `system/approval-architecture-roadmap.md`) · 08-04/05 TASK-441 contract hardening + first unattended self-upgrade (v2.253.0) + S4 waves 2–4 + token incident resolved · 08-01/08-03 S4 wave 1 + Golden AMI v2 merged (operator bake pending) · 07-31 first autonomous train + AWS cost audit (`cdk deploy` pending) · 07-30 spec-guard epic + real-stack-verify SOP · 07-29 S3 backend 10/10 · 07-27/28 S2 EXIT MET · 07-26 SaaS UNPARKED · 07-20 approvals off · 07-16 S6-lite AWS cutover (TASK-409). Detail: git log + `tasks/archive/`.

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
| **P1** | **Pilot SaaS platform** ([TASK-405](tasks/TASK-405-pilot-saas-platform.md)) | S3 **built**, exit gated on founder inputs (Stripe test keys/price/webhook secret · console + sending domains · ACM DNS) → operator staging deploy → S3 exit test. S4 board: **waves 1+2 MERGED** (C1/C2/C7/C3/C4 + kanban UI — console PRs #87/#88/#92/#93/#94; TASK-432–435, 438–440 done). **Wave 3 rolling 08-05, 3 of 6 legs merged same-day**: console#95 C5 (PR#99) + #96 C6 (PR#100) + ui#44 un-stub (PR#46) all **MERGED** (TASK-442/443/446 archived) → running: #97 (C8 dispatch verb, [TASK-444](tasks/TASK-444-console-c8-dispatch-verb.md)) → #98 (C9 metrics, [TASK-445](tasks/TASK-445-console-c9-sync-metrics.md)) · ui#45 (status-map editor, [TASK-447](tasks/TASK-447-ui-statusmap-editor.md)). **Metrics 30d-windowing dispatched**: pilot#4735 ([TASK-448](tasks/TASK-448-metrics-30d-window.md)) — headline cost/success move to a rolling 30-day window (operator decision 08-05; lifetime $0.80/79.9% were era-blended; honest 30d: ~$3.66/delivered issue, 81% delivery). System docs: [`saas-architecture.md`](system/saas-architecture.md) · [`saas-kanban-sync-design.md`](system/saas-kanban-sync-design.md) · [`saas-fleet-design.md`](system/saas-fleet-design.md). Roadmap: [`saas-roadmap.md`](system/saas-roadmap.md) v9.9. |
| **P1** | **Console rail implementation** ([TASK-478](tasks/TASK-478-console-rail-implementation.md)) | 11 approved designs → shipped surfaces. **Build-out COMPLETE 08-15 (overnight run): all 16 autopilotable legs executed + reviewed.** Blocked on founder morning sequence (approve PR#67→72→74→76 · close #73/#77 to arm retries · label ui#78 after) → then GH-69/GH-75 retries re-land · **real-stack verify batch UI-2..12** (SOP; blocked on GH-75 re-land) · CON-5 billing portal (founder Stripe gate) · copy pass ($299 · PR#72 "Includes" line · support@ mailto). Daemon-side follow-ups FILED 2026-08-20: [pilot#5027](https://github.com/qf-studio/pilot/issues/5027) merge-time ancestry check (stacked-superset guard, extends GH-4872) + [pilot#5028](https://github.com/qf-studio/pilot/issues/5028) base-presence check before claim (see pitfall `sequential-gates-on-execution-not-merge-fastfollow-misbase`; 4 family incidents in 6 days, nav-research verdict: both fixes needed). |
| **P1** | **Throughput acceleration** ([TASK-393](tasks/TASK-393-throughput-acceleration.md)) | Phase 1 (instrumentation) ✅ shipped 07-09. **M3 baseline window closed ~07-20 — histograms never harvested; phases 2–5 remain gated on that analysis.** Remaining: (2) execution lanes on `Complexity`, (3) N-concurrent per repo (`ProjectWorker` pool — note this is also the sole serialization point, see mem-101/102), (4) SHA-keyed repo primer, (5) risk-score trust tiers. Roadmap: [`throughput-roadmap.md`](system/throughput-roadmap.md) (M0–M8, D1–D6). |
| **P1** | **Execution lifecycle chokepoint** ([TASK-404](tasks/TASK-404-execution-lifecycle-chokepoint.md)) | B1 shipped (#4243 — `ExecutionLifecycle` Begin/Transition/Finish + typed status vocabulary). Remaining legs open; #4678's cancel verb lands on this seam. |
| P1 | Wire `linear.webhook_public_key` YAML → `gateway.Config.LinearWebhookPublicKey` | TASK-295 follow-up. Ed25519 webhook verification has shipped since v2.149.4 but is gated behind a config field with **no decode path in `cmd/pilot/main.go`** — the security improvement is inert. Small (≤30 LOC). |
| P1 | Fix `shouldTriggerRelease()` | Doesn't check `ResolvedEnv().Release` — only top-level config. |
| P1 | Web dashboard polish | React UI functional but needs a design pass. |
| **P1** | **Jira merge-side close: reachability chain** ([#4999](https://github.com/qf-studio/pilot/issues/4999) + sdk#123/#124 + sdk PR#122 tag/pin) | PR#4992 merged the done leg but it's **dead code in production** (TASK-460 class — pitfall `merged-feature-dead-callback-not-bridged-onprcreated`): sdk jira adapter drops `OnPRCreated`, reconciler adopts only `pilot/GH-*`, external-merge path (how KAN-6's PR actually merged) never calls it, and pinned sdk v0.35.1 does English-name transitions + comment-first-early-return. Chain to close: sdk#123 (bridge OnPRCreated, all tracker adapters) · sdk#124 (statusCategory transitions + decouple from comment failure) · #4999 (external-merge leg + idempotency) · sdk v0.35.2 tag + pin bump (ADF comment fix #122 is merged-untagged). KAN-6 acceptance (card leaves «К выполнению») transfers to #4999. |
| P2 | Delivery-evidence audit — false-success class ([TASK-460](tasks/TASK-460-delivery-evidence-false-success.md)) | Split from TASK-459 by founder scope call 08-08: green CI is not proof the requested change shipped (`mem-151`: scaffold-only PR merged green, parent auto-closed, zero requirements delivered). Planned, NOT dispatched; TASK-459 Phase 4's inventory hook feeds it the success-side site rows. Candidate legs: diff-surface check · ACs fail-when-unwired · epic-collapse guard. |
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
