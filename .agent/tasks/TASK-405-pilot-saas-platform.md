# TASK-405: Pilot SaaS Platform ("Pilot Cloud")

**Created**: 2026-07-13 · **Status**: 🟢 **S3 BUILT — exit gated on founder staging inputs** (console + sending domains · Stripe test keys/price/webhook secret · ACM DNS validation) → operator deploys per infra PR#25 checklist → S3 exit test (signup→payment→credentials→provision→first PR, zero SSH, 3+ tenants). **S4 board track waves 1+2 ALL MERGED** (C1 PR#87 · C2 PR#88 · C7 PR#92 · C3 PR#93 · C4 PR#94 · ui#42/43 — TASK-432–435 + 438–440 archived); **wave 3 dispatched 08-05** as a chained no-decompose quartet: console#95 (C5 statusmap seed+CRUD) → #96 (C6 rate budgeter) → #97 (C8 dispatch verb + execution join) → #98 (C9 Prometheus), TASK-442–445, specs verified against console `f1658e3` + sdk `v0.31.2`. **Cost audit 07-31**: CDK fleet estate was untagged in Environment cost view + 2nd NAT GW contradicted v1 single-NAT design → infra#26→PR#27 merged 07-31 (app-scope `Environment=fleet` tag + NatGateways 1); **operator `cdk deploy` still pending** (tag no-interruption; NAT delete = brief egress blip, time around canary tenant). Pitfall memory `cdk-untagged-estate-invisible-to-cost-view`. Milestone history: `.agent/system/saas-roadmap.md` v8.8–v9.9 (do not re-grow this line; replace it). · **Last Updated**: 2026-08-05
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
7. **Branch protection on `qf-studio/pilot` main** (added 2026-08-03, TASK-437): currently NONE (live-verified — executor sessions could push to main; only advisory CLAUDE.md text prevents it). Any design must be autopilot-compatible (auto-merge + required checks; mind the TASK-431 check-name-mismatch class). Recommended: decide alongside #4671 (gh-guard shim) delivery — if the shim ships and holds, protection is defense-in-depth (required check `test` + autopilot bypass), not urgent; if shim slips, protection first.

## Next actions

1. Founder reviews the 6 decisions above (defaults are safe to adopt wholesale)
2. `/nav-task`-style decomposition of Phase 0 into `pilot`-labeled issues (AMI v2, bootstrap script, concierge runbook, mock-mode board start) — **after** M3 baseline week considerations: Phase 0 issues run on the pilot repo's own executor; they are normal-lane work and metrics-visible, which is fine (they ARE production data), but do not dispatch a flood mid-baseline
3. auth-service pre-work issue: coalesce `uuid.Nil` → Default tenant (or wire fixed-tenant middleware) + integration test
4. studio-sdk pre-work issues: S1 contract, Linear cursor fix, GitHub pagination fix (can proceed independently of Phase 0)

## Refs

- Research/verification artifacts: `/tmp/saas-plan/{research_digest,proposals_text,judge_text,synthesis,kanban_sync_design,fleet_design}.md`, `verification.json`
- Workflow runs: `wf_db755393-33e` (plan), `wf_523e4731-46e` (verify)
