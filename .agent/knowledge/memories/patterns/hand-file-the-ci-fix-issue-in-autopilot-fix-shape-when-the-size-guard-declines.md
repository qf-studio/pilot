# Pattern: When autopilot comments 'CI fix size guard fired: PR has N production additions,

## Summary
When autopilot comments 'CI fix size guard fired: PR has N production additions, over limit 200 … No fix issue will be created', Pilot never retries that PR. Hand-file the fix issue in the autopilot-fix shape and it continues the PR branch: title 'fix(ci): resolve CI failure from PR #<pr>', labels pilot + autopilot-fix + no-decompose, body with Context (PR, issue, SHA, branch), Failed Checks, the exact failing test output, a Diagnosis paragraph, 'Closes #<issue>', and the footer '<!-- autopilot-meta branch:pilot/GH-<n> pr:<pr> iteration:1 source:<n> sha:<full head sha> -->' (feedback_loop.go autopilotMetaFields). Pilot picks it up within a minute, pushes to the PR branch, CI reruns, autopilot merges. Then close the fix issue by hand — borrowed-branch fix issues never self-close — and expect the re-pick pitfall (TASK-632) until #6020 ships. 2026-11-03: #6019 for PR#6017, filed 11:50Z, merged 12:14Z.

## Context
Discovered during development.

## Details
When autopilot comments 'CI fix size guard fired: PR has N production additions, over limit 200 … No fix issue will be created', Pilot never retries that PR. Hand-file the fix issue in the autopilot-fix shape and it continues the PR branch: title 'fix(ci): resolve CI failure from PR #<pr>', labels pilot + autopilot-fix + no-decompose, body with Context (PR, issue, SHA, branch), Failed Checks, the exact failing test output, a Diagnosis paragraph, 'Closes #<issue>', and the footer '<!-- autopilot-meta branch:pilot/GH-<n> pr:<pr> iteration:1 source:<n> sha:<full head sha> -->' (feedback_loop.go autopilotMetaFields). Pilot picks it up within a minute, pushes to the PR branch, CI reruns, autopilot merges. Then close the fix issue by hand — borrowed-branch fix issues never self-close — and expect the re-pick pitfall (TASK-632) until #6020 ships. 2026-11-03: #6019 for PR#6017, filed 11:50Z, merged 12:14Z.

## Recommended Approach
Apply this knowledge when working on related topics.

## Related
- None documented

---
**Captured**: 2026-11-03
**Confidence**: 85%
**Concepts**: autopilot-fix, pilot-issues, ci-gates
