# Decision: Pilot never reads GH comments by design — review feedback enters as issues only

## Summary
Founder-confirmed design decision (2026-08-30): Pilot/autopilot intentionally does NOT read GitHub comments. The revision loop keys ONLY on formal changes_requested reviews (controller.go hasChangesRequested), and since founder PRs are same-account, formal reviews are impossible — so founder REQUEST-CHANGES is delivered by manually filing a pilot-labeled revision issue (with autopilot-meta footer: branch/pr/iteration) that tells Pilot to update the existing branch/PR. Do NOT propose comment-parsing triggers; any future automation must be label- or issue-based.

## Context
Discovered during TASK-489 (receipts digest) review: comment-verdict on PR#5258 was invisible to autopilot; founder corrected the proposed comment-parsing fix with 'Pilot doesn't read GH comments by design'. Manual revision issue #5261 is the designed channel.

## Details
Founder-confirmed design decision (2026-08-30): Pilot/autopilot intentionally does NOT read GitHub comments. The revision loop keys ONLY on formal changes_requested reviews (controller.go hasChangesRequested), and since founder PRs are same-account, formal reviews are impossible — so founder REQUEST-CHANGES is delivered by manually filing a pilot-labeled revision issue (with autopilot-meta footer: branch/pr/iteration) that tells Pilot to update the existing branch/PR. Do NOT propose comment-parsing triggers; any future automation must be label- or issue-based.

## Recommended Approach
To request changes on a Pilot PR: post verdict comment for the human record, convert PR to draft to block auto-merge, then file a pilot-labeled revision issue instructing work on the existing branch, ending with '<!-- autopilot-meta branch:B pr:N iteration:K -->'.

## Related
- TASK-489
- `internal/autopilot/controller.go`
- `internal/autopilot/feedback_loop.go`

---
**Captured**: 2026-08-30
**Confidence**: 95%
**Concepts**: autopilot, review, feedback-loop, workflow-discipline, dispatch
