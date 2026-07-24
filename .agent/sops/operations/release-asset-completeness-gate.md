# Release pipeline: asset completeness gate

## Problem

GH-4523: v2.245.1's GitHub release was reported missing both
`pilot-linux-{amd64,arm64}.tar.gz` — the assets the S2 hosted-instance
bootstrap (pilot-console S3 bridge) and the AWS box self-upgrade both
depend on. Nothing in `.github/workflows/release.yml` verified the
published release actually carried the full asset set, so an incomplete
release could ship silently — the first signal would be a downstream
consumer failing (as it did: today's S2 e2e had to fall back to v2.245.0).

## Root Cause

Investigated against ground truth (workflow run `30014298612`,
`2026-07-23T14:06:55Z`, and `gh release view v2.245.1`): the goreleaser
job for v2.245.1 actually built and uploaded both linux archives at
`14:09:16Z`, and they are present in the release right now. The reported
symptom did not reproduce from the pipeline's own logs or the current
release state — most likely the observation was made during the ~90s
asset-upload window, or hit a transient GitHub API/UI propagation delay.
Regardless of the exact trigger, the pipeline had **no automated check**
that would have caught a genuinely incomplete release, so the class of
bug (partial goreleaser matrix, upload failure swallowed, API lag) was
undetectable until a hosted-tenant deploy broke.

## Solution

- `scripts/verify-release-assets.sh <tag>` checks that every asset named
  in `.goreleaser.yaml`'s build/archive matrix
  (`pilot-{linux,darwin}-{amd64,arm64}.tar.gz`, `pilot-windows-amd64.zip`,
  `checksums.txt`) exists on the tag's GitHub release, retrying up to 5
  times with a 5s backoff to absorb listing propagation delay.
- Wired into `.github/workflows/release.yml` as a "Verify release assets"
  step immediately after the GoReleaser step — fails the job (and the
  release train) loudly if anything is missing.
- Desktop bundles (`Pilot-Desktop-*`, built by the separate
  `release-desktop.yml` workflow) are intentionally NOT included in this
  gate: that workflow can finish well after `release.yml` (three-runner
  matrix + Wails build), so checking for them here would produce false
  failures on every release.

## Prevention

If `.goreleaser.yaml`'s `builds`/`archives`/`ignore` blocks change (new
GOOS/GOARCH, renamed archive template, etc.), update the
`EXPECTED_ASSETS` list in `scripts/verify-release-assets.sh` in the same
PR — the two are not otherwise linked and will silently drift apart
(the gate would then either false-fail on a legitimately new matrix, or
miss a genuinely dropped target).
