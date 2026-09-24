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
| `.agent/product/PRICING-MODEL.md` | **What we charge for and why** — flat fee + BYO tokens + Sonnet 5 default. Canonical; read before any pricing conversation |
| `.agent/product/UNIT-ECONOMICS.md` | Cost, margin, break-even and customer TCO per tenant — measured, not modelled |
| `.agent/tasks/TASK-XX.md` | Active task details |
| `.agent/sops/*.md` | Before modifying integrations |
| `.agent/system/references/reference_slack_notifications_routing.md` | Before touching Slack/alert/approval destinations on the box (which channel gets what, the 3 keys, restart rule) |
| `.agent/.context-markers/` | Resume after break |

## Current State

**Current Version:** box runs **v2.276.3** (09-24; v2.276.2 = GH-5449 fetch fix PR#5457 on 09-23). Previous: **v2.276.1** (2026-09-14 14:11Z; carries the acceptance-evidence gate #5439/#5440, the argv-exec + redaction pin #5443, and auto-preserve escalation #5446). Previous: **v2.276.0** (09-13), **v2.275.0** (09-10). Prior context in the archived markers.

**PRIORITY (founder directive 2026-07-26 — supersedes 07-17):** **SaaS/platform UNPARKED — TASK-405 is active work again.** The 07-17 ordering (pointer delivery → pilot reliability → SaaS parked) held while the dispatch-reliability chain was open; that chain closed with v2.246.0 on 07-25. Pointer and pilot reliability remain live tracks but no longer gate S-milestone dispatch. Memory: `founder-priority-pointer-first-saas-parked` (superseded).

**Recent (Aug 25 – Sep 21 2026; detail lives in `system/saas-roadmap.md`, `system/approval-architecture-roadmap.md`, `tasks/archive/`, and git log — do not re-grow this block, replace it):**
- **09-24: FIRST CLIENT ENGAGEMENT LIVE — and the Pilot gap it exposed.** Fixed-price SOW sent to a client (details live in the private client workspace, not here); the first PR into the client's repo had to be done **by hand** because Pilot cannot run on a `master`-default repo yet → [TASK-500](tasks/TASK-500-client-repo-onboarding-master-default.md) planned (worktree base hardcoded to `origin/main`, no branch/PR-title templates, no CLI `--branch/--body-file`, autopilot only global; **plus a no-history-rewrite rule for client repos** — the ci-fix continuation path force-resets branches today). Not dispatched.
- **09-23 pm→09-24: FALSE SUCCESS CAUGHT, REAL CONSOLE RUN STARTED, FIRST PROVISION ONE CLICK AWAY.** Reconciler on the new AMI (Nelya: rebalancing off) → factory ON (task def rev 6). Founder's "instance is active" turned out to be **the UI mock build in production** (deploy never set `VITE_API_MODE=http`; live bundle had 0× `/api/v1/`) — every browser milestone since 09-22 was fixtures, the "email not a gate" claim is withdrawn (pitfall mem-195; fix [ui#170](https://github.com/qf-studio/pilot-console-ui/issues/170)→PR#171 + bundle guard, live 961bd42). Real run 09-24: register → GitHub connect failed on **console-api `ssm:PutParameter`** (Nelya granted 10:47Z) → both secrets real in SSM → repo `pilot-ship-test-go` (CI added over SSH; `gh` token lacks `workflow` scope) → Provision 14:21Z → reconciler created `pilot-tenant-425f9f99-…` then **`iam:DetachRolePolicy` denied** (Nelya granted 14:42Z; code fix [console#320](https://github.com/qf-studio/pilot-console/issues/320)). Lessons: read-only grants pass every smoke test until the first real write; the reconciler acts within one 60 s tick and logs only on error (my premature "hung loop" read → harmless restart; [console#319](https://github.com/qf-studio/pilot-console/issues/319) reframed as observability). Executor side: GH-5449 fetch fix via steered retry (PR#5457); ci-fix excerpt window #5454→PR#5455 (floored `completed_at` dropped the failing package); box **v2.276.3**. **Next click: Deprovision → Provision → first tenant → Docs real tree → post id to Nelya.** Detail: [TASK-495](tasks/TASK-495-fleet-to-be-alignment-nelya.md) 09-24 entries.
- **09-22→09-23: CONSOLE USABLE END-TO-END, GOLDEN AMI REFRESHED, OLD ESTATE GONE.** **09-22** Nelya shipped all three legs in our order (gRPC over PrivateLink → `/api/v1/me` 401; SES identity verified, prod access granted; UI deploy grants) after a purpose→exists→build→verify brief (her bot had not understood the one-liners). Console UI deployed by our new workflow ([ui#164](https://github.com/qf-studio/pilot-console-ui/issues/164)→PR#165, stamp fix [ui#167](https://github.com/qf-studio/pilot-console-ui/issues/167)→PR#169; edge rewrites extension-less paths, so `build-sha.txt`). **~~First browser login through the edge PASSED~~ — FALSE SUCCESS (found 09-24): the deployed UI is the mock build (`VITE_API_MODE` unset), so login/Docs/provision never reached console-api; fix [ui#170](https://github.com/qf-studio/pilot-console-ui/issues/170); "email not a gate" unproven** — email = a 4-leg track (relay route under `/api/v1/`, template token secret, auth-service env, UI reset/verify pages), planned separately; decision `console-email-transport-ses-not-email-service-repo`. **09-23** founder-box data volume swapped to **encrypted** (restore test passed, ~4 min pause; learning `ebs-volume-swap-runbook-xfs-nouuid-and-fstab-by-uuid`); `user/aleks` now in `pilot-founder-box-operators`. **Old CDK estate (10.30) deleted by Nelya** (stacks, VPC, RDS+final snapshot, orphan snapshots; key `a0e6dc65` scheduled 09-30) — nearly took the founder box's only backup with it (pitfall `dead-cdk-stack-carried-live-founder-box-backup-policy`); backup now owned by her `pilot-data-lifecycle`, `DataLifecycleStack` goes after tonight's snapshot; `pilot-cloud-infra` **ARCHIVED**. Drift on `pilot-agent` role removed (learning `hand-applied-iam-on-cfn-managed-roles-is-a-time-bomb`). **Golden AMI refreshed: `ami-01ecd7cefa1e6f316` (pilot 2.276.1)** via [TASK-499](tasks/TASK-499-golden-ami-refresh-first-fleet-tenant.md) — bake now fetches the release by version with checksum + version pin and writes both SSM params (infra#10/#12; first bake failed on the bucket's SSE-KMS policy, pitfall `s3-accessdenied-from-admin-role-is-bucket-policy-condition`). Both infra PRs needed **manual rebases**: Pilot's worktree branched from an August base after a lost fetch ref-lock → executor fix filed [pilot#5449](https://github.com/qf-studio/pilot/issues/5449) (autopilot ci-fix #5451 in flight). **Blocked now: reconciler redeploy for the AMI pickup rolled back** — ECS rejects AZ rebalancing on the binpack-only singleton (pitfall `ecs-az-rebalancing-requires-az-spread-placement-first`), Nelya's template fix pending. **Gates to a customer, in order:** ~~reconciler on the new AMI~~ ✅ 09-23 (Nelya: rebalancing off) → ~~`tenant_factory=true`~~ ✅ 09-23 (task def rev 6) → ~~ui#170 real-API build~~ ✅ 09-24 (PR#171, live 961bd42) → first fleet tenant (founder re-registers on the real console) + Docs real-tree proof → price + Paddle L7 (+ console model pin). Base-presence gate self-holds ×3 this week (mem-175 updated: releases after ~3 ticks; un-backtick every non-repo dotted token). Marker: see `## 🚀 Pilot Cloud SaaS Program`.
- **09-16→09-21: THE CONSOLE RUNS IN THE VPC — first time ever — and the domain is decided.** Trigger: the Docs page (TASK-466) never rendered because `pilot-console` had only ever run on a laptop with no route to tenant private IPs; an Opus-authored ticket (infra#51 / TASK-498) targeted the retiring CDK role and cited a tenant-box path → held 8× by the base-presence gate → withdrawn 09-15 (`pilot task cancel`), **closed superseded 09-21** — the fleet task role already carries both grants. **09-18** review of Nelya's *deployed* `pilot-fleet-*` stacks + manifest correction (pitfall `fleet-env-doubles-as-environment-tag-value`). **09-20** Nelya delivered 14 stacks, ECS cluster, console service templates, MFA self-service ([TASK-495](tasks/TASK-495-fleet-to-be-alignment-nelya.md) has the log). **09-21** `deploy-quantflow-aws.yml` placed on pilot-console main (`1f07b8c`); run 1 reconciler rolled back on `ec2:DescribeImages` (CFN validates `AWS::EC2::Image::Id` params as the *deployer* — pitfall `cfn-typed-parameter-validates-with-deployer-credentials`), Nelya retyped `AmiId` → String; **run 2 green: console-api 2/2 + reconciler 1/1 on `prod-0.1.0`, `tenant_factory=false`, 10/10 alarms OK.** **Domain: `pilotcloud.dev`** (decision `domain-pilotcloud-dev-keep-pilot-name`; `pilot.build` $720 rejected — no domain buys search for a word Pilot.com owns; `pilothq` is their handle) — sent to Nelya to register + start phase 2. Rename rejected: the `polsia.app` "Pilot" clone is an AI-generated placeholder. **US trademark cleared** (learning `trademark-clearance-pilot-us-2026-09`: bare PILOT unregistrable for us, use-risk low-moderate; **EU/UK still unchecked**). Navigator v7.7.0 synced + typed judge on. **Gates to a customer, in order:** MFA enrol (founder, 5 min) → gRPC NLB :4002 + SES (Nelya; **login is 503 until then**) → fresh golden AMI (2.259.3 is stale) + `tenant_factory=true` → phase 2 under `pilotcloud.dev` → price + Paddle live. New console DB is empty; old tenant `i-0a3bf271d598196ca` (10.30 VPC) unreachable from it. Marker: `2026-09-21_console-live-on-pilot-fleet-domain-decided.md`.
- **09-12→09-15: Paddle Billing designed, built, smoke-tested and priced ([TASK-497](tasks/TASK-497-paddle-billing-integration.md) has the full ledger; do not re-expand it here).** All code legs **L1–L5 merged + reviewed** (console PR#298/#301/#302/#304/#307/#309/#312, ui PR#157/#161): provider core, webhook with own `Paddle-Signature` verifier + insert-first ledger + ordering guard, checkout UI, portal session, `consolectl billing set-status`/`resync`, and the enforcement sweep + reconciler `suspended` lifecycle (**wired but dormant** — `PILOT_CONSOLE_BILLING_ENFORCE_ENABLED` default off). **L6 sandbox smoke run 09-14** against live Paddle sandbox: real card checkout → 3 signed webhooks resolved to the org → subscription bound → chip active. SOP at `sops/billing/paddle-sandbox-smoke.md`. The smoke found **two defects, both fixed and verified live the same day**: grace window truncated to whole hours and vanishing below 1 h (console#313 → PR#314), and the plan page advertising a hardcoded $299 against a $500 catalog (console#315 → PR#316 + ui#162 → PR#163). **Pricing decided 09-14** → `product/PRICING-MODEL.md`: flat fee for box + Pilot, tokens BYO billed by Anthropic directly, default `claude-sonnet-5` with a customer switch; usage-based rejected (our cost doesn't scale with tickets). Measured economics in `product/UNIT-ECONOMICS.md`. **Price number still open** ($299 recommended; Paddle prices are immutable once used, so a change means a new price object). **Known gap**: the console renders no model config, so tenants inherit the daemon default which routes complex work to **Opus**, not Sonnet — pin it before provisioning anyone. Executor side quests: evidence gate (#5437/#5438 → PR#5439/#5440), argv-exec + redaction pin (#5442 → PR#5443, after #5441 was **declined by the model's cyber safeguard on wording alone**), and auto-preserved worktrees now escalate with the sha instead of burning a retry generation (#5445 → PR#5446). Box on **v2.276.1**. Markers: `2026-09-15_paddle-priced-l7-next.md`.
- **09-11→09-12 (parallel session): external-issue triage — 2 closed, 0 code.** **#5419 "OrcaRouter provider support" closed not-planned**: automated vendor campaign, not a contributor — `gh search issues OrcaRouter` shows 30+ identical issues this week from 8+ throwaway accounts (author created 08-26, event feed = fork→PR within seconds across unrelated repos, 5% revenue-share "partner program" is the hook); technically nothing to build, `executor.type: openai-api` + `openai.base_url` already covers any endpoint. **#5359 (PR guard no_op on merge-base failure) closed completed**: fix landed via PR#5373 on the same `pilot/GH-5359` branch (#5369 died on a CI fixture bug only), released v2.273.2 — `resolveEmptyBranchWithFallback` merge-base → rev-list → open-PR lookup, fails closed; issue had been left open after supersession. Memories updated: `external-contributor-vetting-verify-claims-not-profile` (+campaign counter-example), `pitfall_noop_terminal_state_invisible_to_dispatch_guards` (+4th instance). `gh-5359.md`/`gh-5370.md` archived.
- **09-10→09-11: review-everything day + console S5 legs shipped + Paddle decision.** 22 PRs merged across pilot / pilot-console / pilot-console-ui, **every one reviewed with a verdict, follow-ups filed same hour** ([TASK-496](tasks/TASK-496-console-resume-s5.md) has the ledger). **Pilot reliability**: PR#5411's review surfaced a live regression from PR#5402 (`needs_human` terminal with no re-arm probe → clearing the label no longer re-admitted a task) → fixed #5414/PR#5417 + test pin #5420; the fix-issue PR tracking defect took three PRs (#5412 guard → #5416 retry-path-only → **#5421/PR#5423 primary SDK path, mutation-verified**) + registry hardening #5425/#5427 → #5430/#5431 → #5432/#5433; queue endpoint gained `active` filter (#5426/PR#5428). **Console S5**: B7 idle sleep redone from main (#282→PR#284, gate OFF until tenants re-bootstrapped; PR#222/#215 closed) · usage rollup + GET usage (#283→PR#285, REQUEST-CHANGES: migration 0018 collision with #284 + cumulative counters summed → revision #286 shipped in 11 min, autopilot re-adopted the size-guard-held PR = #5378 redrive proven live) · reader active-only + full-page guard (#288/PR#290) · config floor/wiring pin/operator note (#287/PR#289) · **consolectl single-writer advisory lock (#291/PR#292, 09-11)** → #293 wiring tests · UI usage page (ui#150/PR#151 → #152/PR#153 → #154/PR#155). **Founder decisions**: B7 = close+refile (option A) · **billing via Paddle, not Stripe** (jurisdiction; memory `payment-processor-is-paddle-not-stripe`; Stripe scaffolding dead-end). Box v2.274.0 → **v2.275.0** (09-10 train). Navigator plugin 6.18.1 → **v7.3.0** synced (`deep_research` off). Pitfalls: `terminal-status-without-rearm-probe-kills-operator-recovery`, `issue-body-literal-migration-version-collides-with-sibling-leg`. Markers: `2026-09-10_console-resume-prepared-5416-5418-reviewed.md`, `2026-09-11_console-lock-shipped-paddle-next.md`. Next: → 09-12 bullet.
- **09-07→09-09: Nelya cut-over answers, first console release, and a three-day Pilot reliability arc — every merged PR reviewed, ~30 follow-ups filed.** Detail: [TASK-495](tasks/TASK-495-fleet-to-be-alignment-nelya.md) (timestamped log). Highlights: Nelya's five ECS questions answered (sender `console@quantflow.studio`; tenant binary = golden AMI only, no S3 bridge); **pilot-console `prod-0.1.0` released 09-08** (first ECS image; no tag ruleset yet — founder item); CloudFront live in front of quantflow.studio + pilot docs since 09-06 (her reply sat unanswered in a DM thread until 09-09; docs `s-maxage` shipped #5404/PR#5405, live `Hit from cloudfront`); auth-service v0.73.0 deployed by Nelya, `TRUSTED_PROXY_CIDRS` posted; studio-sdk v0.38.1/v0.38.2 (poller branch-lookup fix #138/#140, skip-reason #142). **Pilot incidents**: pilot-console #275 made permanently unpickable by the poller's branch lookup (refiled #280, delivered); PR#5356 abandoned at stage `failed` by autopilot after a size-guard hold (merged by hand; redrive #5378/PR#5387); **PR#5380 merged a self-re-arming loop one minute before REQUEST-CHANGES landed → hand revert PR#5388, the train's v2.273.2 tag cancelled+deleted before publish, box never ran it; fix-forward #5381/PR#5393 then #5398/PR#5401 (GraphQL `lastEditedAt`; Timeline `edited` never fires)**; #5346 stuck 33 h because a body-edit re-arm is not evidence for the stalled sweep (label cycle recovered it); two self-inflicted base-presence holds on my own issue bodies (rule: run the path check BEFORE `gh issue create`). Box: v2.273.1 → v2.273.2 (hand tag 09-09 09:07Z) → v2.274.0 (train 09-09 14:16Z). Memories: `poller-branch-lookup-marks-issue-done-from-another-issues-pr`, `ci-fix-size-guard-holds-own-pr-on-one-lint-annotation`, `stalled-rearm-sweep-ignores-body-edit-loops-forever`, `autopilot-failed-stage-revival-is-a-flag-gated-redrive-before-processpr`, `autopilot-meta-footer-ignored-without-autopilot-fix-label`, `hand-merge-leaves-pilot-needs-human-and-stale-body-capture-at-dispatch`, `board-stalled-timestamp-is-created-at-after-self-upgrade-reconciliation`. Queue at 09-09 close: #5407 · #5408 · #5409 (all shipped 09-09, reviewed same evening — see next bullet).
- **09-06 evening: Slack routing split (founder ask) — alerts → `#pilot-reports` (C0C0SFL7L2C), merge asks → founder bot DM (`D09HGS3BR4J`), `#pointer` back to chat only.** Root: all three Slack keys pointed at `#pointer` (alert channel *named* `slack-engineering`); ~40 alerts/day buried the approval cards, `#engineering` dead since 07-16. Box config edited + backed up (`.bak-20260906-slack-routing`), `task_stuck` cooldown 1h, explicit `lane_starvation` rule 2h. **Restart owed by founder** (config read at start; #263 was looping `no_op`→re-pick). Reference: [`system/references/reference_slack_notifications_routing.md`](system/references/reference_slack_notifications_routing.md); SOP `sops/autopilot/approval-channel-routing.md` § Destination; memory `slack-routing-three-keys-alerts-vs-approvals-vs-chat`; pitfall `task-stuck-and-lane-starvation-fire-on-terminal-and-needs-human` (code fix not yet filed).
- **09-04: pilot-docs GitLab → GitHub migration STARTED ([TASK-492](tasks/TASK-492-pilot-docs-gitlab-to-github-migration.md)) — founder directive: consolidate on `qf-studio`, all hosting to AWS under Nelya.** `qf-studio/pilot-docs` created + full history mirrored (HEAD `ba193a0`), GHCR build seeded and green (`ghcr.io/qf-studio/pilot-docs`, tags on `main` + `prod-*`). Dual-push sync dispatched [#5312](https://github.com/qf-studio/pilot/issues/5312); GitLab keeps deploying `pilot.quantflow.studio` until AWS serves it. AWS-leg questions (hosting shape · ECR vs GHCR · trigger · where the `devops` runner lives) posted to Nelya in Slack `#infrastructure` (new channel C0BV37L87C1, also the home for the `quantflow.studio` site move). **09-05: Step 1 verified e2e** — the fine-grained `PILOT_DOCS_PAT` had silently EXPIRED (release docs chain was already broken); 3 failed secret re-sets (empty stdin · pbpaste newline · wrong payload) before `gh auth token | gh secret set` worked; dispatch pushed content + `prod-2.272.1-…` to both remotes, GHCR tag build green. Residual: token lacks `workflow` scope. Deploy keys are disabled org-wide. Memories: `docs-sync-wipes-target-tree-workflow-must-originate-in-docs`, `qf-studio-org-deploy-keys-disabled-use-pat-or-app`. Marker: `2026-09-04_pilot-docs-github-migration-step1.md`.
- **09-03: GH-257 incident root-caused via nav-research — the base-presence gate is the defect, not the claim layer.** `hasFileExtension` (`dependency_detector.go:213`) classed a backticked branch example `release/1.0` as a file path → 20 holds (~100 min) → `escalateBasePresenceHold` applied `pilot-needs-human` **with no comment** → `skipped`; label blocked admission 27h while `claim_lost_drops` inflated as cooldown bookkeeping (not collisions). **Census: all 165 holds in the box log (10 tasks) were false positives; zero true positives.** Corrections: the manual claim DELETE was unnecessary; #5274 reaper rides the 5-min ticker (not boot-only) and correctly ignores row-present claims → **pilot#5301 mis-filed, label pulled, awaiting re-scope.** Also found: box roots for `pilot-console` and `pilot-console-ui` dirty since 07-29 → **RESOLVED 09-04: forensics proved both were phantom self-review copies (pitfall `runselfreview-runs-in-repo-root-phantom-reimplementation`, fixed #4706 08-04, roots never repaired); no unique content; pinned on `backup/*` branches and reset to `origin/main`.** `finish_tripwire_root_clean` still has no baseline (381 violations = that one condition). Founder decisions pending: gate narrow-vs-harden · domain (`pilot.build` premium $720/yr vs `pilot.engineering` $83). Marker: `2026-09-03_deploy-unblocked-base-presence-gate-incident.md`. **Parallel session same day: TASK-491 env scrub SHIPPED+REVIEWED** ([#5275](https://github.com/qf-studio/pilot/issues/5275) 9 PRs → APPROVE-w-defects: `env_passthrough` never wired, OpenCode key loss, Qwen excluded → [#5302](https://github.com/qf-studio/pilot/issues/5302)/PR#5307 APPROVE) · repick-loop class fixed ([#5297](https://github.com/qf-studio/pilot/issues/5297), 4 PRs, w-notes; #5301's NULL-id theory unsupported by box data, but PR#5306 shipped the needs-human explanatory comment = defect #2 above) · **[#5308](https://github.com/qf-studio/pilot/issues/5308) orphan-claim reaper timezone bug FOUND → PR#5309 merged+reviewed same day** (local cutoff vs UTC `CURRENT_TIMESTAMP` reaped live claims on UTC+ self-hosts; invisible on UTC box/CI; memory `go-time-cutoff-vs-sqlite-current-timestamp-timezone`) → follow-up [#5310](https://github.com/qf-studio/pilot/issues/5310) (executions rows mix local `created_at` with UTC `completed_at`). Box self-upgraded to v2.272.0.
- **09-01→02: both S4 deploy blockers MERGED + reviewed; GH-249 hardening chain shipped end-to-end.** infra#45→PR#47 (ECR pull; APPROVE-w-notes: bootstrap-abort coupling + 12h token expiry → `amazon-ecr-credential-helper`, follow-ups unfiled) · infra#46→PR#48 (JWT PEM materialization; REQUEST-CHANGES: PEM `root:root 0400` unreadable by image `USER appuser` uid 1000 — **fix pushed by hand** + umask 077 + runbook `file://`, rebased onto #47, autopilot-merged). Auth-service contract re-verified (uid 1000, `JWT_PRIVATE_KEY_PATH`, minimal required-var set; EMAIL_*/SAML_*/OAUTH gated default-off; new `auth-service-smoke` image for validation). Console: PR#250 post-merge DEFECT-FOUND → #251 (C1/UTF-8, PR#253) → #252/#254 **declined twice by cyber safeguard** (exploit-forward wording) → #255/PR#256 (pure input-validation framing passed) → residual traversal → #257/PR#258 APPROVE-w-notes. Memory: `safeguard-refuses-exploit-framed-issues-describe-as-input-validation`.
- **08-29→30: TASK-489 receipts digest — full nav cycle (research → plan → dispatch [#5257](https://github.com/qf-studio/pilot/issues/5257) → PR#5258 same-day) → REQUEST-CHANGES → the founder-review channel is now a documented design decision.** Feature: second scheduled brief (`orchestrator.receipts_digest`, default 18:00) — one Telegram line per terminal execution (issue ref · `+adds −dels` · duration · $cost) + day total. Research: all data already on `executions` rows; found a real pre-existing bug — `GetLastBriefSent` filtered by channel only, so ANY second brief type on a shared channel corrupts catch-up for both. PR#5258 (+1093/−20, CI green): brief_type filter fixed + cross-contamination tested, canary rows excluded, failed-run costs counted, empty-day skips send, daily brief untouched. **Review REQUEST-CHANGES**: (1) BLOCKING — window is `created_at ∈ [midnight, now]`, so a run in-flight at 18:00 or created after is **never receipted in any digest** (silent spend undercount; fix = window since-last-digest keyed on `completed_at`); (2) revert smart-quote corruption Pilot introduced into `SetApprovalDecision`'s doc comment; note-only: no Telegram 4096-char guard (~90 runs/day). **Recheck 08-30: revision never started — and that exposed the load-bearing lesson: Pilot doesn't read GH comments BY DESIGN (founder-confirmed).** The revision loop keys only on formal `changes_requested` reviews (`hasChangesRequested`), impossible on same-account PRs — so the canonical founder REQUEST-CHANGES flow is: verdict comment (human record) → PR to **draft** (merge block) → **manually-filed pilot-labeled revision issue with `autopilot-meta branch/pr/iteration` footer** targeting the existing branch = [#5261](https://github.com/qf-studio/pilot/issues/5261). Never propose comment-parsing triggers; automation must be label/issue-based. Decision memory: `pilot-never-reads-gh-comments-by-design`. Status: awaiting #5261 revision; PR#5258 held in draft.
- **Earlier (compressed):** 08-27 box → main tip, AMI arc closed, plan-C tenant-isolation decision, 4 PRs merged+reviewed · 08-27 stevensommer (3rd external) vetted · 08-26 S5 opened+shipped+reviewed in a day (4/5 REQUEST-CHANGES, 11 issues) · 08-25 #5008 hosted retry shipped + GH-5063 core.bare arc closed · 08-25 docs-site truth pass + auto-init contract fixed · 08-24 d3rowy (2nd external) 4/4 verified, ~22 PRs (`2026-08-24_d3rowy-triage-four-issues.md`) · 08-24 PR#5121 takeover → #4888 arc shipped · 08-23 tripwire root-caused (stale `q-<epoch>.md` in root) + #5145 base-presence self-wedge (same gate class as 09-03) · 08-22 `--gitlab` + docs drift audit (#5131/#5141/#5143) · 08-18 evening S3 EXIT PASSED (3 concurrent tenants) + lint-cache incident + v2.262.0 + lkshrk batch #4963–#4968 all merged (markers: `2026-08-18_s3-exit-three-tenant-pass.md`) · 08-17 evening recovery sweep + handleMerged-dead-code discovery → eval metrics live (#4919/#4922 chain, v2.260.1; memory `handlemerged-shadowed-dead-by-external-merge-detector`) · 08-17 late FIRST EXTERNAL CONTRIBUTOR lkshrk: 19 issues + 15 PRs reviewed, 10 merged same day, spawned TASK-479/480/481 (all since shipped; marker `2026-08-17_lkshrk-pr-batch-review.md`) · 08-16→17 TASK-405 un-patched ship test ✅ + estate (FleetVpc, golden AMI, GH-4872 chain, design-conformance program — marker `2026-08-17_lkshrk-pr-batch-review.md` + git log) · 08-15 TASK-478 overnight build-out (six size-held PRs, morning playbook) · 08-14 rail design→12/17 legs merged same day (+pilot#4869) · 08-12 PR#4846 incident closed + 3-generation same-day fix cascade (memory `incidents-always-first`) · 08-11 design sprint ×4 + C16/C17 shipped e2e · 08-06→08-08 GH-Actions outage → recovery → hardening wave (TASK-458 breaker enabled in prod; detail `system/approval-architecture-roadmap.md`) · 08-04/05 TASK-441 contract hardening + first unattended self-upgrade (v2.253.0) + S4 waves 2–4 + token incident resolved · 08-01/08-03 S4 wave 1 + Golden AMI v2 merged (operator bake pending) · 07-31 first autonomous train + AWS cost audit (`cdk deploy` pending) · 07-30 spec-guard epic + real-stack-verify SOP · 07-29 S3 backend 10/10 · 07-27/28 S2 EXIT MET · 07-26 SaaS UNPARKED · 07-20 approvals off · 07-16 S6-lite AWS cutover (TASK-409). Detail: git log + `tasks/archive/`.

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
- **Latest handoff marker**: `.agent/.context-markers/2026-09-23_ami-refreshed-estate-closed-reconciler-blocked.md`
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
| **P2** | **Client-owned repo onboarding** ([TASK-500](tasks/TASK-500-client-repo-onboarding-master-default.md)) | 📋 planned 09-24; first consumer = a client repo (private workspace). Pilot cannot run on a `master`-default repo today: worktree base hardcoded to `origin/main` (`worktree.go:338,344,559`), no branch/PR-title template, no CLI `--branch/--body-file`, autopilot only global (no per-project off), env only global. Five legs; leg 1 (base branch) + leg 4 (per-project autopilot off) + leg 3 (CLI flags) unblock the first dry run. |
| **P1** | **Pilot SaaS platform** ([TASK-405](tasks/TASK-405-pilot-saas-platform.md)) | S0–S2 ✅ · S3 exit met local-first 08-18 · S4 build complete · **S4 exit path UNBLOCKED 09-21: console-api + reconciler LIVE on Nelya's `pilot-fleet` (ECS, `prod-0.1.0`, factory off)** — the minimal-EC2 route ([TASK-490](tasks/TASK-490-s4-minimal-invpc-console.md)) is superseded by the ECS transfer. **Domain decided `pilotcloud.dev`.** Next: MFA → port-forward validation → fresh AMI + `tenant_factory=true` → rig → S4 exit week; login needs Nelya's gRPC listener. |
| **P1** | **Fleet TO-BE alignment (Nelya)** ([TASK-495](tasks/TASK-495-fleet-to-be-alignment-nelya.md)) | 🟢 **Console usable end-to-end 09-22** (gRPC PrivateLink, SES identity, UI on `console.pilotcloud.dev`, founder login passed). 09-23: founder-box volume encrypted, old estate deleted, drift closed. Open on her side: reconciler template fix (AZ rebalancing vs binpack), tonight's snapshot → delete `DataLifecycleStack`, `CDKToolkit`. Ours: email track (planned, not a gate), model pin. |
| **P1** | **Golden AMI refresh → first fleet tenant** ([TASK-499](tasks/TASK-499-golden-ami-refresh-first-fleet-tenant.md)) | L1 ✅ (bake fetches release by version, sha256, version pin, both SSM params) · L2 ✅ `ami-01ecd7cefa1e6f316` pilot 2.276.1 · **L3 BLOCKED**: reconciler redeploy rolled back on Nelya's AZ-rebalancing template (binpack singleton); after her fix: re-run → `tenant_factory=true` (ask founder) → dogfood tenant → Docs real tree. Old AMI kept as rollback. |
| **P1** | **Paddle Billing integration** ([TASK-497](tasks/TASK-497-paddle-billing-integration.md)) | ✅ **Code complete, L6 sandbox smoke passed 09-14 with a real card**; enforcement wired but dormant (`PILOT_CONSOLE_BILLING_ENFORCE_ENABLED` off). **L7 go-live = founder:** the price number ($299 vs $500 catalog; Paddle prices immutable once used → new price object) · live account · webhook destination → `api.pilotcloud.dev` once phase 2 exists · rotate the sandbox key pasted in chat 09-14. Build gaps unfiled: **tenant model default → Sonnet 5 (urgent, money)** · model switch UI · tokens tile · plan-name alignment · "2 parallel executions" claim. |
| P2 | **Console Docs page — live verification** ([TASK-466](tasks/TASK-466-console-docs-page.md)) | Page renders through the edge (09-22 founder login) but the console DB has no tenant yet → the real `.agent`-tree proof is TASK-499 L3's acceptance (first fleet tenant). infra#51/TASK-498 closed superseded. |
| P2 | **pilot-docs → GitHub + AWS deploy** ([TASK-492](tasks/TASK-492-pilot-docs-gitlab-to-github-migration.md)) | ✅ Cutover done by Nelya 09-05; TASK-494 sync fix DONE + reviewed 09-06 (GitHub-only, preserves pilot-docs `.github/`). **Remaining = step 4 operator:** delete `GITLAB_SSH_KEY` secret · drop `gitlab:` block in hosted config · archive `quant-flow/pilot-docs`. Nelya owes: compression + CSP-RO answer. |
| **P1** | **Console rail implementation** ([TASK-478](tasks/TASK-478-console-rail-implementation.md)) | 11 approved designs → shipped surfaces. **Build-out COMPLETE 08-15 (overnight run): all 16 autopilotable legs executed + reviewed.** Blocked on founder morning sequence (approve PR#67→72→74→76 · close #73/#77 to arm retries · label ui#78 after) → then GH-69/GH-75 retries re-land · **real-stack verify batch UI-2..12** (SOP; blocked on GH-75 re-land) · CON-5 billing portal (founder Stripe gate) · copy pass ($299 · PR#72 "Includes" line · support@ mailto). Daemon-side follow-ups FILED 2026-08-20: [pilot#5027](https://github.com/qf-studio/pilot/issues/5027) merge-time ancestry check (stacked-superset guard, extends GH-4872) + [pilot#5028](https://github.com/qf-studio/pilot/issues/5028) base-presence check before claim (see pitfall `sequential-gates-on-execution-not-merge-fastfollow-misbase`; 4 family incidents in 6 days, nav-research verdict: both fixes needed). |
| **P1** | **Throughput acceleration** ([TASK-393](tasks/TASK-393-throughput-acceleration.md)) | Phase 1 (instrumentation) ✅ shipped 07-09. **M3 baseline window closed ~07-20 — histograms never harvested; phases 2–5 remain gated on that analysis.** Remaining: (2) execution lanes on `Complexity`, (3) N-concurrent per repo (`ProjectWorker` pool — note this is also the sole serialization point, see mem-101/102), (4) SHA-keyed repo primer, (5) risk-score trust tiers. Roadmap: [`throughput-roadmap.md`](system/throughput-roadmap.md) (M0–M8, D1–D6). |
| P2 | **Review-feedback trigger hardening** ([TASK-493](tasks/TASK-493-review-trigger-hardening.md)) | ✅ SHIPPED 09-06 in v2.273.0 (PR#5331–5334 via #5314); post-merge review APPROVE-w-notes → follow-up [#5337](https://github.com/qf-studio/pilot/issues/5337) (HIGH re-adoption cutoff blind spot). Also filed [#5336](https://github.com/qf-studio/pilot/issues/5336): autopilot CI-fix issues deadlock on `Depends on:` an open superseded original. From external [#5228](https://github.com/qf-studio/pilot/issues/5228): feature (bot/`COMMENTED` triggers) **DECLINED — security by design** (`pilot-never-reads-gh-comments-by-design`); its two real bugs taken: webhook path has no bot filter · reviews yank PRs out of `awaiting_approval`. Plus identity-based exclusion via `getBotLogin` and human-only revision-issue body. No config, no dashboard change. |
| **P1** | **Execution lifecycle chokepoint** ([TASK-404](tasks/TASK-404-execution-lifecycle-chokepoint.md)) | B1 shipped (#4243 — `ExecutionLifecycle` Begin/Transition/Finish + typed status vocabulary). Remaining legs open; #4678's cancel verb lands on this seam. |
| **P1** | **Hosted retry path for failed executions** ([TASK-485](tasks/TASK-485-hosted-retry-path-failed-executions.md), [#5008](https://github.com/qf-studio/pilot/issues/5008)) | Daemon legs SHIPPED+REVIEWED 08-25 (PR#5214 classifier · PR#5215 stalled re-arm sweep · PR#5220 streak alert) — ride the 08-26 v2.270.0 train. **Remaining: Leg 3** (console repo: C8 dispatch on failed/stalled card cycles trigger label + removes `pilot-blocked`) **after the train lands on the box**, then **Phase 4** live validation on ship-test-js#6 (expect ≤16min re-arm latency — repick backoff gate). |
| — | ~~Wire `linear.webhook_public_key` YAML~~ | **REMOVED 2026-08-26 — the premise was false.** Verified fully wired end-to-end (see the cleared caveat above). Nothing to do. |
| **P1** | **Executor false no-op after backend timeout** ([#5342](https://github.com/qf-studio/pilot/issues/5342), dispatched 09-06) | finalization reuses the expired task ctx; committed work reported `no_op`, never pushed (pilot-console GH-263 ×3). PR#5345 REQUEST-CHANGES → draft; revision [#5346](https://github.com/qf-studio/pilot/issues/5346). Pitfall `backend-timeout-finalization-on-expired-ctx-discards-committed-work`. |
| **P1** | **CI-fix continuation drops original commits** (#5348 → PR#5349 merged 09-07 11:29, review REQUEST-CHANGES → [#5351](https://github.com/qf-studio/pilot/issues/5351): own-close marker uncalled + in-memory, superseded still set at spawn; also [#5350](https://github.com/qf-studio/pilot/issues/5350) decomposer ignores `no-decompose`) | **root-caused 09-07:** executor force-resets the fix branch to main by name (empty base + `-B`), recorded SHA never parsed, own close misread as external → superseded label + branch delete. Shared cause of the week's three incidents: prose continuation state · name-based git · labels on process events, not delivery evidence. Pitfall `ci-fix-continuation-rebuilds-branch-from-main-drops-original-commits`. |
| P2 | Effort classifier 401 on every task ([#5344](https://github.com/qf-studio/pilot/issues/5344), dispatched 09-07) | OAuth token (`sk-ant-oat…`) sent as `x-api-key` since 08-23; subprocess fallback hides it (164 WARNs/day). |
| P1 | Stalled re-arm claim deadlock ([#5272](https://github.com/qf-studio/pilot/issues/5272), dispatched 09-06 verify-first) | claims bound to terminal gen-0/gen-1 executions still win the race; not covered by #5274/#5306/#5309. Pilot repo open issues after triage: this one only (#5008 delivered → closed, #5228 declined → closed). |
| P1 | Fix `shouldTriggerRelease()` | Doesn't check `ResolvedEnv().Release` — only top-level config. |
| P1 | Web dashboard polish | React UI functional but needs a design pass. |
| **P1** | **Jira merge-side close: reachability chain** ([#4999](https://github.com/qf-studio/pilot/issues/4999) + sdk#123/#124 + sdk PR#122 tag/pin) | PR#4992 merged the done leg but it's **dead code in production** (TASK-460 class — pitfall `merged-feature-dead-callback-not-bridged-onprcreated`): sdk jira adapter drops `OnPRCreated`, reconciler adopts only `pilot/GH-*`, external-merge path (how KAN-6's PR actually merged) never calls it, and pinned sdk v0.35.1 does English-name transitions + comment-first-early-return. Chain to close: sdk#123 (bridge OnPRCreated, all tracker adapters) · sdk#124 (statusCategory transitions + decouple from comment failure) · #4999 (external-merge leg + idempotency) · sdk v0.35.2 tag + pin bump (ADF comment fix #122 is merged-untagged). KAN-6 acceptance (card leaves «К выполнению») transfers to #4999. |
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
