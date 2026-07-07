---
name: honor require_ci uniformly — route the GH-411 hijack and scan-recovery through StagePostMergeCI, do not deprecate or scope the flag to webhook-only
description: GH-3994 subtask 2 decision — of the three options (a) honor require_ci at checkExternalMergeOrClose/ScanRecentlyMergedPRs by routing to StagePostMergeCI, (b) deprecate require_ci, (c) document it as webhook-path-only — (a) was chosen. (b)/(c) both leave the release-before-CI gap live on the most common real-world merge paths.
type: decision
---

Decided 2026-07-07 (GH-3994 subtask 2), following subtask 1's reproduction
(`internal/autopilot/controller_require_ci_repro_test.go`, commit 469e988b)
which pinned the bug: with `release.require_ci: true`, both
`checkExternalMergeOrClose` (GH-411 external-merge hijack, controller.go
~4156-4228) and `ScanRecentlyMergedPRs` (scan-recovery, controller.go
~3621-3860) set `prState.Stage = StageReleasing` directly and never call
`CheckCI(mainSHA)`. Only `handleMerged`'s `SkipPostMergeCI` fast path
(controller.go:1931-1951) actually reads `RequireCI`.

**Decision: option (a) — honor `require_ci` at both sites by routing to
`StagePostMergeCI` instead of short-circuiting to `StageReleasing`.**

**Why not (b) deprecate `require_ci`:** it's a documented, currently
load-bearing release safety gate (`RequireCI bool // waits for post-merge CI
before releasing`, types.go:369-370) that some configs rely on today.
Deprecating it doesn't fix the actual defect (release firing before CI on
main finishes) — it just removes the promise instead of keeping it. Bigger,
unrelated blast radius for zero correctness gain.

**Why not (c) document as webhook-path-only:** this launders a correctness
gap into a "known limitation" instead of fixing it, and it contradicts the
field's own unqualified doc comment. It would also leave the flag protecting
only the *minority* merge path — the GH-411 external-merge hijack and
scan-recovery-after-restart are the common real-world triggers (a human
merging via the GitHub UI, or the daemon restarting mid-flight), not an edge
case. A user who sets `require_ci: true` and reads the field name has no way
to discover it silently doesn't apply to how most of their PRs actually get
merged.

**Why (a) is the correct fix, and cheap:** `SkipPostMergeCI`'s own fast path
already conditions release on `!c.resolvedRelease().RequireCI`
(controller.go:1933) — proving `RequireCI` was always meant as a universal
precondition on release, not a per-merge-path opt-in. The state machine to
honor it already exists and is exercised daily: `handlePostMergeCI`
(controller.go:2184-2288) captures `PostMergeSHA`, starts the CI timer,
polls `CheckCI`, and transitions to `StageReleasing` on success (or
fails/holds/removes on timeout/failure) — exactly the behavior both bug
sites need. Fixing this is "route into the existing stage" rather than
"invent new CI-polling logic," which is why the parent task title already
names the destination (`StagePostMergeCI`).

**Concrete shape for the follow-up implementation subtasks:** at both
`checkExternalMergeOrClose` (~4201, where it currently sets
`prState.Stage = StageReleasing` after `ghPR.MergeCommitSHA` is copied to
`prState.HeadSHA`) and `ScanRecentlyMergedPRs` (~3849, the `PRState{...
Stage: StageReleasing, CIStatus: CISuccess ...}` literal), gate on
`c.releaseConfigured() && c.resolvedRelease().RequireCI`:
- `true` → set `Stage: StagePostMergeCI` (leave `PostMergeSHA` unset so
  `handlePostMergeCI`'s existing first-tick capture runs, or seed it
  directly from the already-known merge commit SHA — equivalent, since
  scan-recovery/external-merge already have it in hand) and drop the
  hardcoded `CIStatus: CISuccess` in the scan path (it's currently a lie
  when CI hasn't been checked).
- `false` (today's default-preserving path) → unchanged, direct
  `StageReleasing` — this is the behavior subtask 4's regression test
  pins (`require_ci: false` externally-merged PR still releases at
  `MergeCommitSHA`, no behavior change).

Subtask 1's repro tests
(`TestCheckExternalMergeOrClose_RequireCITrue_ReleasesWithoutCICheck_GH3994`,
`TestScanRecentlyMergedPRs_RequireCITrue_ReleasesWithoutCICheck_GH3994`) are
expected to flip from pinning-the-bug to failing once subtask 5 lands the
fix — they must be updated in lockstep, not left passing against dead code.

Related: [[pitfall_release_before_ci_polled_merge_paths]] (if filed),
`.agent/tasks/archive/gh-3994-1.md`, `.agent/tasks/gh-3994-2.md`.

**Shipped (GH-3994 subtask 5, 2026-07-07):** both sites now gate on
`c.resolvedRelease().RequireCI` exactly as specified above —
`checkExternalMergeOrClose` (controller.go, GH-411 block) and
`ScanRecentlyMergedPRs` (controller.go, scan-recovery block) route to
`StagePostMergeCI` with `PostMergeSHA` seeded from the already-known merge
commit SHA when `true`, and keep the direct `StageReleasing` short-circuit
(scan path's `CIStatus: CISuccess` included) when `false`. Subtask 1's repro
tests (`internal/autopilot/controller_require_ci_repro_test.go`) were
rewritten in lockstep to pin the fixed behavior instead of the bug; subtask
3's `require_ci: false` regression test
(`controller_gh411_release_trigger_pin_test.go`) passes unmodified.
