---
name: hand-applied-iam-on-cfn-managed-roles-is-a-time-bomb
description: Hand-added inline policies on CloudFormation-managed roles vanish on the next stack deploy — record every grant we need on Nelya's stacks as a template change through her pipeline (pilot-agent-tenant-secrets drift, 07-23 → found 09-23)
type: learning
---

# Hand-applied IAM on CFN-managed roles is a time bomb — everything a stack owns lives in its template

**What happened:** on 2026-07-23 (S2 e2e) we added inline policy `pilot-agent-tenant-secrets` (`ssm:GetParameter*` on `/tenants/*`) to the founder box's `pilot-agent` role by hand, with a marker note "fold into TenantBaseStack later". Never folded. On 2026-09-23 Nelya's drift check found it: the role is CloudFormation-managed, so the next deploy of `pilot-agent-role` would have replaced the policy list and dropped the grant silently. In this case the grant was dead (the box never reads `/tenants/*`; tenants have their own role) and was removed. Had it been live, we would have discovered the loss through a daemon failure.

## Rule (Nelya's, adopted)
Anything we need on her stacks — IAM grants, backup policies, security-group rules — goes into her templates first and is deployed through her pipeline; then any old/hand-made piece goes. Never `put-role-policy` / console edits on a role that a stack owns. If an emergency grant is unavoidable, open the template change the same day and link it from the task log.

## Checks
- `aws iam list-role-policies` vs the template's inline policy names; `aws cloudformation detect-stack-drift` on IAM stacks after any manual change.
- Tenant credentials of terminated tenants: verify the upstream tokens are dead (`gh api user`, Anthropic `/v1/models`) before deleting the parameters — both were already invalid here.
