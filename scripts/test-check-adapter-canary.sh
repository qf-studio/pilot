#!/bin/bash
# Test scripts/check-adapter-canary-tests.sh: it must require a call of
# testutil.AssertNoCanaryLeak, not a mention of it (GH-6268).

set -uo pipefail

cd "$(dirname "$0")/.."
SCRIPT="$PWD/scripts/check-adapter-canary-tests.sh"
failures=0

# expect <name> <want: pass|fail> <test file body>
expect() {
    local name="$1" want="$2" body="$3" dir out status got
    dir=$(mktemp -d)
    mkdir -p "$dir/fake"
    printf 'package fake\n\nimport "net/http"\n\nvar _ = http.NewRequest\n' > "$dir/fake/client.go"
    printf '%s\n' "$body" > "$dir/fake/client_test.go"
    out=$(CANARY_ADAPTERS_DIR="$dir" bash "$SCRIPT" 2>&1)
    status=$?
    rm -rf "$dir"
    got=pass
    [ "$status" -eq 0 ] || got=fail
    if [ "$got" = "$want" ]; then
        echo "ok   $name"
    else
        echo "FAIL $name: want $want, got $got"
        echo "$out" | sed 's/^/     /'
        failures=$((failures + 1))
    fi
}

expect "a call passes" pass 'func TestX(t *testing.T) {
	testutil.AssertNoCanaryLeak(t, testutil.CanaryToken, "text")
}'
expect "a call with a tab-indented comment elsewhere passes" pass '	// see testutil.AssertNoCanaryLeak(t, ...)
func TestX(t *testing.T) {
	testutil.AssertNoCanaryLeak(t, c, "text")
}'
expect "a comment-only file fails" fail '// TODO: call testutil.AssertNoCanaryLeak(t, ...) here
	// testutil.AssertNoCanaryLeak(t, testutil.CanaryToken)'
expect "a mention without a call fails" fail 'var name = "testutil.AssertNoCanaryLeak"
func TestX(t *testing.T) { _ = name }'
expect "a call on another receiver fails" fail 'func TestX(t *testing.T) {
	testutil.AssertNoCanaryLeak(nil, "c", "text")
}'
expect "no mention fails" fail 'func TestX(t *testing.T) {}'

# The real tree passes.
if bash "$SCRIPT" > /dev/null 2>&1; then
    echo "ok   real adapters tree"
else
    echo "FAIL real adapters tree"
    failures=$((failures + 1))
fi

if [ "$failures" -gt 0 ]; then
    echo "$failures failure(s)"
    exit 1
fi
echo "all passed"
