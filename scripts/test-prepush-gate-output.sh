#!/bin/bash
# Test harness for the failure output of run_check() in scripts/pre-push-gate.sh
# (GH-6093).
#
# run_check() is extracted from the gate script (the gate itself is never run)
# and evaluated together with scripts/pre-push-gate-summary.sh. Covers: the
# failing lines surfacing from behind `ok` filler, DATA RACE and panic lines,
# the tail -40 fallback, the full log under /tmp/pilot-gate/, and the quiet
# passing path.
#
# Usage: ./scripts/test-prepush-gate-output.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GATE_SCRIPT="$SCRIPT_DIR/pre-push-gate.sh"

# shellcheck source=scripts/pre-push-gate-summary.sh
source "$SCRIPT_DIR/pre-push-gate-summary.sh"

# Colors referenced by run_check; empty so output is plain text.
RED=''; GREEN=''; YELLOW=''; BLUE=''; NC=''
eval "$(sed -n '/^run_check() {/,/^}/p' "$GATE_SCRIPT")"

if ! declare -F run_check >/dev/null; then
    echo "FATAL: could not extract run_check from $GATE_SCRIPT"
    exit 1
fi

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

# Runs run_check with a unique check name; sets OUT and RC.
run_case() {
    local name="$1"
    local cmd="$2"
    set +e
    OUT="$(run_check "$name" "$cmd")"
    RC=$?
    set -e
}

assert_contains() {
    local desc="$1" needle="$2"
    if grep -qxF -- "$needle" <<<"$OUT"; then
        pass "$desc"
    else
        fail_test "$desc (line not printed: $needle)"
    fi
}

assert_absent() {
    local desc="$1" needle="$2"
    if grep -qxF -- "$needle" <<<"$OUT"; then
        fail_test "$desc (line unexpectedly printed: $needle)"
    else
        pass "$desc"
    fi
}

TAG="t$$"

echo "Case 1: failure behind 30 ok lines"
run_case "gate-out-1-$TAG" 'for i in $(seq 1 30); do echo "ok  	pkg/p$i	0.1s"; done; echo "--- FAIL: TestX (0.00s)"; echo "FAIL"; exit 1'
[ "$RC" -eq 1 ] && pass "returns 1" || fail_test "returns 1 (got $RC)"
assert_contains "--- FAIL line printed" "--- FAIL: TestX (0.00s)"
assert_contains "FAIL line printed" "FAIL"
assert_absent "ok filler not printed" "ok  	pkg/p1	0.1s"
assert_absent "late ok filler not printed" "ok  	pkg/p30	0.1s"

echo "Case 2: DATA RACE"
run_case "gate-out-2-$TAG" 'for i in $(seq 1 30); do echo "ok  	pkg/p$i	0.1s"; done; echo "WARNING: DATA RACE"; echo "Write at 0x00c000 by goroutine 8:"; exit 1'
assert_contains "DATA RACE line printed" "WARNING: DATA RACE"
assert_absent "ok filler not printed" "ok  	pkg/p1	0.1s"

echo "Case 3: panic"
run_case "gate-out-3-$TAG" 'for i in $(seq 1 30); do echo "ok  	pkg/p$i	0.1s"; done; echo "panic: runtime error: index out of range"; exit 1'
assert_contains "panic line printed" "panic: runtime error: index out of range"
assert_absent "ok filler not printed" "ok  	pkg/p1	0.1s"

echo "Case 4: no match falls back to the last 40 lines"
run_case "gate-out-4-$TAG" 'for i in $(seq 1 60); do echo "line $i"; done; exit 1'
assert_absent "line 1 not printed" "line 1"
assert_absent "line 20 not printed" "line 20"
assert_contains "line 21 printed" "line 21"
assert_contains "line 60 printed" "line 60"
COUNT="$(printf '%s\n' "$OUT" | grep -cE '^line [0-9]+$')"
[ "$COUNT" -eq 40 ] && pass "exactly 40 lines printed" || fail_test "exactly 40 lines printed (got $COUNT)"

echo "Case 5: full log under /tmp/pilot-gate/"
NAME="Gate Out 5 $TAG"
run_case "$NAME" 'for i in $(seq 1 30); do echo "ok  	pkg/p$i	0.1s"; done; echo "--- FAIL: TestX"; echo "FAIL"; exit 1'
LOG="$(printf '%s\n' "$OUT" | grep -E "^/tmp/pilot-gate/gate-out-5-$TAG-.*\.log$" || true)"
if [ -n "$LOG" ] && [ -f "$LOG" ]; then
    pass "log path printed on its own line and file exists ($LOG)"
else
    fail_test "log path printed and file exists (got: '$LOG')"
fi
if [ -n "$LOG" ] && grep -qxF "ok  	pkg/p1	0.1s" "$LOG" && grep -qxF "FAIL" "$LOG" && [ "$(wc -l < "$LOG")" -eq 32 ]; then
    pass "log holds the full output (32 lines)"
else
    fail_test "log holds the full output"
fi
rm -f /tmp/pilot-gate/gate-out-?-"$TAG"-*.log

echo "Case 6: passing command"
run_case "gate-out-6-$TAG" 'echo "ok  	pkg/p1	0.1s"'
[ "$RC" -eq 0 ] && pass "returns 0" || fail_test "returns 0 (got $RC)"
if grep -q '/tmp/pilot-gate' <<<"$OUT"; then
    fail_test "no log path printed"
else
    pass "no log path printed"
fi
if ls /tmp/pilot-gate/gate-out-6-"$TAG"-*.log >/dev/null 2>&1; then
    fail_test "no log file written"
else
    pass "no log file written"
fi

echo ""
echo "Results: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
