#!/bin/bash
# Test harness for scripts/pre-push-classify.sh (GH-4771).
#
# Exercises the docs-only fast-path classifier against a disposable fixture
# git repo — never touches the real repo's history or hooks. Covers:
# docs-only, code-only, mixed (docs+code across two pushed refs), null-OID
# remote (new branch), and ref-deletion pushes, plus the "no stdin" edge case.
#
# Usage: ./scripts/test-prepush-fastpath.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CLASSIFY_SCRIPT="$SCRIPT_DIR/pre-push-classify.sh"
NULL_SHA="0000000000000000000000000000000000000000"

PASS=0
FAIL=0

pass() {
    echo "  PASS: $1"
    PASS=$((PASS + 1))
}

fail_test() {
    echo "  FAIL: $1"
    FAIL=$((FAIL + 1))
}

# Runs the classifier against fake stdin (all args after the fixture repo
# path are ref-update lines) and asserts stdout equals $expected.
assert_classification() {
    local desc="$1"
    local expected="$2"
    local repo="$3"
    shift 3
    local stdin_lines
    stdin_lines="$(printf '%s\n' "$@")"

    local actual
    actual="$(cd "$repo" && printf '%s\n' "$stdin_lines" | "$CLASSIFY_SCRIPT" 2>/dev/null)"

    if [ "$actual" = "$expected" ]; then
        pass "$desc (got: $actual)"
    else
        fail_test "$desc (expected: $expected, got: $actual)"
    fi
}

setup_fixture() {
    FIXTURE="$(mktemp -d)"
    (
        cd "$FIXTURE"
        git init -q
        git config user.email "test@pilot.dev"
        git config user.name "Pilot Fixture"

        echo "# fixture" > README.md
        git add README.md
        git commit -qm "initial"
        BASE_SHA="$(git rev-parse HEAD)"
        echo "$BASE_SHA" > .base_sha

        echo "more docs" >> README.md
        git add README.md
        git commit -qm "docs change"
        git rev-parse HEAD > .docs_sha

        mkdir -p pkg
        echo "package pkg" > pkg/foo.go
        git add pkg/foo.go
        git commit -qm "code change"
        git rev-parse HEAD > .code_sha

        echo "go.sum entry" > go.sum
        git add go.sum
        git commit -qm "dependency bump"
        git rev-parse HEAD > .gosum_sha
    )
}

teardown_fixture() {
    rm -rf "$FIXTURE"
}

setup_fixture
BASE_SHA="$(cat "$FIXTURE/.base_sha")"
DOCS_SHA="$(cat "$FIXTURE/.docs_sha")"
CODE_SHA="$(cat "$FIXTURE/.code_sha")"
GOSUM_SHA="$(cat "$FIXTURE/.gosum_sha")"

echo "=== test: docs-only push -> docs-only ==="
assert_classification "docs-only diff classifies as docs-only" "docs-only" "$FIXTURE" \
    "refs/heads/main $DOCS_SHA refs/heads/main $BASE_SHA"

echo "=== test: code-only push (.go file) -> full ==="
assert_classification "*.go diff classifies as full" "full" "$FIXTURE" \
    "refs/heads/main $CODE_SHA refs/heads/main $DOCS_SHA"

echo "=== test: go.sum-only push -> full ==="
assert_classification "go.sum diff classifies as full" "full" "$FIXTURE" \
    "refs/heads/main $GOSUM_SHA refs/heads/main $CODE_SHA"

echo "=== test: mixed push across two refs (one docs, one code) -> full ==="
assert_classification "union across refs classifies as full when any ref has code" "full" "$FIXTURE" \
    "refs/heads/docs-branch $DOCS_SHA refs/heads/docs-branch $BASE_SHA" \
    "refs/heads/code-branch $CODE_SHA refs/heads/code-branch $DOCS_SHA"

echo "=== test: mixed push across two refs, both docs-only -> docs-only ==="
assert_classification "union across refs stays docs-only when neither ref has code" "docs-only" "$FIXTURE" \
    "refs/heads/docs-branch-a $DOCS_SHA refs/heads/docs-branch-a $BASE_SHA" \
    "refs/heads/docs-branch-b $DOCS_SHA refs/heads/docs-branch-b $BASE_SHA"

echo "=== test: null-OID remote (new branch push) -> full ==="
assert_classification "new branch (null remote OID) classifies as full" "full" "$FIXTURE" \
    "refs/heads/new-branch $DOCS_SHA refs/heads/new-branch $NULL_SHA"

echo "=== test: ref deletion (null-OID local) -> full ==="
assert_classification "ref deletion (null local OID) classifies as full" "full" "$FIXTURE" \
    "(delete) $NULL_SHA refs/heads/main $BASE_SHA"

echo "=== test: no stdin at all -> full ==="
actual="$(cd "$FIXTURE" && printf '' | "$CLASSIFY_SCRIPT" 2>/dev/null)"
if [ "$actual" = "full" ]; then
    pass "empty stdin classifies as full (got: $actual)"
else
    fail_test "empty stdin classifies as full (expected: full, got: $actual)"
fi

teardown_fixture

echo ""
echo "=== summary: ${PASS} passed, ${FAIL} failed ==="
if [ "$FAIL" -gt 0 ]; then
    exit 1
fi
exit 0
