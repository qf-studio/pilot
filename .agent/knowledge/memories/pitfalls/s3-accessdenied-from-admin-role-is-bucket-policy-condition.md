---
name: s3-accessdenied-from-admin-role-is-bucket-policy-condition
description: S3 PutObject AccessDenied for an AdministratorAccess role = a bucket-policy condition (SSE-KMS/TLS), not IAM — check get-bucket-policy before asking for grants (AMI bake 2026-09-23)
type: pitfall
---

# S3 AccessDenied from an admin role is a bucket-policy condition, not a missing grant

**What happened (2026-09-23):** the golden-AMI bake (`aws-infrastructure-pilot` run 35849443228) failed staging the verified pilot binary: `AccessDenied … s3:PutObject on arn:aws:s3:::pilot-s3-agent-data/binaries/pilot/v2.276.1/…` for `assumed-role/mgmt-infra-admin`. I asked Nelya for an IAM grant. She corrected it: that role is `AdministratorAccess` and `s3:PutObject` simulates as allowed; the deny came from the **bucket policy** (`pilot-s3-agent-data` requires SSE-KMS on every upload) and the `aws s3 cp` I had merged passed no encryption flag. Fix: `--sse aws:kms --sse-kms-key-id alias/pilot` (her commit 7171c35). No IAM change.

## Rule
When an S3 write is denied for a principal that plausibly has the permission (admin role, deployer role), read `aws s3api get-bucket-policy` first and look for `Deny` statements with `s3:x-amz-server-side-encryption`, `aws:SecureTransport`, or `s3:x-amz-server-side-encryption-aws-kms-key-id` conditions. The error text says "not authorized to perform s3:PutObject" for both cases and does not name the policy.

## Also
Nelya's bucket rule is deliberate: every object in `pilot-s3-agent-data` is KMS-encrypted; staging steps that write there must carry the SSE flags, and must stay **hard** steps (a bake from an unstaged binary is unreproducible — her call, adopted).
