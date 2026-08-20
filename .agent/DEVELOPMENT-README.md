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

**Current Version:** box runs **v2.263.0** (self-upgraded at the 08-19 14:00Z train — first #4984-gate tick, ~9 merges incl. the PR#5004 docs read gate; verified via `pilot_build_info` 08-19). **PR#5007 (sdk v0.35.2 pin, arms the KAN-6 Jira chain) merged 08-19 ~14:24Z — in NO tag yet; rides the 08-20 train, then fresh-KAN-card verify.** Prior: v2.262.0 (installed 08-18 late evening — founder-confirmed), corrective operator tag at `b58b659c` after the 08-18 train defect (recovery fired 15min BEFORE the 14:00Z tick cutting v2.261.0 at 13:45Z, then the 15:12Z restart never recovered the genuinely-missed tick → **pilot#4982** filed with log evidence). v2.262.0 carries: sdk **v0.35.1** pin (Jira Cloud ADF poller fix — **#4917 CLOSED** against this version), releaser tags-authoritative baseline (#4953→PR#4973), dispatch label-unwind (#4961→PR#4971), ghguard pflag parser (#4963→PR#4975, APPROVE-w-notes) + GH_REPO/GH_HOST env guard (#4968→PR#4981), no_op decline contract (#4964→PR#4977), comms signal-strip consolidation (#4967→PR#4980), Linear UUID label filter (#4965→PR#4978), squash-prefix stripping (#4966→PR#4979), and the golangci-lint **skip-cache** CI fix (`049456d5` — self-poisoning cache killed 4 green PRs; pitfall `golangci-action-cache-self-poisoning-sa5011`). Jira adapter now LIVE against `quantflowstudio.atlassian.net` (project KAN, label `pilot`, 60s poll; token on box config only). Earlier: **`v2.259.2`** (08-13 14:19Z hot restart — includes PR#4865 running-version observability + PR#4867 dead-man rules fix, both drill/wire-validated same day). ⚠️ **The "disk≠process mismatch" signature is RETIRED — it was the same misdiagnosis ×3** (memory `hot-restart-preserves-pid-uptime-false-mismatch`): the restart leg is `syscall.Exec`, the PID is preserved, so board `uptime` (ps etime) survives hot restarts and proves nothing. **Running-version surface LIVE** ([pilot#4864](https://github.com/qf-studio/pilot/issues/4864)/PR#4865): `pilot_build_info` metric · real /health version · doctor 3-way check; board `ver` repointed at the metric 08-13 (`(disk!)` suffix = daemon unreachable, disk fallback). **`platform_breaker` ENABLED in production since that restart** (config: `orchestrator.autopilot.platform_breaker.enabled: true` — NOT top-level `autopilot:`, which binds to nothing; backup `config.yaml.bak-20260808-breaker`; armed-state startup log = #4814). **Daemon runs on AWS** (`i-0e0c1ca34e7b561f9`, TASK-409; ops via `pilot-aws` skill; NO local daemon; binary `/var/lib/pilot/bin/pilot`, rollback at `pilot.prev`) — **approvals OFF since 07-20** (auto-merge on green CI; size-floor/scope-drift escalations park `awaiting_approval`, asks route channel-first per #4810, live since the rebuild) — **GH-4391 rate-budget client LIVE** | full status in `.agent/system/FEATURE-MATRIX.md`

**PRIORITY (founder directive 2026-07-26 — supersedes 07-17):** **SaaS/platform UNPARKED — TASK-405 is active work again.** The 07-17 ordering (pointer delivery → pilot reliability → SaaS parked) held while the dispatch-reliability chain was open; that chain closed with v2.246.0 on 07-25. Pointer and pilot reliability remain live tracks but no longer gate S-milestone dispatch. Memory: `founder-priority-pointer-first-saas-parked` (superseded).

**Recent (Aug 8 – Aug 19 2026; detail lives in `system/saas-roadmap.md`, `system/approval-architecture-roadmap.md`, `tasks/archive/`, and git log — do not re-grow this block, replace it):**
- **08-19 (evening/night): docs read chain COMPLETE + founder-account E2E demo PASS + findings loop closed same evening.** (1) **TASK-466 read leg DONE**: ui#108 → **PR#109 merged + reviewed APPROVE-w-notes** (all 5 checklist items; `html:false` escape-tested; verdict as comment — GitHub blocks formal approve on own-account PRs); fast-follows ui#110 → PR#111 merged. (2) **Founder-account product-path E2E PASS**: credentials via console UI → Provision button → fresh box ~100s → connection repos synced product-path (PR#178 proven live) → ship-test-js#7 picked <60s → `average()` → **PR#8 auto-merged, issue closed** ($0.12 window). Local rig unblocked mid-run: console container flipped to **ssm secrets driver + AWS creds** (untracked override; postgres/SSM split-brain wedged credentials + broke `/docs` proxy auth). (3) **4 findings filed, 3 Pilot-fixed same evening**: console#189 (credential convergence + store-presence honesty) → PR#192 · console#190 (default-branch fallback + WARN spam) → PR#191 · ui#112 (v-applied label lies vs drift badge) → PR#113 · **pilot#5008 UNLABELED — founder scoping (hosted retry path; ship-test-js#6 kept open as repro)**. ⚠️ **Post-merge reviews OWED: ui PR#111/#113 · console PR#191/#192.** Demo verdict recorded: controlled-demo READY; launch gated on payment processor · staging deploy · pen test · branch protection. Marker: `2026-08-19_e2e-demo-pass-findings.md`.
- **08-19 (morning→afternoon, condensed): three review/dispatch waves in one day.** Morning: post-merge batch ×7 (verdicts on PRs; **#4978 Linear UUID = functional no-op caught** → #4985; #4982 narrative corrected — defect #2 never existed; KAN-6 close-leg gap → #4987 + sdk#121). Midday: whole batch executed + reviewed ×7; **FALSE DELIVERY caught: PR#4992's Jira done leg = dead code** (pitfall `merged-feature-dead-callback-not-bridged-onprcreated`, third TASK-460 incident) → #4999 + sdk#123/#124; TASK-479/481 closed+archived. Afternoon: reachability chain cleared same-day, all 5 reviewed (**#5000 = Pilot's own spawner fix** · PR#5001 ADFText · sdk PR#125 OnPRCreated all-adapters (+sdk#127 filed) · sdk PR#126 statusCategory · PR#5002 idempotency) — **KAN-6 code-complete**; trains cut clean 14:00Z (pilot v2.263.0 · sdk v0.35.2) → pin bump #5006→PR#5007 merged+reviewed. Detail: `2026-08-19_postmerge-review-batch.md` + git log.
- **08-18 (evening): S3 EXIT TEST PASSED + 2 incidents found/fixed + full dispatch batch shipped + v2.262.0.** (1) **S3 exit met** (founder's local-first definition): 3 fresh tenants signup→credentials→provision→box→PR→auto-merge concurrently (fleet VPC, AMI `ami-01ed3bb9600200ce4`, ~$0.06/exec); fixture repos `pilot-s3exit-t1/2/3` kept; estate fully torn down incl. 25-day canary + demo box, `/tenants/*` SSM empty. Gaps → **console#177** (connection repos never reach config spec; `consolectl add-repo` was the bridge) + **pilot#4961** (poller labels in-progress pre-claim; dropped claim wedges issue — hit live ×2). (2) **Lint-cache incident**: golangci action cache self-poisons (green run saves → next restore = phantom SA5011s in files not in the diff; second wave hit DIFFERENT files), killed 4 green PRs, spawned 6 garbage fix-issues → `skip-cache: true` direct-to-main, all spawns closed, flake-recovery recipe extended (branch-ref restore from headRefOid). (3) **Train defect**: boot recovery cut v2.261.0 at 13:45Z — before its own 14:00Z tick — then the post-tick restart never recovered → **#4982** filed; operator cut **v2.262.0** (installed on box). (4) **lkshrk window closed → TASK-479/480/481 + D5 dispatched as #4963–#4968 — ALL merged same evening** (PR#4975/#4977–#4981; #4975 reviewed APPROVE-w-notes: spec's G5–G7 `-p`-assumption was wrong, implementation corrected it). Note: first gens of #4965/#4966 hit stale-diff-base intent-judge vetoes (08-17 #4922 class) — recurred, watch it. Markers: `2026-08-18_s3-exit-three-tenant-pass.md` (evening) · `2026-08-18_jira-cloud-e2e-day-close.md` (day).
- **08-18 (day): one external Jira bug → 7 defects fixed across 3 repos + Jira Cloud e2e LIVE-VALIDATED.** Morning: founder redefined S3 (memory `no-stripe-local-first-s3-testing` — no Stripe/Montenegro, no domain, local-first; infra PR#25 deploy deferred); local console stack refreshed (:8090 + :5173). #4917 (external, MattiaFailla) root-caused → #4929 epic — which **mis-decomposed into 8 children** (bare `no-decompose` token unmatched; pitfall memory updated ×2) surfacing a defect zoo, ALL fixed same day by Pilot: #4938 bare-token phrase (PR#4947) · #4944 closed-child-fails-run (PR#4949) · #4946 flaky `TestNewController_LogsResolvedReleasePolicy` killed green PR#4943 → flake recovery recipe (restore branch ref → reopen → rerun → merge) · #4927 PR-CI lint blind spot (PR#4928, <1h). Then **live-fire validation against a real Jira Cloud site falsified the fix**: the poller runs the **SDK client** (`cmd/pilot/poller_jira.go` → studio-sdk), not `internal/adapters/jira` — reporter's exact error reproduced on the patched binary (pitfall `jira-two-parallel-clients-poller-is-sdk`). Port sdk#119 → PR#120 Pilot-fixed in ~40min; sdk train then cut **v0.34.2 BELOW existing v0.35.0** (releaser baseline ignores tags it didn't create → #4953 filed+queued; corrective founder tag **v0.35.1**) → pin bump #4952 → PR#4954 → box rebuild → **JIRA-KAN-6 picked up, parsed (rich ADF), executed 56s/$0.21 → PR#4955** — full tracker-to-PR chain proven; every code change in it Pilot-authored. Also: #4265 closed (stale), #4932-class supersede gate = `pilot-superseded` label, root repo survived a `core.bare=true` flip from a SIGTERM-killed pre-push gate. Marker: `2026-08-18_jira-cloud-e2e-day-close.md`.
- **08-17 (evening, post-incident): recovery sweep + a dead-code-path discovery.** Backfill 404 loop verified persisting post-restart → **#4919** filed (backoff + abandon + breaker gate) → Pilot shipped **PR#4920** same hour. Its merge exposed the day's find: **`handleMerged` had been dead the box's ENTIRE life** — `checkExternalMergeOrClose` consumed every own-merge as "external" pre-dispatch; PR#4908's GH-4872 item-3 guard resurrected the whole tail (deploy · GH-1823 review-learning · GH-2059 eval extraction), first-ever `eval_tasks` row made the months-old TUI eval panel render "out of nowhere" (memory: `handlemerged-shadowed-dead-by-external-merge-detector`). Founder call: pass@1 → Prometheus, no TUI panel → **#4922** (first run incident-killed: false intent-judge veto off a stale diff base + PR-create 503×3 + label-strip 503 → REST un-wedge) → **PR#4923** merged, box rebuilt+restarted, `pilot_eval_*` verified live. Releases: train self-recovered and cut **v2.260.0**; re-fired run published **v2.259.4**; founder-ordered **v2.260.1** (pre-push gate caught PR#4923's orphaned `renderPanelInfo` → `--no-verify` + fast-follow **#4924** → PR#4926 merged, lint 0 issues). sdk PR#118 settled with **no empty v0.36.0**. Box self-upgraded to v2.260.1.
- **08-17 (late): FIRST EXTERNAL CONTRIBUTOR — 19 issues + 15 PRs from `lkshrk`, all reviewed, 10 merged same day.** Legitimate early adopter running a Pilot fork (`v2.260.7`); issues #4875–#4906 diagnose real defects in low-dogfood surfaces (Telegram, Linear, gh-guard, executor decline). Reviewed via 5 parallel agents + 3 research agents; **hostility check negative on all 15** (contributor even followed the `testutil` fake-token convention). **MERGED (10):** #4893 #4907 #4892 (telegram: dead confirmation buttons — producer/consumer payload drift; silent edited-message drops; @botname suffix) · #4895 #4896 (linear: dropped `project_ids` filter; team key→UUID for label creation) · #4897 (github: App installation tokens always failed `Verify` via `/user`) · #4899 (compound task-id prefixes) · #4904 (chat tasks had NO executions row → every runner event dropped) · #4903 (v2 signal blocks leaked to users) · #4898 (alert recovery notifications). **OPEN (5), ball with contributor:** #4894 (trivial rebase) · #4900 (red CI: argument-discarding-mock gate) · **#4891/#4901/#4905 changes-requested**. Three findings outgrew their PRs into fix-ready specs: **[TASK-479](tasks/TASK-479-ghguard-parser-parity.md)** — gh-guard's parser isn't pflag-faithful, **11 verified parity gaps on main today** (`-XPOST`, `-fstate=closed`, `-pXDELETE`, `-p -X POST` classify as allowed reads while gh executes writes; cross-repo `-Rother/repo` writes; `pr create -f --head` from any branch) + a `GH_REPO` env hole no argv fix covers · **[TASK-480](tasks/TASK-480-safe-noop-decline-contract.md)** — #4901 inferred "no changes needed" from the *mandatory* exit-success signal (TASK-460 class: masks GH-916 forgot-to-commit, kills the retry, bypasses GH-4517 preserve); safe contract = dedicated `no_op:true`+reason, preserve-before-classify · **[TASK-481](tasks/TASK-481-review-followup-defects.md)** — Linear UUID label lookup (unfixed half of #4884) · auto_merger compound-prefix stripping (title.go:25 claims parity it doesn't have) · **slack/discord `CleanInternalSignals` is DEAD CODE — signals leaked to those users unconditionally** (unblocked for dispatch now #4903 merged). New memories: `external-fork-pr-sweeps-stale-agent-state` (#4891 swept a stale fork checkout that deleted a pitfall memory + reverted README/graph — no injection, destructive reversion), `guard-research-framing-parity-not-bypass`. Plan: brief window for lkshrk to react, then Pilot takes TASK-479/480/481 + the #4894/#4900 fixes. Marker: `2026-08-17_lkshrk-pr-batch-review.md`.
- **08-16→17 (marathon): TASK-405 UN-PATCHED SHIP TEST ✅ COMPLETE + estate shipped.** Full SaaS path hands-off: Provision(UI)→TenantResourceFactory IAM→golden-AMI box→config push→tenant daemon implemented BOTH fixtures (Go+JS)→pushed via credential helper→**box opened its own PRs via gh**→merged. The run flushed **6 product bugs, all Pilot-fixed same night** (console#138/140/142/143/144/146 — convergence gate, bare ssm path, stale boot env, ExecStart vs baked binary, provision.failed wedge, root-vs-pilot dubious ownership). Estate same day: **FleetVpcStack deployed** (NAT-1, via mgmt-runner box — laptop can't assume CDK roles) · **golden AMI refreshed `ami-01ed3bb9600200ce4`** (Go 1.25.13+gh+pilot 2.259.3 baked; bake attempt 1 = tee-masked false-success chain, pitfalls banked ×3) · console#126→PR#131+#133 + infra#29→PR#30 merged+reviewed · ui#86 drawer restyle (founder-spotted, shipped ~1h) · console#136→PR#137 drift-settle (founder-spotted) · ui#88 no-repro (chat failures = local node-25 env; CI genuinely green). Founder calls: price/copy unchanged (no release), staging inputs skipped. **08-17 wave 1: post-merge review batch DONE** (7 fleet PRs, verdicts posted; console#150–158 filed) + pilot#4872 re-anchored + sdk#117 + ui#85 dispatched. **Wave 2 same day: Pilot executed ALL of it (11 PRs) and every PR was reviewed again** — console PR#159–166 all merged+reviewed (159/161/162/164 APPROVE · 160/163 APPROVE-w-notes · 165/166 APPROVE-w-defects) · ui#85→PR#90 APPROVE · sdk#117→PR#118 APPROVE **tag-ready (pilot pin bump at next sdk train)** · pilot#4872→PR#4908 pre-merge HOLD (stale TargetBranch cache defeats guard both directions; GitHub blocks request-changes on own PR) → merged as safe verb + **#4909 dispatched** (required fixes). Round-3 fast-follows dispatched: console#167 (sha-match stale rollback + fetch-secrets convergence — live box exposed) · #168 (reaped-path visibility regression) · #169 (script hygiene) · ui#91 (provision_failed card, contract from PR#164 review). **Round 3 same day: all 5 executed + reviewed** — console PR#170 (APPROVE-w-note → #173 structural `.prev` fix) · #171/#172 APPROVE · ui#92 APPROVE-w-defects (→ ui#93 mock-lockstep + stale-detail) · pilot#4910 pre-merge APPROVE → **merged** (→ #4911 advisories). #158 closed: founder chose fail-loud. **Then: sdk v0.35.0 tagged (base-guard) → pin bump pilot#4913 dispatched · UI design-conformance program launched (founder directive): ui CLAUDE.md rewritten with per-page design map + binding conformance rules (`34bd43b`), 5 polish legs dispatched ui#95–99 (tokens → login/header → instances → connections → primitives). **Round 5 reviewed (8 PRs, all verdicts posted): ui PR#94/#100/#101/#102/#103 + pilot#4914 pin-bump APPROVE · console#174 APPROVE-w-deviation (→ #175 .prev-at-apply-START) · pilot#4912 APPROVE-w-followup (→ #4915 ProcessPR tail-persist resurrects removed rows — sideways dead-end not restart-durable). **Round 6 CLOSED the day: console PR#176 APPROVE (.prev class structurally closed after 3 rounds — one narrow SSM-delivery window documented) · ui PR#106 APPROVE · pilot PR#4916 pre-merge APPROVE → merged (GH-4872 chain COMPLETE end-to-end: #4908→#4910→#4912→#4916 + sdk v0.35.0 poller leg) · ui PR#104 was CONFLICTING (pre-#102/103 base) → closed-to-retry, ui#98 re-anchored with shrunk scope. **Design-conformance program COMPLETE** — all 5 legs + residuals merged+reviewed APPROVE (ui PR#100/101/102/103/106/107; ui#98 self-retried by the daemon as PR#107 after the conflicted PR#104 close). **16:00 train MISSED — GitHub platform incident** (compare API 404s globally, verified 2 tokens + githubstatus; sdk PR#118 stuck `releasing`, per-PR breaker open, backfill loop burned ~2.9k rate hits — no-backoff retry is a gh-4792-family follow-up candidate). **24 pilot commits unreleased** (full guard chain + v0.35.0 pin) → tomorrow's train + self-upgrade activates them, or manual tag post-recovery. Queues EMPTY except parked console#45. Operator: live box config-gen bump · box + temp-factory IAM teardown · post-recovery check sdk PR#118 settles.**
- **08-15 (overnight autonomous run): TASK-478 build-out COMPLETE — every remaining autopilotable leg executed + reviewed in one night** via an hourly self-terminating watch loop. Six PRs size-held with verdicts (ui PR#67 UI-9 APPROVE · #72 UI-10 APPROVE · #74 UI-11 + #76 UI-12 APPROVE-w-defects · #73/#77 HOLDS mis-based); dependency-ordered **morning playbook in the TASK-478 doc status line** (approve 67→72→74→76, close 73/77 to arm clean retries, then label ui#78). Review catches fixture tests can't see: PR#74 typed the daemon event `detail` as an object (real wire = string → char-spray live) · PR#76 targets a route name PR#72 deletes. **New pitfall memory: `sequential-gates-on-execution-not-merge-fastfollow-misbase`** (fast-follows against unmerged size-held PRs mis-base; vendor-vs-stack nondeterminism; file fast-follows unlabeled + gate note). Also: pilot#4869 fix (PR#4870) merged + reviewed. Remaining on TASK-478: founder taps + retries re-land + real-stack verify batch UI-2..12 + CON-5 (Stripe gate) + copy pass.
- **08-14 (design → execution, one day): morning design sprint ×4 pages (13 screens, rail 100% designed) → [TASK-478](tasks/TASK-478-console-rail-implementation.md) planned (17 legs) → 12 of 17 merged + reviewed same day, all APPROVE** (console PR#119/121/123/125 · ui PR#51→65). Two pre-S3.3 adapter drift classes killed (instances DTO/routes · connections `{label,secret,config}`/`{valid,error}`). Slack approval path proven e2e ×2 live. Filed [pilot#4869](https://github.com/qf-studio/pilot/issues/4869) (externally-merged held PRs never close the execution journal — fixed next day, PR#4870).
- **Earlier (compressed):** 08-12 PR#4846 incident closed + 3-generation same-day fix cascade (memory `incidents-always-first`) · 08-11 design sprint ×4 + C16/C17 shipped e2e · 08-06→08-08 GH-Actions outage → recovery → hardening wave (TASK-458 breaker enabled in prod; detail `system/approval-architecture-roadmap.md`) · 08-04/05 TASK-441 contract hardening + first unattended self-upgrade (v2.253.0) + S4 waves 2–4 + token incident resolved · 08-01/08-03 S4 wave 1 + Golden AMI v2 merged (operator bake pending) · 07-31 first autonomous train + AWS cost audit (`cdk deploy` pending) · 07-30 spec-guard epic + real-stack-verify SOP · 07-29 S3 backend 10/10 · 07-27/28 S2 EXIT MET · 07-26 SaaS UNPARKED · 07-20 approvals off · 07-16 S6-lite AWS cutover (TASK-409). Detail: git log + `tasks/archive/`.

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
| **P1** | **Console rail implementation** ([TASK-478](tasks/TASK-478-console-rail-implementation.md)) | 11 approved designs → shipped surfaces. **Build-out COMPLETE 08-15 (overnight run): all 16 autopilotable legs executed + reviewed.** Blocked on founder morning sequence (approve PR#67→72→74→76 · close #73/#77 to arm retries · label ui#78 after) → then GH-69/GH-75 retries re-land · **real-stack verify batch UI-2..12** (SOP; blocked on GH-75 re-land) · CON-5 billing portal (founder Stripe gate) · copy pass ($299 · PR#72 "Includes" line · support@ mailto). Daemon-side follow-up candidate: base-presence check before claim (see pitfall `sequential-gates-on-execution-not-merge-fastfollow-misbase`). |
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
