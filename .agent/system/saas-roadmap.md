# Roadmap: Pilot SaaS Build (TASK-405 Program — "S-milestones")

**Source plan**: `.agent/tasks/TASK-405-pilot-saas-platform.md` + `saas-architecture.md` / `saas-kanban-sync-design.md` / `saas-fleet-design.md`
**As of**: 2026-07-23 night — **S0 ✅ · S1 ✅ · H1–H12 ✅ · S3 UI mock ✅ · R-track ✅ · S6-lite ✅ · S2 BUILD TRACK COMPLETE**: B8 ✅ ([console PR#30](https://github.com/qf-studio/pilot-console/pull/30) merged 18:54Z after operator Slack approval — size-gate escalate → async-approval → auto-merge chain proven end-to-end) · A3 ✅ (console#27→PR#31, +1855/−67, merged 20:20Z, 31-min run) · C13 ✅ (PR#29) · B11 ✅ (infra PR#19) (v8.3 of this doc). Evening bonus wave — three operator-investigated defects filed ~19:30Z, all issue→merged <90 min: stdout-reader silent `ErrTooLong` exit → heartbeat SIGKILL (the B8 gen-2 killer; pilot#4519→PR#4521), vestigial AutoReview self-approve 422 per merge (pilot#4520→PR#4522), console workflow.yaml never parsing (console#32→PR#33 — repo overrides were silently dead since creation). Box: `v2.245.1-6-gc3258c33`; next train carries #4514–#4518 + #4521/#4522. **Next: S2 EXIT SEQUENCE** — v8.6 (07-24 eve): **steps 1+2+3 ✅ · step 4 ~95%**. Step 3 done: canary cron parked (`pilot-canary.yml` disabled→re-enabled post-transfer), `pilot-canary-sandbox` out of box config (backup `.bak-s2-ownership-transfer`), clean restart. Step 4: **fleet VPC LIVE** (all 4 CDK stacks deployed via mgmt runner; 2 first-deploy bugs fixed same-day: infra#20 DLM ExecutionRoleArn→PR#22, infra#21 retired postgres 17.4 pin→PR#23 — "CI-green CDK rots without a deploy gate" class) · **consolectl shipped** (console#47→PR#48: org/seed-secret/claim/add-repo/run/status/terminate) · **hosted canary tenant LIVE on fleet VPC**: org 13508a69 inst dadb1744 ec2 i-0decbc0dcf225cf18, **provision→running 99s** (<5min criterion ✅), gen-3 config w/ canary repo applied. Hosted-path defect cascade, all filed+fixed or dispatched: console#51 repo-clone gap (✅ PR#52, token-hygiene clone in apply script) · pilot#4526 daemon scaffolds untracked `.agent/`→its own git_clean preflight deadlocks (hot-fix `.git/info/exclude`) · console#53 unit runs as root→claude refuses + ledger at `/.pilot` off data volume (hot-fix User=pilot drop-in + state moved; permanent fix dispatched) · `/pilot/ANTHROPIC_API_KEY` has ZERO CREDITS + `/pilot/CLAUDE_CODE_OAUTH_TOKEN` stale→401 loops (fix: box's live `~/.claude/.credentials.json` → instance via S3 SSE-KMS; founder decision pending: fund API key vs OAuth-dogfood; console validKeys lacks CLAUDE_CODE_OAUTH_TOKEN) · #4372 once-failed-block hit 3× on v2.245.0 (ledger row-delete workaround). **Executions HEALTHY since ~16:25Z**: GH-101 epic child ran with real tool-use → **PR #103**; GH-102 → **PR #105**. **🔴 v8.7 (07-24 night) — THE MERGE LEG IS NOT FIRING**: #103 (created 16:22Z) and #105 (16:28Z) both sit `test` ✅ SUCCESS and **OPEN/unmerged ~80 min**; open issues 99/100/101/102/104; **no PR has ever merged on `pilot-canary-sandbox`** (#87/#90/#94/#98 all `mergedAt: null`). Execution on the hosted tenant is proven; **merge is not**, so S2 exit evidence (all `pilot` issues closed + ≥2 fresh merged PRs) is NOT met. Leading hypothesis (UNVERIFIED, needs SSM): instance config defaults to `stage.require_approval: true` while the box has run approvals-off since 07-20 21:41Z → every green PR parks at `awaiting_approval` with no approval channel wired. Alternative: autopilot never adopted the PRs. One query separates them on-instance (no gh quota): `select pr_number, stage, ci_status, merge_attempts from autopilot_pr_state;` against `/var/lib/pilot/home/.pilot/data/pilot.db` — no rows = never adopted · `awaiting_approval` = config/approval channel · `waiting_ci` w/ green checks = GH-4384 class. **Remaining for S2 EXIT**: diagnose+fix the merge leg → canary issues actually merge → one clean `pilot-canary.yml` run green (runs 1+2 moot: no-op guard skip / poll-timeout during debug). Then spec S3 backend. Also open: console#45 ready-gate design (queued, pilot-labeled) · infra#2 AMI v2 (PARKED — aws-infrastructure-pilot not onboarded to daemon) · #4265 epic-lifecycle duplicate-pr proof.

**Live status (2026-07-14):**
- **S0 ✅** — auth Nil-tenant (auth-service#429), all sdk SyncCapable + fixes (sdk#83–86, #91–100), PILOT_HOSTED (pilot#4274) all merged. S0.6 (Golden AMI v2) is the one deferred item (folded into S2 infra — not needed until hosted provisioning).
- **S1 ✅** — all three scaffolds shipped E2E and CI-green: pilot-console (config+db+log), pilot-console-ui (Tailwind4+v3 tokens+StatusDot), pilot-cloud-infra (three-stack CDK skeleton). All onboarded to `~/.pilot/config.yaml`.
- **Hardening (H) ✅ COMPLETE 2026-07-15** — all H1–H12 merged; clean restart done; guards proven live (storm never regenerated).
- **S3 UI mock track ✅ COMPLETE 2026-07-15** — pilot-console-ui #1/#3/#5/#7/#8/#11 all shipped E2E: login → onboarding → connections (write-only creds) → provision → instance status, all on the mock adapter. Remaining S3 scope (BFF/auth/Stripe/email) waits on S2 exit per plan.
- **S2 BUILD ✅ COMPLETE 2026-07-23** — all epics merged: B5 (console#11) ✅ · A4 (console#12) ✅ · review hardening (#16/#17/#18) ✅ · B6 provisioner (console#22→PR#23) ✅ · reconciler (console#24→PR#25, TASK-415) ✅ · **B8 config push (console#26→PR#30, TASK-417 — 5-generation delivery, folds C12's `PILOT_HOSTED=1` bootstrap sliver) ✅ merged 07-23 18:54Z** · **A3 IAM/SG factory (console#27→PR#31, per-tenant path-scoped secrets isolation, opt-in flag) ✅ merged 07-23 20:20Z** · C13 instance proxy (console#28→PR#29) ✅ · B11 DLM snapshots (infra#15→PR#19) ✅. Plus console workflow.yaml repaired (console#32→PR#33 — was silently unparsed since repo creation). **S2 exit remaining (the ONLY open S2 work)**: (1) laptop env-gated e2e (`PILOT_CONSOLE_E2E=1`: provision → config push → drift sweep on a throwaway t3, auto-teardown); (2) console-role `ssm:SendCommand`/`GetParameter*` grants via mgmt runner; (3) ownership transfer — remove `pilot-canary-sandbox` from the box config + park canary cron BEFORE any hosted instance serves it (operator consent + restart); (4) exit proof — provision→green `/ready` <5 min on fleet VPC + canary ticket label→PR→merge fully on a hosted instance.
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

**Last Updated**: 2026-07-30 morning (v9.5 — **overnight lead-watch: 6 issues shipped autonomously, dashboard renders on real stack, S3 product punch list DONE**. Prior v9.4: local stack live + first drift defect; v9.3: S3 wave 10/10 merged; v9.2: park cascade + drain incident; v9.1: wave dispatched; v9.0: S2 exit met)

## v8.8 (2026-07-26) — merge leg root-caused; SaaS unparked

**Program UNPARKED** (founder, 07-26) — supersedes the 07-17 "SaaS parked" directive.

**Tenant Anthropic key provisioned.** `/tenants/13508a69-d0e3-4c77-8314-e817d04d2e0d/ANTHROPIC_API_KEY`
written as SecureString v1 under the tenant CMK `0699dcd8-…`, sourced from the
pointer org's funded key (`POINTER_LLM_API_KEY`, probe → HTTP 200). Before this
the tenant path held **only** `GITHUB_TOKEN` + `PILOT_GATEWAY_TOKEN` — no Anthropic
key at all, which is why the operator hot-copied the box's OAuth credentials on
07-24 and stranded the box (the 07-25 incident). Live-verified for contrast:
`/pilot/ANTHROPIC_API_KEY` → HTTP 400 `credit balance is too low`.
⚠️ Pointer org balance is **$107.88 with auto-reload OFF** — it will hard-stop at
zero exactly like `/pilot` did, and it is shared with pointer production traffic.

**THE MERGE LEG: autopilot was never enabled on the tenant daemon.** Verified three ways:

1. **Observed** — `sqlite3 …/pilot.db ".tables"` on `i-0decbc0dcf225cf18`: no
   `autopilot_pr_state` table exists (only `autopilot_metrics`). The roadmap's v8.7
   diagnostic assumed "no rows"; the table itself is absent, i.e. the subsystem never ran.
2. **Config** — `pilot-console/internal/fleet/configrender.go:109` `writeAutopilotBlock`
   emits `autopilot:` at **YAML top level**, but `internal/config/config.go:41` `Config`
   has **no top-level `Autopilot` field** — the only binding is
   `orchestrator.autopilot` (`config.go:153`). There is no normalization lifting the key.
   The entire rendered block is silently discarded by the decoder. It also never
   emits `enabled: true`.
3. **Code** — `cmd/pilot/main.go:422-426` sets `Autopilot.Enabled = true` **only** when
   `--env` is passed. The tenant systemd unit is
   `ExecStart=/opt/pilot/bin/pilot start --config /var/lib/pilot/config.yaml` — no `--env`.
   Every controller site (`main.go:1687/1768/1828/2349`) is guarded by
   `Autopilot != nil && Autopilot.Enabled` → none construct.

⇒ Issues execute → PRs open → **nothing ever adopts them**. #103/#105 sit green and
unmerged not because of approvals but because no CI monitor or merger exists in the process.

**v8.7's leading hypothesis is REFUTED**: the instance config reads
`require_approval: false` (line 41) — and it would not have mattered, since the block
is inert. Also corrected: v8.7 claimed "no PR has ever merged on `pilot-canary-sandbox`";
in fact #71/#72/#77/#78/#81/#84 all merged cleanly through 07-15 under the local daemon.
The break begins at #87 (07-16), three days *before* the hosted cutover — so the local
daemon's canary handling regressed separately and still needs its own look.

**Collateral finding**: `configs/pilot.example.yaml:553` documents the same dead
top-level `autopilot:` shape. Any operator who copies the example gets a silently
ignored autopilot block. Worth a strict-decode (`KnownFields`) issue.

**Third defect, same family**: `autopilot.default_environment` (`internal/autopilot/types.go:88`)
is declared and **read nowhere** — `grep -rn DefaultEnvironment --include=*.go` returns the
declaration plus two comments, no readers. `ResolvedEnv()` (`types.go:332`) checks
`activeEnvName` (set only via `--env`) then falls through to legacy `Environment` → `"stage"`.
So the `hosted` environment can never activate from config alone. Harmless here only by
coincidence: built-in `stage` (`branch: main`, `require_approval: false`, `ci_timeout: 30m`)
matches what `hosted` intended.

### ✅ MERGE LEG FIRING — S2 exit evidence met (2026-07-26 15:56Z)

Operator hot-fix on `i-0decbc0dcf225cf18`: nested the autopilot block under `orchestrator:`
+ `enabled: true` (backup `/var/lib/pilot/config.yaml.bak-premerge-fix`), `systemctl restart pilot`
— whose `ExecStartPre=+/opt/pilot/fetch-secrets.sh` also pulled the new tenant
`ANTHROPIC_API_KEY` into `/run/pilot/env`. Result inside 3 minutes:

```
15:53:08  autopilot enabled ... environment=stage auto_merge=true
15:53:09  restoring Pilot PR for tracking pr=105 / pr=103 → restored=2
15:53:55  waiting_ci → ci_passed   (checks=[test])
15:54:58  ci_passed → merging
15:56:00  PR #105 MERGED (squash) · 15:56:10 PR #103 MERGED
          issues 101/102 closed, branches deleted
```

`autopilot_pr_state` now exists with both rows at `stage=merged, merge_attempts=1`.
**Two fresh merged PRs fully on the hosted path** — first ever. Remaining for the
"all `pilot` issues closed" half of the exit bar: #104 (fresh), #99/#100 (stale
`pilot-retry-1`; #100 also `pilot-blocked`, epic-lifecycle scenario).

⚠️ The hot-fix is **ephemeral** — `configpush.go:27` rewrites `/var/lib/pilot/config.yaml`
on the next config push and will clobber it. Permanent fixes dispatched:
- [pilot-console#55](https://github.com/qf-studio/pilot-console/issues/55) — `writeAutopilotBlock`
  nest + `enabled: true`, with a round-trip-through-pilot's-structs test (a golden-string
  test would not have caught this).
- [pilot#4544](https://github.com/qf-studio/pilot/issues/4544) — honor (or delete) `default_environment`.
- **Unfiled, needs a call**: strict YAML decode (`KnownFields(true)`) in pilot's config loader —
  the class-level fix, but it would reject existing configs carrying legacy/misplaced keys,
  including the dead top-level `autopilot:` block at `configs/pilot.example.yaml:553`.

Cosmetic noise seen during the merge: auto-review 422 "Can not approve your own pull request"
(instance runs 2.245.0, predates pilot#4520→PR#4522) and a 404 removing a non-existent
`pilot-in-progress` label.

## v8.9 (2026-07-27 eve) — permanent merge-leg fixes landed; S2 exit = ops sequence + canary trio

- **pilot-console#55 → PR#56 MERGED** (renderer nests `orchestrator.autopilot` + `enabled: true`,
  round-trip test) and **pilot#4544 CLOSED** (`default_environment` honored). The config-push
  clobber risk is defused **in code**; it remains live on the tenant until the deployed
  console/consolectl build carries PR#56.
- **Tenant live-verified 2026-07-27 ~20:55Z** (SSM `i-0decbc0dcf225cf18`): hot-fix intact
  (`orchestrator.autopilot.enabled: true`), unit active, version **2.245.0** (predates the
  self-approve-422 fix #4520 and v2.246–247).
- **S2 exit remaining**, in order: (1) deploy console build with PR#56 → permanent config push →
  tenant binary upgrade to v2.247.x (one restart covers push+upgrade); (2) canary wedge cleanup —
  clear stale `pilot-retry-1` on #99/#100 + reset dirty worktree (` M version.go`), let #104 run;
  (3) all three canary issues close → S2 exit met → S3 opens.
- Open founder calls unchanged: fund/replace the tenant Anthropic key (pointer org key,
  **$107.88, auto-reload OFF**, shared with pointer prod) · strict YAML decode (`KnownFields`) issue.
- Context: same-day daemon hardening relevant to fleet ops — drift-guard trio #4582/#4583/#4584
  shipped (strip-leg origin base, intent-judge stdin, terminal-error repick classification +
  poller ledger gate incl. the GitLab leg); rides the next release train.

## v9.0 (2026-07-28) — **S2 EXIT MET**; GitHub billing wall found+fixed; S3 opens

**Ops sequence executed in order** (marker `2026-07-28_drift-guard-trio-shipped-s2-last-mile.md`):
(1) consolectl rebuilt from main w/ PR#56 → config **gen 4** created (byte-identical to the
hot-fix, sha `f9c07194` match) → reconciler pushed, apply script idempotently verified — the
clobber risk is dead in the DB, not just on disk. (2) Tenant upgraded **2.245.0 → v2.247.0**
(binary via instance's own GITHUB_TOKEN from the GH release, checksum-verified, inode-verified
after one restart); `environment=hosted` now honored (#4544 live), GH-104 repick storm (244
drops) stopped on the spot.

**Blocker discovered mid-sequence: GitHub org Actions billing.** The org's free 2,000
private-repo minutes exhausted on day 28 + default **$0 Actions budget with Stop-usage** →
GitHub refused to *start* jobs on ALL private repos (canary + pointer; public pilot unaffected).
Autopilot misread it as a code failure (annotation "job was not started…payments", all jobs
`steps_count: 0`), closed correct PR canary#106, spawned a fix-issue chain — loop stopped by
de-labeling. **Fixed same-day**: business account (QuantFlow DOO) + card + **Actions budget
$25/mo, Stop-usage ON, alerts 75/90/100%** (recipient nelyaparfenova-dev). Verified by re-running
pointer's failed run — jobs executed. Forecast: ~$2/mo now, $15–30/mo in build months; tenant
CI bills to tenants' own orgs, so our curve does not scale with tenant count. → filed **#4591**
(classifier must treat never-started jobs as infra, not code).

**Exit evidence:**
- **≥2 fresh merged PRs on the hosted instance** ✅ — #103/#105 (07-26) + **#111 auto-merged
  14:14Z** + **#113 merged via operator approval** of a merge-gate escalation (the gate asked
  for a human; false-positive title-type divergence — see #4595).
- **All `pilot` issues closed** ✅ — canary repo at **0 open**. Epic #100 closed **organically**
  by `reconcileEpicParents` (children #110/#112 shipped; #104 closed as superseded-by-#103 with
  an audited `no_op` ledger row per GH-3780 semantics). Version-bump leg (#99/#107/#109) closed
  operator-side: 4 attempts all killed by the billing outage + the direct-mode clone-corruption
  defect → filed **#4594** (branches cut from stale HEAD, quality-retry double-apply, git_clean
  self-wedge; `useWorktree=false` on the tenant path is the root).
- Golden AMI gap fixed on-instance: **make + go were absent** → default quality gates 127-failed
  every task; installed (go 1.25.8). Fold into AMI v2 (S0.6).

**New defects filed:** #4591 (billing-shape CI classification) · #4594 (direct-mode clone
corruption) · #4595 (escalation on approval-less env hard-fails green PRs + title-type gate
resolves epic parent instead of the child's own issue).

**→ S3 (control plane + auth + dashboard shell) is open.** Next: spec S3 backend (BFF cookie
sessions, orgs/connections CRUD, Stripe flag-gated, direct email sender — S3 row); tenant
binary bump to the next tag once the box proves it (train fired 07-28, box self-installing).

## v9.1 (2026-07-29) — S3 backend wave DISPATCHED (10 issues, 4 repos)

Specced from a 3-agent survey (pilot-console routes/db/secrets · auth-service gRPC/email/tenancy
· pilot-console-ui mock contract) and dispatched in dependency order:

- **pilot-console**: [#57](https://github.com/qf-studio/pilot-console/issues/57) S3.1 BFF cookie
  sessions (auth-service HTTP login leg + vendored `pkg/authclient` ValidateToken; migration 0005
  `sessions`) → [#58](https://github.com/qf-studio/pilot-console/issues/58) S3.2 orgs/members/
  connections CRUD (migration 0006 incl. org billing columns; **test-before-store** credential
  validation — SSM writer stays write-only) → [#59](https://github.com/qf-studio/pilot-console/issues/59)
  S3.3 instances API (drift = `applied_config_generation != config_generation`; gate ladder
  412 missing-connections → 402 billing → 409 claimed; C13 proxy gains session dual-auth) →
  [#60](https://github.com/qf-studio/pilot-console/issues/60) S3.4 Stripe Checkout flag-gated
  (`PILOT_CONSOLE_BILLING_CHECKOUT_ENABLED` default off, routes unregistered when off) →
  [#61](https://github.com/qf-studio/pilot-console/issues/61) S3.5 email sender (vendored
  SES/Resend transports; `POST /send-email` matches auth-service `HTTPSender` contract byte-for-byte,
  synchronous 202/502).
- **auth-service**: [#477](https://github.com/qf-studio/auth-service/issues/477) wire EmailSender +
  reset-link delivery (closes TODO `service.go:323`; tightens token-for-any-email) →
  [#478](https://github.com/qf-studio/auth-service/issues/478) signup verification (Register token
  issuance + `POST /auth/verify-email`; login NOT verification-gated in v1).
- **pilot-console-ui**: [#13](https://github.com/qf-studio/pilot-console-ui/issues/13) real HTTP
  adapter (mock/real seam `VITE_API_MODE`, `getMe` bootstrap, single 401 interception, logout) →
  [#14](https://github.com/qf-studio/pilot-console-ui/issues/14) onboarding gaps (signup/org-create,
  per-tracker field schemas, Test buttons, Anthropic/Slack fields, 402→checkout redirect).
- **pilot-cloud-infra**: [#24](https://github.com/qf-studio/pilot-cloud-infra/issues/24) staging
  control plane (console-api + auth-service(+Redis) systemd/container on control-plane EC2, auth DB
  as logical DB on existing RDS, ALB+ACM, SES identity parameterized, SPA S3+CloudFront; operator
  deploy-validation checklist mandatory — the infra#20/#21 "CDK rots without a deploy gate" lesson).

Survey traps baked into issue bodies: auth-service gRPC `tenant_id` is dead on the wire (all calls
resolve to Default tenant — acceptable for S3 single-tenant mode, do not build against it); access
tokens keep their `qf_at_` prefix; auth-service runs **nowhere** in this AWS account (infra#24 is
real scope); email transports AND `pkg/authclient` get vendored (Go `internal/` visibility / private
module CI friction).

**Founder/operator inputs needed at staging exit (none block merges)**: console + sending domain
names (SES identity/DKIM), Stripe account + test-mode keys + price + webhook secret, ACM DNS
validation, staging deploys per the committed checklist.

## v9.2 (2026-07-29 eve) — pilot-console S3 leg MERGED; 5-PR park cascade + box drain incident

**pilot-console S3 backend is fully on main, same day as dispatch.** The daemon decomposed the
epics into sub-issues and shipped 4 legs itself (#62 vendored authclient · #63 sessions migration ·
#69 S3.2 CRUD incl. its own complete BFF · #76 billing wiring incl. a full Stripe service) — then
the sibling-merge cascade parked the other 5 PRs `needs-manual-rebase` (all sharing
main.go/config.go/go.mod). Operator recovery, TASK-401-style unification audits: **#67 + #74 closed
superseded** (main's inline bff/config were strict supersets); **#75 unified** (grafted the two
missing webhook legs — subscription.deleted→inactive, payment_failed→past_due — new
`orgs.SetBillingStatusByStripeCustomerID`, and the handler test coverage main lacked); **#68 (email
sender)** and **#70 (instances API + proxy session dual-auth)** rebased with union resolution.
All merged manually (a rebased push never re-arms autopilot) → filed
[#4610](https://github.com/qf-studio/pilot/issues/4610) re-adopt-on-branch-update (5× recurrence
of the predicted class). Issues #57–61 all closed; repo back to only #45 open.

**Box drain incident (55 min)**: v2.249.0 released 14:20Z but the box's self-upgrade looped on
`drain timeout: 1 tasks still active: [GH-72]` — a **zombie active-task** from a stalled→retried
execution (no live worker, rows without `started_at`, PR already externally merged); stale
recovery never touches the in-memory registry and pollers pause during each drain attempt.
Operator restart 15:16Z → registry cleared → **self-upgrade completed autonomously 15:17:40Z,
box on v2.249.0** (hot restart, verified). Filed
[#4609](https://github.com/qf-studio/pilot/issues/4609) (drain-time process reconciliation +
finalize-on-stall-retry + drain-timeout alert).

**Still in flight from the wave**: auth#477/478, ui#13/14, infra#24 (their repos' queues — check
after the restart).

**Tenant bumped to v2.249.0 same evening** (~15:36Z): asset via instance's own GITHUB_TOKEN,
sha256 verified, mv-swap (backup `/opt/pilot/bin/pilot.v2.247.0.bak`), one restart — unit active,
`environment=hosted` honored per structured log. ⚠️ The TUI banner mislabels it "stage
environment" (cosmetic, renders a legacy field) → filed
[#4611](https://github.com/qf-studio/pilot/issues/4611); verify env via the structured
`autopilot enabled` log line, not the banner. **Both daemons now on v2.249.0.**

## v9.3 (2026-07-29 eve) — S3 WAVE 10/10 MERGED: both auth email legs closed same-day

- **auth PR#479** (GH-477 EmailSender + password-reset delivery) sat green+CLEAN un-adopted
  11:46Z→15:59Z (~4h13m, no `autoMergeRequest`; board showed autopilot stage=`failed` terminal) —
  the #4610 class, recurrence #6. Operator-reviewed against all acceptance criteria (lookup-first
  anti-enumeration — no token minted for unknown addresses; send-failure→202 with new
  `password_reset_email_failed` audit event; `requireStr` cross-field validation incl. new
  `PASSWORD_RESET_URL_BASE`; 4 delivery tests) → merged manually 15:59Z, recurrence noted on the
  issue.
- **pilot#4610 fixed by the daemon itself**: picked up 15:22Z, PR#4612 merged 15:52Z —
  re-adopt-on-branch-update ships on the next release train (~07-30 14:00Z). Manual merge remains
  the playbook for held PRs until the box carries it.
- **auth#478 (signup verification) shipped 25 min after #479 merged**: daemon pickup 16:00:24Z
  (60s after unblock) → PR#480, green — but autopilot stuck at `waiting_ci` with green Actions
  check-runs (possible GH-4384 recurrence on v2.249.0; evidence commented on the closed issue).
  Operator-reviewed + merged manually ~16:26Z. Review finding →
  [auth#481](https://github.com/qf-studio/auth-service/issues/481): pre-existing
  `ConsumeEmailVerifyToken` never checks `email_verify_token_expires_at` — expired links still
  verify (TTL is dead); also settle double-click semantics (spec said idempotent-200, storage
  gives 400). Low risk while verification is informational-only, must land before enforcement.
- ui#13/14 (PR#15/#16) and infra#24 (PR#25) confirmed merged — **entire 10-issue wave on main.**
- Local pre-push gate red on macOS, both root-caused + filed:
  [pilot#4613](https://github.com/qf-studio/pilot/issues/4613) `TestResolveMemoryDBPath`
  /var→/private/var symlink mismatch · [pilot#4614](https://github.com/qf-studio/pilot/issues/4614)
  check-integration.sh BRE `\(\)` empty-group bug false-flagging parameterized test helpers as
  orphan commands. Until fixed: docs-only pushes from the laptop need `--no-verify` (CI on main
  stays the real gate).
- **Local dev stack dispatched** (founder: "I still didn't see the platform"): UI mock mode
  verified running on :5173 (bun/vite, mock adapter is the default seam). Full-stack gaps filed as
  a 3-issue mini-wave — [auth#483](https://github.com/qf-studio/auth-service/issues/483) expose
  gRPC :4002 in compose · [ui#17](https://github.com/qf-studio/pilot-console-ui/issues/17) vite
  dev proxy for http mode · [console#77](https://github.com/qf-studio/pilot-console/issues/77)
  \`make local-up\` compose (pg×2 + redis + auth + console, zero AWS/Stripe creds) + demo seed +
  quickstart. Target: \`make local-up\` working after the daemon ships them.
- **Next**: operator staging deploys per infra PR#25 checklist (control-plane EC2 → ALB/ACM → SES
  identity + SPA hosting). Founder inputs needed: console + sending domain names, Stripe test
  keys/price/webhook secret, ACM DNS validation. Then the S3 exit test (staging signup → payment →
  credentials → provision → first PR, zero operator SSH, 3+ tenants concurrent).

## v9.4 (2026-07-29 night) — LOCAL STACK LIVE; first real E2E run finds UI wire-shape defect

- **`make local-up && make local-seed` works** — daemon shipped the whole mini-wave same-evening:
  ui#17 (vite proxy) + console#77 (compose: pg two-DB + redis + auth-service + console, JWT
  local-keys, seed script) merged autonomously; auth PR#484 (gRPC :4002 expose) + PR#482 sat
  green un-adopted (same class; fix rides the 07-30 train) → operator-reviewed + merged manually.
  All four containers healthy on first `local-up`; seed creates `demo@pilot.local` /
  `Pilot Demo Org`.
- **auth#481 CORRECTION**: PR#482 is a no-op resolution — the expiry fix + true-idempotency
  (token retained, already-verified short-circuit) were ALREADY in PR#480's final merge; the
  operator review that filed #481 read a stale revision. Verified directly on main
  (`user_repository.go:172-213`) + 4 repo-level Postgres tests pass. v9.3's "expired links
  verify" line is superseded.
- **E2E verified to the API layer**: BFF login (CSRF header `X-Requested-With: pilot-console`) →
  session cookie → `/api/v1/me` embeds org — all correct against the real stack.
- **UI defect found on first real login** → [ui#19](https://github.com/qf-studio/pilot-console-ui/issues/19):
  httpAdapter casts flat BFF JSON to the mock-shaped `Session {user, org}` → `user=undefined`,
  `isAuthenticated` true via `undefined !== null`, every login detours to /onboarding and the
  org-create 409 is swallowed. Exactly the drift class the local stack exists to catch; fix
  includes wire-fixture tests. **UI dev server currently runs http mode on :5173** against the
  live stack; mock mode = drop the env var.

## v9.5 (2026-07-30 morning) — overnight lead-watch: S3 product punch list DONE, dashboard live on real stack

Operator ran a 20-min lead-watch loop (founder AFK; full log:
`.agent/.context-markers/lead-watch-2026-07-29.md`). Six issues dispatch→shipped in ~2.5h,
then 30 quiet iterations (~22:30→08:09Z), zero stuck PRs:

- **ui#19→PR#20** wire-shape fix (login → dashboard, no onboarding detour) — E2E browser-verified.
- **ui#22→PR#23** readiness checklist = real contract (github+anthropic; Linear/Jira gone).
- **ui#21 epic** (instance detail page, the missing S3 status-page leg): daemon decomposed;
  leg #25→PR#28 parked DIRTY on a sibling's stricter `InstanceEventKind` union → operator
  worktree-rebase + union-resolve + 2 fixture tokens fixed, merged manually; parent PR#29
  self-merged; #26 auto-closed superseded **by the daemon itself** (TASK-401 machinery ✅).
- **ui#30→PR#31 dashboard v1** (self-merged in 15 min) — placeholder dead.
- **console#79** structured request log (off by default, on in local stack) — paid off same
  hour: pinpointed the next defect as `GET (unmatched) 404`.
- **console#81→PR#82** `GET /api/v1/credentials` presence-only (key/configured/last4) — the
  dashboard's missing third call; write-only PUT design had no read path.

**Drift-class scoreboard (one night): 4 defects** — #19 auth shapes · #22 requirement tokens ·
#32 list envelopes (`{"connections":[]}` vs bare array) · #81 missing GET. All caught by
operator browser+curl against the live stack; NONE by daemon gates (no docker in worktrees —
its "real-stack AC" passes are fixture-only). Rule of thumb now proven: **operator real-stack
verify gates UI merges** — **SOP ADOPTED 2026-07-30** (founder nod):
`sops/quality/real-stack-verify-gates-ui-merges.md`. Defect #5 same class filed same day:
ui#34 (reload bounces authenticated session to /login — guard runs at router install, before
session bootstrap; mock adapter can't reproduce by construction).

**Pipeline autonomy**: 5/7 PRs self-merged (3–15 min); manual merges only for the DIRTY rebase
(re-adopt fix rides today's 14:00Z train) and the un-adopted #479-class earlier. **Ledger
verified clean pre-train** (08:30Z): zero unfinalized executions — GH-25/26 stalls + GH-21
fails from the epic window all finalized, no #4609 zombie risk. Note: GH-25's stalled run
still created PR#28 → fresh evidence for #4609's finalize-on-stall-retry item.

**Next**: founder staging inputs (domains · Stripe test keys · ACM validation) → staging
deploys → S3 exit test. Brand/visual pass = founder taste call. 14:00Z train watch.

## v9.6 (2026-07-31) — first autonomous self-upgrade; drift-defects #6–#8 shipped+verified; release-train unblock (auth 11 days)

- **v2.251.0 = first fully autonomous self-upgrade** (07-31 train cut 14:11Z; daemon
  downloaded → backed up → hot-restarted via exec → verified at 14:13Z, zero operator).
- **Drift-defects #6/#7/#8 shipped and SOP-verified same day** (all found via the real-stack
  credential flow): ui#36 silent save failure (PR#37) · console#83 env-gated postgres secrets
  driver — local stack can now exercise the FULL credentials write path incl. live Anthropic
  key validation (PR#84; driver e2e 9/9 vs real pg) · ui#38 error-envelope mismatch
  (`{"error":…}` vs `body.message`, PR#39 shipped 6 min after filing). Task docs archived.
- **Release-train incident RCA'd + resolved** (TASK-431 → #4646/PR#4647): global
  `required_checks: [test, lint]` named checks that never post on auth-service (`lint`) /
  studio-sdk (`test` vs `build-test`) → permanent CIPending → 18 scopes stuck/parked,
  **auth-service 11 days unreleased**. Ops fix: per-project `ci_checks.required_checks: []`
  (pointer idiom) + 18 superseded scopes → done + restart ⇒ **auth v0.68.0 + sdk v0.31.2**
  cut within 2 ticks. Code fix (CIConfigMismatch terminal status, honest park message,
  startup lint, stale-panel reconcile) merged; live on box at Mon 08-03 train.
- #4643/#4644's park mechanism validated in production (bounded, converged); its diagnosis
  string was the misleading part — fixed by #4646.
