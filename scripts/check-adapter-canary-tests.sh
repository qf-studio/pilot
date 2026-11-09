#!/bin/bash
# Require a credential-leak (canary) test in every adapter package that owns
# an HTTP client.
#
# Incident: GH-6244 — the Telegram bot token sat in the URL path and leaked
# through every *url.Error into daemon.log for a month. Only Telegram got a
# leak test. The other adapters send the credential in a header, which
# *url.Error does not print, but a body echo, a %v of a request or a header
# dump in an error would still leak it, and nothing would catch that.
#
# For every directory under internal/adapters/ whose non-test Go files
# contain http.Client or http.NewRequest, a _test.go file in the same
# directory must call testutil.AssertNoCanaryLeak (internal/testutil/canary.go).
#
# Used by CI (.github/workflows/ci.yml) and by `make check-adapter-canary`.

set -euo pipefail

cd "$(dirname "$0")/.."

ADAPTERS_DIR="internal/adapters"
missing=()
checked=0

while IFS= read -r dir; do
    has_client=0
    while IFS= read -r file; do
        case "$file" in *_test.go) continue ;; esac
        if grep -qE 'http\.Client|http\.NewRequest' "$file"; then
            has_client=1
            break
        fi
    done < <(find "$dir" -maxdepth 1 -name '*.go' -type f)

    [ "$has_client" -eq 1 ] || continue
    checked=$((checked + 1))

    if ! grep -qlE 'testutil\.AssertNoCanaryLeak\(' "$dir"/*_test.go 2>/dev/null; then
        missing+=("$dir")
    fi
done < <(find "$ADAPTERS_DIR" -type d | sort)

if [ "${#missing[@]}" -gt 0 ]; then
    echo "Adapter packages with an HTTP client but no canary leak test:"
    for dir in "${missing[@]}"; do
        echo "  $dir"
    done
    echo ""
    echo "Add a _test.go that builds the client with testutil.CanaryToken, drives it"
    echo "through testutil.FailingTransport() and testutil.EchoServer(), and calls"
    echo "testutil.AssertNoCanaryLeak. See internal/adapters/github/client_canary_test.go."
    exit 1
fi

echo "OK: all $checked adapter packages with an HTTP client have a canary leak test"
