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

**Current Version:** box runs **`Pilot v2.248.0-8-g51743455`** (main build 07-28 18:39Z — deliberately ahead of the last tag so the GH-4595 park stack runs before the next train; graceful restart 18:44Z verified queue-0-before-stop / 1 proc / banner / metrics; next 14:00Z train tags + self-upgrades onto the released version; rollback `/var/lib/pilot/bin/pilot.prev` if a build misbehaves) — **daemon runs on AWS** (`i-0e0c1ca34e7b561f9`, TASK-409; ops via `pilot-aws` skill; NO local daemon; binary at `/var/lib/pilot/bin/pilot`, `/usr/local/bin/pilot` is a symlink) — **⚡ approvals OFF since 07-20 21:41Z**: auto-merge on green CI, proven unattended 07-21 (pointer #144/#145, pilot #4497) — **GH-4391 rate-budget client LIVE** (15% poller floor, ETag conditional scans, 72h/cursor/3s-stagger startup; metric `pilot_rate_limit_floor_engaged_total`) | full status in `.agent/system/FEATURE-MATRIX.md`

**PRIORITY (founder directive 2026-07-26 — supersedes 07-17):** **SaaS/platform UNPARKED — TASK-405 is active work again.** The 07-17 ordering (pointer delivery → pilot reliability → SaaS parked) held while the dispatch-reliability chain was open; that chain closed with v2.246.0 on 07-25. Pointer and pilot reliability remain live tracks but no longer gate S-milestone dispatch. Memory: `founder-priority-pointer-first-saas-parked` (superseded).

**Recent (July 25–30 2026):**
- **07-30 (mid-day): GH-25/26 takeover-race root-caused + open-backlog triage (10 open → 8 executable).** The 07-29 console-ui "stalled" rows never failed: after the parent lost dispatch claims to the poller, the GH-4536/TASK-419 takeover **force-stalled the queued child rows as bookkeeping**, then (A) the epic waiter's all-rows terminal scan (`findTerminalChildExecution`, built for GH-4381) misread the superseded row as a child failure → epic failed → re-picked and re-implemented #26's scope (TASK-401 class, new vector), and (B) takeover executions are **never finalized** — the loop-local `subExecID` is empty after `ErrClaimLost`, so all five `finalizeSubIssueExecution` sites no-op; GH-25's takeover shipped PR#28 yet sat `running` until the 2h orphan sweep. Mechanism has ZERO test coverage. Dispatched **#4619** (waiter newest-running guard + re-derive `subExecID` seam) + **#4620** (autopilot `StageFailed` finalizes the source row — no failed-side self-heal exists). Also **#4617**: TUI "queue depth" card renders `len(m.tasks)` (every task tracked since daemon start) while the ledger/gauge said 0. Backlog triage: 5 issues were spec-gate-parked on the **header-allowlist formality** (needs `Context/Acceptance/Implementation/...` H2s) — re-spec'd + unblocked #4619/#4609/#4611/#4594/#4580/#4498/#4390 (#4594 authored from the 07-28 marker's evidence; #4390 re-scoped to code-side ACs past its preflight rejection; #4580 half-shipped via #4616, darwin half remains), #4581 retitled + labeled `pilot` (was never dispatchable). **#4265 CLOSED — TASK-407 proof met** (`duplicate-pr` green in every run since 07-24; residual `child-count` fails watched via #4619); TASK-407 archived. **PM/afternoon: 10 PRs merged, v2.250.0 released + manually cut over.** Takeover-race fix #4626 + StageFailed heal + 5 more merged pre-train; **v2.250.0 tagged 14:07Z but self-upgrade drain-blocked** (6 real actives — daemon decomposed spec-guard #4624 into 6 children — + 8 registry zombies whose fix sat inside the release) → operator cutover 15:05Z (clean rebuild after clearing GH-4594-class `M handlers.go` residue). **Spec-guard #4624 fully shipped**: #4638 fresh-body · #4640 confirm-before-block · #4642 fingerprinted marker · #4641 escalation comment (operator-rebased, same-file epic conflict; #4639/GH-4633 superseded) — the #4498 strike-loop class is structurally dead. SDK twin studio-sdk#105→PR#106 merged manually (checks green, watcher blind — GH-4384). **#4643 filed**: scope-release carriers loop forever on post-merge-CI timeout in workflow-less repos, now holding today's train scope. Next self-upgrade is the first with the drain fix live.
- **07-28: S2 EXIT MET (AM) + GH-4595 park stack shipped and live (PM).** AM (detail: marker `2026-07-28_s2-exit-met-billing-fixed.md` + roadmap v9.0): canary 0 open issues, 4 merged hosted PRs (#111 auto, #113 gate-escalation approved), GH org Actions billing wall fixed (QuantFlow DOO, $25/mo budget — the $0 limit had blocked ALL private-repo CI), defects #4591/#4594/#4595 filed. PM: **#4595 decomposed 6 ways → 5 PRs merged same day** — park-instead-of-hard-fail (#4602), parked-state persistence (#4604), own-issue title gate (#4605), tests (#4606/#4607), and **#4603 via manual-rebase saga**: auto-rebase 422 → `escalateAndHold` (`needs-manual-rebase`), then TWO rebase rounds — round 2 hit the TASK-401 sibling-overlap class (main had shipped GH-4597's own scope inline incl. a duplicate `labelParkedAwaitingApproval` const); resolved by unification (kept GH-4600's issue-label convention + `Parked` guard, grafted GH-4597's net-new: `approvalMisconfigKey` naming the exact unset YAML key + `{PR, reason}` cross-cycle alert dedup). Manual merge → daemon's external-merge scan self-cleaned the frozen `failed` row (18:31Z, `scope=schedule`) — proven recovery path for held PRs; a rebased branch alone never re-arms autopilot (file a re-adopt defect if that starts costing time). Side-find: stray **`core.bare=true` in shared `.git/config`** made `git status` fatal repo-wide → go VCS stamping killed 4/6 pre-push-gate checks with misleading errors (mem-161; local gate noise `TestResolveMemoryDBPath` + `newTestStartCmd` remain #4580). Box rebuilt from main tip + restarted 18:44Z so the whole stack runs before the next train. gh-4587/96/97/98/99 task docs archived.
- **07-27 PM: graph-drift prevention program shipped (founder-approved "do all") — the drift gate is no longer decorative on pilot.** Trigger: main CI red ~55min across 4 pushes — `88fad61c` added `learning_stale_ledger_misdiagnosis.md` without a graph node (interactive-session class, distinct from the executor-deletion class) → fixed `0c2392eb`, all 5 jobs green. Forensics on "why do we always fix graphs": **two actors** — (1) Pilot executors DELETING indexed memories (5+ incidents: #4484 · #4500/#4506 · #4534/#4535 · #4551), (2) interactive sessions ADDING unindexed files; **three layered holes** — memory guards diff a stale **local** base (same family as #4568, which only fixed the no-commits legs), the drift gate couldn't block a merge (`required_checks: [test, lint]`), and graph.json is a two-place invariant with no atomic writer. **Fixes**: (a) per-project `ci_checks.required_checks` for pilot = **[test, lint, Knowledge Graph Drift Gate, Check Secret Patterns, Box Scripts]** via GH-4478 plumbing (`main.go:1773/1834`) — box live 12:46Z restart, verified in startup log, backup `config.yaml.bak-drift-gate-required`; other repos keep `[test, lint]`, pointer keeps auto-discovery (avoids the `pointer#108` forever-pending class — global widening was correctly rejected). Closes the 07-26 🔴 CI-gate item and the "occurrence #5/#6" decision. (b) **TASK-424→#4573→PR#4577 merged 13:25Z**: all three memory-guard legs (`Strip`/`Restore`/`Enforce`) go origin-relative. (c) **TASK-425→#4574→PR#4576 merged 13:18Z**: `check-graph.py` in pre-commit + pre-push gate + `--fix` auto-index from frontmatter (serialization spec commented: escaped-ASCII canonical, idempotent — `ensure_ascii=False` ping-pongs with the session hook; pitfall recipe corrected). (d) **TASK-426→#4575→PR#4578 merged 13:48Z**: 4 pre-existing lint findings (errcheck ×3 `scope_schedule_test.go`, dead `renderPanel` chain) failed the local pre-push gate vs CI's pinned v2.8.0 — fixed; but the first post-merge gate run surfaced **2 more local-only findings** (darwin-symlinked `t.TempDir()` breaks `TestResolveMemoryDBPath` from #4486; the integration orphan-command scan false-positives on the `newTestStartCmd` test helper) → **#4580** (pilot, no-decompose) — `--no-verify` retires when it merges. Silver lining: the gate correctly rejected a push from a 4-commit-stale local main first (today's theme, working as designed). All three ran clean under the new 5-gate `required_checks` set, TASK-424/425/426 archived. Also merged: **#4572** (GH-4570 — "PR closed externally" decided from ONE unverified read on a fresh PR, then acts destructively) and **#4571** (GH-4569 — stale-ledger staleness banner + `LEDGER-ARCHIVED` sentinel); morning queue fully drained. **Release state**: **v2.247.0 released 14:20Z — the train self-shipped** (tag + goreleaser fired organically after #4578 merged; carries #4559/#4563/#4565/#4567/#4568/#4571/#4572/#4576/#4577/#4578 + #4579 version sync); box rebuilt from the tag + graceful restart 14:40Z, all SOP checks green. **Repick storm recurred** on GH-4575 mid-execution (WARN 13:26Z `claim_lost_drops=5`, poller re-picks every cycle on "status labels removed"/"failed without PR" mislabels) — TASK-407 claims layer dropped every duplicate, zero double work, but the cleanup/defect-filing from the AM entry is still open. Session note: the Navigator hook auto-indexes new task docs into `graph.json` AFTER the commit — a dirty graph.json with only task nodes + `last_updated` is the hook working, sweep it into a follow-up commit.
- **07-27 AM: epic-failure cluster killed in one day (4 fixes shipped) + stale-ledger hardening.** Trigger: auth-service epic GH-431 — all 4 child PRs (#471/#472/#474/#475) merged, yet the parent died `infra` ("epic PR creation failed: No commits between main and pilot/GH-431", 2nd hit that day after GH-435) and children GH-468/469/470 sat zombie-`running` forever (meter 100%, status running; orphan evictor was memory-only). Shipped same-day, all issue→merged <1h: **#4560→PR#4563** (epic abort sweeps children terminal via `ExecutionLifecycle`) · **#4565** (evictor heals the DB row, not just its tracker entry) · **#4564→PR#4567** (sweep finalizes by ACTUAL outcome — `pr_created` → `completed`; blanket-stall would have painted shipped work failed) · **#4566→PR#4568 — the root cause**: no-commits guards ran `rev-list local-main..HEAD` while every worktree is cut from freshly-fetched `origin/main` and `syncMainBranch` never runs on the TASK-402 independent-sibling path — stale local ref → false nonzero → `CreatePR` on a truly-empty umbrella branch → GraphQL "No commits" → `infra`. Fix = origin-relative guards at all 3 sites + benign-No-commits backstop; likely shrinks the GH-916 "~10% of failures" class (revisit after live proof). RCA via navigator-research: umbrella branch is empty BY DESIGN post-TASK-401; three independent epic-close paths never needed the umbrella PR (TASK-423, archived). Box surgery: GH-468/469/470 rows → `completed` (exact-id, audit notes), stale claims cleared. Also: **#4558→PR#4559** — the GH-4547/#4553 "superseded" close had dropped one slice (`Validate()` accepted built-in `default_environment` that `ResolvedEnv()` couldn't resolve → silently ran stage; GH-4544 class #5, repro-test-proven, TASK-416 archived). **Stale-ledger misdiagnosis incident**: another session read the laptop's frozen pre-cutover `~/.pilot/data/pilot.db` (dead since 07-16) and confidently misdiagnosed the healthy 468/469/470 as "failed" with an invented mechanism — hardening executed: DB renamed `pilot.db.pre-s6-cutover-archive` + `README-MOVED.md`, global `~/.claude/CLAUDE.md` pointer, pilot-aws hard rule 6 ("name your ledger, verify freshness"), learning `stale-ledger-misdiagnosis`, **#4569** filed (staleness banner + `LEDGER-ARCHIVED` sentinel + always print resolved DB path). **#4570** filed: autopilot declared a 29s-old OPEN PR "closed externally" from ONE unverified read (pointer #196; GitHub eventual-consistency, same window as #195's 404 `not_found_count`) then acted destructively — branch delete attempt, `pilot-retry-ready` churn, tracking drop; only a duplicate registration saved the PR. **Still open**: canary config push (console#55/PR#56 built+tested locally, `consolectl` ready — the tenant still runs 2.245.0 on the ephemeral hot-fix; a config push before the new build lands reverts the merge leg) · canary repick storm (GH-99/100/104 wedged on dirty worktree ` M version.go`, 108+ claim-lost drops — cleanup + defect unfiled) · CI gate decision (occurrence #5/#6).
- **07-26: SaaS UNPARKED + the hosted merge leg fixed — canary merged its first PRs ever; v2.246.1 released.** The two-day S2 exit blocker was **not** approvals (v8.7's hypothesis: refuted, the instance reads `require_approval: false`) — **autopilot was never enabled on the tenant daemon**, three independent layers: (a) `pilot-console/internal/fleet/configrender.go:109` `writeAutopilotBlock` emitted `autopilot:` at **YAML top level**, where nothing binds it — `config.go:41` `Config` has no top-level `Autopilot` field, the only binding is `orchestrator.autopilot` (`config.go:153`), and non-strict decode discards the block **silently**; (b) it never emitted `enabled: true`; (c) `main.go:422-426` is the sole path forcing `Autopilot.Enabled = true` and runs only under `--env`, which the generated systemd unit never passes. All four controller sites (`main.go:1687/1768/1828/2349`) are guarded on `Enabled` and skipped. **The tell: no `autopilot_pr_state` table in the tenant ledger** — query `.tables` before reasoning about empty rows (pitfall `top-level-autopilot-yaml-binds-to-nothing`, mem-160). Operator hot-fix (nest + `enabled: true`) + restart → **PRs #103/#105 MERGED 15:56Z**, issues closed, branches deleted, ~3 min end-to-end. **S2 exit: "≥2 fresh merged PRs on a hosted instance" ✅**; "all `pilot` issues closed" still needs canary #104/#99/#100. Tenant `ANTHROPIC_API_KEY` provisioned from the pointer org (tenant path had *only* `GITHUB_TOKEN`+`PILOT_GATEWAY_TOKEN` — the gap that caused the 07-24 OAuth copy that stranded the box); ⚠️ $107.88, auto-reload OFF, shared with pointer prod. Permanent fixes shipped: **console#55→PR#56** (renderer) and **pilot#4544** → 6 children, 5 merged (#4551/#4554/#4555/#4556/#4552), #4553 **closed as superseded** (it would have regressed the unknown-env path from a hard error back to a silent fall-through). #4552/#4553 conflicted because a one-file fix was decomposed 6 ways onto the same file — **a one-file fix should carry `no-decompose`**. Also: 07-16 canary break explained separately (box ledger: #87/#90/#94/#98 `failed / CI timeout after 30m0s` = the **GH-4384** class, distinct from the hosted bug). Released **v2.246.1** (manual tag, patch bump — no `feat:` since v2.246.0) → box self-upgraded cleanly, incl. the "Creating backup…" step that failed on 07-19. **🔴 Open, 6th occurrence today**: PR #4551 auto-merged with the drift gate RED and deleted `required-checks-allowlist-makes-other-gates-decorative.md` — *the memory describing that exact mechanism* (restored, `check-graph.py` green). The real fix is **per-project** `ci_checks.required_checks` (GH-4478, live since v2.244.0), **not** the global widening proposed on 07-25 — widening globally would hit the `pointer#108` forever-pending class on every other repo.
- **Earlier July 24-25 (compressed; detail in `tasks/archive/` TASK-418/419/420/421 + 07-26 entry):** dispatch-reliability chain — 4 defects found/fixed/released in v2.246.0 (CI infra-failure misclassification, epic claim-loss self-deadlock, dashboard panel divergence, repick cap counting non-failures) + the 2h box-credential outage (OAuth session copied to canary, empty-stderr signature). Hosted canary tenant provisioned in 99s (fleet VPC, `consolectl`), 7-layer defect cascade same-day, S2 merge-leg blocker discovered → root-caused 07-26.
- **Earlier July 19-21 (compressed; detail in `git log` + `tasks/archive/`):** approvals flipped OFF 07-20 21:41Z (`stage.require_approval: false`, auto-merge on green CI), made safe by #4487 (CI re-validated at the merge chokepoint) - **GH-4391 rate-budget client** shipped after 4 days (#4495, founder-reviewed past the size guard) - **v2.244.0 cut with zero human touch** (14:00 tick 403d, #4485 retry succeeded, self-upgrade hot-restart) - pointer revealed as **board-sourced** (`source_enabled: true` REPLACES label discovery; pitfall `board-sourced-repo-ignores-labeled-issues`, fix #4488/#4489) - first unattended auto-merges under approvals-off (pointer #144/#145) - nightly S3 ledger backups live (#4466, restore SOP `sops/operations/ledger-restore-from-s3.md`) - self-upgrade preflight #4470 after the root-owned `/usr/local/bin` silent failure - TASK-410 deletion root cause found+fixed (#4496 to #4497). **Still-open watch items**: CI-watcher timeout clock ticks through rate cooldowns; carriers 439/443/103/104 need push-main test workflows in auth-service/studio-sdk.
- **Pilot Cloud program (TASK-405) ACTIVE:** S0 ✅ · S1 ✅ (pilot-console / pilot-console-ui / CDK scaffolds) · S3 UI mock track ✅ (login→onboarding→connections→provision→status on mock adapter) · **S2 in flight** — B5 instances+events store, A4 SSM SecureString writer, **B6 provisioner SHIPPED** (console#22→PR#23, single PR; spec survived an 18-finding adversarial workflow review pre-dispatch — pattern worth repeating). Next: fleet reconciler ([TASK-415](tasks/TASK-415-console-fleet-reconciler.md) — spec drafted 07-22, adversarial review → dispatch) + B8 config push. Plan of record: `system/saas-roadmap.md` v7.
- **Reliability track COMPLETE + proven:** TASK-407 atomic dispatch-admission claim shipped (#4361/#4362/#4363 — `execution_claims` PK + ErrClaimLost at every entry point; kills the 10-incidents/12-days duplicate class, ~185 excess execs). First integration bug found same day: poller retry re-claims gen 0 → once-failed tasks blocked (#4372, workaround = delete stale claim row). Also: stall-watchdog background-task fix (#4364 — watchdog killed healthy sessions awaiting `go test -race`), adapterhealth deflake (#4365), evidence project-scoping (#4356). **Proof COMPLETE 2026-07-30**: `duplicate-pr` green in every scheduled run since 07-24 → #4265 closed, TASK-407 archived (residual `child-count` canary fails = the GH-4536 takeover race, watched via #4619).

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

- **Plan of record + live status**: [`system/saas-roadmap.md`](system/saas-roadmap.md) (v8.7) — S0 ✅ · S1 ✅ · H1–H12 ✅ · S3 UI mock ✅ · R-track ✅ · S6-lite ✅ · **S2 build ✅, S2 exit blocked on the canary merge leg** (hosted tenant executes and opens green PRs that never merge)
- **Program doc**: [`tasks/TASK-405-pilot-saas-platform.md`](tasks/TASK-405-pilot-saas-platform.md)
- **Design**: [`system/saas-architecture.md`](system/saas-architecture.md) · [`saas-kanban-sync-design.md`](system/saas-kanban-sync-design.md) · [`saas-fleet-design.md`](system/saas-fleet-design.md) · [`saas-asset-research.md`](system/saas-asset-research.md)
- **New repos** (created 2026-07-14, in `~/.pilot/config.yaml`): `qf-studio/pilot-console` (Go control plane) · `pilot-console-ui` (Vue3/Vite/Bun SPA) · `pilot-cloud-infra` (Go CDK) — each has its own `CLAUDE.md`
- **Latest handoff marker**: `.agent/.context-markers/2026-07-30_s3-punchlist-done-dashboard-live.md`
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

| Priority | Topic | Why |
|----------|-------|-----|
| ✅ | **Dispatch-reliability chain — 4 fixes, all shipped in v2.246.0 (07-24/25)** | One night's debugging produced a causal chain of four defects, each found while fixing the previous one. All specced, dispatched, merged, released: **[TASK-418](tasks/archive/TASK-418-ci-infra-failure-classification.md)** → #4534/#4535 — classify infra vs code CI failure from job logs, bounded `rerun-failed-jobs`, `failure_class` metrics (a GitHub 429/504 outage was closing correct PRs and spawning garbage fix issues). **[TASK-419](tasks/archive/TASK-419-epic-subissue-claim-loss-deadlock.md)** → #4538 — epic sub-issue claim-loss self-deadlock: on `ErrClaimLost` the epic polled unbounded for a `queued` child that only its own blocked worker could run; confirmed twice in the wild (GH-4531, and GH-436 hung 3h10m on auth-service). **[TASK-420](tasks/archive/TASK-420-dashboard-panel-truth-divergence.md)** → #4539 — dashboard panels disagreed about the same task in opposite directions (queue marked live work done, history kept dead work running). **[TASK-421](tasks/archive/TASK-421-repick-counter-counts-non-failures.md)** → #4541 — repick hard cap counted non-failures, so any task queued behind a >37min task was auto-blocked for waiting its turn. Unblocked #4526 (blocked 20h) which then shipped as #4542. **⚠️ One item NOT fixed**: `required_checks: [test, lint]` makes every other CI gate decorative — see pitfall `required-checks-allowlist-makes-other-gates-decorative` (founder config decision). |
| P1 | **Pilot SaaS platform ("Pilot Cloud")** ([TASK-405](tasks/TASK-405-pilot-saas-platform.md)) | **PLANNED 2026-07-13** — full architecture via judged 3-proposal design competition + 36-claim adversarial source verification (21✅/15⚠️/0❌). One EC2 per tenant (bind-once/terminate-on-unbind, STS-tag ABAC) + new `pilot-console` control plane + three-verb mixed-tracker kanban (Jira+Linear+GitHub on one board); BYO Anthropic key, **no model picker in v1**. System docs: [`system/saas-architecture.md`](system/saas-architecture.md) · [`saas-kanban-sync-design.md`](system/saas-kanban-sync-design.md) · [`saas-fleet-design.md`](system/saas-fleet-design.md) · [`saas-asset-research.md`](system/saas-asset-research.md). **Awaiting 6 founder decisions** (task doc) + engine-question bench experiment (arms A–D designed; arm A = TASK-27 v5-smoke already staged on `feat/aws-bench` infra; needs go-ahead + OpenRouter key). Pre-work dispatchable now: auth-service Nil-tenant FK fix; studio-sdk `SyncCapable` contract, Linear cursor fix, GitHub `ListIssues` pagination fix. |
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
