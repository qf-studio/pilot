# Release train: first release on a repo with zero tags

## Problem

`scheduleReleaseTick` (`internal/autopilot/scope_schedule.go`) synthesizes
`lastTag = tagPrefix + "0.0.0"` when `GetCurrentVersionForRepo` finds no
release/tags to parse, then calls `CompareCommits(lastTag, branch)`. That
synthesized ref was never created as a real git ref, so GitHub's compare API
404s — every scheduled tick, forever. A repo with `release.trigger:
on_schedule` and zero tags can never cut its first release (GH-4174).

## Root Cause

`GetCurrentVersionForRepo` returning the zero `SemVer{}` is overloaded: it
means both "genuinely no tags exist" and "a lookup error occurred" (the
caller defaults to `SemVer{}` on error too, pre-fix). Neither case has an
actual `v0.0.0` ref to compare against. The bug is structural, not a retry
target — retrying the same 404 forever does nothing.

## Solution

- `repoHasAnyTag` checks ref existence directly (`GetLatestRelease` +
  `ListTags`) instead of inferring it from a zero `SemVer`.
- When no tag exists, `firstReleaseTrainMembers` lists every merged PR via
  `ListPullRequests(state=closed)` (filtered on `MergedAt != ""` — the
  list endpoint doesn't populate the `merged` boolean, only the single-PR
  GET does) and uses that as the definitive member set — no commit-message
  `(#N)` suffix parsing needed, since we already have real PR numbers.
- `trainReleaseCommits` (used later by `handleReleasing` to actually cut the
  tag) has the same no-tag branch: falls back to `scopeReleaseCommits`'
  member-PR commit union instead of `CompareCommits`.
- Version is NOT special-cased: `currentVersion.Bump(bumpType)` in
  `handleReleasing` already produces `v0.1.0`/`v1.0.0`/`v0.0.1` correctly
  from the zero `SemVer` — that logic was never broken, only the commit
  sourcing was.

## Prevention

Any new code path that derives `lastTag` from `GetCurrentVersionForRepo` and
feeds it into `CompareCommits` must first confirm the tag/release actually
exists. A "no prior release" repo is a first-class case, not an edge case —
every fresh `on_schedule` repo starts here.
