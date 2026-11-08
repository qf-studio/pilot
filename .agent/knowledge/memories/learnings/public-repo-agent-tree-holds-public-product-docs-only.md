# Learning: The public pilot repo's .agent/ holds daemon docs only; Pilot Cloud docs live in the private control-plane repo

## Summary
qf-studio/pilot is PUBLIC. On 2026-11-08 a scan found the control-plane fleet design, ~40 console task docs, console memories, session markers, 16 EC2 instance IDs, ~20 SSM parameter names and the AWS account id + operator IAM user name in its tracked .agent/ tree (0 secret values). The pair account id + user name + no MFA was the one real exposure. All Pilot Cloud docs moved to qf-studio/pilot-console/.agent (TASK-646); the account id, IAM user and contractor name joined the private denylist.

## Context
TASK-646, 2026-11-08: founder received recon emails naming 'pilot console' and 'pilotcloud'; inventory of the public tree followed.

## Details
qf-studio/pilot is PUBLIC. On 2026-11-08 a scan found the control-plane fleet design, ~40 console task docs, console memories, session markers, 16 EC2 instance IDs, ~20 SSM parameter names and the AWS account id + operator IAM user name in its tracked .agent/ tree (0 secret values). The pair account id + user name + no MFA was the one real exposure. All Pilot Cloud docs moved to qf-studio/pilot-console/.agent (TASK-646); the account id, IAM user and contractor name joined the private denylist.

## Recommended Approach
Before writing any console/fleet/tenant/Paddle/SaaS doc, memory or marker: start the session in pilot-console (its .agent/ is private). In this repo keep daemon docs only; name hosted features generically. Never write an AWS account id, IAM user name, SSM parameter path or EC2 instance id into a public .agent/ file — the denylist gate (scripts/check-secret-patterns.sh) now refuses the first two. Infra identifiers are not secrets, but the account id + user name pair turns random credential attacks into targeted ones: MFA on every IAM user with a console password.

## Related
- TASK-646

---
**Captured**: 2026-11-08
**Confidence**: 95%
**Concepts**: security, documentation, public-repo, pilot-console, aws, iam
