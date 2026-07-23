# Roadmap: Pilot SaaS Build (TASK-405 Program — "S-milestones")

**Source plan**: `.agent/tasks/TASK-405-pilot-saas-platform.md` + `saas-architecture.md` / `saas-kanban-sync-design.md` / `saas-fleet-design.md`
**As of**: 2026-07-23 — **S0 ✅ · S1 ✅ · H1–H12 ✅ · S3 UI mock ✅ · R-track ✅ · S6-lite (founder box on AWS) ✅ · S2 FULLY DISPATCHED (built: B5/A4/B6/reconciler ✅ · queued: B8 console#26, A3 console#27, C13 console#28, B11 cloud-infra#15 · exit after merges: ownership transfer + hosted-canary proof)** (v8 of this doc). Build proceeds on the AWS founder daemon. Final R-proof pending: epic-lifecycle canary green on `duplicate-pr` → close #4265. See **Progress** below; the milestone table is the plan of record.

**Live status (2026-07-14):**
- **S0 ✅** — auth Nil-tenant (auth-service#429), all sdk SyncCapable + fixes (sdk#83–86, #91–100), PILOT_HOSTED (pilot#4274) all merged. S0.6 (Golden AMI v2) is the one deferred item (folded into S2 infra — not needed until hosted provisioning).
- **S1 ✅** — all three scaffolds shipped E2E and CI-green: pilot-console (config+db+log), pilot-console-ui (Tailwind4+v3 tokens+StatusDot), pilot-cloud-infra (three-stack CDK skeleton). All onboarded to `~/.pilot/config.yaml`.
- **Hardening (H) ✅ COMPLETE 2026-07-15** — all H1–H12 merged; clean restart done; guards proven live (storm never regenerated).
- **S3 UI mock track ✅ COMPLETE 2026-07-15** — pilot-console-ui #1/#3/#5/#7/#8/#11 all shipped E2E: login → onboarding → connections (write-only creds) → provision → instance status, all on the mock adapter. Remaining S3 scope (BFF/auth/Stripe/email) waits on S2 exit per plan.
- **S2 FULLY DISPATCHED 2026-07-23** — shipped: B5 (console#11) ✅ · A4 (console#12) ✅ · review hardening (#16/#17/#18) ✅ · B6 provisioner (console#22→PR#23) ✅ · **reconciler (console#24→PR#25, TASK-415, 17-finding review) ✅ merged 07-22**. Dispatched 07-23 (all `pilot`-labeled, in queue): **B8 config push → console#26** (TASK-417; 4-agent 30-finding adversarial review — blockers fixed: YAML key paths `auth.*` top-level / `executor.type: claude-code`, async push execution model, provision pre-flight; **folds in C12's last sliver**: `Environment=PILOT_HOSTED=1` in the bootstrap unit — daemon side shipped in #4274, bootstrap never set it) · **A3 IAM/SG factory → console#27** (per-tenant path-scoped secrets isolation; opt-in flag; depends on #26) · **C13 instance proxy → console#28** (read-only allowlist passthrough, per-instance bearer, interim static console token) · **B11 DLM snapshots → pilot-cloud-infra#15** (policy on `pilot:data-volume=true` + restore runbook). **S2 exit remaining after merges**: (1) ownership transfer — remove `pilot-canary-sandbox` from the box config + park canary cron BEFORE any hosted instance serves it; (2) exit proof — provision→green `/ready` <5 min on fleet VPC + canary ticket label→PR→merge fully on a hosted instance (operator runs env-gated e2e; console role needs `ssm:SendCommand`/`GetParameter*` grants first — infra follow-up).
- **2026-07-16/17 (v7 line): Reliability track PROVEN + S6-LITE EXECUTED EARLY.** v2.241.0 = first-ever automated release (R1 proven); reliability cluster (#4373/#4374/#4379/#4382/#4385/#4386) shipped + hot-released as **v2.241.1** (incident exception, tag-only). **Founder daemon CUT OVER to AWS** (TASK-409: i-0e0c1ca34e7b561f9, t3.xlarge, tmux TUI over SSM, v2.241.1, path-shim = zero ledger surgery) — laptop retired, always-on achieved ahead of the S6 schedule (pilot repo tenancy still local-clone-based on the box; full platform tenancy remains S6). Post-cutover hardening open: #4391 (user-aggregate rate pool), #4392 (orphan claim reconciliation), #4388 in flight. Monitoring kit + `pilot-aws` skill shipped. Canary `duplicate-pr` proof → close #4265 still pending.
- **Reliability track (R) — in flight 2026-07-15/16** (surfaced by dogfooding at full cadence; parallels the H-track): R1 release-train death (terminal `failed` unrecoverable) ✅ #4331→#4337 · R2 briefs deflake ✅ #4332→#4345 · R3 required_checks precedence ✅ #4333→#4342 · R4 no_op invisible to dispatch guards ✅ #4347→#4350 · R5 mechanical go.mod conflict resolution ✅ #4328→#4334 · R6 test-evidence guard ✅ #4329→#4336 (default off) · **R7 atomic dispatch-admission claim ✅ SHIPPED** (TASK-407/#4349 → #4361/#4362/#4363; canary `duplicate-pr` proof pending → close #4265; follow-up #4372 poller-retry gen+1 in flight) · R8 canary comment hygiene ✅ #4348→#4351 · R9 evidence project-scoping ✅ #4352→#4356. **Releases: PROVEN 2026-07-16 — v2.241.0 cut automatically by the 14:00Z train (state=done, attempts=0), first ever**; v2.240.x were manual (tag push only). Ledger-truth follow-ups: #4369 HISTORY heal ✅ live · #4370 released-backfill in flight.

## Operating decisions (founder, 2026-07-13)

1. **Builder = local daemon.** Pilot executes the entire SaaS build from the local machine via `pilot`-labeled issues. The pilot repo migrates onto the platform only at S6 cutover ("I'll move there") — until then NO second daemon/instance ever serves a repo the local daemon owns (duplicate-dispatch: ProcessedStore is per-DB).
2. **Hosted-path dogfood target = `pilot-canary-sandbox`** — with an explicit **ownership transfer** before the hosted instance takes it (see S2). Never dual-served.
3. **The 6 TASK-405 decisions adopted at recommended defaults** (provisional): $500/mo design-partner · BYOK+PAT onboarding · eu-central-1 only · dogfood #0 · accept session-listing gap · models-as-recommended. Only S3+ Stripe work and S5 pricing hard-depend on #1.
4. **Engine/OpenRouter bench experiment: PARKED** (arms A–D designed, arm A staged) — independent of this roadmap; v1 ships on `claude_code` per architecture doc §5.
5. **Design partner: optional accelerant, not a gate.** May slot after S2 exit **for public-repo partners only** — the hostile-ticket pen test (S5) is a hard gate before any private-repo partner onboards (architecture §3). A private-repo partner signing early pulls a scoped pen test forward into S4.

## Interplay with the throughput program (M-milestones)

- M3 baseline window (→ ~07-20): SUPERSEDED by founder decision 2026-07-13 evening — S-waves dispatch at full cadence immediately; S-traffic is production data and enriches the baseline. Watch the M3 histograms as the build runs (queue-wait under multi-repo load is exactly D2's input). The window still closes ~07-20 for the D1/D2 median computation.
- M4–M7 (post-M3) share the local daemon with S-waves. File overlap is low (S-work is mostly other repos + greenfield); the one touchpoint is S0.7 (`config.go` Save no-op) vs M-phase config work — S0.7 is tiny, land it first.
- S6 cutover is the M-program's host-change moment: write a metrics-continuity note (ledger DB migrates with the daemon's replacement instance) before M8 close-out if S6 lands first.

## Milestone Table

| Milestone | Entry criteria | Scope (issues) | Exit criteria | Repos |
|---|---|---|---|---|
| **S0 — Pre-work** ✅ **DONE 2026-07-14** | Dispatch pre-flight complete (rule 0 below) | S0.1 auth-service: coalesce `uuid.Nil`→Default tenant + integration test (verified defect: `internal/domain/context.go:20-24`, migration 000015 FK) · S0.2 sdk: `SyncCapable`/`IssueSnapshot`/`SyncSource`/`SyncWriter` contract in `sdk/core` + `api.golden` extension · S0.3 sdk: generalize `github/retry.go` → `sdk/util/retry` (typed `RateLimitError`/`AuthError`, per-connector classification) · S0.4 sdk: Linear cursor-follow + `updatedAt` delta filter · S0.5 sdk: GitHub list-endpoint real pagination (incl. `ListIssues` single-page bug) · S0.6 infra: Golden AMI v2 (pilot binary, **add `gh`**, **pin `claude_code_version`**, systemd unit, `fetch-secrets.sh`, bootstrap; `/pilot/GITHUB_PAT` String→SecureString) · S0.7 pilot: `PILOT_HOSTED=1` (Save() no-op + hosted-mode assertions) · **S0.8 sdk: GitHub `SyncCapable` impl** (on S0.2+S0.5) · **S0.9 sdk: Linear `SyncCapable` impl** (on S0.2+S0.4) · **S0.10 sdk: Jira `SyncCapable` + new `CreateIssue`/`UpdateFields` + retry adoption** (on S0.2+S0.3) | All merged + SDK tagged; AMI bakes green and a throwaway instance passes `pilot doctor` + `gh --version` + pinned-CC assert; auth patch integration-tested | auth-service, studio-sdk, aws-infrastructure-pilot, pilot |
| **S1 — Scaffolds** ✅ **DONE 2026-07-14** (console + UI + CDK all shipping E2E, CI green) | S0.2 (sdk#83) merged | S1.1 `pilot-console` (NEW repo): Go service skeleton — config, RDS migration harness, health probes, CI · S1.2 `pilot-console-ui` (NEW repo): lift fleet-manager-frontend (design-system, auth store, `useForm`, **mock adapter**), prune fleet pages, design-dna v3 tokens + Drift 5-state hues · S1.3 infra: CDK app skeleton (`FleetVpcStack`/`ControlPlaneStack`/`TenantBaseStack`, porting sandbox network/IAM/KMS semantics; **IMDSv2 `HttpTokens: required` on every instance**) | Both NEW repos onboarded per onboarding SOP, in `~/.pilot/config.yaml`, one canary issue each shipped E2E by Pilot; CI green | NEW ×2, aws-infrastructure-pilot |
| **S2 — Hosted instance path** | **S1.1 exit (pilot-console canary shipped) + S0.6 + S0.7.** S1.3 runs in parallel — provisioner develops/tests against EXISTING sandbox stacks (test strategy below); fleet-VPC ids required only for S2 exit cutover | **Pre-step: canary-sandbox ownership transfer** — remove `pilot-canary-sandbox` from local `~/.pilot/config.yaml` + park the local canary cron BEFORE any hosted instance config includes it · Epic A3–A4 (per-tenant IAM/SG factory, SecureString writer) · Epic B5–B6 (instances table + reconciler + `instance_events`; provisioner `RunInstances` — **IMDSv2 required in MetadataOptions as an AC** — + data-volume + bootstrap + `/ready` gate) · B8 (config render/push via SSM `pilot-apply-config`, spec versioning) · B11 (DLM snapshots) · Epic C12–C13 (hosted patch integration, instance proxy w/ per-instance bearer) · **B7 sleep/wake NOT here — hard-depends on S4's board-sync worker (the wake signal); scheduled S5** | Provision→green `/ready` <5 min on fleet VPC; a `pilot-canary-sandbox` ticket ships label→PR→merge fully on a hosted instance (existing canary scenarios = acceptance suite); deprovision terminates cleanly | pilot-console, aws-infrastructure-pilot, pilot |
| **S3 — Control plane + auth + dashboard shell** | S0.1 merged; S1.1/S1.2 exit; **S2 exit** (provision path is in this milestone's exit). UI track starts EARLIER on the mock adapter — only the milestone exit waits on S2 | BFF httpOnly-cookie sessions over auth-service (gRPC ValidateToken) · orgs/members/connections CRUD · connection-test buttons per tracker · Stripe Checkout (flag-gated) · **transactional email — direct sender, NOT the email-service deployment** (2026-07-13 asset map verdict, `saas-asset-research.md` § email-service): vendor `email-service/internal/transports/{ses,resend}.go` into pilot-console behind a `POST /send-email` endpoint matching auth-service's existing `HTTPSender` contract (`internal/email/http.go` — built, tested, unwired); wire auth-service's `EmailSender` in `main.go` + reset-link base-URL config + close the TODO at `auth/service.go:323`; add signup-verification token flow. Covers verification, password reset, instance-down notice with zero Redis/ops surface. Deploying email-service itself REJECTED: auth off in practice, at-most-once queue w/o DLQ, plain-text only · UI: login, org creation, credential forms (write-only, last-4), provision button, instance status page (spec-version drift visible) | Staging signup→payment(test)→credentials→provision→first PR, zero operator SSH; 3+ tenants concurrent (canary + synthetic) | pilot-console, pilot-console-ui, auth-service |
| **S4 — Board + observability** | **S3 exit**; S0.2–S0.10 tagged | Sync doc §9 v1 slice, full enumeration: **C1 migrations — `cards`, `card_links`, `link_shadows`, `card_ops`, `sync_cursors`, `status_maps`, `sync_conflicts`** (the "conflict journal") · C2 reconciler (3-way diff — exhaustively table-tested FIRST) · C3 leased ingest workers (delta poll + overlap + 6h sweep + orphan badge) · **C4 outbound op worker** (FIFO-per-card, compare-before-write, park/retry) · C5 status-map auto-seed + wizard UI · **C6 per-provider rate budgeter + write preemption** ("before tenant #20") · C7 board API · C8 dispatch verb (additive `pilot` label — NEVER strip `pilot-*` status labels) · **C14 full: `approval_pending` → `approval_mirror` mirroring feeding "Needs You"** + decision endpoint on `DecisionRecorder` seam · close verb via state transition · per-card timeline from 22-stage `execution_events` · live log via `/ws/dashboard` proxy · redaction scrubber · C15 ops Prometheus w/ tenant labels + alarms · **second-tracker test rig: synthetic qf-studio Linear + Jira sandbox workspaces connected to the canary tenant** | The canary tenant + synthetic Linear/Jira sandboxes operated dashboard-only for a full week across ≥2 tracker types on one board; approvals survive instance restart | pilot-console, pilot-console-ui, pilot (1 endpoint), studio-sdk |
| **S5 — Hardening + GA gates** | S4 exit | Hostile-ticket isolation pen test (hard gate) · **B7 sleep/wake scheduler** (wake = board-sync signal, `PENDING_WAKE` label-hold) · **C16 usage rollup** (hourly proxy-poll → `usage_rollup`) + usage page · billing lifecycle (suspend on past_due) · EBS restore runbook (test the restore) · AMI rolling upgrade (canary-first, batch 3) · egress domain-allowlist proxy · Postgres RLS defense-in-depth · pricing from measured COGS | Architecture Phase-3 exit: self-served payer flow E2E on staging; pen test passed; AMI upgrade rolls with **no downtime beyond the restart window** (measured: per-instance `/ready` gap ≤ restart window, batch 3, canary org first) | all |
| **S6 — Cutover: "I'll move there"** | S5 exit; founder go | Pilot repo (and sibling repos) onboarded as a real tenant · atomic cutover: stop local daemon → migrate `~/.pilot/data/pilot.db` to tenant data volume → hosted instance owns the repos · M-program metrics-continuity note · local daemon retired | Pilot builds Pilot on Pilot Cloud; local machine out of the loop | pilot + platform |

---

## Hardening track (H) — daemon/autopilot robustness (parallel to S-milestones)

Not in the original plan — surfaced by dogfooding the build (the daemon building its own SaaS is the best stress test we have). These are **orchestration-layer** defects (CI monitoring, fix-spawning, daemon lifecycle, adapter resilience), NOT code-generation defects. The core pipeline (issue → decompose → PR → CI → merge) is solid; this track hardens the control plane. **Gate: substantially landed + one clean restart (per `.agent/sops/operations/safe-daemon-restart.md`) before S2 leans harder on fleet ops.** Root causes: `.agent/system/incident-duplicate-cifix-2026-07-14.md`.

| # | Defect | Issue | State |
|---|---|---|---|
| H1 | Cross-project `task_id` collision short-circuits epic decomposition (blocked S1's low issue numbers) | #4276→PR#4297 | ✅ merged + live |
| H2 | CI-fix size guard counts tests/docs as cascade contamination (auto-closed a correct PR) | #4284→#4291 | ✅ merged |
| H3 | Pilot writes unindexed `.agent/` memory docs → self-inflicted drift-gate CI red | #4286→#4289/#4290 | ✅ merged |
| H4 | HISTORY stage-label frozen at running/ci_passed (heal-event backfill + ladder) | #4277/#4298 | ✅ merged |
| H5 | **Duplicate CI-fix issue spawning** — no idempotency on fix-issue creation (PRIMARY) | #4309 | ⏳ queued |
| H6 | Scheduled-canary failure misattributed to a PR's post-merge CI (dead `required_checks`) | #4310 (+ config stopgap live) | ⏳ queued |
| H7 | **No single-instance lock** — two daemons can run concurrently (double-daemon → dup work) | #4311 (+ SOP live) | ⏳ queued |
| H8 | Post-merge-CI-failed PR not marked `StageFailed` → release-scan respawn loop | #4312 | ⏳ queued |
| H9 | Transient failure mid-decomposition silently drops planned subtasks; parent closes partial | #4300 | ⏳ queued |
| H10 | **Adapter goroutine panic crashes the WHOLE daemon** (discord nil-conn took it down) | #4314 + studio-sdk#101 (✅) | ⏳ pilot#4314 queued |
| H11 | Discord gateway nil-conn deref on network failure (P0 crash) | studio-sdk#101 | ✅ merged (SDK); discord disabled in config as belt-and-suspenders |
| H12 | Canary epic-lifecycle scenario mis-designed (single-package fold) — restore its value | #4315 (+ sandbox greeter/counter packages added) | ⏳ queued |

**Config stopgaps applied (effective on the restart that activates them):** `ci_checks.exclude` += canary jobs (H6); `discord.enabled: false` (H10/H11). Both in `~/.pilot/config.yaml`.
**Ops action outstanding (founder):** rotate the Discord bot token (was plaintext in config).

## Progress log

- **2026-07-13 eve → 07-14:** S0 dispatched + fully landed (auth + all sdk SyncCapable + PILOT_HOSTED). S1 dispatched + all three scaffolds shipped. Hardening track opened as the daemon dogfooded the build and surfaced the orchestration defect cluster (H1–H12), root-caused via a 3-investigator workflow and dispatched.
- **2026-07-15/16:** H-track completed + clean restart. S3 UI mock track shipped whole (6 issues, all E2E same-day). S2 opened: B5+A4+review-hardening shipped in pilot-console. Reliability track opened after release-train RCA (4-investigator workflow) + duplicate-execution class RCA (10 incidents inventoried; systemic fix = TASK-407 atomic claim, #4349). v2.240.0/v2.240.1 cut manually. Verification gates: daemon-cut release at 14:00Z (proves R1) · epic-lifecycle canary green on `duplicate-pr` (proves R7, closes #4265).

## Dependency graph

```
S0.1 (auth Nil-tenant) ────────────────────────► S3 (BFF/auth)
S0.2 ─► S0.3/S0.4/S0.5 ─► S0.8/S0.9/S0.10 ─────► S4 (sync workers)
S0.6 (AMI v2) ─────► S2 (provisioner)
S0.7 (hosted flag) ─► S2 (C12)
S1.1 (console scaffold+canary) ─► S2 ─► S3 ─► S4 ─► S5 ─► S6
S1.2 (UI scaffold) ─► S3 (UI track starts early on mocks)
S1.3 (CDK, parallel) ─► S2 EXIT (fleet-VPC cutover) — not S2 entry
S4 (board-sync worker) ─► S5 B7 (sleep/wake — wake signal source)
```

Parallelism: S0 spans 4 repos — fully parallel at natural cadence. S2 (infra/Go) and the S3 UI track (mock-adapter-first) overlap deliberately — the frontend never waits for the control plane (Deck sequencing).

## Test strategy: existing AWS infra is the test bed (founder, 2026-07-13)

Full CLI access confirmed (IAM user `aleks`, acct `529088297614`, eu-central-1). Live state verified 2026-07-13: ASG `pilot-agent-pool` idle 0/5 · Golden AMI `ami-0bb00da3a38b9c176` · all workload+mgmt stacks green · secrets ALREADY SecureString except `/pilot/GITHUB_PAT` (String — folded into S0.6; the April verification finding is otherwise stale).

- **S0.6 AMI v2 testing**: bake → launch a throwaway instance → assert `pilot doctor` + `gh --version` + pinned CC version → terminate. All within existing stacks.
- **S2 provisioner e2e**: develop and test `RunInstances`+data-volume+bootstrap against the existing `pilot-network`/`pilot-security-groups`/`pilot-kms` stacks BEFORE the fleet VPC exists; the provisioner takes subnet/SG ids as config, so S1.3 only gates the S2 exit cutover.
- **Hosted-path dogfood**: hosted instances serve `pilot-canary-sandbox` only, AFTER the S2 ownership-transfer pre-step; the existing canary scenarios (`pilot-canary-scenario.yml`, TASK-403) become the hosted path's acceptance suite.
- **Cost discipline**: test instances terminate same-session; the ASG stays at desired=0 between runs.

## Dispatch rules for this program

0. **Pre-flight (before the first S0 label)**: for EACH of auth-service, studio-sdk, aws-infrastructure-pilot — confirm the repo is in `~/.pilot/config.yaml` projects with working token + per-repo quality gates (`.pilot/workflow.yaml`; note aws-infrastructure-pilot is Packer/CFN — global `make build/test/lint` gates do not apply, set repo-appropriate gates). Any repo newly added gets a trivial canary issue first (onboarding SOP rule 6).
1. Issue bodies come from the system docs' numbered items (fleet §9, sync §9) — copy the Files/contract detail verbatim, mirroring how #4127 was decomposed.
2. NEW repos (`pilot-console`, `pilot-console-ui`): follow `sops/onboarding/new-project-issue-authoring.md` — scaffold-first, H2 section headers, `#N` dependency refs, per-project config checklist. Nothing else dispatches to a new repo until its canary issue ships.
3. sdk issues land behind the `api.golden` lock (core) + version tags; pilot-console pins the SDK version per release.
4. Dispatch wave-sized batches per milestone (founder 2026-07-13: no M3 throttle). The daemon serializes per repo; cross-repo waves parallelize naturally. Keep an eye on `pilot_queue_wait_seconds` as wave size grows — sustained queue growth is the signal to pace.
5. Board/tracker write-backs are additive for the `pilot` trigger label and NEVER remove `pilot-in-progress`/`pilot-done`/`pilot-failed` — stripping re-arms ProcessedStore by design (verified).

## Issue count estimate

S0: 10 · S1: 3 · S2: ~9 (incl. ownership-transfer pre-step) · S3: ~6 · S4: ~12 · S5: ~8 · S6: ~3 → **~50 issues across 7 milestones (S0–S6)**. Phase-1 throughput precedent (#4127: 4 sub-issues same-day) suggests S0 ≈ 1 week wall-clock at M3-compatible cadence, S1 days.

---

**Last Updated**: 2026-07-23 (v8 — S2 fully dispatched: reconciler merged (console PR#25), B8/A3/C13/B11 all queued (console#26/#27/#28, cloud-infra#15) after a 4-agent 30-finding review of B8; C12 folded into B8. Prior v7: S6-lite founder-box cutover complete and battle-proven, 2 latent-on-darwin P0s fixed, first automated SaaS PRs from AWS)
