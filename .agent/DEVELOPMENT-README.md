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
| `.agent/system/jev-gates-roadmap.md` | Before adding a Jev/TypeSafe classifier to any gate: triage rule, UPGRADE vs SKIP verdict per gate, sequencing, shadow rollout protocol |
| `.agent/product/PRICING-MODEL.md` | **What we charge for and why** — flat fee + BYO tokens + Sonnet 5 default. Canonical; read before any pricing conversation |
| `.agent/product/UNIT-ECONOMICS.md` | Cost, margin, break-even and customer TCO per tenant — measured, not modelled |
| `.agent/tasks/TASK-XX.md` | Active task details |
| `.agent/sops/*.md` | Before modifying integrations |
| `.agent/system/references/reference_slack_notifications_routing.md` | Before touching Slack/alert/approval destinations on the box (which channel gets what, the 3 keys, restart rule) |
| `.agent/.context-markers/` | Resume after break |

## Current State

**Current Version:** box runs **v2.277.4** (train 10-05 14:41Z, self-upgraded; adds #5613 sk-ant-oat-prefix OAuth classifier so service-account pool keys are API keys; v2.277.2 train 10-02 14:23Z; adds #5588 poller supervisor, #5589 epic PR via resolver, #5592 studio-sdk v0.38.3; v2.277.1 was the hand-cut Linear Invoices GitLab cutover with Linear per-project routing #5573/#5579/#5581, held-task starvation #5580, per-project quality gates in worktrees #5582, per-project GitLab forge #5583, Linear OnPRCreated controller fix #5584). Post-train merges awaiting the next cut: #5594/#5595 (10-01 review follow-ups), #5598 + #5600 (live-smoke hold chain).

**PRIORITY (founder directive 2026-07-26 — supersedes 07-17):** **SaaS/platform UNPARKED — TASK-405 is active work again.** The 07-17 ordering (pointer delivery → pilot reliability → SaaS parked) held while the dispatch-reliability chain was open; that chain closed with v2.246.0 on 07-25. Pointer and pilot reliability remain live tracks but no longer gate S-milestone dispatch. Memory: `founder-priority-pointer-first-saas-parked` (superseded).

**Recent (Sep 10 – Oct 8 2026; detail lives in `system/saas-roadmap.md`, `system/approval-architecture-roadmap.md`, `tasks/archive/`, and git log — do not re-grow this block, replace it):**
- **10-08: TWO REVIEW WAVES CLOSED SAME DAY — 10 PRs merged + reviewed across console-ui and console.** Gap wave from TASK-513 (ui#205/206/207, console#373/374) merged by 11:17Z; reviews APPROVE-w-notes (PR#209 freeze root cause unproven; PR#375 N+1 GitHub calls unbounded). PR#376 conflict (two EOF test appends) merged in a temp worktree → autopilot re-adopted (GH-4610 path). GH-205 was held 5 ticks by a cross-repo docs path in the body — body edit cleared it. Review notes → two navigator-research briefs → follow-up wave ui#211/212/213 + console#377/378 filed 12:21Z, all merged by 12:47Z, all reviewed with mutation pins proven by the reviewer (Pilot lists pins 'Not verified' — mem-220). Open: chat.ts reset() late-poll leak (widened by PR#216); console Validate has no total deadline (21×10 s); ui `.pilot/workflow.yaml` is plain YAML → defaults every run (workflow-yaml pitfall recurrence). Loop recorded as mem-221.
- **10-07→10-08: TASK-513 REAL EXHAUSTION TEST DONE ON PROD (archived) — and it found three fleet-breaking regressions.** Provision had been dead since console PR#321 (09-24): `dispatchLong` ran on the per-tick ctx → every provision canceled, `fail()` could not settle → 1/min loop (console#367→PR#368, prod-0.1.16); same for config push → boxes never received repos (console#369→PR#370, prod-0.1.17); golden AMI had no `make` → every Makefile gate exit 127 (Nelya: ami-06825978577ce40d4; pilot#5622→PR#5623 fallback + gate detail in the failure comment). Then both allowance branches proved on two test orgs: own-key switch (Testica), and no-key suspend → EC2 stop → own-key save → resume in 36 s → PR#324 on the own key with pool-02 flat (Test 2). 8 gap issues filed (ui#205–207, console#371–374, sdk#146). Learned: no operator-safe single-box AMI roll exists (console#371); never terminate a test org (retires its pool key); the console SPA freezes long-lived tabs (ui#206).
- **10-06→10-07: TASK-502 LEG 3 ENFORCEMENT PLANNED→SHIPPED→LIVE IN ~26 H (TASK-512, archived).** 3 nav-research agents → console#361/#362 (3a/3b) → PR#363/#364 merged 10-06, console#365→PR#366 + ui#203→PR#204 merged 10-07, all reviewed APPROVE-w-notes, ui PR#204 real-stack verified pre-merge. Nelya set `PILOT_CONSOLE_ALLOWANCE_ENFORCE_ENABLED=true` + `PILOT_CONSOLE_USAGE_POLL_INTERVAL=15m` (infra dc10719); reconciler rev 27 on prod-0.1.15 since 10-07 ~10:20Z; UI live 12a7b46. Learned: console deploy is automatic on main now (mem-202 update); never cancel a deploy run (mem-217); local compose override points at prod SSM (mem-216); evidence runner refuses `|` in `-run` (SOP 5c); cross-repo paths in backticks trip the base-presence hold (SOP 3b). **TASK-513 opened 10-07: real exhaustion on a prod test org** (founder registers org + GitHub token, Navigator provisions + labels issues, Nelya one `set-allowance` command, founder pastes own key; never terminate the org — it retires pool-01). TASK-511 archived.
- **10-05→10-06: TASK-502 LEG 1 SHIPPED+REVIEWED → POOL LIVE (free 5, 10-06 via Nelya) · SSM + ORG CAP DONE · LEG 4 PLANNED→MERGED→LIVE SAME DAY.** Console PR#359/#360 (leg 1b/1c) post-merge reviewed APPROVE-w-notes; founder wrote `/pilot-fleet/console/ANTHROPIC_ADMIN_API_KEY` + `platform-keys/pool-01..05` to SSM, Nelya synced api/workers (reconciler admin key still an open ask — my 11:16Z 'not needed' answer was wrong, corrected 14:53Z), console prod-0.1.12 redeployed; Anthropic org cap $200→$1,000 (+$500 notice), **credits $9.57 prepaid, auto-reload off — founder deferred (build/test phase)**; Paddle stays sandbox (L7 open). Train cut v2.277.4 (40 min in `releasing`, normal 20–27; Homebrew tap 403 → #5615), golden AMI `ami-090d2679c8be3e19a` baked — reconciler still on the old AMI until the combined redeploy. **Leg 4 → [TASK-511](tasks/TASK-511-console-ui-included-allowance-leg4.md)**: 3 nav-research + plan critique → ui#197/#198/#199 → PR#200/#201/#202 merged + reviewed APPROVE-w-notes, auto-deployed (ui#178) and verified live at 07f57a7 — mem-202 updated. Evidence-gate defect found: a 'paste the output' bullet treats every backticked span as a command → #5617; SOP 5b updated.
- **10-02 pm→10-04: LIVE-SMOKE HOLD CHAIN REVIEWED THREE DEEP · #5601 SPEC VERIFIED · CANARY CRON DISARMED.** Post-merge review of PR#5595 (GH-5568 evidence block) found the key-gated Not-verified hold sat in `handleMerging` *after* approval and left a `StageFailed` row the posted instruction could not release → #5597 → PR#5598 (hold moved into the `handleCIPassed` escalation chain, released by approval) → pre-merge review found the approval flow can auto-release it (`default_action: approved` timeout; stale `ApprovalDecision` not reset on re-escalation; approver never told what approving means) → #5599 → PR#5600 → post-merge review → **#5602** (Telegram drops non-live-smoke segments; `CancelPending` leaves `m.pending` so cancels write a bogus system timeout decision; dedupe keyed on whole-string equality). PR#5594 notes-only (probabilistic cancel-after-send pin). Founder-filed **#5601** (base-presence false holds on GitLab projects) spec-verified by nav-research: root cause confirmed (host dropped from remote parse, gh fallback maps 404→absent), but only path checks false-hold, and the body-edit release does not work for Linear tasks (live-body refresh gated to the github adapter). **10-04:** bare "Reopened #4265" emails = `pilot-canary.yml` reopening its tracker after `CANARY_GH_TOKEN` expired; the sandbox had been de-onboarded 08-14 with the cron left on and the skip guard reporting green for 7 weeks → workflow disabled, #4265/#4366/#318/#319 closed (pitfall `canary-cron-outlives-sandbox-deonboard-skip-guard-reports-green`, with the PAT expiry calendar: brew-tap fine-grained 10-05, `pilot-saas` + classic brew-tap 10-28). Founder directive: **Linear Invoices tickets (LIN-QF-*) are the founder's — never touch** (learning `linear-invoices-tickets-founder-owned-hands-off`). **10-04 pm→10-05: HOLD CHAIN CLOSED · TASK-502 LEG 1 REDESIGNED AS A KEY POOL AND DISPATCHED · POOL MINTED.** Pilot: PR#5605 (the #5601 fix, merged unreviewed under a CI-fix title) APPROVE-w-defects → #5608 → PR#5609 merged + reviewed; PR#5607 (#5602) reviewed pre-merge, autopilot-merged; #5601 closed; brew-tap PAT rotated (secret 08:06Z) and #5610→PR#5611 (canary `workflow_dispatch` + expiry log) shipped + verified live. Console: PR#351/#352 (TASK-502 legs 0+2) reviewed APPROVE-w-notes, both LIVE in prod. **Leg 1 blocker found on the live Admin API reference: no create-key endpoint, no key value ever returned → founder chose a KEY POOL** (over shared key / WIF); dispatched console#353 (merged PR#358, reviewed) → #354 (in progress) → #355; founder created service account `pilot-platform-pool`, five workspaces `pilot-pool-01..05`, five service-account keys verified (200, hints match) in Doppler prd `PLATFORM_KEY_POOL_NN`. Admin key `ANTHROPIC_ADMIN_API_KEY` in Doppler prd (200). Pool workspaces capped at **$150/month** each; **org monthly cap is $200 (Console → Billing → Adjust limit) — raise before a second tenant runs on pool keys.** Daemon gap pilot#5612 (any non-`sk-ant-api` key misread as OAuth) → PR#5613 merged same day, awaiting the train; must be on the tenant AMI before a pool key reaches a tenant. Nelya ask posted 08:52Z (duplicate from the Navigator chat 16 s later → pitfall mem-213; reconciliation note pending founder go). Memories: mem-211 (pasted `ok 0.00Xs` = skip), mem-212 (Admin API cannot mint keys), mem-213. WIF deferred to a leg 1d at ~15 tenants.
- **10-01 pm→10-02: LINEAR INVOICES MOVED FROM GITHUB MIRRORS TO GITLAB — PILOT'S FIRST GITLAB-HOSTED PROJECTS, LIVE.** Founder froze the GitHub mirrors; canonical repos + CI/prod deploy are on GitLab. [TASK-510](tasks/TASK-510-linear-invoices-gitlab-cutover.md): nav-research mapped the gaps (single global GitLab project, PR creator chosen by source adapter, Linear PRs registered into the default autopilot controller) → #5583 per-project `gitlab:` forge + #5584 controller fix merged overnight → v2.277.1 cut by hand 08:43Z → box flipped (fine-grained PAT on the group — GitLab Free refuses group/project tokens; GitLab clones; `main` locked MR-only after the probe's push to `main` proved an Owner's token bypasses Maintainer protection; Linear polling on with the team KEY because sdk v0.38.2 breaks on a UUID → sdk#144/PR#145, tagged v0.38.3, pin bump #5592). Self-upgrade re-exec died on the new env var (wrapper restart needed). 8 PRs reviewed same day, all APPROVE(-w-notes); follow-ups #5588 (poller supervisor, merged), #5589 (epic PR, merged). Founder scope: configure only, no tickets labelled by Navigator, no commits to GitLab.
- **09-30 pm→10-01 am: JEV SHIPPED END-TO-END IN ONE DAY, BOTH GATES LIVE IN SHADOW.** TASK-505 epic #5509 → 4 PRs + 6 follow-ups merged and reviewed same day; audit before the train found the kind questions byte-identical across items (all `low_confidence`) → #5535 fixed, pitfall `jev-identical-questions-return-identical-answers`; v2.276.11 released manually 21:09Z, key rotated in the wrapper env, shadow data 3 tasks/14 items/0 overrides. 10-01: gate 2 (TASK-506 → PR#5546, live smoke 5/5), per-item debug lines #5549, autopilot tick freeze root-caused (`buildScopeMembers` serial GitHub calls, 57 min) → #5545 + #5550, stacked-branch exact rebase #5540, gh-guard test-origin filter #5544 → **v2.276.12 released 09:13Z, both gates live in shadow 09:16Z**. Decision: flip per gate at ≥100 decisions or 7d (~10-07) after hand-labelling `overrode`/`low_confidence` from the debug lines. Incidents: GH-5525 orphaned poll-loop hang (#5530 shipped), GH-5527 Rule 3b dotted range + epic misdetection (SOP 3b/3c), 4 docs pushes masked by `| tail` (check ahead/behind after every push). Both tasks archived.
- **09-29→09-30: HYGIENE SWEEP · PADDLE L7 STARTED · PRICE DECIDED $499 · EVIDENCE GATE FOUND DEAD, FIXED · SONNET 5.5 EVERYWHERE.** **09-30 am: ALERT SEAM CLOSED · REVIEW LEDGER LIVE · NELYA BILLING ASK POSTED.** External PR#5465 (#5448 idle `autopilot_deadlock` re-fire; mark-sent/fire asymmetry) reviewed with two mutation pins, fork CI un-wedged by close/reopen, hand-merged `540947e3f`; follow-ups shipped same morning by Pilot: #5499→PR#5501 (test pins), #5498→PR#5502 (`heartbeat_timeout` alert implemented: callback wired at all six `backendExecute` sites, engine handler, default rule, cross-package string pin); #5500 filed (`task_timeout`/`watchdog_kill` dropped by the engine, decision pending). Post-merge reviews on #5496 (review-verdict ledger) and #5497 (acceptance pattern subset): APPROVE-w-notes. **Ledger parser is strict: line 1 must START with `APPROVE-w-notes`/`APPROVE-w-defects`/`APPROVE`/`REQUEST-CHANGES`; `Verdict: APPROVE …` parses to empty** → mem-204 updated, two reviews rewritten. Five alert-seam memories added (heartbeat callback unset, engine drops uncased events, adapter string coupling, per-rule cooldown, deadlock asymmetry). **Nelya ask for the Paddle template posted 09:57Z** (#infrastructure thread `1790762207.809729`; `BillingEnabled` gated like `EmailOn`, three SecureStrings; console has no `SUCCESS_URL` env). All four Pilot PRs merged pre-train with no `## Evidence` (expected; v2.276.8 train 14:00Z carries #5465/#5493/#5501/#5502). Marker: `2026-09-30_alerts-closed-ledger-live-nelya-billing-ask-train-next.md`. Sweep: Navigator 7.8.0 synced, box config trimmed to 9 projects + restart, laptop model pins → 5.5, TASK-501 archived. **Secrets → Doppler** (decision `secrets-live-in-doppler-pilot-console-project`, project `pilot-console` dev/stg/prd; sandbox key `PADDLE_SANDBOX_API_KEY` scoped + verified 200). **L7**: sandbox price **$499 `pri_01m3rf7qws3gzscj9fttg4aywe`** (founder decision 09-30: **$499/mo with an included Sonnet 5.5 token volume, overage on the customer's own key**; $299 sandbox price archived; margin table in `product/UNIT-ECONOMICS.md`, recommendation 100 tickets = $100 allowance, **volume number pending**); prod console-api carries no billing env (Nelya ask drafted in TASK-497); product legs for bundled tokens → [TASK-502](tasks/TASK-502-included-tokens-platform-key.md). **Model**: daemon default routes every tier to Sonnet 4.6 (doc said Opus, wrong) → #5481/PR#5482 → 5.5; console renders a `model_routing` pin for tenants (console#347/PR#348, prod-0.1.7; running tenant still on AMI 2.276.1 + unpinned until a connection save bumps its config generation); leftovers #5484/PR#5487 (REQUEST-CHANGES) → #5490/PR#5492. **Evidence gate has never run in production**: two causes, both fixed — extractor only read "## Acceptance Criteria" (#5485/PR#5488) and the dispatcher rebuilds tasks from the ledger without criteria (#5491/PR#5493, ships in v2.276.8); plus phrasing recall #5479/PR#5480 and precedence #5486/PR#5489; follow-ups #5495 (adapter pattern subset), #5494 (review verdicts → ledger for a defect-rate column). **Proof pending: first PR after the 09-30 train must carry "## Evidence".** Eleven PRs reviewed, every one with a verdict as a comment review (mem-204). Sonnet 5.5: 12/12 first-try green (bench tally). Memories: mem-203 ×3 updates, mem-175 ×2, mem-204 new. Marker: `2026-09-30_price-499-decided-gate-fixed-l7-live-next.md`.
- **09-25→09-28: FIRST FLEET TENANT VERIFIED · CONSOLE SPLIT · MERGE-TO-LIVE AUTOMATED · PROFILE EDIT (TASK-501 A) · EMAIL TRANSPORT (TASK-501 C) LIVE, SMOKE PASSED.** Tenant `i-0289111000cebcee9` verified by Nelya 09-25 (two ships, 4m35s/4m28s). **09-28**: console-workers split complete (console-api ×2 flags off, `console-workers` ×1, board watch clean); Docs page renders `DEVELOPMENT-README.md` on the live tenant (WAF fix by Nelya's own commit; TASK-466 hosted-path proof); **rule mem-201: `aws-infrastructure-pilot` is Nelya's — asks only, never our PRs**. **Merged ≠ live class fixed** (pitfall mem-202): UI auto-deploys on green CI on main (ui#178→PR#179, proven 4×), console auto-cuts prod-X.Y.Z + Release dispatches the deploy (#331/#333/#336 → PR#332/#334/#338; two REQUEST-CHANGES on the way, TenantFactory guard verified in three rendered cases; **first fully automatic chain prod-0.1.3**; prod-0.1.5 by end of day). **Merge-gate defect found & fixed**: autopilot merged console PR#337 with a red check because global `required_checks: [test, lint]` leaked into projects without a `ci_checks` block → pilot#5468→PR#5470 (unlisted red checks block) + docs #5472; **v2.276.6 cut by hand 13:17Z, box hot-upgraded 13:36Z**. Evidence gate: diff-coverage (#5466→PR#5467) + live-body (#5469→PR#5471); tunnel tests hermetic (#5475→PR#5476). **TASK-501 leg A LIVE + founder-verified**: auth PR#517 → **v0.74.0**, console PR#337 → prod-0.1.2, ui PR#183/#185 (a11y). **Leg C our side done**: console PR#342 (relay `/api/v1/send-email` + public verify/reset relays) → prod-0.1.4, PR#344 (#343) → prod-0.1.5; ui PR#187/#190 (landing pages + focus/empty-body); SOP `sops/email/console-ses-smoke.md`; consolidated brief to Nelya 14:26Z (thread 1790605566.855279) → **she shipped infra `5ccf0320` + secret + both deploys by 15:35Z** (worked from the earlier note) → **smoke PASSED 16:12–16:29Z** (register → verify → reset → login; SES 2/0/0; four timestamps posted). Verify link idempotent by design (SOP corrected, no issue). **Leg B: auth-service LIVE v0.75.0** (#518 → PR#519 reviewed, revision #520 fixed 400-tokens/rate-limit-order/pipelined-INCR/e2e-test, merged 19:31Z, tag by hand 19:54Z, auto-deployed 20:26Z; change route 503 until Nelya adds two template params — ask posted 19:56Z, no reply). **Console part LIVE**: console#345 → PR#346 (APPROVE-w-notes as comment), autopilot-merged 21:04Z → **prod-0.1.6** deployed 21:11Z. **UI part LIVE**: ui#191 → PR#192 (+986/-32, APPROVE-w-notes) released by founder 22:11Z → edge on `4235b6f` 22:14Z. **Leg B SMOKE PASSED 2026-09-29 06:26Z** (change → revert → change → confirm → login with the new address; follow-ups filed + shipped 09-29: auth-service#521 → PR#522 merged, **v0.75.1 live 10:01Z (td 13)** (limiter fixed-window + humanized expiry copy in all four mails); ui#195 → PR#196 **live 09:33Z** (Save disabled for the window after 429); pre-auth pages ignore dark theme → ui#193 → PR#194 **live 08:42Z**, verified in served CSS). **TASK-501 archived 09-29** (`tasks/archive/`). Deployed 22:26Z: Nelya synced the two params 22:20Z → auth-service redeployed (task def 12, env verified) → founder smoke pending. Bench: `system/bench/sonnet-5-5-first-cut-2026-09-28.md` (n=2, ~3× lines/min, ~3–4× cheaper/line) → [#5477](https://github → PR#5478 (+233, APPROVE-w-notes 22:40Z, 3rd Sonnet 5.5 run, first-try green).com/qf-studio/pilot/issues/5477) filed (executions `lines_added` never populated; seam runner.go:5088 per nav-research), running on 5.5 as the third run. Both were the first runs on Sonnet 5.5: first-try green, 5 and 8 min. **Box default model → `claude-sonnet-5-5` 20:41Z** (config edit + restart; first run unobserved). 22 PRs reviewed today, every one with a verdict. Learnings mem-203 (Acceptance list + no unverified claims in issues); mem-198 corrected (09-25 repo switch was delivered by `syncRepos` in 10 min; PATCH bump cannot carry repos). Tokens rotated 09-28 (tenant GitHub, Homebrew tap). Markers: `2026-09-28_before-break-leg-b-auth-live-sonnet55.md`.
- **09-23 pm→09-24: FALSE SUCCESS CAUGHT, REAL CONSOLE RUN STARTED, FIRST PROVISION ONE CLICK AWAY.** Reconciler on the new AMI (Nelya: rebalancing off) → factory ON (task def rev 6). Founder's "instance is active" turned out to be **the UI mock build in production** (deploy never set `VITE_API_MODE=http`; live bundle had 0× `/api/v1/`) — every browser milestone since 09-22 was fixtures, the "email not a gate" claim is withdrawn (pitfall mem-195; fix [ui#170](https://github.com/qf-studio/pilot-console-ui/issues/170)→PR#171 + bundle guard, live 961bd42). Real run 09-24: register → GitHub connect failed on **console-api `ssm:PutParameter`** (Nelya granted 10:47Z) → both secrets real in SSM → repo `pilot-ship-test-go` (CI added over SSH; `gh` token lacks `workflow` scope) → Provision 14:21Z → reconciler created `pilot-tenant-425f9f99-…` then **`iam:DetachRolePolicy` denied** (Nelya granted 14:42Z; code fix [console#320](https://github.com/qf-studio/pilot-console/issues/320)). Lessons: read-only grants pass every smoke test until the first real write; the reconciler acts within one 60 s tick and logs only on error (my premature "hung loop" read → harmless restart; [console#319](https://github.com/qf-studio/pilot-console/issues/319) reframed as observability). Executor side: GH-5449 fetch fix via steered retry (PR#5457); ci-fix excerpt window #5454→PR#5455 (floored `completed_at` dropped the failing package); box **v2.276.3**. **Next click: Deprovision → Provision → first tenant → Docs real tree → post id to Nelya.** Detail: [TASK-495](tasks/TASK-495-fleet-to-be-alignment-nelya.md) 09-24 entries.
- **09-22→09-23: CONSOLE USABLE END-TO-END, GOLDEN AMI REFRESHED, OLD ESTATE GONE.** **09-22** Nelya shipped all three legs in our order (gRPC over PrivateLink → `/api/v1/me` 401; SES identity verified, prod access granted; UI deploy grants) after a purpose→exists→build→verify brief (her bot had not understood the one-liners). Console UI deployed by our new workflow ([ui#164](https://github.com/qf-studio/pilot-console-ui/issues/164)→PR#165, stamp fix [ui#167](https://github.com/qf-studio/pilot-console-ui/issues/167)→PR#169; edge rewrites extension-less paths, so `build-sha.txt`). **~~First browser login through the edge PASSED~~ — FALSE SUCCESS (found 09-24): the deployed UI is the mock build (`VITE_API_MODE` unset), so login/Docs/provision never reached console-api; fix [ui#170](https://github.com/qf-studio/pilot-console-ui/issues/170); "email not a gate" unproven** — email = a 4-leg track (relay route under `/api/v1/`, template token secret, auth-service env, UI reset/verify pages), planned separately; decision `console-email-transport-ses-not-email-service-repo`. **09-23** founder-box data volume swapped to **encrypted** (restore test passed, ~4 min pause; learning `ebs-volume-swap-runbook-xfs-nouuid-and-fstab-by-uuid`); `user/aleks` now in `pilot-founder-box-operators`. **Old CDK estate (10.30) deleted by Nelya** (stacks, VPC, RDS+final snapshot, orphan snapshots; key `a0e6dc65` scheduled 09-30) — nearly took the founder box's only backup with it (pitfall `dead-cdk-stack-carried-live-founder-box-backup-policy`); backup now owned by her `pilot-data-lifecycle`, `DataLifecycleStack` goes after tonight's snapshot; `pilot-cloud-infra` **ARCHIVED**. Drift on `pilot-agent` role removed (learning `hand-applied-iam-on-cfn-managed-roles-is-a-time-bomb`). **Golden AMI refreshed: `ami-01ecd7cefa1e6f316` (pilot 2.276.1)** via [TASK-499](tasks/TASK-499-golden-ami-refresh-first-fleet-tenant.md) — bake now fetches the release by version with checksum + version pin and writes both SSM params (infra#10/#12; first bake failed on the bucket's SSE-KMS policy, pitfall `s3-accessdenied-from-admin-role-is-bucket-policy-condition`). Both infra PRs needed **manual rebases**: Pilot's worktree branched from an August base after a lost fetch ref-lock → executor fix filed [pilot#5449](https://github.com/qf-studio/pilot/issues/5449) (autopilot ci-fix #5451 in flight). **Blocked now: reconciler redeploy for the AMI pickup rolled back** — ECS rejects AZ rebalancing on the binpack-only singleton (pitfall `ecs-az-rebalancing-requires-az-spread-placement-first`), Nelya's template fix pending. **Gates to a customer, in order:** ~~reconciler on the new AMI~~ ✅ 09-23 (Nelya: rebalancing off) → ~~`tenant_factory=true`~~ ✅ 09-23 (task def rev 6) → ~~ui#170 real-API build~~ ✅ 09-24 (PR#171, live 961bd42) → first fleet tenant (founder re-registers on the real console) + Docs real-tree proof → price + Paddle L7 (+ console model pin). Base-presence gate self-holds ×3 this week (mem-175 updated: releases after ~3 ticks; un-backtick every non-repo dotted token). Marker: see `## 🚀 Pilot Cloud SaaS Program`.
- **09-16→09-21: THE CONSOLE RUNS IN THE VPC — first time ever — and the domain is decided.** Trigger: the Docs page (TASK-466) never rendered because `pilot-console` had only ever run on a laptop with no route to tenant private IPs; an Opus-authored ticket (infra#51 / TASK-498) targeted the retiring CDK role and cited a tenant-box path → held 8× by the base-presence gate → withdrawn 09-15 (`pilot task cancel`), **closed superseded 09-21** — the fleet task role already carries both grants. **09-18** review of Nelya's *deployed* `pilot-fleet-*` stacks + manifest correction (pitfall `fleet-env-doubles-as-environment-tag-value`). **09-20** Nelya delivered 14 stacks, ECS cluster, console service templates, MFA self-service ([TASK-495](tasks/TASK-495-fleet-to-be-alignment-nelya.md) has the log). **09-21** `deploy-quantflow-aws.yml` placed on pilot-console main (`1f07b8c`); run 1 reconciler rolled back on `ec2:DescribeImages` (CFN validates `AWS::EC2::Image::Id` params as the *deployer* — pitfall `cfn-typed-parameter-validates-with-deployer-credentials`), Nelya retyped `AmiId` → String; **run 2 green: console-api 2/2 + reconciler 1/1 on `prod-0.1.0`, `tenant_factory=false`, 10/10 alarms OK.** **Domain: `pilotcloud.dev`** (decision `domain-pilotcloud-dev-keep-pilot-name`; `pilot.build` $720 rejected — no domain buys search for a word Pilot.com owns; `pilothq` is their handle) — sent to Nelya to register + start phase 2. Rename rejected: the `polsia.app` "Pilot" clone is an AI-generated placeholder. **US trademark cleared** (learning `trademark-clearance-pilot-us-2026-09`: bare PILOT unregistrable for us, use-risk low-moderate; **EU/UK still unchecked**). Navigator v7.7.0 synced + typed judge on. **Gates to a customer, in order:** MFA enrol (founder, 5 min) → gRPC NLB :4002 + SES (Nelya; **login is 503 until then**) → fresh golden AMI (2.259.3 is stale) + `tenant_factory=true` → phase 2 under `pilotcloud.dev` → price + Paddle live. New console DB is empty; old tenant `i-0a3bf271d598196ca` (10.30 VPC) unreachable from it. Marker: `2026-09-21_console-live-on-pilot-fleet-domain-decided.md`.
- **09-12→09-15: Paddle Billing designed, built, smoke-tested and priced ([TASK-497](tasks/TASK-497-paddle-billing-integration.md) has the full ledger; do not re-expand it here).** All code legs **L1–L5 merged + reviewed** (console PR#298/#301/#302/#304/#307/#309/#312, ui PR#157/#161): provider core, webhook with own `Paddle-Signature` verifier + insert-first ledger + ordering guard, checkout UI, portal session, `consolectl billing set-status`/`resync`, and the enforcement sweep + reconciler `suspended` lifecycle (**wired but dormant** — `PILOT_CONSOLE_BILLING_ENFORCE_ENABLED` default off). **L6 sandbox smoke run 09-14** against live Paddle sandbox: real card checkout → 3 signed webhooks resolved to the org → subscription bound → chip active. SOP at `sops/billing/paddle-sandbox-smoke.md`. The smoke found **two defects, both fixed and verified live the same day**: grace window truncated to whole hours and vanishing below 1 h (console#313 → PR#314), and the plan page advertising a hardcoded $299 against a $500 catalog (console#315 → PR#316 + ui#162 → PR#163). **Pricing decided 09-14** → `product/PRICING-MODEL.md`: flat fee for box + Pilot, tokens BYO billed by Anthropic directly, default `claude-sonnet-5` with a customer switch; usage-based rejected (our cost doesn't scale with tickets). Measured economics in `product/UNIT-ECONOMICS.md`. **Price number still open** ($299 recommended; Paddle prices are immutable once used, so a change means a new price object). **Known gap**: the console renders no model config, so tenants inherit the daemon default which routes complex work to **Opus**, not Sonnet — pin it before provisioning anyone. Executor side quests: evidence gate (#5437/#5438 → PR#5439/#5440), argv-exec + redaction pin (#5442 → PR#5443, after #5441 was **declined by the model's cyber safeguard on wording alone**), and auto-preserved worktrees now escalate with the sha instead of burning a retry generation (#5445 → PR#5446). Box on **v2.276.1**. Markers: `2026-09-15_paddle-priced-l7-next.md`.
- **09-11→09-12 (parallel session): external-issue triage — 2 closed, 0 code.** **#5419 "OrcaRouter provider support" closed not-planned**: automated vendor campaign, not a contributor — `gh search issues OrcaRouter` shows 30+ identical issues this week from 8+ throwaway accounts (author created 08-26, event feed = fork→PR within seconds across unrelated repos, 5% revenue-share "partner program" is the hook); technically nothing to build, `executor.type: openai-api` + `openai.base_url` already covers any endpoint. **#5359 (PR guard no_op on merge-base failure) closed completed**: fix landed via PR#5373 on the same `pilot/GH-5359` branch (#5369 died on a CI fixture bug only), released v2.273.2 — `resolveEmptyBranchWithFallback` merge-base → rev-list → open-PR lookup, fails closed; issue had been left open after supersession. Memories updated: `external-contributor-vetting-verify-claims-not-profile` (+campaign counter-example), `pitfall_noop_terminal_state_invisible_to_dispatch_guards` (+4th instance). `gh-5359.md`/`gh-5370.md` archived.
- **09-10→09-11: review-everything day + console S5 legs shipped + Paddle decision.** 22 PRs merged across pilot / pilot-console / pilot-console-ui, **every one reviewed with a verdict, follow-ups filed same hour** ([TASK-496](tasks/TASK-496-console-resume-s5.md) has the ledger). **Pilot reliability**: PR#5411's review surfaced a live regression from PR#5402 (`needs_human` terminal with no re-arm probe → clearing the label no longer re-admitted a task) → fixed #5414/PR#5417 + test pin #5420; the fix-issue PR tracking defect took three PRs (#5412 guard → #5416 retry-path-only → **#5421/PR#5423 primary SDK path, mutation-verified**) + registry hardening #5425/#5427 → #5430/#5431 → #5432/#5433; queue endpoint gained `active` filter (#5426/PR#5428). **Console S5**: B7 idle sleep redone from main (#282→PR#284, gate OFF until tenants re-bootstrapped; PR#222/#215 closed) · usage rollup + GET usage (#283→PR#285, REQUEST-CHANGES: migration 0018 collision with #284 + cumulative counters summed → revision #286 shipped in 11 min, autopilot re-adopted the size-guard-held PR = #5378 redrive proven live) · reader active-only + full-page guard (#288/PR#290) · config floor/wiring pin/operator note (#287/PR#289) · **consolectl single-writer advisory lock (#291/PR#292, 09-11)** → #293 wiring tests · UI usage page (ui#150/PR#151 → #152/PR#153 → #154/PR#155). **Founder decisions**: B7 = close+refile (option A) · **billing via Paddle, not Stripe** (jurisdiction; memory `payment-processor-is-paddle-not-stripe`; Stripe scaffolding dead-end). Box v2.274.0 → **v2.275.0** (09-10 train). Navigator plugin 6.18.1 → **v7.3.0** synced (`deep_research` off). Pitfalls: `terminal-status-without-rearm-probe-kills-operator-recovery`, `issue-body-literal-migration-version-collides-with-sibling-leg`. Markers: `2026-09-10_console-resume-prepared-5416-5418-reviewed.md`, `2026-09-11_console-lock-shipped-paddle-next.md`. Next: → 09-12 bullet.

**Caveat CLEARED 2026-08-26 (was wrong since at least v2.149.4):** the long-standing claim that `gateway.Config.LinearWebhookPublicKey` has no YAML decode is **false** — nav-research verified it is fully wired: `internal/adapters/linear/types.go:20` (`yaml:"webhook_public_key"`) → `internal/pilot/linear_key.go` (PEM/PKIX/Ed25519 parse) → `internal/pilot/pilot.go:492` → `internal/gateway/server.go:126`, with the disabled-path logged at `pilot.go:495`. Ed25519 verification works when the key is configured. The real incident of this shape was **`gateway.Config.Auth` (GH-4784)** — validated + defaulted but both production constructors called the auth-less `NewServer` (memory `unwired-config-field-validated-but-dead`). This entry sat stale for months and was repeated as fact; do not reinstate it without re-grepping.

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
- **New repos** (created 2026-07-14, in `~/.pilot/config.yaml`): `qf-studio/pilot-console` (Go control plane) · `pilot-console-ui` (Vue3/Vite/Bun SPA) · ~~`pilot-cloud-infra` (Go CDK)~~ **archived 2026-09-23** (estate deleted; fleet infra = Nelya's `aws-infrastructure-pilot`) — each has its own `CLAUDE.md`
- **Latest handoff marker**: `.agent/.context-markers/2026-10-01_console-execution-continue-task502-legs-1-3-4-5.md`
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
| **P1** | **Pilot SaaS platform** ([TASK-405](tasks/TASK-405-pilot-saas-platform.md)) | S0–S2 ✅ · S3 exit met local-first 08-18 · S4 build complete · **S4 exit path UNBLOCKED 09-21: console-api + reconciler LIVE on Nelya's `pilot-fleet` (ECS, `prod-0.1.0`, factory off)** — the minimal-EC2 route ([TASK-490](tasks/TASK-490-s4-minimal-invpc-console.md)) is superseded by the ECS transfer. **Domain decided `pilotcloud.dev`.** **09-25: first fleet tenant provisioned + shipped twice on the hosted path; board live after enabling the sync workers in prod (infra#14) — S4 exit evidence on the fleet.** Next: **Paddle L7** (sandbox MCP key → $299 price object → live checkout) → console model pin. Email track (TASK-501) shipped, smoked and archived 09-29. |
| **P1** | **Fleet TO-BE alignment (Nelya)** ([TASK-495](tasks/TASK-495-fleet-to-be-alignment-nelya.md)) | ✅ **EXIT EVIDENCE MET 09-25**: reconciler-provisioned tenant `i-0289111000cebcee9` shipped two issues end-to-end; tenant id posted to Nelya for her account-side checklist (Slack draft). Old-estate backup chain verified closed 09-25. Open on her side: none blocking. Ours: infra#16 (replicas=1) via her pipeline = our dispatch. |
| **P1** | **Golden AMI refresh → first fleet tenant** ([TASK-499](tasks/TASK-499-golden-ami-refresh-first-fleet-tenant.md)) | ✅ **L3 DONE 09-25: first fleet tenant provisioned + issue→PR→merge in 4m35s on the hosted path** (defects filed: console#323 repo-switch inert · console#324 worktree parity · pilot#5459 pr_url · pilot#5460 hook cleanup). Prior: L1 ✅ (bake fetches release by version, sha256, version pin, both SSM params) · L2 ✅ `ami-01ecd7cefa1e6f316` pilot 2.276.1 · **L3 BLOCKED**: reconciler redeploy rolled back on Nelya's AZ-rebalancing template (binpack singleton); after her fix: re-run → `tenant_factory=true` (ask founder) → dogfood tenant → Docs real tree. Old AMI kept as rollback. |
| **P1** | **Paddle Billing integration** ([TASK-497](tasks/TASK-497-paddle-billing-integration.md)) | Code complete, L6 smoke passed 09-14. **L7 started 09-29: sandbox key in Doppler, sandbox price $499 `pri_01m3rf7qws3gzscj9fttg4aywe` (decided 09-30).** **Nelya template ask POSTED 09-30 09:57Z** (thread `1790762207.809729`; flag-off gated, values later). Next, founder: live $499 twin (paddle-live OAuth or dashboard) · live API key / client token / notification destination → Doppler `prd` · OK the Nelya ask for the six console-api billing values (three SSM secrets). Then point prod at live, verification readiness audit. |
| — | ~~Jev gate 1 — acceptance classifier~~ ([TASK-505](tasks/archive/TASK-505-jev-acceptance-classifier-shared-client.md)) | ✅ Archived 10-01: live in shadow since 09-30 21:09Z; flip decision ~10-07 from the per-item debug lines. |
| P2 | **Autopilot per-stage deadline + tick-liveness alert** ([#5541](https://github.com/qf-studio/pilot/issues/5541) → PR#5545) | ✅ Shipped 09-30 22:35Z, reviewed 10-01 APPROVE-w-notes. Culprit was `buildScopeMembers` serial GitHub calls (60s budget now). Follow-up [#5547](https://github.com/qf-studio/pilot/issues/5547): deadline errors must not open the per-PR circuit. |
| P3 | **gh-guard audit journals unit-test denials** ([#5542](https://github.com/qf-studio/pilot/issues/5542) → PR#5544) | ✅ Shipped 09-30, reviewed 10-01 APPROVE-w-notes: origin = the model running `go test` in the guarded session (not the quality gates); denial never relaxed; residual WARN count + untested shim-side detection noted. |
| — | ~~Jev gate 2 — base-presence path token~~ ([TASK-506](tasks/archive/TASK-506-jev-base-presence-path-token.md)) | ✅ Archived 10-01: live in shadow since 10-01 09:16Z (v2.276.12). Gate 3 (CI failure class) is the next roadmap item, unfiled. |
| **P2** | **Jev gate 3 — CI failure class** ([TASK-507](tasks/TASK-507-jev-ci-failure-class.md) → [#5555](https://github.com/qf-studio/pilot/issues/5555)) | Dispatched 10-01: choice per failed check on a redacted log tail, shadow first, live override only `code` → `infra`/`infra_billing`. Also queued: [#5553](https://github.com/qf-studio/pilot/issues/5553) test-pin bundle from the 10-01 reviews, [#5554](https://github.com/qf-studio/pilot/issues/5554) 404-eviction test flake, [#5556](https://github.com/qf-studio/pilot/issues/5556) per-item shadow lines at INFO (box never logs DEBUG), [#5557](https://github.com/qf-studio/pilot/issues/5557) credential-shaped span pre-filter + bounded key=value redaction (gate-2 first live miss, nav-research 10-01). Five PRs to review on return; #5555 needs the live smoke. **10-01 pm: #5555→PR#5561, #5556→PR#5562, #5557→PR#5563 merged; all three reviewed 10-01 pm, APPROVE-w-notes (gate 3: 10 mutations caught, live smoke run by the reviewer on the box: code/infra/infra_billing at 1.0). Follow-up [#5565](https://github.com/qf-studio/pilot/issues/5565): gate-3 per-check shadow line still at DEBUG (no flip data until merged).** Also from the reviews: [#5566](https://github.com/qf-studio/pilot/issues/5566) redaction leaks slash-containing secrets to Jev (bug) · [#5567](https://github.com/qf-studio/pilot/issues/5567) test pins · [#5568](https://github.com/qf-studio/pilot/issues/5568) evidence tail truncation + silent Not-verified live smoke. |
| **P1** | **Post-merge CI fix issues borrow the merged PR branch → false delivery / infinite poll** ([TASK-508](tasks/TASK-508-post-merge-fix-borrowed-branch.md) → [#5564](https://github.com/qf-studio/pilot/issues/5564)) | RCA 10-01: 6 post-merge fix issues in 60 days, 1 real fix. Footer carries the merged branch → dispatcher short-circuit marks the task completed with the ORIGINAL PR URL, Claude never runs; issue left open forever (#5551) or closed `pilot-done` as false success (#5560, #5385). Dispatched: A footer omits branch/pr/sha for post-merge · B short-circuit supersedes + closes when the borrowed origin PR is the merged one · C completed rows stop arming repick-backoff. Follow-ups unfiled: flake re-run before filing, ownership check in external-merge close, dedup vs human-filed. **Operator: close #5551 by hand.** |
| **P1** | **Linear Invoices → GitLab cutover** ([TASK-510](tasks/TASK-510-linear-invoices-gitlab-cutover.md); forge half supersedes [TASK-509](tasks/TASK-509-linear-invoices-onboarding.md)) | ✅ **LIVE 10-02 08:46Z on v2.277.1** (cut by hand; the train is daily 16:00 Berlin, the 10-01 "not cutting" note was a misread). [#5583](https://github.com/qf-studio/pilot/issues/5583) per-project `gitlab:` forge + [#5584](https://github.com/qf-studio/pilot/issues/5584) Linear OnPRCreated controller fix merged (PR#5585/#5586 **reviewed 10-02, APPROVE-w-notes**; follow-up [#5589](https://github.com/qf-studio/pilot/issues/5589) epic PR path; [#5588](https://github.com/qf-studio/pilot/issues/5588) poller retry; sdk#144 Linear UUID). Box: PAT + GitLab clones + Linear polling on; both GitLab `main` locked to MR-only; GitHub mirrors frozen. Founder scope: configure only, no tickets labelled by Navigator. Merges stay manual (GitLab `main` → prod on `prod-*` tags). |
| **P1** | **Included token volume — platform key, metering, own-key overage** ([TASK-502](tasks/TASK-502-included-tokens-platform-key.md)) | ✅ **LEGS 0+1+2+4 LIVE 10-06.** Leg 1 key pool: console PR#358/#359/#360 merged + reviewed; SSM + reconciler wired (Nelya infra 79a4cc4); console redeploy 37352817795 green; pool import on console-reconciler:23 → `status` free 5 / assigned 0 / retired 0. First pool assignment = a TEST org only. Leg 3 shipped 10-06 as TASK-512 (console PR#363/#364, live in dry-run). Next: leg 5 copy (moves with the Paddle catalog); WIF = leg 1d later. Org credits prepaid $9.57, auto-reload off (founder deferred). |
| ~~P1~~ | **Real allowance exhaustion on a prod test org** ([TASK-513](tasks/archive/TASK-513-allowance-exhaustion-prod-test.md), TASK-502 close-out) | ✅ **DONE 10-08** — both branches proven on prod (own-key switch; no-key suspend → EC2 stop → own-key save → resume in 36 s → PR on the own key). 3 prod defects fixed on the way (console#367/#369, pilot#5622) + AMI `make`. 8 gap issues filed (ui#205–207, console#371–374, sdk#146). |
| **P1** | **Allowance enforcement at exhaustion — own-key switch, suspend without one, per-period state** ([TASK-512](tasks/archive/TASK-512-allowance-enforcement-leg3.md), TASK-502 leg 3) | ✅ **DONE 10-07, archived.** console#361/#362/#365 → PR#363/#364/#366, ui#203 → PR#204; all reviewed; enforcement live (reconciler rev 27, prod-0.1.15, flag on, 15m poll); UI live 12a7b46. Open under TASK-502: one real exhaustion on a test org. |
| **P1** | **console-ui included allowance — Plan & Billing card, exhausted state, own-key prompt, provision gating** ([TASK-511](tasks/archive/TASK-511-console-ui-included-allowance-leg4.md), TASK-502 leg 4) | ✅ **MERGED + REVIEWED + LIVE 10-05**: ui#197/#198/#199 → PR#200/#201/#202, post-merge APPROVE-w-notes, auto UI deploy at build 07f57a7. **Real-stack verified 10-06** on the local stack: normal / exhausted-no-key / exhausted-with-key, Connections pool row, provision gating github-only. ✅ DONE, archived 10-06. |
| P2 | **Console Docs page — live verification** ([TASK-466](tasks/TASK-466-console-docs-page.md)) | Page renders through the edge (09-22 founder login) but the console DB has no tenant yet → the real `.agent`-tree proof is TASK-499 L3's acceptance (first fleet tenant). infra#51/TASK-498 closed superseded. |
| P2 | **pilot-docs → GitHub + AWS deploy** ([TASK-492](tasks/TASK-492-pilot-docs-gitlab-to-github-migration.md)) | ✅ Cutover done by Nelya 09-05; TASK-494 sync fix DONE + reviewed 09-06 (GitHub-only, preserves pilot-docs `.github/`). **Remaining = step 4 operator:** delete `GITLAB_SSH_KEY` secret · drop `gitlab:` block in hosted config · archive `quant-flow/pilot-docs`. Nelya owes: compression + CSP-RO answer. |
| **P1** | **Console rail implementation** ([TASK-478](tasks/TASK-478-console-rail-implementation.md)) | 11 approved designs → shipped surfaces. **Build-out COMPLETE 08-15 (overnight run): all 16 autopilotable legs executed + reviewed.** Blocked on founder morning sequence (approve PR#67→72→74→76 · close #73/#77 to arm retries · label ui#78 after) → then GH-69/GH-75 retries re-land · **real-stack verify batch UI-2..12** (SOP; blocked on GH-75 re-land) · CON-5 billing portal (founder Stripe gate) · copy pass ($299 · PR#72 "Includes" line · support@ mailto). Daemon-side follow-ups FILED 2026-08-20: [pilot#5027](https://github.com/qf-studio/pilot/issues/5027) merge-time ancestry check (stacked-superset guard, extends GH-4872) + [pilot#5028](https://github.com/qf-studio/pilot/issues/5028) base-presence check before claim (see pitfall `sequential-gates-on-execution-not-merge-fastfollow-misbase`; 4 family incidents in 6 days, nav-research verdict: both fixes needed). |
| **P1** | **Throughput acceleration** ([TASK-393](tasks/TASK-393-throughput-acceleration.md)) | Phase 1 (instrumentation) ✅ shipped 07-09. **M3 baseline window closed ~07-20 — histograms never harvested; phases 2–5 remain gated on that analysis.** Remaining: (2) execution lanes on `Complexity`, (3) N-concurrent per repo (`ProjectWorker` pool — note this is also the sole serialization point, see mem-101/102), (4) SHA-keyed repo primer, (5) risk-score trust tiers. Roadmap: [`throughput-roadmap.md`](system/throughput-roadmap.md) (M0–M8, D1–D6). |
| P2 | **Review-feedback trigger hardening** ([TASK-493](tasks/TASK-493-review-trigger-hardening.md)) | ✅ SHIPPED 09-06 in v2.273.0 (PR#5331–5334 via #5314); post-merge review APPROVE-w-notes → follow-up [#5337](https://github.com/qf-studio/pilot/issues/5337) (HIGH re-adoption cutoff blind spot). Also filed [#5336](https://github.com/qf-studio/pilot/issues/5336): autopilot CI-fix issues deadlock on `Depends on:` an open superseded original. From external [#5228](https://github.com/qf-studio/pilot/issues/5228): feature (bot/`COMMENTED` triggers) **DECLINED — security by design** (`pilot-never-reads-gh-comments-by-design`); its two real bugs taken: webhook path has no bot filter · reviews yank PRs out of `awaiting_approval`. Plus identity-based exclusion via `getBotLogin` and human-only revision-issue body. No config, no dashboard change. |
| **P1** | **Hosted retry path for failed executions** ([TASK-485](tasks/TASK-485-hosted-retry-path-failed-executions.md), [#5008](https://github.com/qf-studio/pilot/issues/5008)) | Daemon legs SHIPPED+REVIEWED 08-25 (PR#5214 classifier · PR#5215 stalled re-arm sweep · PR#5220 streak alert) — ride the 08-26 v2.270.0 train. **Remaining: Leg 3** (console repo: C8 dispatch on failed/stalled card cycles trigger label + removes `pilot-blocked`) **after the train lands on the box**, then **Phase 4** live validation on ship-test-js#6 (expect ≤16min re-arm latency — repick backoff gate). |
| — | ~~Wire `linear.webhook_public_key` YAML~~ | **REMOVED 2026-08-26 — the premise was false.** Verified fully wired end-to-end (see the cleared caveat above). Nothing to do. |
| P1 | Web dashboard polish | React UI functional but needs a design pass. |
| P2 | Delivery-evidence audit — false-success class ([TASK-460](tasks/TASK-460-delivery-evidence-false-success.md)) | Split from TASK-459 by founder scope call 08-08: green CI is not proof the requested change shipped (`mem-151`: scaffold-only PR merged green, parent auto-closed, zero requirements delivered). Planned, NOT dispatched; TASK-459 Phase 4's inventory hook feeds it the success-side site rows. Candidate legs: diff-surface check · ACs fail-when-unwired · epic-collapse guard. |
| P2 | E2E test suite | No integration tests — reliability untested. |
| P2 | Web dashboard auth | Token-based auth for remote access. |
| P2 | Mobile-responsive dashboard | Primary use case is phone access. |
| P3 | GitHub App auth | PAT → installable GitHub App. |
| P3 | Audit §3 Wave 4+ candidates | Not yet decomposed: `RecordAPIError` wiring beyond github · `AlertTypeOOMKilled` · multi-gate scanner phase discipline · subprocess migration end-to-end validation · `autopilot` adapter coupling refactor · SQL `withTx` helper · generic `Poller[T]` extraction · `Releaser` frozen-at-startup fix. Source: `.agent/audits/AUDIT-2026-05-25.md` §3. |

**Operator-parked (not autopilotable):** ~~restart the hosted daemon~~ **DONE 09-07 09:18Z** — box rebuilt from main (`v2.273.0-23-ge054b01c`, carries #5339–#5347) and restarted; `orchestrator.receipts_digest` active (scheduler started, catch-up digest for 09-06 delivered to Telegram 283716179; next 18:00 Europe/Berlin). daily_brief still on America/New_York (arrives 14:00 CEST). ~~`PILOT_DOCS_PAT` workflow scope~~ obsolete since TASK-494 (sync never writes `.github/`) · **domain pick** (`pilot.build` premium $720/yr · `pilot.engineering` $83 · `pilothq.dev` ~$15; = OIDC issuer, sticky) · branch protection on `qf-studio/pilot` main (TASK-405 founder decision 7 — main is currently unprotected) · EBS restore DRILL (runbook exists at pilot-cloud-infra `docs/RESTORE-RUNBOOK.md`; the drill itself is operator work) · tenant box `i-0a3bf271d598196ca` still on **2.259.3** (predates TASK-485 daemon legs — fleet images are immutable by invariant, rolling-upgrade machinery is console#216; operator binary swap is the interim for Phase 4) · rotate ALL box tracker/API tokens (founder-planned; Jira token exposed in a 08-26 session) · infra#2 Golden AMI v2 (stale claim corrected 08-26: `aws-infrastructure-pilot` IS in the box config — issue itself needs re-triage) · console#45 (`pilot-spec-incomplete`/`blocked` since 07-24 — needs rewriting into an implementable spec). NOTE 08-26: infra PR#27 `cdk deploy` DONE at some point — FleetVpc NAT verified at 1, `Environment=fleet` tag live in cost view.

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
