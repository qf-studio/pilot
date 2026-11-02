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
| `.agent/product/PRICING-MODEL.md` | **What we charge for and why** — $499 flat, 100 tickets included (Sonnet 5.5), own key past the cap; v3 (packs) researched 10-13, TASK-546. Canonical; read before any pricing conversation |
| `.agent/product/LEGAL-TERMS.md` | **Source of truth for `/terms`, `/refunds`, `/privacy` and the licence summary on pilotcloud.dev** — DRAFT 10-14, `[FOUNDER]` fields + lawyer review open; numbers live on the pricing page, never here |
| `.agent/product/UNIT-ECONOMICS.md` | Cost, margin, break-even and customer TCO per tenant — measured, not modelled |
| `.agent/tasks/TASK-XX.md` | Active task details |
| `.agent/sops/*.md` | Before modifying integrations |
| `.agent/system/references/reference_slack_notifications_routing.md` | Before touching Slack/alert/approval destinations on the box (which channel gets what, the 3 keys, restart rule) |
| `.agent/.context-markers/` | Resume after break |

### Research Reports (research/)

- `research/ai-coding-agent-products-price-extra/report.md` — Cost-plus 1-4% per ticket loses money; keep Pilot's flat plan (47 sources, 2026-10-13)
- `research/many-software-development-agencies-3-15/report.md` — Small software agencies: founder-bought tools, cheap seats, consent-gated agent adoption (45 sources, 2026-10-22)

## Current State

**Current Version:** **v2.308.0** cut by hand 11-02 11:26Z from green main bf937e9b7 (TASK-628 PR#6003 absent make target fallback + fast-follow PR#6006, TASK-620 L8 PR#6008 minors sink `file_minors` default off); box hot-upgraded by 11:29Z (`ver=2.308.0`, uptime preserved). Release-day fixes: branch protection on main (test + lint required) broke the Sync Docs Version auto-merge step → repo `allow_auto_merge` enabled 11:3xZ, PR#6009 armed to squash on green. Previous: **v2.307.0** cut by hand 11-01 16:45Z from green main dcbbd0ec5 (TASK-627 P1 PR#6000: Slack click line carries `action_ts` + `delivery_delay_ms`, studio-sdk pin v0.38.5); box hot-upgraded 17:02:46Z, 1 proc, `config reloaded`, both reviewers `passes=2`, pre-PR `mode=shadow`. Earlier 11-01 16:01Z: hand restart after a config edit — studio-sdk project got its own `quality:` block (`make test`; backup `config.yaml.bak-sdkgates-20261101`) because the global `make test-changed` gate fails on repos without that target (TASK-628). v2.306.0 cut by hand 11-01 13:15Z from green main 727094685 (TASK-626 PR#5997 reviewer residue); box hot-upgraded 13:19:25Z, 1 proc, `config reloaded`, both reviewers `passes=2`, pre-PR `mode=shadow`. v2.305.0 cut by hand 10-31 21:08Z from green main 9276b5cdf (TASK-624 D1–D4 PR#5987–#5990 + TASK-625 PR#5993 + autopilot fix PR#5994); box hot-upgraded 21:13:03Z, 1 proc, `config reloaded`, both reviewers `passes=2`, pre-PR `mode=shadow`. v2.304.0 cut by hand 10-30 16:14Z from green main 19ee1e854 (TASK-623 P2 #5981 two-pass mechanics); box 16:21:31Z with **`passes: 2` on both reviewers** (`Pre-PR review initialized … passes=2`, `post-merge reviewer enabled … passes=2`; backup `config.yaml.bak-passes2-20261030`). v2.303.0 cut by hand 10-30 13:28Z from green main 2f84f1e39 (TASK-623 #5978 two-pass reviewer; box 13:34:53Z, `passes=1` logged — box still single-pass). v2.302.0 cut by hand 10-30 10:58Z from green main 521243001 (TASK-621 #5973 live-smoke mutation exclusion, TASK-622 #5975 adjacent-code reviewer item); box 11:03:23Z after one asset-missing retry, shadow re-initialised. v2.301.0 cut by hand 10-29 20:28Z from green main 6fce37a31 (founder "let's train now"; carries TASK-620 L3 PR#5966 + fix PR#5970, L5 PR#5963, L7 PR#5969). Box: self-upgrade paused admission 20:32Z, let GH-5971 finish (20:36:39Z), hot-restarted 20:36:45Z, `Pre-PR review initialized mode=shadow` — **L1+L2+L3+L5+L7 live; enforce still off**. v2.300.0 cut by hand 10-29 16:51Z from green main a371c0cfc (TASK-620 L2 PR#5957 + its post-merge fix PR#5959; box hot-upgraded 16:55:27Z, `Pre-PR review initialized mode=shadow` — **L2 live: PR-body section, `pilot-review-rc` label, `pr_reviews` row as pre-pr-reviewer, counters**). v2.299.0 by the train 10-29 15:00Z (members PR#5945/#5948/#5950/#5951/#5953 — TASK-618 tolerant verdict parse, TASK-619 caller tracing, TASK-620 L1 pre-PR review shadow, TASK-616 G3 scope parser; box hot-upgraded 15:08Z, config reloaded, **`Pre-PR review initialized mode=shadow model=claude-opus-5-5`**). v2.298.0 cut by hand 10-29 11:42Z (`c162601d9`; box hot-upgraded 11:49Z after one asset-missing retry; carries the whole reviewer chain — TASK-613 M1/M2/M3, TASK-614, TASK-617, TASK-615 flake fix + follow-ups, TASK-616 G1/G2 changed-package gate). v2.297.0 cut by hand 10-28 14:04Z (#5902/#5904/#5905/#5907/#5909/#5912). v2.296.0 by the train 10-27 15:02Z (PR#5892–#5896 + the reviewer #5868). Earlier versions in `git tag` and the 10-27 markers.

**PRIORITY (founder directive 2026-07-26 — supersedes 07-17):** **SaaS/platform UNPARKED — TASK-405 is active work again.** The 07-17 ordering (pointer delivery → pilot reliability → SaaS parked) held while the dispatch-reliability chain was open; that chain closed with v2.246.0 on 07-25. Pointer and pilot reliability remain live tracks but no longer gate S-milestone dispatch. Memory: `founder-priority-pointer-first-saas-parked` (superseded).

**Recent (Oct 20 – Nov 2 2026; detail lives in `system/saas-roadmap.md`, `system/side-chat-navigator.md`, `tasks/archive/`, memories and git log — do not re-grow this block, replace it):**
- **11-02: v2.308.0 RELEASED, 18 TASK DOCS ARCHIVED (39 → 21), MAIN PROTECTED, L8 MINORS SINK SHIPPED.** TASK-628 closed: PR#6003 Navigator review APPROVE-w-notes (Opus pre-PR + post-merge had found 3 of 4 notes; the human pass added only test/mutation execution and one caller hop) → fast-follow [#6005](https://github.com/qf-studio/pilot/issues/6005) → PR#6006 merged in 23 min. TASK-620 L8: minor defects had no sink (only REQUEST-CHANGES files an issue) → [#6007](https://github.com/qf-studio/pilot/issues/6007) → PR#6008 merged 11-01 20:11Z: post-merge APPROVE-WITH-NOTES files one batched Navigator-template issue for filtered minors, `file_minors` default off, cap 1/project/day, label review-minor — **box opt-in pending**. Archive sweep (triage by navigator-research + GitHub state): 627/628 + 11 finished (502, 509, 582, 590, 591, 600, 603–606, 623) + 7 stalled (404 stale duplicate deleted; 489/490/491 shipped or obsolete; 492 step 4 → TASK-629 D; 393 plan stays in `system/throughput-roadmap.md`; 496 residue → TASK-584 § Carried). Repo settings: branch protection on main (test + lint required, no force-push, admins not enforced) → broke the release workflow's version-sync `gh pr merge --auto` → repo `allow_auto_merge` enabled; PR#6009 hand-merged 11:42Z. v2.308.0 cut 11:26Z, box hot-upgraded by 11:29Z. TASK-629: M1 PR#6004 + M2 site#28 merged 11-01 evening; 301 live by 12:11 CET, `Location` carries `:443` (deviation, unreported). Memories: `navigator-pr-review-executes-reviewer-not-verified-list-not-a-reread` · `gate-exit-2-no-rule-to-make-target-was-a-claude-fixable-failure` · `hasmakefiletarget-is-the-make-target-probe-never-make-n-or-q`. Open: file_minors opt-in · TASK-620 enforce read 11-05 / 20 runs (~13 so far) · console tag ruleset · board `RATE limit-hits=181` + stale autopilot row 577/GH-531.
- **11-01: SLACK CLICK LATENCY CLOSED, SDK + PILOT SHIPPED SAME DAY, GATE GAP FOUND ([TASK-627](tasks/archive/TASK-627-pr5990-approval-to-merge-latency-research.md) → [TASK-628](tasks/archive/TASK-628-quality-gate-absent-make-target-fallback.md)).** PR#5990's "≈2 h approval→merge" was 2m00s of daemon time after the click; the 2h16m sat before the click reached the daemon, founder cannot recall an earlier click, the Slack card shows the post time not the click → undecidable. Found on the way: the SDK Socket Mode client never handled Slack's `disconnect` (no `envelope_id` → dead branch, 4 blind ~11 s windows/day) and no click time was logged. S1 sdk#152 → PR#153 (founder approved over a pre-PR RC: the gate retry had rewritten the SDK Makefile) → post-merge RC auto-filed sdk#154 → PR#155 restored it → **studio-sdk v0.38.5 by hand** (`pilot release` from the SDK dir resolves the pilot repo — use `git tag` + `gh release create`). P1 pilot#5999 → PR#6000 merged 16:38Z → **v2.307.0 by hand** ("no Mondays"). Root cause of the Makefile damage = global `make test-changed` gate on a repo without the target → box override for studio-sdk + restart 16:01Z, product fix [TASK-628](tasks/archive/TASK-628-quality-gate-absent-make-target-fallback.md) → **#6002 dispatched** (fallback to the gate's default target / native command; "No rule to make target" = runner failure, no Claude retry). TASK-620 sample 10/20 two-pass runs, agreement 10/10.
- **10-27→10-30: REVIEWER PROGRAM — SEVEN CALIBRATION RUNS, PRE-PR REVIEW IN SHADOW, TWO PASSES MEET THE BAR.** 10-27 reviewer knob ON; scrappydog trio shipped (TASK-607/608/609). 10-28: TASK-610–617 (budget, worktree default, memories+checkout+confinement, severity contract, flake, adoption binding) and the box gates 22 → 3–5 min (TASK-616: `-short` + `-trimpath`, then `make test-changed`; its scope never applied until G3 #5952 fixed a `go list` last-line parse on 10-29). 10-29: Opus for the reviewer; runs 1–3 (recall 32–39 %); TASK-618 tolerant parse / TASK-619 caller tracing; founder "why review after CI at all?" → [TASK-620](tasks/TASK-620-pre-pr-reviewer-replaces-self-review-and-judge.md): **pre-PR review in shadow mode live 15:08Z** (L1), L2 section+ledger+label, L3 fix round, L5 autopilot RC hold, L7 reason ordering — all on the box by 20:36Z (v2.299.0 train, then v2.300.0/v2.301.0 by hand). First side-by-side: pre-PR and post-merge agree on 3 of 3 PRs; one blocking miss (PR#5966, expired git ctx) caught post-merge only. [TASK-621](tasks/archive/TASK-621-evidence-gate-live-smoke-hold-on-mutation-bullets.md) false live-smoke hold on mutation bullets fixed. 10-30: run 4 44.2 % → [TASK-622](tasks/archive/TASK-622-reviewer-adjacent-code-tracing-and-met-failure-case.md) adjacent-code rule → run 5 43.3 % (flat: prompt rules move *which* defects, not how many) → [TASK-623](tasks/archive/TASK-623-reviewer-two-pass-hunk-and-adjacent-union.md) **two passes** → run 6 **51.0 %** → P2 mechanics fix → run 7 **51.0 % stable, 0 pass failures**; **box on `passes: 2` for both reviewers since 16:21Z (v2.304.0)**; calibration track closed (bench report §"Decision inputs after seven runs"). Founder: keep shadow until 20 two-pass runs or 5 working days (≈ 11-03). Box lint pinned to CI's golangci-lint v2.8.0. Lessons in the 10-29 marker: dispatch probes (receiver form, anchored `absent`), no shell operators in evidence commands, stuck check-run recurrence (job rerun), docs pushes need a foreground `git push` for the fast path. **10-31: the four daemon defects traced, dispatched and ALL MERGED same day as [TASK-624](tasks/archive/TASK-624-daemon-defects-stuck-checkrun-restart-repick-hold-binding-body-cut.md) #5983–#5986 → PR#5987–#5990; review notes → [TASK-625](tasks/archive/TASK-625-task624-review-notes-startup-label-strands-crash-orphans.md) #5991 (one D2 regression: startup label guard strands crash orphans) → PR#5993; v2.305.0 by hand 21:08Z, box 21:13Z; then reviewer residue [TASK-626](tasks/archive/TASK-626-reviewer-residue-union-tolerance-empty-claim-marker-paren-exact-lookup-nv-count.md) → #5996** (stuck check-run = `mapCheckStatus` ignores `conclusion` under `in_progress`; restart re-pick = boot reconcile has no open-PR heal + `StartupRecover` strips the label blind; hold binding = `is:pr #N` search, mention-only; body cut = claim text left in `Location`, 200-byte literal cut). Open founder items: `executor.timeout.complex` 60→90m · branch/stash purge · TASK-596 P1 · Nelya PR#577 + infra#23 · PR#5950's two reviewer notes · release notices since v2.298.0 · nine verified NEW-VALIDs in the bench report.
- **10-22: WAVE 0–1 SHIPPED AND FULLY REVIEWED — 18 LEGS, 10 MERGES REVIEWED, 3 REQUEST-CHANGES, 26 FOLLOW-UPS FILED, P1 RE-ARMED.** Morning: all 18 wave-0/1 issues filed title-only (`nav-dispatch-legs.py` ended legs at `## `) → parser fixed `f4a456352`, re-armed 10:58Z; by 13:30Z autopilot had merged 14 unreviewed. Afternoon/evening: every wave-0/1 merge now has a posted review — site#17–#20 APPROVE(-w-notes) · console#427 (C7) APPROVE-w-notes · ui#254 (U3) + ui#246 (U9) + pilot#5799 (P9) + pilot#5822 (P10) APPROVE-w-defects · **REQUEST-CHANGES ×3: ui#252 (U2 epic), ui#261 (U4 epic), ui#255 (U5)** — all three bind UI to wire fields no backend sends (`orgName`/`poolPercentUsed` on the dashboard DTO; `project`/`parentId`/`durationMs` on board cards; a `graph` prop nothing passes); CI green because unit tests mock the adapter (**mem-274**, four instances in one wave incl. console#426). Follow-ups filed: ui#262–#266, #271, #272 · console#430–#432, #436 (landed via autopilot fix PR#439) · pilot#5810 (P17 subagent events, design), #5812–#5814 (P9 fixes, all merged same evening as PR#5823/#5830/#5834), #5816 (P18 docs/graph summary), #5837/#5838 (P10 `kind` filter, example config). Six ui fix PRs (#267–#270, #273, #274) merged within the hour, unreviewed. Phase A items 1–2 done: pilot#5820 docs Cloud copy (S9), pilot#5821 `modelPricing` (P15 — Opus 5.5 $4/$20, Sonnet 5.5 + Sonnet 5 $2/$10, Haiku 5.5 $0.10/$0.50, cache-read per model). Queue: U4 reached main through stacked PR#261 (PR#260 closed, ui#257 done); **P1 PR#5805 held `needs-manual-rebase` → closed, #5790 re-armed, queued 18:51Z**; P16 split #5807/#5809 merged, #5808 on retry; P11 fix #5819 merged (gate run on the box after the next train); v2.293.0 cut 14:27Z (P9 + pen-test harness). Prior detail (failure research mem-268..273, TASK-589 daily-cap decision, console#428 operator step) in marker `2026-10-22_wave01-ran-reviews-failure-research-task589.md`. Still open from this day: P11 `make pentest` on the box; console#428 operator step; Phase A items 3–5; founder inputs F1/F4/D1–D7/SES.
- **10-21→10-22: PILOT CLOUD POSITIONING DECIDED, 71-TICKET ROADMAP, WAVES 0–1 DISPATCHED ([TASK-584](tasks/TASK-584-pilot-cloud-execution-roadmap.md)).** Founder session → `system/project/project_pilot_cloud_positioning.md` §1–11: identity ("hosted autonomous engineer … on a dedicated box, for small teams with more tickets than engineers"), research→tickets thesis (spec for the executor; feedback loop = moat), proof ranking (lead: 1,653/2,212 merged PRs on this repo from `pilot/*`), segments agencies → product teams → PM-only, offer `$499 · 100 shipped tickets · no seats · your own box`, **$300 fair-use ceiling on 30-day ledger evidence** (mean $0.96/run, p50 $0.52), no cost on platform-key surfaces, onboarding (interview vs scan, deploy templates), specs 8.1–8.8, post-launch scopes, market §11 with a sourced deep-research block (mem-252..260). Console design reviewed (Figma `BfAqtVPBp0i4iWHg2jzAnB`). `system/project/project_pilot_cloud_roadmap_tickets.md` = 71 tickets per repo. **Dispatched 10-22: 18 issues** — site #12–#16 (TASK-582), ui#239–#244, pilot#5790–#5793, console#422–#424 (TASK-583) via `scripts/nav-dispatch-legs.py` (LEG sections → gh, path pre-flight; mem-264). TASK-581 bootstrap cost harness → #5788 → PR#5789 merged. Memories mem-251, mem-261..264. Open: legal facts (F1), Nelya deploy PR (F4), TASK-581 run (F3), Figma D1–D7, 8 held wave-1 legs, Phase A items 1–2.
- **10-20 am: #5770 UN-PARKED (OUR OWN BASE-PRESENCE MISS) · CONSOLE#390 PREMISE DEAD, RE-SCOPED + DISPATCHED · GOLDEN-AMI GAP FOUND.** [#5770](https://github.com/qf-studio/pilot/issues/5770) (TASK-577) sat `pilot-needs-human` 11 h: the task doc backticked the two new files it asks for → gate hold → park (pitfall `issue-body-backticked-paths-not-on-main-trip-base-presence-hold`, 8th). Seven tokens → prose, label off. [console#390](https://github.com/qf-studio/pilot-console/issues/390): nav-research + two live checks (GH-5651 first tag **v2.282.0**; tenant `i-0289111000cebcee9` on **2.289.0**, hot self-upgraded 10-19 14:06Z) killed the "train never reaches tenants" premise; founder 10-13 decision stands (daemon self-upgrade = tenant path). Re-titled + re-scoped to **observed Pilot version per tenant** (`/health` in the existing ready probe → migration 0030 `observed_pilot_version` → `consolectl status` VERSION → floor warning `PILOT_CONSOLE_FLEET_MIN_PILOT_VERSION`), 701 words, gate-clean, labeled `pilot`+`no-decompose`. **New infra gap:** reconciler AMI `ami-06825978577ce40d4` bakes pilot **2.277.4** (< v2.282.0), no S3 override → a tenant provisioned today never self-upgrades until one SSM hop. Needs an AMI refresh in `aws-infrastructure-pilot` (not filed yet). SOP `tenant-binary-upgrade-ssm.md` re-headed as LEGACY. Memory `verify-issue-premise-against-live-fleet-before-dispatch`. **Reviews 10-20 09:xx:** [PR#5775](https://github.com/qf-studio/pilot/pull/5775) (#5770 sweeper) merged 08:23Z, post-merge **APPROVE-w-notes** → D1 `ListBranches` paginates `matching-refs`, which ignores `per_page` → [#5777](https://github.com/qf-studio/pilot/issues/5777). [console PR#417](https://github.com/qf-studio/pilot-console/pull/417) (#390) **REQUEST-CHANGES** (wire-contract test missing + snake_case json tags; fleet side sound), parked `pilot-needs-human` by a daemon bug: GH-5468 fails the gate on unlisted checks but evidence collectors still allowlist-scope → zero evidence → [#5776](https://github.com/qf-studio/pilot/issues/5776) (`pilot`); box mitigation = per-project `ci_checks.required_checks: []` for the 3 console projects. [infra#23](https://github.com/qf-studio/aws-infrastructure-pilot/issues/23) golden-AMI refresh filed, but the box does not poll that repo → route to Nelya or add the project. **PR#417 hand-fixed 10-20 08:50Z** (founder call): camelCase tags + `TestHandleListObservedPilotVersionJSON`, pushed `94e2bea`; autopilot re-adopted (redrive 1/2), `test` flaked (shared-Postgres TRUNCATE race, memory `console-ci-shared-postgres-truncate-races-instances-e2e`), size guard re-parked it, rerun → all 5 checks green → **squash-merged by hand 09:29Z (`f3c8555c1`), #390 closed**; next: deploy + `consolectl status` shows `2.289.0`. infra#23 **handed to Nelya 10:05 CEST** (thread ts `1792489082.804169`); the box does not poll `aws-infrastructure-pilot`. **Both follow-ups shipped + post-merge reviewed before noon:** [#5776](https://github.com/qf-studio/pilot/issues/5776) → [PR#5778](https://github.com/qf-studio/pilot/pull/5778) merged 09:10Z, **APPROVE-w-notes** (one `failureInScope` predicate for gate + 3 collectors, both mutations caught; stale doc comment only) · [#5777](https://github.com/qf-studio/pilot/issues/5777) → [PR#5779](https://github.com/qf-studio/pilot/pull/5779) merged 09:27Z, **APPROVE** (single request + dedupe, request count asserted, 3 mutations caught). All three (#5775/#5778/#5779) ride v2.290.0 at 14:00Z. **Train landed 14:10Z (hot restart); post-train config applied 15:04Z** (backup `config.yaml.bak-20261020-train290`): `lifecycle_notifications: true` · `stale_branch_sweep: true` · `required_checks: []` on the 3 console projects; **restarted 15:06Z, verified 15:09Z** (1 proc, 2.290.0, queue 0, Slack ingress OK, no lifecycle WARN). **First sweep deleted 46 orphaned `pilot/*` branches across 10 projects, 0 errors; all repos now at 0 `pilot/*` refs.** No open founder calls on this line.
- **10-20 pm: FOLLOW-UPS SHIPPED, CONSOLE TEST-DB FLAKE CLOSED, QUEUE PANEL GROUPED.** [#5781](https://github.com/qf-studio/pilot/issues/5781) → PR#5782 merged (sweep logs carry `repo`, empty-train skip at Info; review APPROVE-w-notes: Info also fires on every empty daily tick). Its post-merge CI red was a pre-existing race in the epic-parent pass deadline accounting (`TestEpicParentPass_DeadlineLogsAndCounts`) → autopilot #5784 → PR#5786 (production change in `controller.go`, unreviewed). [console#418](https://github.com/qf-studio/pilot-console/issues/418) → PR#419 per-package test databases: held as draft, two revisions ([TASK-578](tasks/archive/TASK-578-console-419-revision-test-db-lock.md) → console#420, [TASK-580](tasks/archive/TASK-580-console-419-revision-2-scratch-migrate-lock.md) → console#421) after a local `-count=3` reproduced `tuple concurrently updated` (migration 0014 `ALTER ROLE` from unlocked scratch-DB migrations); final: every test migration under one base-DB advisory lock, CI green 3/3 on fresh Postgres, merged 17:37Z. [TASK-579](tasks/archive/TASK-579-dashboard-queue-grouped-by-project.md) → #5783 → PR#5785: queue panel grouped by project (3 × 3, `+N projects`, dim header with phase/age, finished rows `run · ago`, `TaskDisplay` timestamps, selection by ID), review APPROVE. Lessons: the executor *session* has no `DATABASE_URL` (the box itself runs a shared Postgres for the gate — corrected 10-22), so Pilot's pasted DB-test evidence is invalid — verify on CI reruns; nav-task ID generator ignores `tasks/archive/` (returned the archived TASK-577).
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
- **Positioning + roadmap of record (2026-10-21/22)**: [`system/project/project_pilot_cloud_positioning.md`](system/project/project_pilot_cloud_positioning.md) (decisions §1–11) · [`system/project/project_pilot_cloud_roadmap_tickets.md`](system/project/project_pilot_cloud_roadmap_tickets.md) (71 tickets per repo, filed status) · [`tasks/TASK-584-pilot-cloud-execution-roadmap.md`](tasks/TASK-584-pilot-cloud-execution-roadmap.md) (phases A–H, triggers, owners) · wave docs [TASK-582](tasks/archive/TASK-582-wave0-site-unblock-the-sale.md) (site) · [TASK-583](tasks/TASK-583-wave1-design-update-proof-surface.md) (design + proof)
- **Program doc**: [`tasks/TASK-405-pilot-saas-platform.md`](tasks/TASK-405-pilot-saas-platform.md)
- **Design**: [`system/saas-architecture.md`](system/saas-architecture.md) · [`saas-kanban-sync-design.md`](system/saas-kanban-sync-design.md) · [`saas-fleet-design.md`](system/saas-fleet-design.md) · [`saas-asset-research.md`](system/saas-asset-research.md)
- **New repos** (created 2026-07-14, in `~/.pilot/config.yaml`): `qf-studio/pilot-console` (Go control plane) · `pilot-console-ui` (Vue3/Vite/Bun SPA) · ~~`pilot-cloud-infra` (Go CDK)~~ **archived 2026-09-23** (estate deleted; fleet infra = Nelya's `aws-infrastructure-pilot`) — each has its own `CLAUDE.md`
- **Latest handoff marker**: `.agent/.context-markers/2026-11-02_release-v2.308.0-archive-sweep-main-protected-l8-shipped.md` (local, untracked; previous: `2026-10-26_review-policy-change-c1-c2-u6-u8-merged-c14-refiled-reviewer-dispatched.md`)
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
| **P1** | **Executor fast-follow track — self-close hand-off re-pick, bootstrap gate follow-up** ([TASK-573](tasks/archive/TASK-573-selfclose-handoff-marks-original-superseded.md) · [574](tasks/archive/TASK-574-bootstrap-callsite-test-and-attempt-guard-key.md)) | ✅ 10-17: #5741 → PR#5743 merged 13:35Z, **reviewed 10-19 APPROVE-w-notes** (supersede at both self-close sites, 3 pins hold; notes: closed original keeps `pilot` labels, silent no-op on a `running` row) → [TASK-575](tasks/archive/TASK-575-selfclose-supersede-running-row-and-closed-original-labels.md) → #5747 → PR#5751 merged 10-19 11:09Z, reviewed APPROVE-w-notes (closed+no-overlap now flagged — correct; "leaving open" wording stale; CAS-reject ERROR log per hand-off). TASK-573 archived. TASK-574 → #5745 → PR#5750 merged 10-19 10:38Z, **reviewed APPROVE-w-notes**, archived. Done 10-17: PR#5740 reviewed APPROVE-w-notes, PR#5742 APPROVE-w-defects, 571/572 archived. Open: Slack approve click not received (unfiled); infra re-pick cap 8 with no backoff (memory). |
| P3 | **Post-merge CI fix — three unfiled follow-ups** ([TASK-508](tasks/TASK-508-post-merge-fix-borrowed-branch.md)) | #5564 shipped. Unfiled: flake re-run before filing (code class) · ownership check in `checkExternalMergeOrClose` (PR merged before the issue existed) · dedup vs human-filed fix issues (`findExistingFixIssue` is exact-title only). |
| **P1** | **Pricing v3 — honest allowance + prepaid overage packs** ([TASK-546](tasks/TASK-546-pricing-v3-honest-allowance-overage-packs.md)) | 🟢 **11-02: all dispatched legs merged** — pilot #5676/#5677/#5678, console #401–#404 + **#424 ($300 ceiling, packs 25/$99 · 100/$349, 10-22)**, ui#231; daily cap moved to the daemon (TASK-589). Unfiled: pack checkout endpoint + webhook credit, Buy 25/100 UI, copy/catalog/docs (Phase 5). Price stays $499; D1–D11 price-bearing decisions open. |
| **P1** | **Legacy repo bootstrap — real Navigator context on first attach, persisted, priced** ([TASK-549](tasks/TASK-549-legacy-repo-navigator-bootstrap.md)) | 🚀 10-14: #5679 (bootstrap session, trigger default manual) + #5680 (copy-back) dispatched. Verified: scaffold's injected context sections are placeholders nothing fills; no copy-back from worktrees; chat turns are cold research sessions. Next: cold-start bench (Navigator job), then D1–D3 (auto trigger, console snapshot, 10-ticket price). |
| **P1** | **Reviewer program — pre-PR review replaces self-review + judge; post-merge becomes a sampled audit** ([TASK-620](tasks/TASK-620-pre-pr-reviewer-replaces-self-review-and-judge.md) · [TASK-612](tasks/TASK-612-post-merge-reviewer-calibration.md) · [TASK-618](tasks/archive/TASK-618-reviewer-tolerant-verdict-parse-raw-on-unknown-evidence-minor.md) · [TASK-619](tasks/archive/TASK-619-reviewer-caller-tracing-cited-met.md)) | 🚀 10-29 14:30Z: reviewer live on **claude-opus-5-5**; 3 calibration runs (RC 3/3, false 0.07, recall 32–36 % — bar 0.5 not met); **#5946 → PR#5948 (13:05Z) and #5947 → PR#5950 (13:59Z) merged, both Opus APPROVE-w-notes** (PR#5950 notes unfiled: NOT-VERIFIED cap overwrites a real entry; an uncited [blocking] "met" defect is demoted); TASK-618/619 archived; **calibration run 4 (20:58Z, v2.301.0): RC 3/3 · recall 32→44 % (bar 0.5 still not met) · false 0.07 · 0 format failures · over-escalation 5→3 · files_read 5.0 — every structural defect of run 3 fixed; the miss class (adjacent code outside the hunks) is unchanged. **Run 5 (10-30, TASK-622 item 2b): 43.3 % — flat; prompt rules move which defects are found, not how many → next lever is budget-shaped. Run 6 (10-30 14:13Z, TASK-623 two passes): 51.0 % — the bar is met by the union of a hunk pass and an adjacent pass. **Run 7 (10-30 16:55Z, after the P2 mechanics fix): 51.0 % again, 0 pass failures — stable; box on two passes for both reviewers since 16:21Z; calibration track closed.** **TASK-620 L1 #5949 → PR#5951 merged 14:42Z, Opus APPROVE-w-notes** (notes: no test reaches the runner's runPrePRReview call; judge-off gate untested; mode≠off with no claude binary leaves a run with no in-run review; memory recall gated on MemoryInjection) · **box `executor.pre_pr_review.mode: shadow` + Opus live since 15:08Z** (backup `config.yaml.bak-prepr-shadow-20261029`); **L2 #5956 → PR#5957 merged 16:05Z**; **first shadow verdict 15:50Z: REQUEST-CHANGES (1 blocking, 1 minor, 6 files, 51 s) — run pushed and merged anyway (shadow)**; L1 persists counts only, so no text to compare until L2's section is on the box; post-merge Opus on PR#5957 REQUEST-CHANGES (counter never incremented in prod; test self-injects) → daemon fix #5958 → PR#5959 merged 16:38Z (APPROVE-w-notes; its own shadow verdict: APPROVE-w-notes 0 blocking/2 minor, 50 s) → **v2.300.0 by hand 16:51Z, box 16:55Z — L2 LIVE**; **L3 #5961 (enforce fix round) + L5 #5962 (autopilot escalate on RC) dispatched 17:10Z; L5 → PR#5963 merged 17:49Z = first PR with the section + first side-by-side: pre-PR (58 s, shadow) and post-merge both APPROVE-w-notes with the same two defects** (escalation-reason ordering breaks the live-smoke bullet parser; hold event untested → **L7 #5967 filed**); **L3 → PR#5966 merged 19:01Z: first pre-PR MISS of a blocking defect** (fix round reads HEAD on an expired 30 s git ctx → every real fix rolls back; pre-PR 0/2 runs, post-merge 1/1 → daemon fix #5968); box lint gate cannot predict CI lint (2.14.0 vs v2.8.0, `required: false`); then enforce (exit criteria, founder) → L4 → L6, L2→L5∥L3→enforce→L4→L6; box step after L1+L2: `executor.pre_pr_review.mode: shadow`. TASK-591 ✅ archived-in-place (knob ON 10-27). **11-01 enforce sample read: 7 of 20 two-pass runs, all five criteria green (0 errors · 7/7 agree · RC 14 % · median +2.6 min · 0 CreatePR failures); next read at 20 runs or 11-05 (5th working day; the doc's ≈ 11-03 was an estimate). Then stage 3, then L4 + L6 (written, not dispatched).** · **L8 ✅ SHIPPED 11-01 — [#6007](https://github.com/qf-studio/pilot/issues/6007) → PR#6008, in v2.308.0, `file_minors` OFF on the box (opt-in pending)**: minors sink — post-merge APPROVE-WITH-NOTES files one batched Navigator-template issue for filtered `[minor]` defects, `file_minors` default off, cap 1/project/day, label review-minor (PR#6003 had 7 minors, 2 real, hand-filed #6005); reviewer gaps section added to the task doc. |
| **P1** | **Box quality-gate speed — changed-package gate** ([TASK-616](tasks/TASK-616-box-quality-gate-speed.md)) | ✅ interim `go test -short` + `-trimpath` 10-28: gates 22 → 3–5 min; G1/G2 merged; box on `make test-changed` since 10-29 11:49Z — **measured 10-29 14:00Z: the scope NEVER applied** (`listPackages` trims the whole `go list` output; the last package, tests/pentest/cmd/pentest with no tests, loses its empty columns → "unexpected line" → `full` on every gate; GH-5946 3m08 · GH-5947 5m17 fail → 4m21, all full suite under -short) → **G3 #5952 → PR#5953 merged 14:47Z** (Opus APPROVE-w-notes: Errorf vs Fatalf on the sort-order guard; listPackages-at-repo-root not verified by a test), in v2.299.0 on the box since 15:08Z. **Scope LIVE 15:46Z: GH-5956 `package_count=27 full=false` (executor diff), 3m08** — executor diffs stay ~3 min (executor is the wall-bound package); a leaf diff is the unmeasured win. CI (12 min) is now the long pole. |
| **P1** | **Pilot Cloud wave 1 — design update + proof surface** ([TASK-583](tasks/TASK-583-wave1-design-update-proof-surface.md)) | 🚧 **11-02: wave-1 code complete except C6/U11/U12 (gate: Nelya I3 SES); exit evidence open** — merged + live: U1–U9, U19, X2, W1 (UI at main `659063ae5`, deployed 10-27) · C1, C2, C3, C7, C8, C14, X1 (console `prod-0.1.51` = main) · P1, P9, P10, P16, P18, P11 fix. **Not met**: P11 real-run report on pilot#5793 (hard gate) → I2 pen test (Nelya); canary checks of Run Story / share / Context / cap banner after 10-27. Open: pilot#5810 (P17, design), ui#266 (decision), D1–D5/D7 Figma, C14 review notes unfiled (RLS on `ticket_pack_draws`, 0037 down path, ordinal read outside the org lock). Review policy since 10-26: tier-1 merges only. History: TASK-583 line 3 / git log. |
| **P1** | **Pilot Cloud wave 2 — research → tickets** ([TASK-585](tasks/TASK-585-wave2-research-to-tickets.md)) | 🚀 **11-02: DISPATCHED** — P2 pilot#6010 · P3 pilot#6011 · C5 console#459 · C10 console#460 (D1–D4 accepted); C4/U10 held on upstream merges. Six legs written (P2 drafts + shippable check · P3 reject → learning · C5 draft store + approve → `pilot` issue · C4 reject write-through · C10 research cap 50 · U10 Create tickets + draft review); dry-run pre-flight green for P2/P3/C5/C10, C4/U10 held on upstream pins. Seam findings: no `mode` on any chat wire (pills cosmetic → D4 hide), report not stored (sent inline), reject reason dropped by the console today, no Go memory-file writer (D2: SQLite now). Next: review C5/C10 pre-merge; file C4 after P3+C5, U10 after P2+C5. |
| **P1** | **Bootstrap unit economics — deep-research cost per repo size, cap, degrade path** ([TASK-581](tasks/TASK-581-bootstrap-unit-economics.md)) | 🚀 10-21: harness dispatched → [#5788](https://github.com/qf-studio/pilot/issues/5788) (runner over 4 public repos S/M/L/XL × 3 stages, prompt files, report tool, fixture tests; no live spend in the executor). Live 30-session matrix = operator step on the box (≤ $250 guard). Provisional cap $25 soft / $40 hard per repo; degrade = scope by churn → sample → min 5 tickets. D1–D5 open for the founder. Found: `modelPricing` prices Opus 5.5/Sonnet 5.5 ~25–50% high (unfiled). |
| **P1** | **Pilot SaaS platform** ([TASK-405](tasks/TASK-405-pilot-saas-platform.md)) | S0–S2 ✅ · S3 exit met local-first 08-18 · S4 build complete · **S4 exit path UNBLOCKED 09-21: console-api + reconciler LIVE on Nelya's `pilot-fleet` (ECS, `prod-0.1.0`, factory off)** — the minimal-EC2 route ([TASK-490](tasks/archive/TASK-490-s4-minimal-invpc-console.md)) is superseded by the ECS transfer. **Domain decided `pilotcloud.dev`.** **09-25: first fleet tenant provisioned + shipped twice on the hosted path; board live after enabling the sync workers in prod (infra#14) — S4 exit evidence on the fleet.** Next: **Paddle L7** (sandbox MCP key → $299 price object → live checkout) → console model pin. Email track (TASK-501) shipped, smoked and archived 09-29. |
| **P1** | **Fleet TO-BE alignment (Nelya)** ([TASK-495](tasks/TASK-495-fleet-to-be-alignment-nelya.md)) | ✅ **EXIT EVIDENCE MET 09-25**: reconciler-provisioned tenant `i-0289111000cebcee9` shipped two issues end-to-end; tenant id posted to Nelya for her account-side checklist (Slack draft). Old-estate backup chain verified closed 09-25. Open on her side: none blocking. Ours: infra#16 (replicas=1) via her pipeline = our dispatch. |
| **P1** | **Paddle Billing integration** ([TASK-497](tasks/TASK-497-paddle-billing-integration.md)) | Code complete, L6 smoke passed 09-14. **L7 started 09-29: sandbox key in Doppler, sandbox price $499 `pri_01m3rf7qws3gzscj9fttg4aywe` (decided 09-30).** **Nelya template ask POSTED 09-30 09:57Z** (thread `1790762207.809729`; flag-off gated, values later). **10-09: live $499 twin `pri_01m4g870rh98m207cjwhjn8taq` + live client token created via paddle-live MCP; token in Doppler `prd`.** Next, founder (dashboard): live API key · notification destination (`console.pilotcloud.dev/api/v1/billing/webhook`, `subscription.*` + `transaction.completed`) · submit checkout domain · payment link. Nelya template ask unanswered since 09-30 — nudge. Then point prod at live, readiness audit. |
| P2 | **Console Docs page — live verification** ([TASK-466](tasks/TASK-466-console-docs-page.md)) | Page renders through the edge (09-22 founder login) but the console DB has no tenant yet → the real `.agent`-tree proof is TASK-499 L3's acceptance (first fleet tenant). infra#51/TASK-498 closed superseded. |
| **P1** | **Console rail implementation** ([TASK-478](tasks/TASK-478-console-rail-implementation.md)) | 11 approved designs → shipped surfaces. **Build-out COMPLETE 08-15 (overnight run): all 16 autopilotable legs executed + reviewed.** Blocked on founder morning sequence (approve PR#67→72→74→76 · close #73/#77 to arm retries · label ui#78 after) → then GH-69/GH-75 retries re-land · **real-stack verify batch UI-2..12** (SOP; blocked on GH-75 re-land) · CON-5 billing portal (founder Stripe gate) · copy pass ($299 · PR#72 "Includes" line · support@ mailto). Daemon-side follow-ups FILED 2026-08-20: [pilot#5027](https://github.com/qf-studio/pilot/issues/5027) merge-time ancestry check (stacked-superset guard, extends GH-4872) + [pilot#5028](https://github.com/qf-studio/pilot/issues/5028) base-presence check before claim (see pitfall `sequential-gates-on-execution-not-merge-fastfollow-misbase`; 4 family incidents in 6 days, nav-research verdict: both fixes needed). |
| **P1** | **Hosted retry path for failed executions** ([TASK-485](tasks/TASK-485-hosted-retry-path-failed-executions.md), [#5008](https://github.com/qf-studio/pilot/issues/5008)) | Daemon legs SHIPPED+REVIEWED 08-25 (PR#5214 classifier · PR#5215 stalled re-arm sweep · PR#5220 streak alert) — ride the 08-26 v2.270.0 train. **Remaining: Leg 3** (console repo: C8 dispatch on failed/stalled card cycles trigger label + removes `pilot-blocked`) **after the train lands on the box**, then **Phase 4** live validation on ship-test-js#6 (expect ≤16min re-arm latency — repick backoff gate). |
| P1 | Web dashboard polish | React UI functional but needs a design pass. |
| P2 | Delivery-evidence audit — false-success class ([TASK-460](tasks/TASK-460-delivery-evidence-false-success.md)) | Split from TASK-459 by founder scope call 08-08: green CI is not proof the requested change shipped (`mem-151`: scaffold-only PR merged green, parent auto-closed, zero requirements delivered). Planned, NOT dispatched; TASK-459 Phase 4's inventory hook feeds it the success-side site rows. Candidate legs: diff-surface check · ACs fail-when-unwired · epic-collapse guard. |
| P2 | E2E test suite | No integration tests — reliability untested. |
| P2 | Web dashboard auth | Token-based auth for remote access. |
| P2 | Mobile-responsive dashboard | Primary use case is phone access. |
| P3 | GitHub App auth | PAT → installable GitHub App. |
| P3 | Audit §3 Wave 4+ candidates | Not yet decomposed: `RecordAPIError` wiring beyond github · `AlertTypeOOMKilled` · multi-gate scanner phase discipline · subprocess migration end-to-end validation · `autopilot` adapter coupling refactor · SQL `withTx` helper · generic `Poller[T]` extraction · `Releaser` frozen-at-startup fix. Source: `.agent/audits/AUDIT-2026-05-25.md` §3. |

**Operator-parked (not autopilotable):** TASK-576 leg D box steps (enable the opt-in Telegram PR-notice key; correct the Slack-routing reference row) — doc archived 10-26, steps still open · ~~restart the hosted daemon~~ **DONE 09-07 09:18Z** — box rebuilt from main (`v2.273.0-23-ge054b01c`, carries #5339–#5347) and restarted; `orchestrator.receipts_digest` active (scheduler started, catch-up digest for 09-06 delivered to Telegram 283716179; next 18:00 Europe/Berlin). daily_brief still on America/New_York (arrives 14:00 CEST). ~~`PILOT_DOCS_PAT` workflow scope~~ obsolete since TASK-494 (sync never writes `.github/`) · **domain pick** (`pilot.build` premium $720/yr · `pilot.engineering` $83 · `pilothq.dev` ~$15; = OIDC issuer, sticky) · branch protection on `qf-studio/pilot` main (TASK-405 founder decision 7 — main is currently unprotected) · EBS restore DRILL (runbook exists at pilot-cloud-infra `docs/RESTORE-RUNBOOK.md`; the drill itself is operator work) · tenant box `i-0a3bf271d598196ca` still on **2.259.3** (predates TASK-485 daemon legs — fleet images are immutable by invariant, rolling-upgrade machinery is console#216; operator binary swap is the interim for Phase 4) · rotate ALL box tracker/API tokens (founder-planned; Jira token exposed in a 08-26 session; **10-19: the laptop config's Slack bot+app, Telegram bot and Discord bot tokens printed into a session transcript — rotate all four**) · infra#2 Golden AMI v2 (stale claim corrected 08-26: `aws-infrastructure-pilot` IS in the box config — issue itself needs re-triage) · console#45 (`pilot-spec-incomplete`/`blocked` since 07-24 — needs rewriting into an implementable spec). NOTE 08-26: infra PR#27 `cdk deploy` DONE at some point — FleetVpc NAT verified at 1, `Environment=fleet` tag live in cost view.

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
