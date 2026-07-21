---
name: require-approval-flip-doesnt-release-held-prs
description: Flipping environments.<env>.require_approval to false only affects PRs that reach handleCIPassed AFTER restart — PRs already persisted at awaiting_approval rehydrate with their old ApprovalRequestID and wait out the 24h approval_timeout default action instead of auto-merging
type: pitfall
---

# require_approval flip doesn't release already-held PRs (2026-07-20)

## What happened

Founder decision 2026-07-20: `stage.require_approval: true → false` (auto-merge
without human approval). Config flipped on box + laptop, daemon restarted on
v2.243.0-7. PR #4485 — escalated to `awaiting_approval` pre-restart with green
CI — did NOT auto-merge after the restart. It sat with zero log activity.

## Root cause

The stage machine only consults `ResolvedEnv().RequireApproval` inside
`handleCIPassed` (controller.go:1761), i.e. at the ci_passed→next transition.
A PR already persisted at `StageAwaitApproval` rehydrates into
`handleAwaitApproval` (controller.go:2154), which has exactly three paths:

1. no `ApprovalRequestID` → submit a new request (re-notifies, still waits),
2. decision recorded → advance,
3. otherwise → wait until `ApprovalRequestedAt + approval_timeout` (24h on
   stage), then apply `default_action`.

#4485 had a persisted `ApprovalRequestID` from before the restart (whose Slack
message was ALSO lost — see [[slack-approval-socket-mode-unroutable]] send-side
loss mode), so it landed on path 3: a silent 24h wait. The config flip is
simply never re-read for held PRs.

## How to apply

- After turning approvals off, sweep for PRs already at `awaiting_approval`
  and merge them manually (`gh pr merge --squash` — squash keeps the ` (#N)`
  suffix that `resolveTrainMemberPRs` needs) or clear their
  `ApprovalRequestID` so path 1 resubmits.
- Symptom signature: PR at approval stage, green CI, no `pr=<N>` log lines
  after restart — it is on path 3, not stuck.
- Possible future fix: `handleAwaitApproval` could short-circuit to merging
  when the escalation reason was env `require_approval` and the resolved env
  no longer requires it (gate escalations must still hold).
- Related: [[hard-cap-rearm-in-memory-gate]] (same class: persisted state
  outliving the config/in-memory view that produced it).
