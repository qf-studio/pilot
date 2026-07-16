# Pilot SaaS — AWS Fleet Orchestration (Design)

**Created**: 2026-07-13 · **Status**: DESIGN
**Parent**: `saas-architecture.md` · **Task doc**: `.agent/tasks/TASK-405-pilot-saas-platform.md`

**Reconciliation note:** this doc's §2/§6 stop-start sleep/wake scheduler is v1 (it is the
margin lever: ~$52/mo typical tenant vs ~$89 always-on). The architecture doc's
"scale-to-zero warm pool with terminate-on-release" remains the v2 evolution (requires state
externalization). Both are true; do not conflate them.

## ⚠️ Post-verification corrections

1. **Golden AMI (§8 verdict, Epic A item 2)**: `gh` CLI is NOT in the AMI — pilot shells out to `gh` for PR creation and CI monitoring; add it. Claude Code installs unpinned (`claude_code_version` default `latest`); Golden AMI v2 must pin it (the variable already exists).
2. **SSM String-secrets nuance (§4)**: the plain-String pattern is the workload README's *manual CLI instructions* (no IaC creates those params); the mgmt deploy script already creates SecureString+KMS params — the correct pattern exists in-repo to copy.
3. **`set-desired-capacity` is not called from the infra repo** — it lives in the pilot repo's deploy workflows via the scoped `agent-deployer` role (which does NOT have AdministratorAccess; the mgmt runner does). The §8 "drop as scheduler" verdict stands; the retirement target is the pilot-repo workflow + mgmt-runner path.
4. **IMDSv2 (§5/§8)**: enforced only on the agent launch template today; runner instances have no MetadataOptions. Fleet CDK: `HttpTokens: required` on every instance, no exceptions.

---

# Pilot Fleet Orchestration on AWS — Technical Design

Scope: how N customers' Pilot instances run, schedule, and balance on AWS for Pilot Cloud v1 ("One Box Per Customer"), with the v2 evolution path. Assumes the winning architecture context (console-api control plane, polling-only ingest, tracker-as-message-bus, restart-to-apply config).

---

## 1. Compute choice: plain EC2, one instance per tenant, **no ASG, no ECS, no EKS**

### Decision

**Per-tenant plain EC2 instances (t3.large default) launched from the Golden AMI, managed directly by console-api's provisioner via the EC2 API, with a CloudWatch auto-recovery alarm per instance.** Not Fargate, not an ASG-per-tenant, not EKS.

### Justification against Pilot's anatomy

| Pilot property (verified) | ECS/Fargate | EC2 ASG-per-tenant | Plain EC2 | EKS |
|---|---|---|---|---|
| Long-lived stateful daemon; SQLite at `~/.pilot/data/pilot.db` (WAL, MaxOpenConns(1)) is the system of record | ✗ Fargate EBS volumes can't be re-attached to a replacement task; task churn = data loss or snapshot gymnastics. EFS + SQLite WAL is a documented corruption hazard | ✗ ASG instance replacement re-creates root from AMI — SQLite gone unless we build volume-reattach hooks (lifecycle hook + Lambda = complexity for zero gain at N=1) | ✓ EBS data volume survives stop/start and instance replacement; snapshot-friendly | ✓ possible via PV, but see ops row |
| Git worktrees on local disk, 800MB+ each (GH-2168), heavy npm/pip churn | ~ Fargate ephemeral storage caps at 200GB and is slower; cost of provisioned storage is per-task | ✓ | ✓ 120GB gp3 | ✓ |
| Spawns `claude -p --dangerously-skip-permissions`, shells out to `git`/`gh`; RCE-as-a-service → **hard VM boundary per tenant** | ~ Fargate is Firecracker-isolated per task, actually acceptable on isolation — but loses on state | ✓ | ✓ | ~ only with strict node-per-tenant pinning, which defeats the point of K8s |
| Ambient toolchain: claude CLI, Node 22, gh, git, uv — already baked into the existing Packer Golden AMI | ✗ requires new container image pipeline (ECR repo exists but is unused; no Dockerfile for the daemon exists) | ✓ AMI reuse | ✓ AMI reuse | ✗ image pipeline + cluster |
| Idle-tenant economics: daemon polls, so it must run to work — cheapest idle = **stopped instance** (pay EBS only) | ✗ Fargate has no stop/start; only kill/relaunch | ~ scale-to-0 works but re-launch loses root state | ✓ `ec2:StopInstances` / `StartInstances`, 40–90s wake | ~ |
| Ops model already proven: SSM RunCommand delivery, SSM Parameter Store secrets, `/health /ready /live` probes | ✓/~ | ✓ | ✓ direct reuse of the sandbox's SSM patterns | ✓ but new |
| Fleet size horizon: 10–100 tenants in v1 | overkill either way | — | ✓ a reconciler loop over `DescribeInstances` is trivial at this scale | ✗ cluster ops burden unjustified below ~500 heterogeneous workloads |

**Why not the sandbox's warm-pool ASG as the tenant model:** the existing `pilot-agent-pool` ASG is a *task* pool (scale up per task, scale to 0), and its `ReuseOnScaleIn` warm pool explicitly reuses EBS across tasks — the digest flags this as cross-tenant data residue. It's the wrong shape for always-on-ish per-tenant daemons. We keep the warm-pool *trick* for a different purpose (§2, provisioning accelerator), not as the tenant substrate.

**Instance replacement instead of in-place mutation:** each tenant instance carries two volumes — root (from AMI, immutable, holds binary + toolchain) and a **data volume** (gp3, mounted at `/var/lib/pilot`, holding `config.yaml`, `data/pilot.db`, repo clones). AMI upgrade = stop → detach data volume → launch replacement from new AMI → attach → start. SQLite, repo clones, and approval state survive; the AMI stays immutable. This also makes `upgrade.auto_hot_upgrade: false` (config field exists, defaults true — must be flipped in the rendered config) non-negotiable: the fleet upgrades via image replacement, never self-exec.

---

## 2. Instance lifecycle

### State machine (owned by console-api, persisted in `instances` table in RDS)

```
REQUESTED → PROVISIONING → BOOTSTRAPPING → RUNNING ⇄ STOPPED
                                              │        │
                                              ├→ UPGRADING (stop→swap AMI→start)
                                              ├→ SUSPENDED (billing lapse: stopped + SG quarantined)
                                              └→ TERMINATED (org deleted: snapshot → terminate → delete secrets)
```

| Transition | Trigger | Mechanism |
|---|---|---|
| REQUESTED → PROVISIONING | Stripe checkout success + integrations saved | `ec2:RunInstances` from `/pilot/GOLDEN_AMI_ID`, tags `pilot:org_id`, `pilot:env`; create data volume; write SSM SecureStrings under `/tenants/{org_id}/*` |
| PROVISIONING → BOOTSTRAPPING | instance `running` | cloud-init: format/mount data volume, fetch env file from SSM, **clone repos with tenant PAT** (the one new instance-side script — clone-on-provision doesn't exist today), render nothing (config.yaml is delivered by console via SSM RunCommand), `systemctl start pilot` |
| BOOTSTRAPPING → RUNNING | console polls `GET :9090/ready` (existing ReadinessChecker) green | mark active; board goes live. Target < 5 min signup→green |
| RUNNING → STOPPED (sleep) | no queued/running executions for **4h** (via proxied `/api/v1/queue` + `/api/v1/status`) AND board-sync sees no open `pilot`-labeled items for the org | `ec2:StopInstances`. Idle tenant costs EBS only |
| STOPPED → RUNNING (wake) | any of: customer drags card to Queued; board-sync poller detects a new `pilot`-labeled issue on the tracker; customer opens dashboard "live" views; approval decision pending delivery | `ec2:StartInstances`; console holds the board action in `PENDING_WAKE` until `/ready`, then applies the label. Wake latency 40–90s — invisible next to a 5–30 min execution |
| RUNNING → UPGRADING | fleet AMI rollout (batched, canary-first: our own dogfood org is tenant #0) | stop → detach data vol → `RunInstances` new AMI → attach → start → `/ready` gate → next batch |
| any → SUSPENDED | Stripe subscription past_due | stop instance; revoke console-proxy access; keep volume + secrets 30 days |
| SUSPENDED/any → TERMINATED | org deletion / 30-day lapse | final EBS snapshot (30-day retention), `ec2:TerminateInstances`, delete `/tenants/{org_id}/*` SSM params, delete per-tenant SG |

**Key point on "hibernate idle tenants / wake on webhook":** v1 has no webhooks — but the console's **board-sync worker already polls every connected tracker** for kanban sync. It is therefore also the wake signal: a stopped tenant's tracker is still watched centrally, and actionable work (new/labeled issue, card dragged to Queued) starts the box. The instance's own 30s poller takes over once up. No EC2 Hibernate (it caps at instance-RAM constraints and buys ~20s over plain stop for a daemon that cold-starts in seconds); plain stop/start is enough.

**Provisioning accelerator (optional, cut-line):** keep a shared pool of 1–2 *unassigned* stopped instances pre-launched from the current AMI (the warm-pool idea, re-homed). Assignment = tag with org_id, attach fresh data volume, move to tenant SG, bootstrap. Turns signup-to-green from ~4 min to ~60s. Ship without it first; add if onboarding funnel data says the wait hurts.

---

## 3. State

### Decision: SQLite-on-EBS stays the per-tenant system of record in v1. RDS Postgres is control-plane-only.

- **Per tenant (data volume, `/var/lib/pilot`):** `pilot.db` (executions, approval_pending, usage_events, memories, patterns), repo clones, rendered `config.yaml`. Durability = gp3 (five-nines annual durability) + **nightly EBS snapshots via Amazon Data Lifecycle Manager** (one DLM policy targeting tag `pilot:data-volume=true`, 7 daily + 4 weekly). RPO 24h on a catastrophic volume loss is acceptable for v1 — the tracker and GitHub hold the ground truth of issues and PRs; pilot.db loss loses learning patterns and history, not customer work.
- **Control plane (RDS Postgres, single db.t4g.small multi-AZ):** `organizations`, `org_members`, `connections`, `instances` (incl. desired_state/observed_state for the reconciler), `cards`, `card_links`, `approval_mirror`, `usage_rollup`.
- **The dashboard never touches pilot.db directly.** All reads go console-api → instance `:9090 /api/v1/{status,queue,history,logs,metrics,gitgraph}` with the per-instance bearer token (existing `api-token` auth type, verified constant-time compare). This respects MaxOpenConns(1) and keeps the DB file an implementation detail.
- **Why not migrate executions to RDS in v1:** the schema is threaded through the executor, approval, autopilot, and learning subsystems; a Postgres port is a multi-week fork of the core product for zero customer-visible value. The export path that *does* need central state (usage rollups for billing, board status) is event-shaped: v1 does it by proxy-polling `/api/v1/metrics` + `/api/v1/history` into `usage_rollup` hourly. v2 item: an outbound event stream from the instance (see §8).

---

## 4. Config & secrets push (control plane → instance)

### Secrets layout (SSM Parameter Store, SecureString, per-env KMS key)

```
/tenants/{org_id}/ANTHROPIC_API_KEY      (BYO, required at onboarding)
/tenants/{org_id}/GITHUB_TOKEN           (repo PAT)
/tenants/{org_id}/LINEAR_API_KEY         (optional)
/tenants/{org_id}/JIRA_API_TOKEN         (optional)
/tenants/{org_id}/SLACK_BOT_TOKEN        (optional)
/tenants/{org_id}/PILOT_GATEWAY_TOKEN    (generated by console; the api-token bearer)
```

This **fixes the sandbox's plain-String gap** (today `/pilot/ANTHROPIC_API_KEY` etc. are type String) and reuses its one genuinely good IAM idea: the per-instance role grants `ssm:GetParameter*` on `arn:...parameter/tenants/{org_id}/*` **only** — direct lift of the path-scoping in `stacks/workload/iam-pilot-agent.yml`. Console-api's role is the only principal with `ssm:PutParameter` on `/tenants/*`. Choice of SSM over Secrets Manager: same KMS encryption, the agent-side fetch pattern already exists, and $0 vs $0.40/secret/month × 6 × N tenants; we don't need rotation lambdas in v1.

### Delivery: EnvironmentFile at unit start, `${VAR}` refs in YAML

1. Console renders `config.yaml` from the tenant's `connections` + defaults. **All sensitive values are `${VAR}` references** — never literals. Rendered config hard-sets: `gateway.auth.type: api-token`, `gateway.host: 0.0.0.0` (SG is the firewall), `upgrade.auto_hot_upgrade: false`, `tunnel: disabled`, pollers on / webhooks off, `executor.backend.type: claude_code`.
2. systemd unit: `ExecStartPre=/opt/pilot/fetch-secrets.sh` writes `/run/pilot/env` (mode 0400, tmpfs — secrets never touch the EBS volume as env material) from `ssm get-parameters-by-path /tenants/{org_id} --with-decryption`; `EnvironmentFile=/run/pilot/env`.
3. `config.Load()` does the rest: its existing `os.ExpandEnv` + GH-3755 guard **hard-fails on any unset `${VAR}` under token/key/secret/password keys** — a free integrity check that a missing SSM param can't silently launch a broken adapter.
4. **Config change = push + restart** (design rule 5): console writes the new YAML and runs `systemctl restart pilot` via one SSM RunCommand document (`pilot-apply-config`). ~10s of downtime; the poller's ProcessedStore dedup means no double-pickup, and approval_pending persistence (GH-3825) means in-flight approvals survive.
5. **Accepted debt, tracked:** `config.Save()` can round-trip expanded secrets to disk. Tolerable v1 (single-tenant, KMS-encrypted volume); v1 hardening issue = audit and disable Save()-invoking flows in hosted mode (env flag `PILOT_HOSTED=1` making Save() a no-op is a ~20-line upstream patch — the one place we bend "binary ships unmodified").

---

## 5. Networking

```
VPC pilot-fleet 10.30.0.0/16 (new, one per env; sandbox VPCs untouched)
├─ public subnets  ×2 : ALB (api.pilot.dev), NAT GW
├─ private "control" subnets ×2 : console-api (ECS Fargate or 1–2 small EC2),
│                                 auth-service, RDS, Redis, ops Prometheus
└─ private "tenant" subnets ×2 (or ×4) : tenant EC2 fleet
```

- **Inbound to tenant instances: exactly one rule.** Per-tenant SG allows TCP 9090 **from the console-api SG only** (proxy for logs/metrics/queue/history + the approval-decision endpoint). Nothing from the internet, nothing tenant↔tenant (per-tenant SGs have no cross-references; default deny covers east-west). SSM Session Manager (VPC endpoints, no SSH, port 22 closed everywhere) is the ops break-glass.
- **Egress:** tenant subnets route 443 via the shared NAT GW to api.anthropic.com, github.com, tracker APIs, npm/pypi (build steps inside worktrees need it — do **not** try to allowlist package registries in v1). VPC endpoints for SSM/SSMMessages/EC2Messages/CloudWatch Logs/S3/KMS keep control traffic off the NAT (copied from the sandbox's endpoint set). One NAT GW per env in v1 ($33/mo + $0.05/GB); per-AZ NAT is a v2 availability upgrade.
- **Webhook ingress: there is none in v1 — by design.** Polling-only deletes the tunnel/per-tenant-webhook-routing problem. The v2 path is already sketched by existing code: a central ingest endpoint on api.pilot.dev reusing the gateway's signature-verified webhook handlers (HMAC-SHA256 GitHub, Ed25519 Linear, etc.), mapping webhook → org via per-tenant secrets, and nudging the target instance (wake + "poll now"). That's a latency optimization (30s → ~1s pickup), not a functional gap.
- Prometheus scrape (`:9090/metrics`) from the ops box rides the same console-SG-shaped rule (put the Prometheus box in the console SG or a peer SG with the identical 9090 rule), with `tenant`/`org_id` external labels attached at scrape config.

---

## 6. The scheduler / "dynamic load balancing" — what it concretely means here

Instances are **stateful and tenant-pinned**, so classic load balancing (spray requests across a pool) does not apply and should not be built. "Dynamic scheduling" decomposes into four real mechanisms:

### 6.1 Fleet reconciler (the core new component, part of console-api)
A single-goroutine control loop, 60s tick, no new infrastructure:
- **Desired state** lives in the `instances` table (`desired: running|stopped|suspended|terminated`, `desired_ami`, `desired_instance_type`, `config_generation`).
- **Observed state** from `ec2:DescribeInstances` (tag-filtered) + proxied `/ready` + `config_generation` reported by a tag or the status endpoint.
- Diff → actions: start/stop, replace-for-AMI, push-config-and-restart, quarantine. Every action idempotent, journaled to an `instance_events` table (the ops audit trail the sandbox lacks).
- This replaces the sandbox's "GitHub Actions job calls set-desired-capacity" — the deployer-runner pattern is explicitly *not* carried forward as the scheduler.

### 6.2 Demand-based lifecycle scheduling (the actual "dynamic" part)
Wake/sleep per §2 driven by board activity. Fleet-wide effect: at any time only tenants with live work pay compute. Expected duty cycle for a typical customer (work queued during business hours, bursts) is 30–60% running; the scheduler makes idle-tenant cost ≈ EBS-only automatically, with no capacity planning.

### 6.3 Per-instance concurrency, not cross-instance spreading
Within a box, load is governed by knobs that already exist: `orchestrator.max_concurrent` (default 2, validated ≥1) and the sequential/parallel/auto execution modes with the scope-overlap guard. v1 policy: plan-tiered — Starter `max_concurrent: 1`, Team `2–3`. "Balancing" a hot tenant means **vertical resize** (reconciler action: stop → `ModifyInstanceAttribute` t3.large→t3.xlarge → start, ~2 min), triggered manually from the admin panel in v1, threshold-automated later (sustained queue_depth > 5 for 1h, or memory pressure from the existing Prometheus gauges).
### 6.4 Placement
Round-robin new tenants across tenant subnets/AZs at provision time; respect per-AZ vCPU headroom. That's the whole placement problem at N ≤ 100. On `InsufficientInstanceCapacity`, retry in the alternate AZ (data volume must be created in the target AZ — reconciler handles the snapshot-restore-cross-AZ move; rare, but code it, it's ~50 lines).

**Explicit non-goals in v1:** bin-packing multiple tenants per host (violates the VM-boundary invariant), Spot for tenant daemons (interruption mid-execution kills a Claude run and risks WAL truncation), request-level LBs in front of instances (there are no inbound requests), K8s-style rescheduling (state is the instance).

---

## 7. Cost model (eu-central-1, on-demand, rough)

### Per tenant

| Item | Active (running 24/7) | Typical (≈50% duty cycle) | Idle/parked (stopped) |
|---|---|---|---|
| t3.large ($0.096/h) | $70 | $35 | $0 |
| EBS gp3: 30GB root + 100GB data | $12 | $12 | $12 |
| DLM snapshots (~40GB changed, $0.054/GB-mo) | $3 | $3 | $2 |
| NAT data + share (~20GB/mo egress-processed) | $4 | $2 | $0 |
| **Infra subtotal** | **~$89/mo** | **~$52/mo** | **~$14/mo** |

With a 1-yr Compute Savings Plan on the baseline fleet, compute drops ~35% → typical tenant lands **~$40/mo infra**. LLM tokens are **BYO Anthropic key = $0 COGS to us** — the pricing model's biggest lever, and another reason the §4 BYO-key decision is right. Margin math: a $199–499/mo plan carries ~$50 infra comfortably; anything below ~$99/mo does not survive an always-on tenant, so the sleep scheduler (§6.2) is a P0 revenue feature, not an optimization.

### Fixed control plane (per environment)

| Item | $/mo |
|---|---|
| ALB | ~$25 |
| NAT GW (base) | ~$33 |
| RDS db.t4g.small multi-AZ + 50GB | ~$60 |
| console-api (2× Fargate 0.5vCPU/1GB or 1× t3.small pair) | ~$35 |
| auth-service + Redis (t4g.small + cache.t4g.micro) | ~$30 |
| CloudFront + S3 (SPA) + Route53 + ACM | ~$5 |
| Ops Prometheus/Grafana box (t3.small + 50GB) | ~$25 |
| **Total fixed** | **~$215/mo** |

Break-even on fixed costs: ~2 customers at $199/mo. The model scales linearly and boringly, which is the point.

---

## 8. Reuse verdict: aws-infrastructure-pilot

**Verdict: keep ~50% as building blocks; keep 0% as the orchestration layer, because none exists there. Do not extend the repo in place — extract, port to CDK (or Terraform), and leave the sandbox running as-is for bench.**

| Asset | Verdict |
|---|---|
| Packer Golden AMI (`packer/pilot-agent.pkr.hcl`: AL2023, Node 22, Claude Code pinned, git/gh, SSM agent hook) | **Reuse, extend** — add pilot binary, systemd unit, `fetch-secrets.sh`, bootstrap script. This is the single highest-value asset |
| Network baseline (parameterized `network.yml`, NACLs, VPC endpoints, flow logs, IMDSv2, no-inbound posture, `shared/kms.yml`) | **Reuse semantics** — re-express in CDK for the new fleet VPC; the endpoint list and hardening posture copy verbatim |
| IAM path-scoping (`iam-pilot-agent.yml`: ssm:GetParameter scoped to path, inlined SSM-agent perms, no managed-policy sprawl) | **Reuse as template** — becomes the per-tenant role, `/pilot/*` → `/tenants/{org_id}/*` |
| S3 bucket + lifecycle layout (artifacts 30d / logs 90d) | **Reuse** — one bucket, per-org prefixes, same lifecycle rules |
| Warm Pool ASG + set-desired-capacity dispatch | **Drop as tenant model** (ReuseOnScaleIn = cross-tenant residue; ASG replace = SQLite loss). Optionally re-home the idea as the unassigned provisioning pool (§2) |
| GitHub-Actions-as-scheduler + admin-runner deploy pipeline | **Drop** — replaced by the reconciler; fleet IaC deploys from CI with OIDC role assumption, retiring the AdministratorAccess mgmt runner from the SaaS path |
| SSM String secrets | **Fix** — SecureString everywhere, per-env KMS |
| Raw CloudFormation + Fn::ImportValue exports, hardcoded region/profile/org | **Drop** — export lock-in is already brittle at 11 stacks; per-tenant stacks would make it unmanageable. CDK app: `FleetVpcStack`, `ControlPlaneStack`, `TenantBaseStack`; per-tenant resources created by the provisioner via SDK (not per-tenant CFN stacks — 100 stacks of 5 resources each is CFN abuse; tag-based + reconciler is simpler) |
| 2h HealthCheckGracePeriod blindness | **Fix** — reconciler `/ready` gate + CloudWatch alarm on StatusCheckFailed → auto-recover, plus queue-stall alerting from Prometheus |

---

## 9. v1 / v2 split — cut into issues

### v1 (ship a paying customer)

**Epic A — Fleet substrate**
1. CDK app: fleet VPC, subnets, NAT, VPC endpoints, KMS, S3, RDS, ALB (ports from sandbox templates).
2. Golden AMI v2: pilot binary + systemd unit + `fetch-secrets.sh` + bootstrap (mount data vol, clone repos, start); Packer param for pilot version.
3. Per-tenant IAM role + SG factory (SDK-created, tagged, path-scoped SSM).
4. SecureString secret writer in console-api; `/tenants/{org_id}/*` convention; migration note for sandbox params.

**Epic B — Reconciler & lifecycle (the heart)**
5. `instances` table + desired/observed model + `instance_events` journal.
6. Provisioner: RunInstances + data volume + bootstrap + `/ready` gate (target <5 min).
7. Sleep/wake scheduler: idle detection via proxied queue/status; wake on board-sync signal; `PENDING_WAKE` label-hold.
8. Config push: render config.yaml (`${VAR}`-only, hosted hard-sets), `pilot-apply-config` SSM document, restart, generation tracking.
9. AMI rolling upgrade: stop→swap→reattach→verify, canary org first, batch size 3.
10. Suspend/terminate flows: Stripe webhook → quarantine; snapshot-and-delete with 30-day retention; SSM param cleanup.
11. DLM snapshot policy + restore runbook (test the restore, not just the snapshot).

**Epic C — Instance integration**
12. `PILOT_HOSTED=1` upstream patch: Save() no-op + hosted-mode assertions (the one pilot-repo change).
13. Console instance-proxy: per-instance bearer, `/api/v1/*` passthrough, SG rule.
14. Approval relay: mirror `approval_pending` → "Needs You" cards; decision endpoint → DecisionRecorder.
15. Ops Prometheus: scrape config generator from `instances` table, `org_id` labels, grom panels → Grafana; alerts: queue stall, /ready flapping, NAT egress anomaly.
16. Usage rollup: hourly proxy-poll of `/api/v1/metrics` → `usage_rollup` (billing groundwork, flat plan for now).

**Models management (explicit, per the founder's question): not a v1 surface.** `claude_code` backend only, BYO Anthropic key mandatory at onboarding, stock model routing, read-only per-execution model/token/cost display. The multi-backend engine is real and stays warm for v2 — but it multiplies the QA matrix, its non-Claude paths are un-dogfooded, and BYO-key already zeroes our token COGS.

### v2 (when v1 has >10 tenants or data demands it)

- Central webhook edge (reuse gateway verifiers) → instant pickup + poll-nudge; retire 30s latency.
- Outbound event stream from instances (execution_events → SQS/Kinesis) replacing proxy-polling; enables real usage-metered billing from `usage_events`.
- Auto vertical resize + per-plan concurrency automation (§6.3 thresholds).
- Provisioning accelerator pool (§2) if funnel data justifies it.
- Multi-region cell architecture (copy the whole env per region; reconciler is already env-scoped).
- Models management tier: expose openai-compatible/OpenRouter BYOK behind a flag, only after direct-API loop quality and cost-accounting (unknown-model-priced-as-Sonnet bug) are fixed.
- Postgres port of executions/board state only if the event stream proves insufficient — not before.
- OAuth apps for trackers (replace paste-a-PAT onboarding).

### Hard invariants (encode in review checklist)
1. One tenant = one VM. Never shared compute for the executor.
2. No inbound to tenant instances except :9090 from console SG.
3. Secrets: SecureString + path-scoped IAM + tmpfs env file; never literal in config.yaml.
4. Fleet images immutable; `auto_hot_upgrade: false`; upgrades via replacement.
5. Data volume is the tenant's state; root is disposable.