#!/bin/bash
# Verify a GitHub release carries the full expected asset set.
#
# Context (GH-4523): v2.245.1's release page was observed missing both
# pilot-linux-{amd64,arm64}.tar.gz — the assets the S2 hosted-instance
# bootstrap (pilot-console S3 bridge) and the AWS box self-upgrade both
# depend on. Root-cause investigation against the actual workflow run
# (goreleaser run 30014298612, 2026-07-23T14:06:55Z) showed the linux
# archives WERE built and uploaded (14:09:16Z) and are present in the
# release right now — the goreleaser job itself did not skip the linux
# matrix. The pipeline had no automated check confirming asset
# completeness though, so a transient upload failure or GitHub API
# propagation delay could silently ship an incomplete release with no
# signal until a downstream consumer (S2 e2e) broke. This script closes
# that gap: it runs immediately after the goreleaser step and fails the
# release train loudly if any expected asset is missing.
#
# Usage: scripts/verify-release-assets.sh <tag>
#   GITHUB_TOKEN / GH_TOKEN must be set (gh CLI auth) when run in CI.

set -euo pipefail

TAG="${1:-}"
if [ -z "$TAG" ]; then
    echo "usage: $0 <tag>" >&2
    exit 2
fi

# Expected assets for every release train run. Keep in sync with the
# builds/archives/ignore blocks in .goreleaser.yaml — if that matrix
# changes, update this list too.
EXPECTED_ASSETS=(
    "pilot-linux-amd64.tar.gz"
    "pilot-linux-arm64.tar.gz"
    "pilot-darwin-amd64.tar.gz"
    "pilot-darwin-arm64.tar.gz"
    "pilot-windows-amd64.zip"
    "checksums.txt"
)

# GitHub's asset listing can lag a few seconds behind the final upload in
# a goreleaser run, so retry with backoff before failing the train.
MAX_ATTEMPTS=5
SLEEP_SECONDS=5

echo "Verifying release assets for tag ${TAG}..."

ATTEMPT=1
ACTUAL_ASSETS=""
while [ "$ATTEMPT" -le "$MAX_ATTEMPTS" ]; do
    ACTUAL_ASSETS=$(gh release view "$TAG" --json assets -q '.assets[].name' 2>/dev/null || true)
    MISSING=()
    for asset in "${EXPECTED_ASSETS[@]}"; do
        if ! grep -qFx "$asset" <<<"$ACTUAL_ASSETS"; then
            MISSING+=("$asset")
        fi
    done

    if [ "${#MISSING[@]}" -eq 0 ]; then
        echo "✓ All expected assets present for ${TAG}:"
        printf '  - %s\n' "${EXPECTED_ASSETS[@]}"
        exit 0
    fi

    if [ "$ATTEMPT" -lt "$MAX_ATTEMPTS" ]; then
        echo "Attempt ${ATTEMPT}/${MAX_ATTEMPTS}: missing ${#MISSING[@]} asset(s), retrying in ${SLEEP_SECONDS}s..."
        sleep "$SLEEP_SECONDS"
    fi
    ATTEMPT=$((ATTEMPT + 1))
done

echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "RELEASE ASSET COMPLETENESS CHECK FAILED for ${TAG}"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""
echo "Missing asset(s):"
printf '  - %s\n' "${MISSING[@]}"
echo ""
echo "Present asset(s):"
if [ -n "$ACTUAL_ASSETS" ]; then
    printf '  - %s\n' $ACTUAL_ASSETS
else
    echo "  (none — could not list release assets)"
fi
echo ""
echo "This release is un-deployable to hosted tenants (GH-4523: the S2"
echo "hosted-instance bootstrap and AWS box self-upgrade both require the"
echo "linux tarballs). Investigate the goreleaser run for ${TAG} before"
echo "shipping this tag anywhere."
echo ""
exit 1
