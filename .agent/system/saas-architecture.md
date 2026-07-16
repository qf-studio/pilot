# Pilot SaaS — Architecture & Roadmap (Program Doc)

**Created**: 2026-07-13 · **Status**: PLANNED (pre-dispatch)
**Task doc**: `.agent/tasks/TASK-405-pilot-saas-platform.md`
**Siblings**: `saas-kanban-sync-design.md` · `saas-fleet-design.md` · `saas-asset-research.md`
**Provenance**: judged 3-proposal design competition (One-Box / TOWER / Deck) over a 7-asset
verified research digest; synthesis = One-Box skeleton + TOWER isolation invariants + Deck
product sequencing. 36 load-bearing source claims adversarially re-verified (21 confirmed,
15 partial-with-caveat, 0 refuted) — corrections below.

## ⚠️ Post-verification corrections (read before the body)

The body below is the synthesis as produced; these verified corrections OVERRIDE it where they conflict:

1. **auth-service has no working single-tenant "Default" mode today.** TenantMiddleware exists but is never wired (`cmd/server/main.go` sets no Tenant middleware); `domain.TenantIDFromContext` then returns `uuid.Nil`, and tenant-scoped queries/inserts run against a nonexistent zero-UUID tenant, failing the FK to `tenants(id)` (migration 000015). §2's "runs single-tenant on the Default tenant" requires a small auth-service patch first: coalesce Nil→Default-tenant UUID (or wire the middleware in fixed-tenant mode). New Phase 1 work item.
2. **Restart does NOT log users out** (better than the body's risk #5 states): auth is stateless JWT + Postgres-persisted refresh tokens; the in-memory store only backs session *listing/revocation* visibility. Redis-backed sessions demoted from availability risk to feature-completeness work.
3. **Live log tail is `/ws/dashboard` (WebSocket), not `/api/v1/logs`** — the REST endpoint is a `GetRecentLogs` snapshot. The console instance-proxy must either proxy the WS or poll snapshots (§ Data flows).
4. **Golden AMI gaps**: `gh` CLI is NOT installed (pilot shells out to `gh` heavily — hard blocker), and Claude Code installs unpinned (`claude_code_version` defaults `latest`). Both fixed in the "Golden AMI v2" work item.
5. **Board label hygiene rule (new invariant)**: pilot's ProcessedStore re-arms dispatch when status labels (`pilot-in-progress`/`pilot-done`/`pilot-failed`) are removed from a processed issue (SDK poller Unmark path). Board write-backs must be label-additive for pilot labels and never strip pilot status labels, or dispatch dedup breaks by design.
6. **usage_events proto-metering confirmed weaker**: `user_id` is never assigned in non-test code and `project_id` carries a filesystem path — v2 metering needs identity plumbing through `Execution`, as planned.
7. **Model pricing table**: hardcoded with unknown-models-priced-as-Sonnet confirmed, but it is not Anthropic-only (a Qwen branch exists). The §5 recommendation is unchanged.
8. **IMDSv2 is only enforced on the agent launch template** in the sandbox stacks — the fleet CDK must enforce it on every instance.
9. **studio-sdk `api.golden` lock covers `sdk/core` only** — integrations packages are unfrozen v0.x surface; pin SDK versions per console release train.
10. **auth-service client types are `user`/`service`/`agent`** (no "system" type); the console authenticates as a `service` client; `qf_ak_` API keys + `:4001` admin CRUD confirmed working.

---

# Pilot Cloud — Master Architecture & Roadmap

**Final synthesis of the design competition: "One Tenant, One Box" (winner) hardened with the isolation invariants from TOWER and the product/auth sequencing from Deck.**

---

## 1. Executive Summary

The bet: the fastest path to a paying Pilot Cloud customer is to change almost nothing about Pilot itself — run the existing daemon, unmodified, on one dedicated EC2 instance per customer, because "one tenant, one box" is simultaneously the cheapest thing to build, the only isolation model honest about an executor running `claude --dangerously-skip-permissions` on customer code, and the configuration the codebase already assumes. Every hard problem the full vision implies — remote control protocol, multi-tenant TSDB, OIDC provider, bidirectional sync engine, dynamic scheduling — is either replaced by a boring existing mechanism (polling instead of webhooks, the tracker's own `pilot` label as the dispatch bus, SSM RunCommand + restart instead of hot config push, the instance's existing JSON API proxied per-tenant) or explicitly deferred to v2. We spend the entire novelty budget on the one genuinely new artifact: a thin control plane plus a three-verb mixed-tracker kanban (dispatch / approve-reject / close) that sells as Pilot's control surface, not a tracker replacement. From the judged competition we graft in the structural isolation ideas that cost little now and are expensive to retrofit (STS session-tag ABAC, terminate-never-reassign instances, BFF cookie auth instead of building OIDC, write-only BYOK, versioned instance specs, a redaction gate before any transcript surfaces) and stage the demo-defining "watch it work" experience and scale-to-zero economics as the explicit v2 wedge. A concierge design partner pays in weeks 1–3, the Pilot repo itself is tenant #0 from the first provisioned instance, and GA is gated on 10 stable tenants with MRR covering fleet COGS.

---

## 2. Final Architecture

### Design rules

1. **The Pilot binary ships unmodified wherever possible.** It already runs headless, uses CGO-free SQLite, and exposes `/health /ready /live` probes. We change its environment, not its architecture.
2. **No shared compute, ever.** One EC2 instance per customer org IS the isolation model, not a stopgap. Instances are never reassigned across tenants; deprovision = terminate (bind-once/terminate-on-unbind, adopted from TOWER — it eliminates disk-residue policy by construction).
3. **Polling only; zero inbound to the data plane from the internet.** Every tracker connector supports polling. This deletes webhook ingress, tunnels, and per-tenant webhook routing from v1.
4. **The tracker is the message bus.** The board dispatches work by writing the `pilot` trigger label; the instance's stock poller + ProcessedStore dedup picks it up. Zero changes to Pilot's ingest path — and this remains the degraded-mode fallback forever, even after v2 adds a live channel.
5. **Restart-to-apply config, with versioned specs.** Config change = render a new immutable, versioned `config.yaml`, push via SSM RunCommand, restart the systemd unit. The instance reports its applied spec version back through `/api/v1/status` so drift is detectable from day one (stolen from TOWER; costs one field now, saves an incident later).
6. **BFF, not OIDC.** The dashboard talks to a backend-for-frontend that holds auth-service tokens server-side behind httpOnly cookie sessions (stolen from Deck). This sidesteps auth-service's nil OIDC provider — the single biggest greenfield auth item — and keeps tokens out of the browser entirely. OIDC lands later without blocking login.

### System diagram

```
                        Internet
                           │
         ┌─────────────────┼──────────────────────────────────┐
         │           CloudFront / ALB                         │
         │        ┌────────┴────────┐                         │
         │   app.pilot.dev      api.pilot.dev                 │
         │   (SPA, static S3)       │                         │
         │                    ┌─────▼──────────┐   ┌────────┐ │
         │                    │  console-api   │──▶│ Stripe │ │  CONTROL PLANE
         │                    │  (BFF: httpOnly│   └────────┘ │  (one VPC, shared)
         │                    │  cookie sessions)             │
         │                    └┬─────┬───────┬─┘              │
         │       gRPC ValidateToken  │       │ AWS APIs:      │
         │    ┌────────▼───┐  ┌─────▼──┐    │ EC2 RunInstances│
         │    │auth-service│  │  RDS   │    │ SSM SecureString│
         │    │:4000/:4001*│  │Postgres│    │ SSM RunCommand  │
         │    └─────┬──────┘  └────────┘    │ (STS tags ABAC) │
         │     Redis│   (*:4001 private,    │                 │
         └──────────┼──── never proxied)────┼─────────────────┘
                    │  board sync (studio-sdk│      ops Prometheus scrapes
                    │  clients + per-provider│      :9090/metrics w/ tenant
                    ▼  rate budgeter)        │      external labels → Grafana
    ┌────────────── tracker APIs ────────────┼──────────────────────────────┐
    │  GitHub / Linear / Jira  ◀── the "message bus": board writes          │
    └────────▲──────────────────▲────────────┼──── 'pilot' label ───────────┘
             │ poll (30s)       │ poll       │ :9090 bearer, SG-restricted
    ┌────────┴────────┐  ┌──────┴────────────▼┐
    │  EC2 tenant A   │  │  EC2 tenant B      │   ... one box per customer
    │  pilot daemon   │  │  pilot daemon      │   DATA PLANE
    │  SQLite + git   │  │  SQLite + git      │   (private subnets, zero
    │  claude CLI     │  │  claude CLI        │    inbound from internet)
    └────────┬────────┘  └────────┬───────────┘
             └── outbound 443 (NAT): api.anthropic.com, github.com,
                 tracker APIs, own /tenants/{org}/* SSM path only ──▶
```

### Component list → source repo mapping

| Component | Source asset | Reuse mode |
|---|---|---|
| Tenant workload (executor, autopilot, approvals, pollers, gateway API, ledger) | `pilot` | As-is binary; new packaging (systemd + SSM env wrapper), one new REST approval-decision endpoint on the existing `DecisionRecorder` seam, `auto_hot_upgrade: false` |
| Worker image | `aws-infrastructure-pilot` Packer Golden AMI | Extend: add pilot binary, systemd unit, secrets-fetch wrapper, clone-on-provision bootstrap (genuinely new, small) |
| Network/IAM/KMS baseline | `aws-infrastructure-pilot` stacks | As-is semantics: VPC endpoints, NACLs, IMDSv2, no-inbound posture; SSM String→SecureString upgrade; path-scoped IAM via STS session tags |
| Identity | `auth-service` | As-is, single Default tenant; live subset only (register/login/TOTP/refresh/JWKS, gRPC ValidateToken); :4001 admin private, only console-api calls it; tenant middleware/RLS **not wired** in v1 |
| console-api + BFF (NEW) | — | The one significant new backend: orgs, members, connections, instances, cards, approval mirror, provisioner, board sync worker, instance proxy, cookie-session BFF |
| Board sync read/write layer | `studio-sdk` tracker clients | As-is for GitHub/Linear/Jira; small upstream patches (Linear cursor-follow past `first:50`, Jira `updated >=` delta JQL); mandatory `sdk/util/text` sanitization; **new global per-provider rate budgeter wrapping all clients** (stolen from TOWER — built before tenant #20, not after the first provider ban) |
| Dashboard SPA | `fleet-manager-frontend` + `design-dna.json` v3 | Lift stack, auth store, axios client, design system, `useForm`, ws/liveRead, mock adapter; v3 is the single visual language (the Drift-vs-v3 conflict is decided once: v3 wins) |
| Kanban/HITL IA | `drift-ui` wireframes | IA spec only: lanes, hero "Needs You" column, on-card approve/reject, waiting timers, per-run cost chips; 5 state hues mapped onto v3 semantic tokens |
| Ops observability | `pilot` `/metrics` + `deploy/grafana` PromQL | One central ops Prometheus scraping every instance with `tenant` external labels; grafterm panels ported to Grafana. Customer-facing charts come from the instance JSON API via proxy, not a TSDB. `grom` internals not extracted |
| Models engine | `pilot` executor backends | `claude_code` only, BYOK via GH-2371 env injection; multi-backend engine stays an internal v2 lever |
| Transactional email | `email-service` | Thin: verification, password reset, instance-down notice |
| Billing | Stripe Checkout | Flat plan, no metering in v1; `usage_events` accumulates on-instance for later |

### Data flows

**Signup → first shipped PR:** register/login (auth-service password flow via BFF cookies) → create org → Stripe Checkout → paste credentials (GitHub PAT + repo pick, Linear key / Jira email+token, Anthropic key, optional Slack bot token; each with a connection-test button) → click Provision → console writes SecureStrings, launches EC2, bootstrap clones repos, renders versioned config.yaml, starts pilot → green `/ready` in <5 min → board fills → drag card to Queued → console writes `pilot` label → instance poller (30s) picks it up → worktree → Claude Code → PR → CI → approval rules fire → "Needs You" card → customer approves in dashboard → decision hits the new instance endpoint → existing DecisionRecorder → autopilot merges → card to Done → tracker issue transitioned.

**Monitoring/logs:** dashboard → console proxy → instance `/api/v1/{logs,history,metrics,queue}`: live log tail via the existing SubscribeLogs-backed endpoint, the `pilot trace` execution_events timeline rendered as the per-card activity feed, success rate / queue depth / per-model tokens+cost from the existing ledger. All customer-surfaced free text (`executions.output`, `execution_events.detail`) passes a redaction scrubber in the proxy before rendering.

### The kanban: a control surface, not a replica (v1)

No sync engine, no ID mapping, no conflict resolution exists in the asset base — true bidirectional sync is the single biggest scope trap. v1 refuses it:

- **Read path (60s per connection):** studio-sdk clients normalize issues into a `cards` table (org_id, provider, connection_id, native_issue_id, sequence_id preserving the provider-prefix scheme — load-bearing for Pilot branch naming — title, body excerpt, labels, `core.NormalizePriority`, tracker state, URL, updated_at). A board is a query over `cards` across connections, so one board mixes Jira + Linear + GitHub for free.
- **Columns (Drift lanes, adapted):** Backlog / Queued / Working / Needs You / Done. Backlog/Done derive from tracker state; Queued/Working overlay execution state joined on sequence_id from `/api/v1/queue` + `/history`, with the 22-stage execution_events vocabulary driving badges and the timeline drawer; Needs You mirrors `approval_pending`. Reserved semantic columns and a per-connection status-mapping wizard (stolen from Deck) make the board↔execution contract explicit at setup time, especially for Jira transitions.
- **Write path — exactly three verbs:** (1) **Dispatch**: add the trigger label (exists on all 7 connectors). (2) **Approve/Reject**: console → new instance endpoint → existing approval package (the dashboard is "just one more Handler implementation"). (3) **Close**: `UpdateIssueState` / `TransitionIssueTo` (existing client methods).
- **Conflict policy: there isn't one, by design.** Tracker wins on content and open/closed state; the board never edits titles/bodies/comments; races reconcile at the next poll (≤60s convergence). **But:** every write the board loses is journaled in a `conflict_journal` table (stolen from TOWER), and the UI surfaces it as a visible conflict chip with one-click retry (stolen from Deck) — losing writes are never silently dropped, and the journal becomes the storage backing for the v2 sync engine.
- **Explicitly not in v1:** issue creation from the board, field editing, comment/assignee sync, webhook real-time, GitLab/AzDO/Asana/Plane on the board. If v2 wants true sync: a new `SyncCapable` contract in studio-sdk, not a bent `IssueEvent`.

---

## 3. Tenancy & Security Model

**The VM is the tenant boundary.** One dedicated EC2 per customer org, launched from the shared Golden AMI. Every single-operator assumption in the codebase (one `~/.pilot`, one SQLite, fixed `/tmp` pool paths, port 9090, process-global metrics) becomes a per-tenant fact instead of a multi-tenancy bug. Noisy-neighbor is structurally impossible. Instances are bound to exactly one org for their entire lifetime and terminated on deprovision — never wiped and reassigned.

**Compute isolation is structural; we harden the rest incrementally:**

- **IAM via STS session-tag ABAC (adopted from TOWER, day one):** the instance role's `ssm:GetParameter` condition is `/tenants/${aws:PrincipalTag/tenant_id}/*` — a compromised instance *cannot express* a request for another tenant's secret; it is an IAM condition, not code discipline. This upgrades the sandbox's `iam-pilot-agent.yml` path-scoping pattern from convention to constraint.
- **Network:** all tenant instances in private subnets, zero inbound from the internet; the only inbound rule is :9090 from the console-api SG, with a per-instance static bearer token (the gateway's existing `api-token` auth) stored in the tenant's SSM path. Instances cannot reach each other, RDS, or auth-service. Outbound 443 via NAT to api.anthropic.com, github.com, and tracker APIs. A domain-allowlisting egress proxy is explicitly scheduled (Phase 3), not deferred indefinitely — it is the only real compensating control for exfil-via-allowed-hosts.
- **Trust direction:** the console never trusts instance-asserted tenancy. Every proxied call is resolved control-plane-side (org → instance-id → SG/token), and any instance response referencing a task/card outside its bound org is rejected and alarmed (protocol invariant stolen from Deck/TOWER).
- **Data:** execution state, memory, logs, ledger live in on-instance SQLite on per-tenant KMS-encrypted EBS; the dashboard reads only through the authenticated JSON API (respecting MaxOpenConns(1)). Control-plane data lives in RDS Postgres, every table keyed by org_id, every query org-scoped in the service layer — WHERE-clause tenancy, deliberately simple, with Postgres RLS (`FORCE ROW LEVEL SECURITY` + `app.current_tenant_id`) scheduled as Phase 3 defense-in-depth, not v1 critical path.
- **Identity:** auth-service runs single-tenant on the Default tenant; org membership and authorization live in console-db (validated JWT user_id → org_members row). This dodges auth-service's unwired-tenancy integration risk entirely.
- **Verification, not assertion:** a hostile-ticket pen test — a prompt-injected run attempting exfil of env, disk, SSM, and network beyond the allowlist — is a hard phase-exit gate before any design partner with a private repo onboards (stolen from TOWER via the business judge).

**Blast-radius statement (the one we tell customers):** a fully compromised execution — hostile repo code, prompt-injected agent — can reach exactly that customer's own repo credentials, their own Anthropic key, their own instance disk, and their own SSM path. It cannot reach another tenant's compute, secrets, data, or network path, and it cannot reach the control plane beyond authenticated egress to public tracker APIs. The residual risk we disclose: a prompt-injected run can exfiltrate the customer's *own* PAT — mitigated by scoped-PAT guidance in v1 and replaced by short-lived GitHub App installation tokens in v2.

---

## 4. Config & Secrets Distribution (dashboard → instance)

**Secrets:**
- Per-tenant SSM SecureString namespace `/tenants/{org_id}/{ANTHROPIC_API_KEY, GITHUB_TOKEN, LINEAR_API_KEY, JIRA_API_TOKEN, SLACK_BOT_TOKEN, PILOT_GATEWAY_TOKEN}` — fixing the sandbox's plain-String gap. KMS key per environment. console-api is the only writer.
- **Write-only from the dashboard** (stolen from Deck): secrets are never displayed back; console-db stores metadata + last-4 only. Rotation = overwrite SecureString + restart.
- On-instance: injected as env via a systemd `EnvironmentFile` fetched from SSM at unit start. `config.yaml` contains only `${VAR}` references, resolved by the existing `os.ExpandEnv` + GH-3755 sensitive-key guard. Flows that call `Save()` (which would round-trip secrets to disk) are disabled in the hosted profile; where disabling is impractical it is logged as tracked debt — the disk is single-tenant and KMS-encrypted, so this is contained, not ignored.

**Config flow-down:**
1. Customer edits settings in the dashboard → console-api validates and renders a complete `config.yaml` from the tenant's connection records (the pilot config schema *is* the instance spec — the control plane renders exactly the YAML the daemon already parses).
2. Each rendered spec is **immutable and versioned** (monotonic `spec_version`, stored in console-db with an audit trail of who changed what).
3. Push = SSM RunCommand: write spec, `systemctl restart pilot`. If an execution is in flight, the push queues until the instance is idle (the orchestrator has no pause API — honest constraint, surfaced in the UI as "applying after current task").
4. The instance reports its applied `spec_version` via `/api/v1/status`; console alarms on drift between desired and applied. No hot reload is built — a 10-second restart on a settings save is acceptable v1 UX, locked in as a non-goal.

---

## 5. Models Management — Final Recommendation: DO NOT expose in v1

The founder flagged this as questionable. The answer is a firm no for v1, with a narrow, pre-decided v2 surface.

**v1 ships:** `claude_code` backend only, **BYO Anthropic API key required at onboarding**. The GH-2371 env-injection hook (`ANTHROPIC_BASE_URL`/`AUTH_TOKEN`/`MODEL` into the subprocess) makes BYOK a config-field change, not a feature. Model routing ships with stock defaults (trivial→Haiku, rest→Sonnet; Opus for epic planning) and is not customer-visible. The dashboard shows per-execution model, tokens, and cost **read-only** from the existing ledger. The key is write-only, last-4 displayed.

**Reasoning:**
1. The CLI backend is the only battle-tested path — it is the only backend exercised by the project's own dogfooding. The direct-API `anthropic-api`/`openai-api` backends re-implement the agent loop (own tool dispatch, 60-turn cap) with unproven quality; offering them as customer choices converts every quality gap into a support ticket.
2. Cost accounting for non-Anthropic models is wrong today — the pricing table is hardcoded Anthropic-only and defaults unknown models to Sonnet rates. Shipping a model picker on top of broken cost display destroys trust in the one billing surface we do show.
3. A model picker multiplies the QA matrix (5 backends × N models × routing tiers) while buying zero acquisition value for the first ten customers. Nobody's first question is "can I run Qwen"; it's "did my ticket ship."
4. BYOK means Anthropic enforces the customer's own rate limits and spend caps — deferring our own quota system for free.
5. Complexity routing depends on English keyword heuristics; exposed as a knob it invites "why did it use Haiku" tickets.

**v2 surface (pre-decided, in this order):** (a) platform-managed keys as an alternative to BYOK (requires per-tenant metering — `usage_events` UserID plumbing — and side-channel call capture for the judge/classifier LLM calls); (b) a single "quality vs cost" preset mapping server-side onto the existing ModelRoutingConfig tiers; (c) only then, OpenAI-compatible/OpenRouter as a distinct tier with correct per-provider pricing. Full model pickers remain out indefinitely unless a paying customer segment demands them. The multi-backend engine stays exactly what it is today: our internal lever and future option, not a surface.

---

## 6. Phased Roadmap

### Phase 0 — v0 internal + concierge: one hosted design partner, by hand (weeks 1–3)

**Scope:** No control plane. Extend the Golden AMI Packer template (pilot binary, systemd unit, SSM-secrets fetch wrapper). Manually launch **two** instances in the existing pilot VPC: tenant #0 = the Pilot repo itself (dogfood rule — the hosted path gets the same battle-testing the laptop path has), and one paying design partner. Hand-write their config.yaml (BYO Anthropic key, GitHub PAT, polling-only, `auto_hot_upgrade: false`), hand-create SecureStrings with session-tag-scoped IAM from day one, clone via a bootstrap script (this births clone-on-provision). Partner interacts via GitHub labels and PR review only. Charge them — even a token amount; this is the willingness-to-pay test. **In parallel**, the frontend track starts against the fleet-manager mock adapter: the kanban and card-timeline UI are built and usability-tested weeks before console-api exists (stolen from Deck — backend depth must not serialize product work).

**Exit criteria:** a design partner's real ticket goes label → PR → CI → merge entirely on AWS with zero laptop involvement; the partner has paid; AMI, IAM ABAC, SecureStrings, and bootstrap are committed as code — the manual runbook IS the provisioner spec for Phase 1; the mock-mode board demo exists.

**Deferred:** everything else.

### Phase 1 — v1 core: self-serve control plane, signup → provisioned instance (weeks 4–9)

**Scope:** console-api (orgs, members, connections, instances; provisioner via EC2 RunInstances + SSM; versioned spec push with applied-version reporting; restart-to-apply). RDS Postgres. auth-service deployed as-is behind the BFF cookie-session layer (gRPC ValidateToken for the console; :4001 private). Dashboard SPA: login, org creation, paste-credential integration forms with connection-test buttons, provision button, instance status page proxying `/api/v1/status` + `/ready`. Stripe Checkout gate before provision. Dogfood tenant #0 migrates onto the provisioned path.

**Exit criteria:** a new customer completes signup → payment → paste credentials → Provision → green instance → first PR with zero operator SSH; deprovision terminates cleanly; 3+ tenants (incl. dogfood) run concurrently without interference; spec-version drift detection demonstrably fires.

**Deferred:** the board (Phase 2), OIDC, org RBAC beyond owner/member, webhooks, any scheduling.

### Phase 2 — v1 product: mixed-tracker kanban, approvals, logs (weeks 10–16)

**Scope:** Board sync worker (GitHub + Linear + Jira via studio-sdk; `cards` table; 60s polling; Linear cursor patch; Jira delta JQL; **per-provider global rate budgeter**; conflict_journal + visible conflict chips). Kanban UI on the fleet-manager design system with Drift's hero "Needs You" lane; per-connection status-mapping wizard. The three write verbs (dispatch label / approve-reject via the new instance endpoint on the DecisionRecorder seam / close via state transition). Per-card execution timeline from execution_events; live log tail via console proxy; run history + token/cost display. Redaction scrubber on all surfaced free text. Ops Prometheus + Grafana with tenant labels + basic alarms (instance down, `/ready` failing, queue stalled). **Hostile-ticket isolation pen test.** Measured signup→first-PR funnel (target ≤30 min).

**Exit criteria:** a customer operates for one full week exclusively from the dashboard — dispatching, approving, closing — across at least two tracker types on one board; approval decisions from the board survive an instance restart (GH-3825 verified end-to-end); the pen test passes (no cross-tenant reach, no secret exfil beyond the disclosed single-tenant blast radius); redaction reviewed against N real runs before customer exposure.

**Deferred:** issue creation/editing/comment sync, webhook real-time, transcripts, GitLab/AzDO/Asana/Plane boards.

### Phase 3 — v1 GA: harden, bill, second cohort (weeks 17–21)

**Scope:** Stripe subscription lifecycle (suspend instance on payment failure). Nightly EBS snapshots + restore runbook. AMI rolling-upgrade automation (one org at a time). Member invitations (owner/member roles in console-db). Onboarding polish (empty states, connection diagnostics, first-run checklist). Usage page (read-only cost/token rollups from instance ledgers into `usage_rollup`). Secrets-rotation runbook. Security pass: SG audit, scoped-PAT guidance docs, **egress domain-allowlist proxy**, Postgres RLS wiring in console-db as defense-in-depth. Pricing set from Phase-2 measured COGS (instance-hours + NAT + EBS + support), floor $300–500/mo; **no free tier that implies an always-on instance**.

**Exit criteria:** first fully self-served paying customer (never spoke to us before card-on-file) ships PRs weekly; 10 tenants stable for 30 days; an AMI upgrade rolls the fleet with no customer-visible downtime beyond the restart window; MRR covers fleet COGS.

**Deferred:** everything in v2.

### Phase 4 — v2 scale: economics, sync, and the wedge (post-GA, prioritized backlog)

**Scope, in priority order:**
1. **Scale-to-zero warm pool with terminate-on-release** — the margin unlock that enables cheaper tiers and trials. Reuses the sandbox's Warm Pool ASG pattern, but instances bind to one tenant per wake and terminate on release (never `ReuseOnScaleIn` across tenants). Requires state externalization (SQLite → EBS snapshot/restore or event export).
2. **"Watch it work": transcript persistence + Run Theater/Run Story views** (Deck's product wedge). Persist Claude Code stream-json transcripts to per-tenant S3 behind the redaction pipeline; redaction review on real runs is a hard exit gate before customer exposure. The shareable Run Story ("$0.42, 14 minutes, merged") is a built-in sales artifact.
3. **GitHub App installation tokens + tracker OAuth apps** (Jira 3LO, Linear OAuth) minted control-plane-side — long-lived PATs leave the instance env; the disclosed exfil residual shrinks to 1-hour tokens.
4. **True bidirectional sync**: a new `SyncCapable` contract in studio-sdk, ID-mapping store, field-level diffs, conflict UI built on the already-populated conflict_journal; issue creation and comment sync from the board.
5. **Webhook ingress** at the control plane for <5s board freshness (label-bus stays as fallback).
6. Platform-managed keys + metered billing (usage_events plumbing), the quality/cost preset, then OpenRouter tier.
7. auth-service tenancy wiring + OIDC provider, Redis-backed sessions, multi-region parameterization.

**Exit criteria:** trial tier with sub-$100 COGS viable; Theater demo closes deals; PATs eliminated from instance env for GitHub-based tenants.

---

## 7. Top 10 Risks (ranked)

1. **COGS ceiling forces premium pricing before the product earns it.** Always-on t3.large (~$60–70/mo + EBS + NAT) makes anything below ~$300/mo unviable and free trials structurally unaffordable. *Mitigation:* explicit per-tenant COGS model from Phase 0; design-partner pricing $300–500+/mo; target the agency/team segment; scale-to-zero is the first v2 item; never launch a tier the architecture can't serve.
2. **Kanban expectation mismatch.** Marketing says "manage everything from our kanban"; v1 ships three verbs and read-only content. *Mitigation:* sell the board as Pilot's control surface, not a tracker replacement, from the first demo; visible conflict chips make the sync boundary honest; the sync engine is a sequenced v2 item, not a rescue project pulled into the busiest phase.
3. **Customer secrets live in the env of a VM executing arbitrary repo code.** A prompt-injected run can exfiltrate the customer's own PAT/tracker keys. *Mitigation:* blast radius structurally capped at one tenant (VM + SG + STS-tag ABAC); scoped-PAT guidance; disclosed honestly; egress domain-allowlist proxy in Phase 3; GitHub App / OAuth tokens in v2 remove long-lived creds from the box.
4. **Instance SQLite is the sole execution record.** Instance loss between nightly snapshots loses up to 24h of history and in-flight state. *Mitigation:* nightly EBS snapshots + restore runbook (Phase 3); approval state mirrored in console-db; conflict/dispatch state reconstructable from tracker; real event export scheduled with v2 scale-to-zero (which requires it anyway).
5. **auth-service reality gap.** README overstates completeness; wiring lags tests; Redis loss logs out every user; in-memory sessions are a single-replica SPOF. *Mitigation:* depend only on the demonstrably live subset (password auth, tokens, gRPC); BFF isolates the browser from token mechanics; integration tests on the live subset in Phase 1; Redis persistence review and Redis-backed sessions before ~20 tenants.
6. **Two adapter codebases touch the same trackers** (console uses studio-sdk; instances run pilot's duplicated internal adapters; SDK is v0.x with breaking changes allowed). *Mitigation:* api.golden lock test; pin SDK versions per release train; freeze instance adapters for v1; finish M7 on the SDK's schedule, not the SaaS critical path; provider-API canary alerts on both paths.
7. **Label-as-bus latency and races.** Up to 30s+60s between drag and visible reaction; rapid dispatch/undispatch can race the poller. *Mitigation:* optimistic "dispatching…" card state; ProcessedStore dedup absorbs races; measured as a funnel metric; webhooks are the known fix, deliberately deferred with a fallback story that never goes away.
8. **Restart-to-apply delays urgent changes.** A credential rotation can wait behind a running task (worst case ~60 min timeout tier). *Mitigation:* queued pushes with UI visibility; "force restart (abandons current task)" escape hatch for security-urgent rotations; spec versioning makes pending-vs-applied unambiguous.
9. **Surfaced output leaks repo code or secrets to the dashboard.** `executions.output` / `execution_events.detail` hold raw model text; metric semantics (e.g. `pilot_prs_failed_total` overcounting) can misreport if copied into customer-facing numbers. *Mitigation:* redaction scrubber in the proxy from Phase 2 with a reviewed-on-real-runs exit gate; single-tenant data path caps severity; customer-facing metric names re-derived with documented semantics, never copied blind.
10. **Single-region, single-account hardcoding** (eu-central-1, profile quantflow, org qf-studio naming everywhere). *Mitigation:* accepted for v1 and possibly a data-residency selling point; parameterization budgeted as explicit Phase 3+/v2 debt, triggered the day a US customer signs — tracked, not hidden.

---

## 8. Open Decisions for the Founder

1. **Pricing floor and segment.** Recommended default: **$500/mo design-partner price, agency/team segment, no free tier** — the always-on economics demand it, and Phase 0 tests willingness-to-pay before any tiering debate.
2. **BYO Anthropic key + pasted PATs as the onboarding bar.** Recommended default: **yes for the first cohort** — it matches studio-sdk's auth model exactly and costs nothing; if two of the first five prospects balk at Jira PATs, pull OAuth 3LO forward and let Jira slip to fast-follow behind GitHub+Linear.
3. **Region: EU-only launch.** Recommended default: **yes, eu-central-1 only**, marketed as EU data residency; parameterize on first US signature, not before.
4. **Dogfood as tenant #0 from Phase 0.** Recommended default: **yes, non-negotiable** — it is the only way the hosted path accumulates the battle-testing the laptop path has, and it makes every fleet upgrade a self-inflicted canary first.
5. **auth-service availability tradeoff.** Recommended default: **accept single-replica in-memory sessions until the first outage or 20 tenants, whichever comes first** — signed off explicitly as an availability tradeoff, with Redis-backed sessions pre-scoped so the fix is a sprint, not a project.
6. **Models management.** Recommended default: **adopt the Section 5 recommendation as written** — claude_code only, mandatory BYOK, read-only cost display, no picker; revisit only when a paying customer makes model choice a closing condition.