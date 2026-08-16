---
name: cdk-tenant-role-not-an-instance-role
description: pilot-cloud-infra TenantAccessRole trusts account-root + session tags, NOT ec2.amazonaws.com — wrapping it in an instance profile can never work (burned 3 ship-test provisions); the ONLY in-code instance-role path is pilot-console's TenantResourceFactory
type: pitfall
---

# CDK TenantAccessRole is not an instance role — don't wrap it in an instance profile

**What happened (2026-08-16 reduced ship test, TASK-405):** attempts #1–3
provisioned tenant boxes against a hand-made instance profile wrapping the
CDK `TenantBaseStack` `TenantAccessRole`. Beyond the missing permissions
(no `AmazonSSMManagedInstanceCore`, no `s3:GetObject`), the role's trust
policy is `SessionTagsPrincipal(AccountRootPrincipal())`
(`internal/stacks/tenantbase/tenant_role.go:23-26`) — an EC2 instance
profile cannot assume it at all. It is a session-tagged ABAC role for
parameter access, not a box role.

**The real instance-role path:** pilot-console's `TenantResourceFactory`
(`internal/fleet/tenantres.go` — `ensureRole` + `ensureInstanceProfile`)
creates EC2-trusted per-tenant roles at runtime. There is **no static
instance-profile path in code**; the ship test's "static path" was operator
improvisation. Bootstrap-permission fixes belong in the factory
(console#126), not the CDK role (decision recorded in infra#29).

**Sibling trap in the same class:** the control-plane instance role
(`internal/stacks/controlplane/instance.go:112-128`) also lacked
`s3:GetObject` for its `userdata.sh.tmpl` binary download — any role that
runs an `aws s3 cp` bridge needs the grant explicitly.

**Related:** the configpush origin scrub (`configpush.go:380`) is
deliberate token-hygiene policy guarded by `TestConfigPushScriptTokenHygiene`
— daemon `git push` needs a repo-local **credential helper** reading
`$GITHUB_TOKEN` at use-time, never a tokened remote URL.

**Refs:** console#126 · pilot-cloud-infra#29 · aws-infrastructure-pilot#4 ·
marker `2026-08-16_task405-reduced-ship-test-staged.md`
