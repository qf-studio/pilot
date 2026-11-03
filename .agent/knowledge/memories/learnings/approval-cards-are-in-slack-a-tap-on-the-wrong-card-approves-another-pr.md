# Learning: Autopilot merge-approval requests (resolvedApprovalSource) post Slack cards, not

## Summary
Autopilot merge-approval requests (resolvedApprovalSource) post Slack cards, not Telegram; the daemon log line is 'Slack approval interaction received request_id=pr-N-…'. On 2026-11-02 the founder's first tap landed on the pr-292 card while the ask was pr-476; the wrong PR advanced to merging and the right one stayed in awaiting_approval. When asking for an approval, name the PR number AND the request id prefix (pr-476-…) and say Slack.

## Context
Discovered during development.

## Details
Autopilot merge-approval requests (resolvedApprovalSource) post Slack cards, not Telegram; the daemon log line is 'Slack approval interaction received request_id=pr-N-…'. On 2026-11-02 the founder's first tap landed on the pr-292 card while the ask was pr-476; the wrong PR advanced to merging and the right one stayed in awaiting_approval. When asking for an approval, name the PR number AND the request id prefix (pr-476-…) and say Slack.

## Recommended Approach
Apply this knowledge when working on related topics.

## Related
- None documented

---
**Captured**: 2026-11-03
**Confidence**: 85%
**Concepts**: approval, autopilot, review
