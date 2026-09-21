---
name: cfn-typed-parameter-validates-with-deployer-credentials
description: A CloudFormation parameter typed `AWS::EC2::Image::Id` (or any `AWS::*` / `AWS::SSM::Parameter::Value<>` typed param) is validated by CFN with the DEPLOYING principal's credentials — a least-privilege runner fails at +2 s with `AccessDenied … ec2:DescribeImages` that reads like a runtime bug; fix = plain `String` + `AllowedPattern`, not a wider runner role
type: pitfall
created: 2026-09-21
---

# Typed CFN parameters are validated as the deployer, not the stack

**What happened (2026-09-21, first `pilot-fleet` console deploy).** `console-reconciler.yml`
declared `AmiId: {Type: AWS::EC2::Image::Id}`. `pilot-fleet-runner` (the self-hosted deployer
role, scoped to `pilot-fleet-*`, deliberately no `ec2:*`) ran `cloudformation deploy` and the
stack went `ROLLBACK_IN_PROGRESS` two seconds after `CREATE_IN_PROGRESS`:

```
AccessDenied. User doesn't have permission to call ec2:DescribeImages. Rollback requested by user.
```

No resource had been created, no ECS task started, no log group existed — the failure was
CloudFormation *validating the parameter value* by calling `ec2:DescribeImages` with the
runner's credentials. `describe-stack-events` filtered on `FAILED` showed nothing (the only
event carries `ROLLBACK_IN_PROGRESS`); the raw event list was the tell.

**Why it misleads.** The error names an EC2 action the *template's resources* never call
(the reconciler validates the AMI itself at `RunInstances` with the task role). Two obvious
wrong fixes: widen the runner role (defeats the least-privilege runner) or chase the task
role (never reached).

**How to apply.**

- On a least-privilege deployer, keep parameters `Type: String` with an `AllowedPattern`
  (`^ami-[0-9a-f]{8,17}$`, `^subnet-…`, `^sg-…`, `^vpc-…`). Nelya's fix `552c3c0` is the model;
  the runner kept zero EC2 rights.
- Same class: `AWS::EC2::Subnet::Id`, `AWS::EC2::SecurityGroup::Id`, `AWS::EC2::VPC::Id`,
  `AWS::EC2::KeyPair::KeyName`, `AWS::Route53::HostedZone::Id`, and every
  `AWS::SSM::Parameter::Value<…>` (needs `ssm:GetParameters` as the deployer).
- Diagnosis recipe: a stack that dies at +2 s with no resource events and an `AccessDenied`
  naming a `Describe*` action → typed parameter, not a resource. Read the raw
  `describe-stack-events` output; the status filter hides it.
- The pilot-console `deploy-quantflow-aws.yml` job deletes a `ROLLBACK_COMPLETE` stack before
  redeploying, so the retry is the same inputs — no manual cleanup.

Related: [[fleet-env-doubles-as-environment-tag-value]] (same deploy, same day — both are
"the boundary keys on something the code emits/validates implicitly") ·
`.agent/tasks/TASK-495-fleet-to-be-alignment-nelya.md`
