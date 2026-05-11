---
name: 3-PR-burst auto-release starvation
description: When multiple PRs merge within ~1min of each other, only the first auto-releases; subsequent ones are silently skipped. Root cause is blocking handlePostMergeCI starving the sequential processAllPRs tick loop.
type: project
originSessionId: 3957463d-01d6-42ee-be51-48b04032d57a
---
**Pattern observed:** 2026-05-06 with PRs #2720, #2723, #2725 merging within ~17 min (#2720: 09:43:54, #2723: 09:55:49, #2725: 10:00:15). Only #2720 auto-released as v2.128.3. The other two had to be hand-tagged as v2.128.4.

**Why:** `internal/autopilot/controller.go` runs `processAllPRs` as a sequential for-loop. `handlePostMergeCI` (~line 1487) calls `ciMonitor.WaitForCI` which polls in-loop for up to 30 minutes. While PR-A is in that block, no other PR ticks. By the time the loop resumes, B and C have already merged externally, but their stage machine never advanced through `StageReleasing` because:

1. The docs-sync PR (e.g. #2721) merged in between and bumped `mainSHA`, so HEAD-SHA-based safety checks failed
2. `removePR` was called as part of B/C's stage chain before `handleReleasing` ran
3. The running daemon at the time of the burst was on a binary predating the Gap 1 (releaser init from resolved config) or Gap 2 (non-blocking post-merge CI) fixes

**Why:** Architectural: `handlePostMergeCI` was missed during the earlier refactor that made `handleWaitingCI` non-blocking.

**How to apply:**
- If you see 2+ adjacent merge commits without corresponding tags → suspect this pattern, not a release-config bug
- Mitigation 1: hand-tag the missing release (`git tag vX.Y.Z <sha> && git push origin vX.Y.Z`)
- Mitigation 2 (real fix): GH-2717 / TASK-35 / v2.128.4 ports `handlePostMergeCI` to the same non-blocking pattern as `handleWaitingCI`
- Detection query: `gh run list --workflow=release.yml --limit 10` — gaps in the timeline relative to merge commits indicate skipped releases

**Cross-refs:**
- `bug_telegram_approval_callback_unwired.md` — the 3-PR burst was triggered by the smoke-tests of that resolved bug
- `pattern_hot_upgrade_bootstrap.md` — fixes shipped in burst N can't take effect until daemon hot-upgrades to that binary; first burst after the fix may still exhibit the bug
