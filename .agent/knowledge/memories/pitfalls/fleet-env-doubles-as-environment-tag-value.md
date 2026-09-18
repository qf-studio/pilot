---
name: fleet-env-doubles-as-environment-tag-value
description: PILOT_CONSOLE_FLEET_ENV is both the fleet env name and the `Environment` cost-allocation tag value on every tenant resource (GH-261); Nelya's pilot-fleet task boundary keys RunInstances/Terminate/SendCommand on `Environment = pilot-fleet`, so the env name `prod` we put in the 09-07 manifest is a hard IAM deny at first provision
type: pitfall
created: 2026-09-18
---

# `PILOT_CONSOLE_FLEET_ENV` is the `Environment` tag value — infra boundaries key on it

**Found 2026-09-18** while checking the 09-07 ECS manifest against the deployed
`pilot-fleet-task-boundary` (Nelya's `stacks/pilot-fleet/iam.yml`, live since 09-07).

- pilot-console writes the `Environment` tag on every tenant instance, volume, SG, role and
  instance profile from `PILOT_CONSOLE_FLEET_ENV` **raw** (`internal/fleet/tenantres.go:117`
  `EnvironmentTag`, `config.go:1271 EnvironmentTag: rawEnv`; default `fleet`). GH-261 made the
  env name and the cost-allocation tag the same variable.
- Her boundary conditions three statement groups on that tag, value fixed to the VPC name
  `pilot-fleet` (`!ImportValue ${NetworkStack}-VpcName`):
  `Ec2RunTaggedInEnvironment` (`aws:RequestTag/Environment`, StringEqualsIfExists — the tag is
  always present in our TagSpecifications, so it must match) ·
  `Ec2ManageEnvironmentResources` (`aws:ResourceTag/Environment`, StringEquals) ·
  `SsmRunCommandOnTenants` (`ssm:resourceTag/Environment`).
- Our manifest told her `PILOT_CONSOLE_FLEET_ENV=prod`. Result: first `RunInstances` denied;
  anything ever tagged `prod` could never be terminated/stopped by the reconciler.

**How to apply**

- Interim: the task env must carry `PILOT_CONSOLE_FLEET_ENV=pilot-fleet` (sent to Nelya
  2026-09-18 in the TO-BE thread). Verify nothing else keys on the `prod` literal before relying
  on it.
- Proper fix (unfiled as of 2026-09-18): a separate `PILOT_CONSOLE_FLEET_ENVIRONMENT_TAG` so
  the infra owner sets the tag value independently of env semantics.
- Reviewing any infra permissions boundary against pilot-console: grep the console for every
  tag key the boundary conditions on (`Environment`, `pilot:org_id`, `pilot:env`) and confirm
  the *value* source, not just that the key is emitted. Same class as [[doc-vs-wire]]: the
  producer's value wins.

Related: [[pilot-console-tenant-role-abac-tag-key-mismatch]] (not yet written — TenantAccessRole
uses `tenant_id`, TenantBoundary uses `pilot:org_id`; only the latter is emitted by the factory) ·
`.agent/tasks/TASK-495-fleet-to-be-alignment-nelya.md`
