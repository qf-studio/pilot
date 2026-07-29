# TASK-405: Pilot SaaS Platform ("Pilot Cloud")

**Created**: 2026-07-13 · **Status**: ✅ **S3 wave 9/10 MERGED 2026-07-29** (roadmap **v9.3**) — console#57–61 closed · ui PR#15/#16 merged · infra PR#25 merged (**operator deploys pending**) · auth PR#479 (GH-477 EmailSender + reset delivery) reviewed + merged manually 15:59Z after ~4h13m green un-adopted (#4610 recurrence #6; daemon self-shipped the fix, PR#4612 merged 15:52Z, rides next train). **Last leg: auth#478 signup verification** (unblocked, pilot-labeled, awaiting pickup). Wave dispatched AM (v9.1); daemon shipped 4 console legs inline, 5-PR park cascade recovered by operator unification (#67/#74 superseded · #75 webhook-lifecycle graft · #68/#70 rebased). Box drain incident root-caused (zombie active-task, pilot#4609) → **both daemons on v2.249.0** (banner-env cosmetic defect pilot#4611). **Next: operator staging deploys** per infra PR#25 checklist — founder inputs: domains (SES/console), Stripe test keys/price/webhook secret, ACM DNS validation. · Prior: 🟢 UNPARKED 2026-07-26 (supersedes the 07-17 "SaaS parked" directive) · **MERGE LEG ROOT-CAUSED 07-26** — autopilot was never enabled on the tenant daemon: `writeAutopilotBlock` (`pilot-console/internal/fleet/configrender.go:109`) emits `autopilot:` at YAML top level where nothing binds it (only `orchestrator.autopilot` exists, `config.go:153`), never emits `enabled: true`, and the tenant systemd unit passes no `--env` — the sole path that forces `Autopilot.Enabled = true` (`main.go:422-426`). Tell: **no `autopilot_pr_state` table** in the tenant ledger. v8.7's `require_approval` hypothesis is REFUTED (instance reads `false`). Tenant `ANTHROPIC_API_KEY` provisioned from the pointer org (SecureString v1, tenant CMK); ⚠️ $107.88 balance, auto-reload off, shared with pointer prod. **✅ MERGE LEG FIRING 2026-07-26 15:56Z** — hot-fix (nest under `orchestrator:` + `enabled: true`) + restart → PRs #103/#105 both **MERGED** on the hosted path, issues closed, branches deleted; first ever hosted-path merges. S2 exit "≥2 fresh merged PRs" ✅; "all `pilot` issues closed" still needs #104/#99/#100. Hot-fix is ephemeral (`configpush.go:27` clobbers it) — **permanent fixes LANDED 07-27**: pilot-console#55→PR#56 MERGED + pilot#4544 CLOSED; tenant hot-fix live-verified intact (2.245.0) 07-27 ~20:55Z; clobber risk remains until the deployed console build carries PR#56. S2 exit remaining: console deploy → permanent config push → tenant upgrade to v2.247.x, then canary trio #99/#100/#104. Strict-decode still unfiled pending a call. See roadmap v8.9. · S0 ✅ · S1 ✅ · H1–H12 ✅ · S3 UI mock ✅ · R-track ✅ · S6-lite ✅ · **S2 BUILD COMPLETE 2026-07-23 night** (all epics merged: B5/A4/B6/reconciler/C13 PR#29/B11 PR#19/**B8 PR#30 merged 18:54Z**/**A3 PR#31 merged 20:20Z**; TASK-417 archived). S2 exit **steps 1–3 ✅ · step 4 BLOCKED ON THE MERGE LEG (2026-07-24 night)**: fleet VPC live (4 CDK stacks), consolectl shipped, **hosted canary tenant EXECUTING on fleet VPC** (i-0decbc0dcf225cf18, provision 99s ✅ <5min) — but **PRs #103/#105 sit green-CI and unmerged ~80 min** and no PR has ever merged on `pilot-canary-sandbox` (every closed PR is `mergedAt: null`). **Exit evidence (all `pilot` issues closed + ≥2 fresh merged PRs) NOT met**; 5 issues open (99/100/101/102/104). Execution is proven, merge is not. Leading hypothesis (unverified): instance config defaults to `stage.require_approval: true` while the box has run approvals-off since 07-20 — diagnostic = `select pr_number, stage, ci_status from autopilot_pr_state` on the instance. Day total: 14 issues filed across 4 repos, 10 Pilot-shipped same-day. Open decisions: fund `/pilot/ANTHROPIC_API_KEY` vs OAuth-dogfood; console#45 ready-gate design. Live status: `.agent/system/saas-roadmap.md` v8.7
**Owner**: Aleks (founder decisions) + Pilot (execution)
**Execution roadmap**: `.agent/system/saas-roadmap.md` — S-milestones, dispatch rules, test strategy

**Founder decisions (2026-07-13):** build with the LOCAL daemon until the SaaS is complete; pilot
repo migrates onto the platform at S6 cutover ("I'll move there"). Hosted-path dogfood during the
build targets `pilot-canary-sandbox`, never a repo the local daemon owns. Existing AWS infra
(acct 529088297614, full CLI access, all stacks green) is the test bed. The 6 open decisions
below are adopted at recommended defaults, provisionally. Engine/OpenRouter bench experiment: parked.
**System docs** (the real content — this doc is the program index):
- `.agent/system/saas-architecture.md` — final architecture, tenancy/security, config/secrets, models decision, roadmap, risks
- `.agent/system/saas-kanban-sync-design.md` — mixed-tracker sync engine (the hardest novel component)
- `.agent/system/saas-fleet-design.md` — per-tenant EC2 fleet, reconciler, lifecycle, cost model
- `.agent/system/saas-asset-research.md` — verified 7-asset research digest + 36-claim verification appendix

## The bet (one paragraph)

Run the existing Pilot daemon **unmodified** (one patch: `PILOT_HOSTED=1`) on **one EC2 instance
per customer** — the only isolation model honest about `--dangerously-skip-permissions` execution —
behind a thin new control plane (`pilot-console`) and a mixed-tracker kanban whose v1 write surface
is exactly three verbs (dispatch / approve-reject / close). Polling only, tracker-as-message-bus,
restart-to-apply config, BFF cookie auth over auth-service (no OIDC build), BYO Anthropic key
(zero token COGS), no model picker. Structural isolation adopted day one where retrofit is
expensive: STS session-tag ABAC on `/tenants/{org}/*`, bind-once/terminate-on-unbind instances,
write-only secrets, versioned immutable instance specs. Product wedge ("watch it work" theater,
true field-level sync) is sequenced v2, on data models laid in v1.

## Provenance

Planned via two workflows (2026-07-13, ~1.37M tokens, 20 agents): 7-researcher asset map → 3
independent proposals (*One Tenant, One Box* / *TOWER* / *Deck*) → judge panel (security → TOWER,
business → Deck; feasibility judge lost to an API error) → synthesis merging One-Box skeleton +
TOWER isolation + Deck product sequencing → 2 deep dives → 36-claim adversarial verification
(21 confirmed / 15 partial / 0 refuted). Raw artifacts (proposals, judge verdicts): `/tmp/saas-plan/`.

## What exists vs what gets built

| Layer | Verdict |
|---|---|
| Executor/autopilot/approvals/ledger | `pilot` as-is + `PILOT_HOSTED=1` patch + one REST approval-decision endpoint on the `DecisionRecorder` seam |
| Identity | `auth-service` live subset behind a BFF; **pre-work: fix Nil-tenant FK failure** (unwired middleware → `uuid.Nil`), single-tenant mode |
| Connectors | `studio-sdk` clients + new `SyncCapable` contract; fix Linear cursor bug, GitHub `ListIssues` single-page bug, Jira `CreateIssue`/`UpdateFields` |
| Infra | `aws-infrastructure-pilot`: keep AMI pipeline + IAM path-scoping + network hardening semantics (port to CDK); drop ASG-as-scheduler; **AMI v2: add `gh`, pin Claude Code** |
| Frontend | Lift `fleet-manager-frontend` (Vue 3.5/Tailwind 4/design-dna v3 tokens); `drift-ui` wireframes as IA spec for kanban/theater/story screens (v3 visual language wins; Drift's 5 state hues mapped onto v3 semantic tokens) |
| Monitoring | Central ops Prometheus scraping instance `:9090/metrics` with tenant labels; customer charts via instance JSON API proxy; `grom` internals not extracted |
| Email | `qf-studio/email-service` as a PARTS BIN, not a deployment (asset-mapped + org copy synced 2026-07-13): vendor `transports/{ses,resend}.go` into pilot-console; auth-service's unwired `EmailSender`/`HTTPSender` scaffolding + complete-minus-delivery reset flow (TODO `service.go:323`) make S3 email a wiring task, not a build. Local clone still tracks old GitLab origin — repoint |
| Models | `claude_code` backend only, BYOK via GH-2371 env injection, read-only cost display. **No picker in v1** (full reasoning: architecture doc §5) |
| Genuinely new | `pilot-console` (control plane: orgs, connections, provisioner, reconciler, board, sync worker, BFF, instance proxy) + the kanban UI |

## Phases (full exit criteria in architecture doc §6)

| Phase | Weeks | Scope | Exit |
|---|---|---|---|
| **0 — Concierge** | 1–3 | AMI v2; hand-provision 2 instances (tenant #0 = pilot repo dogfood, + 1 **paying** design partner via GitHub labels); frontend track starts on mock adapter | Partner's real ticket ships label→PR→merge fully on AWS; partner paid; runbook committed = provisioner spec |
| **1 — Control plane** | 4–9 | pilot-console, RDS, provisioner, reconciler, sleep/wake, spec push, BFF auth, Stripe checkout, dashboard (no board) | Signup→payment→credentials→provision→first PR, zero operator SSH; 3+ tenants concurrent; drift detection fires |
| **2 — Product** | 10–16 | Board read path + 3 verbs, status-map wizard, conflict chips, per-card timeline (22-stage `execution_events`), live logs (`/ws/dashboard` proxy), redaction scrubber, **hostile-ticket isolation pen test** | Customer runs 1 week dashboard-only across ≥2 tracker types on one board; pen test passes; approvals survive restart |
| **3 — GA** | 17–21 | Billing lifecycle, EBS snapshot/restore, AMI rolling upgrades, egress allowlist proxy, RLS defense-in-depth, pricing from measured COGS | First fully self-served payer; 10 tenants × 30 days stable; MRR ≥ fleet COGS |
| **4 — v2** | post-GA | Scale-to-zero warm pool; transcript Theater/Run Story; GitHub App + tracker OAuth; true field-level sync; webhooks; managed keys + metering; OpenRouter tier | Trial tier <$100 COGS; PATs off instances; Theater demo closes deals |

Work-item decomposition ready to cut into GitHub issues: fleet doc §9 (Epics A/B/C, 16 items),
sync doc §9 (S1–S5 SDK + C1–C9 console). Dispatch mirrors the #4127 epic pattern.

## Unit economics (fleet doc §7)

Typical tenant (~50% duty cycle via v1 sleep/wake): **~$52/mo infra** (~$40 with savings plan);
idle/parked ~$14; fixed control plane ~$215/mo/env. BYO Anthropic key ⇒ $0 token COGS.
Break-even ≈ 2 customers at $199/mo. No tier below ~$99/mo until v2 scale-to-zero.

## Open decisions for the founder (recommended defaults)

1. **Pricing/segment**: $500/mo design-partner, agency/team segment, no free tier
2. **Onboarding bar**: BYO Anthropic key + pasted PATs — yes for first cohort; pull OAuth forward only if ≥2 of first 5 prospects balk
3. **Region**: eu-central-1 only, marketed as EU data residency
4. **Dogfood tenant #0 from Phase 0**: yes, non-negotiable
5. **auth-service single-replica session-listing gap**: accept until first outage or 20 tenants
6. **Models**: adopt architecture doc §5 as written (no picker, BYOK-only)

## Next actions

1. Founder reviews the 6 decisions above (defaults are safe to adopt wholesale)
2. `/nav-task`-style decomposition of Phase 0 into `pilot`-labeled issues (AMI v2, bootstrap script, concierge runbook, mock-mode board start) — **after** M3 baseline week considerations: Phase 0 issues run on the pilot repo's own executor; they are normal-lane work and metrics-visible, which is fine (they ARE production data), but do not dispatch a flood mid-baseline
3. auth-service pre-work issue: coalesce `uuid.Nil` → Default tenant (or wire fixed-tenant middleware) + integration test
4. studio-sdk pre-work issues: S1 contract, Linear cursor fix, GitHub pagination fix (can proceed independently of Phase 0)

## Refs

- Research/verification artifacts: `/tmp/saas-plan/{research_digest,proposals_text,judge_text,synthesis,kanban_sync_design,fleet_design}.md`, `verification.json`
- Workflow runs: `wf_db755393-33e` (plan), `wf_523e4731-46e` (verify)
