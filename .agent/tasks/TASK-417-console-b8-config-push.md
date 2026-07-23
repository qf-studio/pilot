# feat(fleet): B8 config push — render, versioned spec, SSM apply with rollback, reconciler drift phase

**Status**: ✅ PR UP + REVIEWED 2026-07-23 eve — [pilot-console#30](https://github.com/qf-studio/pilot-console/pull/30) (+2971/−85, CI green, awaiting auto-merge). Delivery took 5 generations: gen 0/1 stall-killed (v2.245.0, TASK-416 class) → false `declined-preflight` (4000-char judge cap, fixed via pilot#4507→PR#4509) → gen 2 heartbeat-SIGKILL on stdout base64 flood (unfiled) → gen 3 implemented everything but **never committed** (post-compaction amnesia; work destroyed by no_op worktree cleanup → pilot#4517→PR#4518 auto-preserve fix) → gen 4 delivered after re-arm + dispatch-note inoculation. Review verdict: AC4 rollback truthfulness (DONE/NOOP markers, no blind rollback on unknown outcome) + AC5 async drift sweep (decoupled from decide(), cooldowns, fleet-wide cap 2) + C12 `PILOT_HOSTED=1` fold all confirmed in diff. Archive once #30 merges.
Adversarial review same day: 4-agent pass (3 lenses + surface research), 30 findings
(3 blocker / 8 major) all incorporated below.
**Repo**: `qf-studio/pilot-console` · Labels: `pilot`, `no-decompose`.

## Context

Pilot Cloud tenants each run one EC2 instance with the pilot daemon. B6 provisions
the instance with a **placeholder** `/var/lib/pilot/config.yaml` ("just enough for
`/ready`; real config delivery is B8" — `internal/fleet/bootstrap.sh.tmpl` config
heredoc). B8 delivers the real thing: render a tenant's full `config.yaml`,
version it immutably, push it to the instance via SSM, restart the daemon
(restart-to-apply is the locked-in v1 design — no hot reload), and teach the
reconciler to detect and correct config drift as a new sweep dimension. B8 also
closes Epic C item 12's remaining sliver: the bootstrap systemd unit gains
`Environment=PILOT_HOSTED=1` (the daemon side shipped in GH-4274; the rendered
config below satisfies both hosted invariants, so the flag is safe to set here
and only here).

**Existing code surface you build on** (verified against main @ `52cc3c2`, re-verified 2026-07-23):

- `internal/fleet/store.go` — `Instance.ConfigGeneration int` (:95-107) backed by
  `instances.config_generation` (migration `0002_instances.up.sql:8`, desired-side
  only); `BumpConfigGeneration(ctx, id)` (:317-327) exists with zero PRODUCTION
  callers (only `store_test.go:214`); typed-error setter pattern to copy:
  `SetObservedState`/`SetDesiredState` (:282-315, `checkRowAffected`,
  `fmt.Errorf("fleet: <op>: %w")`, sentinels via `errors.Is`); `ListAll`
  (:253-265) is the reconciler's per-tick snapshot; journal API is
  `Store.AppendEvent(ctx, instanceID uuid.UUID, eventType string, detail any)`
  with `<domain>.<past_tense>` names (`provision.started`, `reconcile.drift_observed`, …).
- `internal/fleet/reconciler.go` — `tick()` = observe → sync → decideAndAct →
  probeReady (:226-245). `decide()` (:50-98) is the SINGLE pure decision owner,
  invariant 2 = at most one state-correcting action per row per tick; the `action`
  enum (:33-42) has no config verb and MUST NOT gain one (see invariant 3 below).
  Async lifecycle actions run via `dispatchLong` + the `r.inflight` map
  (:446-486), tracked by `r.wg`, capped by `MaxConcurrentActions` (default 4).
  `probeReady` (:709-748) models an independent, gated sweep phase but is
  SYNCHRONOUS with an `Interval/2` budget — too small for a push (see AC5's
  explicit execution model). Reconciler drives provisioning only via
  `provisionAPI.ProvisionClaimed` (:134-140) — never `ClaimInstance`.
- `internal/fleet/bootstrap.go` — `RenderBootstrap`/`BootstrapParams` (:16-34),
  `//go:embed` template, base64 → `RunInstances` UserData (`provisioner.go:258-305`).
  Systemd unit: `ExecStartPre=/opt/pilot/fetch-secrets.sh`,
  `EnvironmentFile=-/run/pilot/env`; binary `/opt/pilot/bin/pilot`; config on the
  data volume at `/var/lib/pilot/config.yaml`. EC2 UserData hard limit: 16KB raw.
- `internal/fleet/ready.go` — `ReadyChecker` interface (:20-22); `SSMReadyChecker`
  (:43-105) already does `SendCommand`/`GetCommandInvocation` polling against the
  AWS-managed `AWS-RunShellScript` document (:61) via the narrow `ssmSendGetAPI`
  interface (:38-41 — exactly those two methods; extend it, the same
  `*ssm.Client` also serves Parameter Store reads). `HTTPReadyChecker`
  (:112-177) is the alternate mode (`cfg.Fleet.ReadyMode`).
- `internal/secrets/writer.go` — SecureString paths `/tenants/{orgID}/{KEY}`
  (:119-126), six typed keys (:23-30) incl. `PILOT_GATEWAY_TOKEN`;
  `GenerateGatewayToken` (:219-232) has zero production callers — the e2e test
  generates the token manually as an operator pre-step
  (`provisioner_e2e_test.go:112`); AC6 moves that into the provision path. NOTE:
  the exported `Writer` interface (:71-81) is Put/Delete/DeleteAll/
  GenerateGatewayToken only — no read/existence method — and
  `GenerateGatewayToken` Puts with `Overwrite: true`, so calling it when the
  token exists silently ROTATES it. AC6 adds an existence check first.
- Daemon side (pilot repo, already shipped — no pilot-repo changes in B8): config
  loads with `os.ExpandEnv` + GH-3755 guard that HARD-FAILS on unset
  secret-shaped `${VAR}` refs; `PILOT_HOSTED=1` makes `config.Save()` a no-op and
  `AssertHostedInvariants` requires `upgrade.auto_hot_upgrade=false` +
  `tunnel.enabled=false` (pilot `internal/config/config.go:682-736`).

**Design invariants** (each has bitten this program before):

1. Rendered config contains secrets ONLY as `${VAR}` references to the six A4
   keys — never literals. A literal in the rendered YAML is a stored secret in
   Postgres and a leak in SSM command output. The same rule applies to the
   `params` jsonb: no field of `ConfigParams` may carry a credential.
2. A push whose `${VAR}` refs are not all present under `/tenants/{orgID}/` will
   brick the daemon at next start (GH-3755 hard-fail). Pre-flight-verify secrets
   before every push AND before provision-time render; reject with an event,
   never push blind.
3. Config drift is a THIRD dimension, orthogonal to desired/observed lifecycle
   state. It gets its own sweep phase — NOT a new `decide()` action. Extending
   `decide()` would force artificial priority ordering against
   provision/start/stop/terminate (the `ProvisionClaimed` collision class from
   the reconciler review, TASK-415 finding).
4. Spec versions are immutable and monotonic. A generation row, once inserted, is
   never updated; applied-generation only ever moves forward.
5. File content crossing SSM or UserData boundaries travels base64-encoded —
   inline heredocs/`\n` escapes do not survive SSM JSON (verified operational
   failure mode on the founder box), and heredoc delimiters can collide with
   content.
6. A failed apply must not strand the instance dead, and the journal must never
   claim an outcome that wasn't observed: roll back only on TERMINAL command
   failure or confirmed post-restart unhealth; an ambiguous outcome (invocation
   timeout) is journaled as unknown and left to the drift sweep — never
   "resolved" by firing a rollback that can interleave with a still-running
   apply.

<!-- pilot:no-decompose -->

## Golden rendered config (normative for AC2)

Key paths verified against the daemon schema (pilot `internal/config/config.go:43-62`,
`configs/pilot.example.yaml`). `auth` is TOP-LEVEL (not under `gateway`);
executor backend is `executor.type: "claude-code"` (hyphen — `claude_code` fails
the backend factory). With repos configured:

```yaml
version: "1.0"
gateway:
  host: "0.0.0.0"
  port: 9090
auth:
  type: "api-token"
  token: "${PILOT_GATEWAY_TOKEN}"
tunnel:
  enabled: false
upgrade:
  auto_hot_upgrade: false
executor:
  type: "claude-code"
adapters:
  github:
    enabled: true
    token: "${GITHUB_TOKEN}"
    repo: "<owner>/<name of first repo>"
    polling:
      enabled: true
      interval: 30s
      label: "pilot"
projects:
  - name: "<repo name>"
    path: "/var/lib/pilot/repos/<repo name>"
    default_branch: "<branch>"
    github:
      owner: "<owner>"
      repo: "<name>"
```

With ZERO repos (the provision-time default): same file but
`adapters.github.enabled: false`, no `token` line, no `repo` line, empty
`projects: []` — so the only `${VAR}` ref is `${PILOT_GATEWAY_TOKEN}`.
`ANTHROPIC_API_KEY` is env-injected by fetch-secrets, not a config key.

## Acceptance

1. **Migration + store surface.** New migration `0004` adds
   `instances.applied_config_generation integer NOT NULL DEFAULT 0` and table
   `instance_config_specs (id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
   instance_id uuid NOT NULL REFERENCES instances(id), generation integer NOT
   NULL, params jsonb NOT NULL, rendered_yaml text NOT NULL, rendered_sha256
   text NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
   UNIQUE(instance_id, generation))`. Rows are retained after instance
   termination — deliberate audit-trail decision, document it in the migration
   comment. Store gains `SetAppliedConfigGeneration(ctx, id uuid.UUID, gen int)
   error` (typed-error/`checkRowAffected` pattern of `SetObservedState`; rejects
   gen lower than current value with `ErrInvalidState` — invariant 4),
   `InsertConfigSpec(ctx, spec ConfigSpec) error`, `GetConfigSpec(ctx,
   instanceID uuid.UUID, gen int) (ConfigSpec, error)` (`ErrNotFound` when
   absent), and `Instance.AppliedConfigGeneration int` hydrated everywhere
   `ConfigGeneration` is. Postgres-backed tests via the `newTestStore(t)`
   pattern (`store_test.go:17-54`): monotonic-rejection, unique-violation on
   duplicate generation mapped to a typed error, round-trip of params jsonb +
   rendered_yaml.
2. **Renderer.** `internal/fleet/configrender.go`: `type ConfigParams struct {
   OrgID string; EnvName string; GatewayPort int; Repos []RepoParam }` with
   `RepoParam { Owner, Name, DefaultBranch string }` (all jsonb-serializable;
   NO credential-bearing fields — invariant 1) and `RenderTenantConfig(p
   ConfigParams) (string, error)`. Output is exactly the golden config above
   for the given params, deterministic (stable key/repo ordering) so
   `sha256(rendered)` is a stable identity; returns a typed error if rendered
   size ≥ 8KB. Unit tests assert: parses as YAML; every golden hard-set field
   present with the exact required value (`auth.type`, `auth.token`,
   `gateway.host`, `upgrade.auto_hot_upgrade`, `tunnel.enabled`,
   `executor.type`, polling on / webhooks absent); zero secret-shaped literals
   in BOTH the rendered YAML and `json.Marshal(params)` (regex sweep);
   zero-repos output disables the github adapter and references only
   `${PILOT_GATEWAY_TOKEN}`; identical params ⇒ identical bytes.
3. **Spec creation.** `CreateConfigSpec(ctx, instanceID uuid.UUID, p
   ConfigParams) (int, error)` on the store: in ONE transaction, bump via the
   atomic form `UPDATE instances SET config_generation = config_generation + 1
   WHERE id = $1 RETURNING config_generation` (the mechanism is REQUIRED, not
   just the observable outcome — a read-then-write bump loses updates at READ
   COMMITTED) and insert the immutable `instance_config_specs` row (params +
   rendered_yaml + sha256). Returns the new generation. This becomes the only
   production caller of a bump; the standalone `BumpConfigGeneration` may be
   absorbed. Test: two concurrent CreateConfigSpec calls on one instance yield
   distinct, gap-free generations (row-lock serializes them).
4. **Pusher.** `internal/fleet/configpush.go`: `ConfigPusher` consuming a narrow
   SSM interface (extend `ssmSendGetAPI` with the Parameter Store read it
   needs — same `*ssm.Client`) + a `ReadyChecker`. `Push(ctx, inst Instance,
   gen int) error` does, in order:
   (a) load spec via `GetConfigSpec` and use the STORED `rendered_yaml`
   (verify `rendered_sha256` matches the stored bytes as a corruption check —
   do NOT re-render from params: renderer evolution would orphan every
   pre-existing spec);
   (b) pre-flight: `GetParametersByPath` under `/tenants/{orgID}/` and verify
   every `${VAR}` referenced by the stored config exists — on miss, journal
   `config.push_rejected` with the missing key NAMES (never values) and return a
   typed error (invariant 2);
   (c) `SendCommand` (`AWS-RunShellScript`, same document as `ready.go:61` —
   deliberate decision: no custom SSM document in v1, see Scope fence) whose
   script receives the config base64-encoded (invariant 5) plus its sha256, and:
   if the current on-disk config already has the target sha, exits 0 WITHOUT
   restarting (makes re-push after an ambiguous outcome restart-free);
   otherwise decodes to a temp file, backs up current config to
   `/var/lib/pilot/config.yaml.prev`, atomically `mv`s the new file in, and
   `systemctl restart pilot`;
   (d) poll `GetCommandInvocation` under a dedicated per-push timeout
   (`ConfigPushTimeout`, default 5m — NOT the reconciler tick budget);
   (e) outcome branches, journaled truthfully (invariant 6):
   - command Success + ready green (via the injected `ReadyChecker`) →
     `SetAppliedConfigGeneration` + `config.push_completed`;
   - command terminal Failed, or Success + ready red → rollback command:
     `[ -f /var/lib/pilot/config.yaml.prev ] && mv … && systemctl restart
     pilot` (conditional — a pre-backup failure has nothing to roll back and
     is NOT an error); then re-run the `ReadyChecker`: green →
     `config.push_rolled_back` (with `noop: true` when `.prev` was absent),
     red → `config.push_failed` with `rollback_unhealthy: true`;
   - rollback command itself fails → `config.push_failed`;
   - invocation still InProgress at timeout (ambiguous — the script may yet
     complete) → NO rollback, journal `config.push_outcome_unknown`, leave
     applied generation unchanged; the drift sweep re-pushes and the sha guard
     in (c) makes that convergence restart-free;
   - applied generation is untouched on every non-success branch.
   `config.push_started` at entry. Unit tests with a fake SSM client cover
   every branch above, including sha-guard skip, `.prev`-absent rollback,
   rollback-unhealthy, and timeout-no-rollback.
5. **Reconciler drift phase.** New phase `syncConfig` called from `tick()` after
   `probeReady` (`reconciler.go:226-245`), reusing probeReady's ELIGIBILITY
   style but with an explicit ASYNC execution model (probeReady's synchronous
   `Interval/2` budget cannot contain a multi-minute push):
   - Eligible rows: `desired=running && observed=running`, not in `r.inflight`,
     not already pushing, `ConfigGeneration > AppliedConfigGeneration`, past
     the per-instance failure cooldown (`ConfigPushRetryCooldown`, default 5m)
     and phase interval (`ConfigSyncInterval`, default 60s) — both on
     `ReconcilerConfig` via the existing zero-value→default pattern in
     `NewReconciler` (:172-184); no validation-error path exists there, so
     non-positive values simply default.
   - Execution: each push runs in its own goroutine tracked by `r.wg` (so
     shutdown's 30s drain sees it) and registered in a DEDICATED
     `configPushInflight` map — NOT `r.inflight`, NOT counted against
     `MaxConcurrentActions` — with its own cap `ConfigPushMaxConcurrent`
     (default 2). At most one in-flight push per instance; pushes for
     different instances proceed concurrently.
   - Lifecycle collision rule (both directions): a row with an in-flight
     lifecycle action is ineligible for push (existing direction); a lifecycle
     action arriving MID-PUSH is not blocked — the push completes or fails on
     its own, and push/rollback SSM errors against a stopping/terminated
     instance are journaled as `config.push_failed`, never retried against a
     non-running row.
   - `reconcile.config_drift_detected` journaled once per drift episode
     (dedupe on the (ConfigGeneration, AppliedConfigGeneration) pair,
     in-memory per-process like `lastReadyProbe` — a console restart may emit
     one duplicate event and retry one cooldown early; accepted, do not build
     journal-backed dedupe).
   - `decide()` and the `action` enum are UNTOUCHED (invariant 3) — add a test
     asserting `decide()`'s outputs are identical for rows differing only in
     config-generation fields. Loop tests (table-driven, fake pusher):
     (a) drift + running/running ⇒ exactly one Push; (b) drift + stopped ⇒ no
     Push, drift persists (apply on next running sweep — no queueing
     machinery); (c) push failure ⇒ cooldown respected on subsequent ticks;
     (d) no drift ⇒ zero Push calls; (e) in-flight lifecycle action ⇒ push
     skipped that tick; (f) desired flips to terminated mid-push ⇒ push
     outcome journaled, no retry, lifecycle proceeds.
6. **Provision-time initial config + token wiring + PILOT_HOSTED.** The
   provisioner renders the tenant's REAL config at provision time with the
   zero-repos default params (`ConfigParams{OrgID: …, EnvName: cfg.Env,
   GatewayPort: 9090}` — repo population arrives with the future settings
   epic): `ProvisionClaimed` calls `CreateConfigSpec` and embeds that
   generation's `rendered_yaml` into the bootstrap config block
   base64-encoded, decoded by the bootstrap script (unique delimiter
   `PILOT_CONFIG_B64_EOF`; base64 charset cannot collide with a delimiter —
   invariant 5 — replacing the current quoted-heredoc plaintext block). After
   the `/ready` gate passes, `SetAppliedConfigGeneration(id, gen)` **where
   `gen` is the value returned by the `CreateConfigSpec` call whose render
   went into UserData** — never a literal; a crash-retry of ProvisionClaimed
   creates a fresh generation and that burned number is accepted. Before
   `RunInstances`: (i) pre-flight ALL `${VAR}` refs of the rendered config
   against `/tenants/{orgID}/` (same check as AC4b) — on miss, fail the
   provision pre-launch with the missing key names journaled; no EC2
   resources created (a blind launch relaunch-loops fetch-secrets for 5m,
   then `fail()` burns the instance AND volume — `provisioner.go:385-400`);
   (ii) ensure `PILOT_GATEWAY_TOKEN` exists: `secrets.Writer` gains
   `Exists(ctx, orgID string, key Key) (bool, error)` (SSM `GetParameter`,
   `ParameterNotFound` → false), and the provisioner — which gains an
   injected `secrets.Writer` dependency, wired in `main.go` — calls
   `GenerateGatewayToken` ONLY when absent (it overwrites, i.e. rotates, when
   present); (iii) assert total UserData < 16KB raw (typed error — EC2's hard
   limit; the 8KB render cap in AC2 plus the ~3KB bootstrap leaves headroom).
   The systemd unit template gains `Environment=PILOT_HOSTED=1` (closes Epic C
   item 12; the golden config's `auto_hot_upgrade: false` + `tunnel.enabled:
   false` satisfy the daemon's `AssertHostedInvariants`). Tests: rendered
   bootstrap contains the base64 config and decodes to the golden zero-repos
   YAML with `${VAR}` refs un-expanded; unit sets PILOT_HOSTED=1; token
   generated exactly once (absent→created, present→untouched); provision
   rejected pre-launch on missing gateway token.
7. **e2e (env-gated, NOT CI).** Extend the `PILOT_CONSOLE_E2E=1` suite
   (`reconciler_e2e_test.go` conventions: documented env vars in the header,
   unconditional `t.Cleanup` teardown): provision an instance → assert applied
   generation == the generation CreateConfigSpec returned at provision →
   `CreateConfigSpec` with one added repo → wait for the reconciler's
   `syncConfig` to push → assert on-instance file changed (SSM cat), daemon
   restarted and `/ready` green, applied generation == the new returned value,
   journal contains the full `config.*` event trail. Operator preconditions
   documented in the test header: console's AWS principal needs
   `ssm:SendCommand` + `ssm:GetParameter*` on tenant resources (prior e2e runs
   used operator-local credentials — a passing SSM ready-check does NOT prove
   the deployed console role has the grant), and `pilot-canary-sandbox` must
   NOT appear in any pushed config (S2 pre-step: ownership transfer not yet
   done).

## Implementation

File plan:

- `internal/db/migrations/0004_config_specs.up.sql` / `.down.sql` — AC1
- `internal/fleet/store.go` — applied-gen setter, spec insert/get, CreateConfigSpec — AC1/AC3
- `internal/fleet/configrender.go` (+ `configrender_test.go`) — AC2
- `internal/fleet/configpush.go` (+ `configpush_test.go`) — AC4
- `internal/fleet/reconciler.go` — `syncConfig` phase + `ReconcilerConfig` knobs;
  `NewReconciler` gains a `pusher` parameter (nil-safe so existing tests and a
  push-less deployment keep working) — AC5
- `internal/secrets/writer.go` — `Exists` on the `Writer` interface — AC6
- `internal/fleet/bootstrap.go` / `bootstrap.sh.tmpl` / `provisioner.go` —
  base64 config embed, PILOT_HOSTED, pre-flight, token wiring, provisioner's
  new `secrets.Writer` dependency — AC6
- `internal/config/config.go` + `cmd/pilot-console/main.go` — env plumbing for
  the three new knobs (`ConfigPushTimeout`, `ConfigPushRetryCooldown`,
  `ConfigSyncInterval` + `ConfigPushMaxConcurrent`), ConfigPusher construction
  and injection into `NewReconciler` and `NewProvisioner`
- `internal/fleet/reconciler_e2e_test.go` or new `configpush_e2e_test.go` — AC7

Sequencing inside the PR: AC1 (schema+store) → AC2 (renderer, pure) → AC3 →
AC4 (pusher against fakes) → AC5 (reconciler wiring) → AC6 (provision path) →
AC7 (e2e, env-gated).

Execution constraint: implement fully locally — make NO real AWS calls; every
AWS interaction goes through the existing narrow-interface pattern with fakes in
tests. The e2e test is delivered env-gated only; the operator runs it
deliberately.

Accepted, documented limitation: a config push restarts the daemon and may
abandon an in-flight tenant task; an ambiguous-outcome re-push can add a second
restart for the same generation (the sha guard usually prevents it). Idle-aware
pushing needs the C13 proxy — deferred.

## Scope fence — out of scope, do not build

- **Tenant settings/connections model + API.** No `connections` table, no HTTP
  handlers. `ConfigParams` is supplied by callers (provisioner, tests, future
  settings epic). Do not invent a settings schema.
- **Custom `pilot-apply-config` SSM document / any pilot-cloud-infra change.**
  v1 reuses `AWS-RunShellScript` (ready.go:61 precedent). Keep the apply script
  in one function so a future custom-document swap is mechanical. The
  `ssm:SendCommand` IAM grant is an operator/infra follow-up, not this PR.
- **Daemon-side (pilot repo) changes.** No `/api/v1/status` applied-version
  field; console self-records applied generation on confirmed push (design doc's
  status-endpoint reporting is deferred).
- **Idle-queueing / "applying after current task" UX.** Requires the C13 proxy.
- **B7 sleep/wake** (S5), **B9 AMI upgrade**, **B10/B11 snapshots/DLM** — untouched.
- **`pilot-canary-sandbox`** must not be added to any config, template, test
  fixture, or default — S2 ownership-transfer pre-step is not done.

## Refs

- Status: drafted 2026-07-22 from main @ `52cc3c2`; adversarial review 2026-07-23
  (3 review lenses + 1 surface-research agent, 30 findings incorporated —
  blockers: wrong YAML key paths `gateway.auth.*`/`executor.backend.type`,
  unspecified push execution model); dispatch as pilot-console issue
  (`pilot` + `no-decompose`).
- Program: [TASK-405](TASK-405-pilot-saas-platform.md) · plan of record
  `.agent/system/saas-roadmap.md` (§S2) · design `.agent/system/saas-fleet-design.md`
  §config push (:107-110) · `.agent/system/saas-architecture.md` (:150-153, :232).
- Siblings: [TASK-415](TASK-415-console-fleet-reconciler.md) (reconciler, console#24→PR#25,
  merged 2026-07-22 16:06Z — left `config_generation` drift to B8 by explicit
  scope fence) · TASK-408 (B6 provisioner, console#22→PR#23).
- Console PRs this builds on: #13 (B5 store), #15 (A4 secrets), #23 (B6), #25 (reconciler).
- Hard invariants (cross-project, restated): secrets by `${VAR}` reference only ·
  one action per row per tick belongs to `decide()` alone · never `ClaimInstance`
  from the reconciler · canary-sandbox stays local until ownership transfer.
