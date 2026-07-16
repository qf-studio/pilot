# feat(fleet): B6 provisioner — RunInstances + data volume + bootstrap + /ready gate

## Context

This is **B6 of workstream B (fleet reconciler & lifecycle)** for Pilot Cloud: the provisioner that takes an org from `requested` to a `running` EC2 instance serving pilot's `:9090/ready`. It builds **greenfield** on the B5 store (#11) and A4 secrets writer (#12) in THIS repo. Everything you need is embedded below — do not assume access to any other repository or design doc.

**The product being hosted**: `pilot` is a Go daemon that polls GitHub/Linear/Jira for labeled issues and executes them with Claude Code. One tenant = one EC2 instance (t3.large default), hard VM isolation. The instance carries two volumes: root (from a golden AMI: AL2023 + Node 22 + git + gh + claude CLI; immutable, disposable) and a **data volume** (gp3, mounted at `/var/lib/pilot`, holds `config.yaml`, `data/pilot.db`, repo clones — the tenant's state, survives instance replacement).

**Lifecycle state machine** (already enforced by `internal/fleet/store.go` vocab + SQL CHECK constraints):

```
REQUESTED → PROVISIONING → BOOTSTRAPPING → RUNNING ⇄ STOPPED
                                              ├→ UPGRADING / SUSPENDED / TERMINATED
```

B6 owns `requested → provisioning → bootstrapping → running` and the failure path (→ `terminated` + journaled event). Everything else is later issues.

**Existing code surface you build on (verified 2026-07-16):**

- `internal/fleet/store.go` — `Store` (Postgres via pgx/v5 stdlib): `CreateInstance(ctx, orgID uuid.UUID, desiredAMI, instanceType string) (Instance, error)` (inserts desired=`running`, observed=`requested`, default type `t3.large`), `GetInstance`, `ListInstancesByOrg`, `ListAll`, `SetDesiredState`, `SetObservedState(ctx, id, state, ec2InstanceID, az string) error`, `BumpConfigGeneration`, `AppendEvent(ctx, instanceID, eventType string, detail any) (InstanceEvent, error)`, `ListEvents`. Typed vocab: `ObservedRequested/Provisioning/Bootstrapping/Running/Stopped/Upgrading/Suspended/Terminated`, `DesiredRunning/...`. Errors: `fleet.ErrNotFound`, `fleet.ErrInvalidState`.
- `internal/secrets/writer.go` — SSM SecureString writer at `{pathPrefix}/{orgID}/{KEY}`, KMS-encrypted; `Writer` interface incl. `GenerateGatewayToken(ctx, orgID) (string, error)` and `DeleteAll(ctx, orgID)`. The **instance** reads these at boot. The provisioner code does NOT write secrets — but the e2e test seeds them (see Acceptance 8).
- `internal/config/config.go` — env-driven `Load()`: `PILOT_CONSOLE_ADDR`, `DATABASE_URL`, `LOG_LEVEL`, `PILOT_CONSOLE_SECRETS_KMS_KEY_ID`, `PILOT_CONSOLE_SECRETS_PATH_PREFIX` (default `/tenants`). Extend this (see Implementation).
- `internal/db/migrate.go` — golang-migrate over `//go:embed migrations/*.sql`; add `0003_*.up.sql/.down.sql` pairs.
- Test conventions: table-driven, stdlib `testing` only; AWS mocking = **narrow private interface + hand-rolled fake in the same `_test.go`** (see `ssmAPI`/`fakeSSM` in `internal/secrets`); DB tests gated on `DATABASE_URL`, hard-fail if unset while `CI` is set.
- Event-type convention (from B5 tests): dotted names, e.g. `"provision.started"` with a JSON detail map.
- deps present: `aws-sdk-go-v2/service/ssm` (+types), pgx/v5, golang-migrate, google/uuid. **NOT present: `aws-sdk-go-v2/config`, `aws-sdk-go-v2/service/ec2` — you add them.**

**Deliberate bridges (both temporary, both must be explicit in code comments):**

1. **No golden AMI with the pilot binary exists yet** (AMI v2 is a separate infra task). Bootstrap therefore installs the binary from S3 (`aws s3 cp` of a pre-uploaded release tarball; URI from config). When AMI v2 lands, an empty `BinaryS3URI` config skips the install step.
2. **No per-tenant IAM/SG factory yet** (the A3 workstream). Subnet, security group, and instance-profile come from config; tests use existing sandbox-stack values.

This task must NOT be decomposed — implement as a single PR. <!-- pilot:no-decompose -->

## Acceptance

1. `internal/fleet/provisioner.go` — `Provisioner` with constructor injection: `New(store *Store, ec2c ec2API, ready ReadyChecker, cfg ProvisionerConfig)`. `ec2API` is a **private narrow interface** covering exactly the SDK calls used: `DescribeSubnets`, `CreateVolume`, `RunInstances`, `AttachVolume`, `DescribeInstances`, `DescribeVolumes`, `TerminateInstances`, `DeleteVolume`. No `CreateTags` — all tagging rides `TagSpecifications` on `RunInstances` (ResourceType=instance) and `CreateVolume` (ResourceType=volume). Fake in `provisioner_test.go`.
2. `Provision(ctx, orgID uuid.UUID) (Instance, error)` drives, with an `instance_events` journal entry and `SetObservedState` at each transition, **in this order** (volume BEFORE instance so its ID can be templated into user-data):
   - claim row (AC 3) → event `provision.started`
   - `DescribeSubnets(cfg.SubnetID)` → AZ
   - `CreateVolume`: gp3, size from config (default 100 GiB), in that AZ, `TagSpecifications` (ResourceType=volume): `pilot:org_id`, `pilot:env`, `pilot:data-volume=true` → event `volume.created` (detail: volume id, az). The root volume must NOT carry `pilot:data-volume=true` (a snapshot policy will target that tag) — which this ordering guarantees, since only the data volume gets these tags.
   - `RunInstances`: explicit params — AMI/type/subnet/SG/instance-profile from config, `ClientToken` = instance row UUID (AWS-side idempotency), **`MetadataOptions: HttpTokens=required, HttpEndpoint=enabled`** (IMDSv2 — hard invariant, assert in unit test), `TagSpecifications` (ResourceType=instance): `pilot:org_id`, `pilot:env`, `Name=pilot-tenant-{orgID}`. **`UserData` MUST be base64-encoded by the caller** — `base64.StdEncoding.EncodeToString(renderedBootstrap)`; aws-sdk-go-v2 does NOT encode it for you and RunInstances rejects raw text. → observed `provisioning`, event `instance.launched` (detail: ec2 instance id, az)
   - poll `DescribeInstances` until state=`running`, then `AttachVolume` at device `/dev/sdf` → event `volume.attached` → observed `bootstrapping`
   - ready gate (AC 6) → observed `running` + event `provision.completed` when ready within `ReadyTimeout` (default 5 min, config)
   - The RunInstances unit test must base64-decode the captured UserData and assert it equals the rendered template — pinning both the encoding and the no-secrets invariant on the wire value.
3. **Atomic claim (no double-provision)**: migration `0003` adds a **partial unique index** `CREATE UNIQUE INDEX instances_org_active_uniq ON instances(org_id) WHERE observed_state <> 'terminated'`. The up migration must first normalize pre-existing data (keep the newest active row per org via `row_number() OVER (PARTITION BY org_id ORDER BY created_at DESC)`, set the rest to `observed_state='terminated'`) so the index creation cannot fail on populated dev DBs. New store method `ClaimInstance(ctx, orgID, desiredAMI, instanceType) (Instance, error)` performs the INSERT and maps unique-violation to a typed `ErrAlreadyProvisioned` **only when** the `*pgconn.PgError` has `Code=="23505" && ConstraintName=="instances_org_active_uniq"`. `Provision` uses it — INSERT is the claim; check-then-act is forbidden. Store test proves two concurrent claims yield exactly one winner. **Audit existing store tests**: any test holding 2+ non-terminated instances for one org now violates the index (e.g. the B5 create/list test) — give each concurrently-active instance a distinct org UUID or terminate the prior one first.
4. **Failure = cleanup + journal, no retry loop**: any error after the claim → best-effort cleanup of resources this run created, observed `terminated`, event `provision.failed` (detail: stage, error string — **error strings must not contain secret values**). Cleanup ordering matters: if the instance launched, `TerminateInstances` → poll `DescribeVolumes` until the data volume is `available` (bounded, 5 min; on timeout journal the leaked volume id in the event detail) → `DeleteVolume`. A volume never attached (pre-launch failure) is `available` and deletes immediately. The fake must reject `DeleteVolume` while it considers the volume attached, so ordering is asserted. Unit tests cover: DescribeSubnets error, CreateVolume error, RunInstances error, AttachVolume error, ready-timeout — each asserting the correct cleanup calls.
5. **Bootstrap user-data** (`//go:embed bootstrap.sh.tmpl`, rendered via `text/template`): MUST contain **zero secret material** (unit test asserts rendered output contains no values from a secrets fixture; only org id, region, SSM path prefix, S3 URI, and the data-volume device path are templated). Script (AL2023, cloud-init runs it once as root, first line `#!/usr/bin/env bash` + `set -euo pipefail` so failures surface in cloud-init logs):
   - resolve the data device by exact ID — the volume is created before the instance, so template it: `DEV=/dev/disk/by-id/nvme-Amazon_Elastic_Block_Store_{{.VolumeIDNoDash}}` (EBS volume id with the dash removed, e.g. `vol-0abc...` → `vol0abc...`; on Nitro instances EBS attaches as NVMe and udev creates this symlink; do NOT glob `nvme-Amazon_Elastic_Block_Store_vol*` — the root volume is also EBS-NVMe and matches). Poll up to 300s for the symlink (AttachVolume happens after boot begins). Unit test asserts the rendered script contains the exact templated path.
   - `mkfs -t xfs "$DEV"` **only if** `blkid` shows no filesystem on it; then **unconditionally** `mkdir -p /var/lib/pilot`, mount, and ensure an fstab entry by UUID with `nofail` (idempotent) — an already-formatted volume (instance replacement) must still mount.
   - install binary if S3 URI set (URI points at a `pilot-linux-amd64.tar.gz` release asset, binary `pilot` at tarball top level): `aws s3 cp {{.BinaryS3URI}} /tmp/pilot.tar.gz && tar -xzf /tmp/pilot.tar.gz -C /tmp pilot && install -D -m 0755 /tmp/pilot /opt/pilot/bin/pilot` (`-D` creates `/opt/pilot/bin` — it does not exist on the AMI; `s3:GetObject` on that key is an operator test-setup step, not code)
   - write `/opt/pilot/fetch-secrets.sh`: first line after the shebang is `mkdir -p /run/pilot` (tmpfs, wiped every boot), then `aws ssm get-parameters-by-path --path {{.SecretsPath}} --with-decryption` → `/run/pilot/env` (KEY=value lines), `chmod 0400` — secrets never touch the EBS volume as env material
   - write `/var/lib/pilot/config.yaml` **via a quoted heredoc (`<<'EOF'`)** so `${PILOT_GATEWAY_TOKEN}` survives literally (pilot expands it from the systemd environment at load time and hard-fails if it expands empty). Exact content — key names must be exact because pilot silently ignores unknown YAML keys; `auth` is a TOP-LEVEL key, sibling of `gateway`, NOT nested under it:

     ```yaml
     gateway:
       host: 0.0.0.0
       port: 9090
     auth:
       type: api-token
       token: ${PILOT_GATEWAY_TOKEN}
     ```

     This is a placeholder — just enough for `/ready`; real config delivery is B8, out of scope.
   - write systemd unit `pilot.service`: `[Service]` with `ExecStartPre=/opt/pilot/fetch-secrets.sh`, **`EnvironmentFile=-/run/pilot/env`** (the leading `-` is load-bearing: systemd reads EnvironmentFile before EVERY Exec* process including ExecStartPre — without `-`, a missing file on first boot fails the unit before fetch-secrets.sh can create it, and Restart loops into permanent `failed`), `ExecStart=/opt/pilot/bin/pilot start --config /var/lib/pilot/config.yaml`, `Restart=on-failure`, `RestartSec=5` (boot-time IAM/SSM settling must not exhaust the start limit); then `systemctl daemon-reload && systemctl enable --now pilot`. Unit test asserts the rendered unit contains `EnvironmentFile=-/run/pilot/env`.
6. `ReadyChecker` interface `Ready(ctx, inst Instance) error` with two impls, both unit-tested with fakes, selected via config:
   - `SSMReadyChecker` (default): SSM `SendCommand` (document `AWS-RunShellScript`, command `curl -sf localhost:9090/ready`) + `GetCommandInvocation` poll — works with zero inbound networking. SSM client dep is already in go.mod.
   - `HTTPReadyChecker` (for in-VPC console later): `Instance` carries no IP field and must not gain one (no schema change) — construct the checker with the same narrow describe capability (it may reuse `ec2API`) and resolve the target per call via `DescribeInstances(inst.EC2InstanceID)` → `PrivateIpAddress`, then GET `http://{ip}:9090/ready`. `Ready(ctx, inst Instance)` keeps its signature.
   - On ready-timeout, the provisioner best-effort captures the SSM invocation stdout/stderr (or HTTP error) into the `provision.failed` event detail — a terminated instance leaves no other evidence.
7. Config: `FleetConfig` sub-struct on `Config` mirroring `SecretsConfig` style — `PILOT_CONSOLE_FLEET_REGION`, `_SUBNET_ID`, `_SECURITY_GROUP_ID`, `_INSTANCE_PROFILE_ARN`, `_AMI_ID`, `_ENV` (tag value, default `dev`), `_DATA_VOLUME_GIB` (default 100, validate ≥ 8), `_BINARY_S3_URI` (optional), `_READY_TIMEOUT` (default `5m`), `_READY_MODE` (`ssm`|`http`, default `ssm`). Validation + defaults in `Load()` with table-driven tests. **Do not modify `main.go` in this task** — the provisioner is a library entry point consumed by the e2e now and the reconciler next task; the `config.LoadDefaultConfig(ctx, config.WithRegion(...))` client-construction pattern applies inside the e2e test; production wiring lands with the reconciler.
8. **E2E test** `provisioner_e2e_test.go`, skipped unless `PILOT_CONSOLE_E2E=1` AND `DATABASE_URL` AND the `PILOT_CONSOLE_FLEET_*` vars AND `PILOT_CONSOLE_SECRETS_KMS_KEY_ID` (+ optionally `_PATH_PREFIX`) are set — document all required vars in the test header. Steps: (1) insert an `organizations` row (FK on `instances.org_id`); (2) **seed tenant secrets** — `w, _ := secrets.NewWriter(ssmClient, kmsKeyID, pathPrefix); w.GenerateGatewayToken(ctx, orgID.String())` — without `/…/{orgID}/PILOT_GATEWAY_TOKEN` the instance's pilot hard-fails config load and ready can never go green; (3) `Provision` → ready green within 5 min; (4) assert instance tags + `HttpTokens=required` via `DescribeInstances`; (5) deprovision. **`t.Cleanup` MUST unconditionally: terminate the instance, wait for the volume to detach, delete the volume, and `w.DeleteAll(ctx, orgID)`** — test resources never outlive the run. Does NOT run in CI (no AWS creds there); CI runs unit + store tests only.
9. `make build`, `make test`, `make lint` green. Conventional-commit PR title.

## Implementation

Suggested file plan (follow existing conventions; deviate only with reason):

- `internal/fleet/provisioner.go` — Provisioner, ec2API interface, ProvisionerConfig, transitions + cleanup
- `internal/fleet/bootstrap.sh.tmpl` + `bootstrap.go` (embed + render + `BootstrapParams`: OrgID, Region, SecretsPath, BinaryS3URI, VolumeIDNoDash)
- `internal/fleet/ready.go` — ReadyChecker + SSM/HTTP impls
- `internal/fleet/store.go` — add `ClaimInstance` + `ErrAlreadyProvisioned`
- `internal/db/migrations/0003_org_active_unique.up.sql` / `.down.sql` (normalize, then index)
- `internal/config/config.go` — `FleetConfig`
- tests alongside, per package

Sequencing inside the PR: migration + ClaimInstance + store-test audit first (self-contained), then bootstrap template + tests, then provisioner, then ready checkers, then config. Do NOT wire an HTTP API for provisioning (no auth story yet — a later workstream owns that).

**Execution constraint: implement fully locally — make NO real AWS calls during this task.** All verification happens against the hand-rolled fakes and Postgres store tests; `make build/test/lint` green on fakes is the completion bar. Do NOT set `PILOT_CONSOLE_E2E=1`, do NOT invoke the AWS CLI/SDK against the real account even if credentials are present — the operator pre-steps are intentionally not in place yet. Deliver the e2e as env-gated code only; the operator runs it later, deliberately.

**Scope fence (out of scope, do not build):** reconciler loop (next task), config render/push + SSM apply-config document (B8), sleep/wake (B7), per-tenant IAM/SG creation (A3), CDK/VPC work, stop/start/upgrade/suspend transitions, any HTTP endpoint, `main.go` changes. Provisioner code never writes SSM secrets (the e2e's seeding via the existing `secrets.Writer` is test setup, not provisioner behavior). Do not add `pilot-canary-sandbox` or any tenant repo to anything — hosted instances must not serve repos the local daemon owns.

**Test-setup facts for the e2e (operator-verified 2026-07-13, embed as test-header comment):** AWS account `529088297614`, region `eu-central-1`, IAM user `aleks` has full CLI access (covers the checker's `ssm:SendCommand`/`GetCommandInvocation`). Existing sandbox stacks provide subnet/SG/instance-profile values (`pilot-network`, `pilot-security-groups`, `iam-pilot-agent`); golden AMI `ami-0bb00da3a38b9c176` (toolchain, NO pilot binary — hence the S3 bridge). The `iam-pilot-agent` role is already SSM-managed via inline SSM-agent permissions (no managed policy needed) but its parameter access is scoped to `/pilot/*` — **operator pre-steps before the first e2e run** (document them in the test header; they are NOT code):
1. Grant the instance role `ssm:GetParameter*` on `arn:aws:ssm:eu-central-1:529088297614:parameter/tenants/*` and `kms:Decrypt` on the key behind `PILOT_CONSOLE_SECRETS_KMS_KEY_ID` (fetch-secrets.sh dies with AccessDenied otherwise, which presents as an opaque ready-timeout).
2. Grant the instance role `s3:GetObject` on the binary key.
3. Upload the CLI release asset: `gh release download v2.240.1 --repo qf-studio/pilot --pattern 'pilot-linux-amd64.tar.gz' && aws s3 cp pilot-linux-amd64.tar.gz s3://<bucket>/pilot/releases/v2.240.1/` (NOT the `Pilot-Desktop-Linux-*` asset).

## Refs

- **Status**: 🚀 Dispatched to Pilot 2026-07-16 · Pilot issue: https://github.com/qf-studio/pilot-console/issues/22 (labels: pilot, no-decompose)
- Parent program: TASK-405 (Pilot Cloud) · S2 plan · fleet design §2 (lifecycle), §9 item 6
- Prior issues in this repo: #11 (B5 store), #12 (A4 secrets writer), #16/#17/#18 (review hardening — expect the same mutation-level review scrutiny on this PR)
- Hard invariants: one tenant = one VM · no inbound to tenant instances except :9090 from console SG · secrets = SecureString + path-scoped IAM + tmpfs env file, never literal in config or user-data · images immutable, upgrades via replacement · data volume is the tenant's state, root is disposable
