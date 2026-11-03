# Learning: Pre-PR shadow review finds the minors but fixes nothing: the fix round needs enforce AND [blocking]; file_minors flipped ON 2026-11-03

## Summary
On PR#6017 (GH-6016) the pre-PR review (shadow, two passes) listed 8 of the 9 minors the post-merge review found, on the same lines, yet nothing was fixed in-run. Three gates: (1) the box runs pre_pr_review.mode shadow, so runReviewFix never starts; (2) even in enforce the fix round takes only [blocking] defects (pre_pr_fix.go:108-116), and the reviewer tagged an unmet issue promise ('non-Anthropic setups behave as today') as [minor]; (3) minors had no automatic path because post_merge_review.file_minors was off. Flipped ON on the box 2026-11-03 14:16Z (config.yaml.bak-fileminors-20261103, graceful restart on 2.309.0, log file_minors=true). Expect one 'fix(review): post-merge minors from PR #N' issue per project per UTC day on the next APPROVE-WITH-NOTES post-merge review; labels pilot+autopilot-fix+post-merge-review+no-decompose+review-minor, so it auto-dispatches (one Pilot run per filed issue). Filter drops minors without path:line or with 'PR body', 'out of scope', 'not verified', 'cannot run', 'residual'.

## Context
Founder 2026-11-03 read the PR#6017 post-merge log and asked why the in-run review did not fix on the fly. Pre-PR body section and post-merge comment compared line by line.

## Details
On PR#6017 (GH-6016) the pre-PR review (shadow, two passes) listed 8 of the 9 minors the post-merge review found, on the same lines, yet nothing was fixed in-run. Three gates: (1) the box runs pre_pr_review.mode shadow, so runReviewFix never starts; (2) even in enforce the fix round takes only [blocking] defects (pre_pr_fix.go:108-116), and the reviewer tagged an unmet issue promise ('non-Anthropic setups behave as today') as [minor]; (3) minors had no automatic path because post_merge_review.file_minors was off. Flipped ON on the box 2026-11-03 14:16Z (config.yaml.bak-fileminors-20261103, graceful restart on 2.309.0, log file_minors=true). Expect one 'fix(review): post-merge minors from PR #N' issue per project per UTC day on the next APPROVE-WITH-NOTES post-merge review; labels pilot+autopilot-fix+post-merge-review+no-decompose+review-minor, so it auto-dispatches (one Pilot run per filed issue). Filter drops minors without path:line or with 'PR body', 'out of scope', 'not verified', 'cannot run', 'residual'.

## Recommended Approach
Before the enforce flip (TASK-620), fix severity calibration: a defect whose evidence cites an issue requirement must be [blocking], else the fix round never fires on spec deviations. Watch the box log for 'post-merge review: minor follow-up filed' and 'daily cap reached'; raise minor_issue_daily_cap only after the first filed issue ships cleanly.

## Related
- TASK-620
- TASK-631
- TASK-633
- `internal/executor/pre_pr_fix.go`
- `internal/autopilot/post_merge_review.go`
- `internal/executor/post_merge_reviewer.go`

---
**Captured**: 2026-11-03
**Confidence**: 95%
**Concepts**: post-merge-review, pre-pr-review, autopilot, reviewer-calibration
