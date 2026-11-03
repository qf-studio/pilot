# Pitfall: GitHub can leave refs/pull/N/merge stale after a push (PR#293 on 2026-11-03: hea

## Summary
GitHub can leave refs/pull/N/merge stale after a push (PR#293 on 2026-11-03: head moved to ec1d01a, merge ref still yesterday's merge of the old head; mergeable UNKNOWN for 45 min; no pull_request run because the workflow runs on the merge ref; Vercel suite queued forever; githubstatus green). An empty commit, the update-branch API and a base-branch toggle (base_ref_changed events recorded) did NOT recompute it. Close + reopen via the API (two PATCH calls, 2 s apart) rebuilt the merge ref and fired CI at once. Autopilot risk: an external close past the grace window is treated as a rejection and deletes the branch — keep a local backup tag of the head and do the two calls back-to-back. Separately, autopilot's 30-min CI-timeout moves the PR to terminal StageFailed (TerminalLabel pilot-failed, no HoldActive) with no revival on later green — the merge is then a hand click.

## Context
Discovered during development.

## Details
GitHub can leave refs/pull/N/merge stale after a push (PR#293 on 2026-11-03: head moved to ec1d01a, merge ref still yesterday's merge of the old head; mergeable UNKNOWN for 45 min; no pull_request run because the workflow runs on the merge ref; Vercel suite queued forever; githubstatus green). An empty commit, the update-branch API and a base-branch toggle (base_ref_changed events recorded) did NOT recompute it. Close + reopen via the API (two PATCH calls, 2 s apart) rebuilt the merge ref and fired CI at once. Autopilot risk: an external close past the grace window is treated as a rejection and deletes the branch — keep a local backup tag of the head and do the two calls back-to-back. Separately, autopilot's 30-min CI-timeout moves the PR to terminal StageFailed (TerminalLabel pilot-failed, no HoldActive) with no revival on later green — the merge is then a hand click.

## Recommended Approach
Apply this knowledge when working on related topics.

## Related
- None documented

---
**Captured**: 2026-11-03
**Confidence**: 90%
**Concepts**: github, ci, autopilot, merge
