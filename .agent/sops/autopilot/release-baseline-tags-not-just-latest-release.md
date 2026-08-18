# Release baseline must be max(latest Release, all tags) — not release OR tags

## Problem

The sdk repo's release train cut PR#120 as **v0.34.2** while tag **v0.35.0**
already existed on the repo. The new commit shipped under a version that Go
module resolution ranks *below* an older commit's version — consumers
pinning/upgrading normally could never reach the fix. An operator had to cut
a corrective out-of-band tag `v0.35.1` (GH-4953, live specimen 2026-08-18
14:01Z).

## Root Cause

`Releaser.GetCurrentVersionForRepo` (`internal/autopilot/releaser.go`) used
"latest Release OR tags" instead of "latest Release AND tags, take the max":

```go
release, err := r.ghClient.GetLatestRelease(ctx, owner, repo)
if release != nil {
    return ParseSemVer(release.TagName)   // early return — tags never checked
}
// tags only consulted when there is NO release at all
```

`v0.35.0` had been pushed as a **tag only** — a base-guard tag, no GitHub
Release object. `GetLatestRelease` (`GET /releases/latest`) only sees
published Release objects, so it returned the older `v0.34.1` release and the
early return skipped the tags list entirely. mem-093 established "read the
baseline live from git tags" as the safety property for exactly this kind of
out-of-band tag — but that property only held on the *no-release-at-all*
path, not when an older Release already existed.

## Solution

`GetCurrentVersionForRepoWithSource` (and `GetCurrentVersion`) now always
fetch **both** the latest Release and the full tag list (paginated
exhaustively, not just the first page) and take
`max(releaseVersion, maxTagVersion)`, regardless of who created the tag or
whether it has a Release object. The winning candidate's source
(`"latest GitHub Release vX.Y.Z"` / `"git tag vX.Y.Z (ahead of latest GitHub
Release vA.B.C)"` / `"git tag vX.Y.Z (no GitHub Release found)"`) is returned
alongside the version and logged on every release decision
(`"creating release"` log line, `baseline_source` field) so a future
mismatch is visible in the logs instead of requiring a live repro to
diagnose.

## Prevention

Any code that computes "the current/latest version" for a repo must treat
git tags as authoritative on their own — never assume a tag implies (or is
implied by) a GitHub Release object. If you add a new version-baseline
lookup, source it from `GetCurrentVersionForRepoWithSource` rather than
re-deriving it from `GetLatestRelease` alone.
