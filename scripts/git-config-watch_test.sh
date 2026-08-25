#!/bin/bash
# Plain-sh test suite for git-config-watch.sh (GH-5218) — no bats
# dependency, same style as scripts/box/pilot-backup-s3_test.sh.
#
# Exercises the single-instance lock guard only: acquire, refuse-and-name a
# live second launch, release-on-exit, and stale-lock reclaim. Does NOT
# exercise the core.bare flip/heal path — that would mutate this repo's
# real git config (the very thing GH-5063 exists to catch), and the flip
# path already has a documented manual verification recipe in the script's
# own header.
#
# Usage: ./scripts/git-config-watch_test.sh

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WATCH_SCRIPT="${SCRIPT_DIR}/git-config-watch.sh"

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

setup_sandbox() {
    SANDBOX="$(mktemp -d)"
    LOG_FILE="${SANDBOX}/watch.log"
    LOCK_FILE="${SANDBOX}/watch.lock"
}

# Best-effort hard timeout so a bug that turns a should-refuse-immediately
# invocation into a runaway (never-exiting) watcher can't hang the test
# suite/CI — e.g. if GCW_LOCK_FILE isn't threaded through correctly.
run_with_timeout() {
    if command -v timeout >/dev/null 2>&1; then
        timeout 10 "$@"
    else
        "$@"
    fi
}

teardown_sandbox() {
    # Best-effort: kill anything we backgrounded that's still alive.
    if [ -n "${BG_PID:-}" ] && kill -0 "$BG_PID" 2>/dev/null; then
        kill "$BG_PID" 2>/dev/null
        wait "$BG_PID" 2>/dev/null
    fi
    BG_PID=""
    rm -rf "$SANDBOX"
}

# Starts the watcher in the background with a long poll interval (the lock
# is acquired before the first poll, so tests don't need to wait out
# GCW_POLL_INTERVAL) and waits for it to report it's watching.
start_watcher_bg() {
    GCW_LOG_FILE="$LOG_FILE" GCW_LOCK_FILE="$LOCK_FILE" GCW_POLL_INTERVAL=60 \
        "$WATCH_SCRIPT" >"${SANDBOX}/bg.log" 2>&1 &
    BG_PID=$!
    for _ in 1 2 3 4 5 6 7 8 9 10; do
        if grep -q "watching" "${SANDBOX}/bg.log" 2>/dev/null; then
            return 0
        fi
        sleep 0.3
    done
    return 1
}

echo "=== test: first launch acquires the lock and starts watching ==="
setup_sandbox
if start_watcher_bg; then
    pass "background instance reports it is watching"
else
    cat "${SANDBOX}/bg.log"
    fail_test "background instance never reported watching"
fi
if [ -f "$LOCK_FILE" ] && [ "$(cat "$LOCK_FILE")" = "$BG_PID" ]; then
    pass "lock file contains the running instance's pid"
else
    fail_test "lock file missing or pid mismatch"
fi
teardown_sandbox

echo "=== test: second concurrent launch is refused and names the running pid ==="
setup_sandbox
start_watcher_bg || { cat "${SANDBOX}/bg.log"; fail_test "setup: background instance failed to start"; teardown_sandbox; }
FIRST_PID="$BG_PID"
if GCW_LOG_FILE="$LOG_FILE" GCW_LOCK_FILE="$LOCK_FILE" GCW_POLL_INTERVAL=60 \
    run_with_timeout "$WATCH_SCRIPT" >"${SANDBOX}/second.log" 2>&1; then
    fail_test "second concurrent launch should have exited non-zero"
else
    pass "second concurrent launch exits non-zero"
fi
if grep -q "already running as pid ${FIRST_PID}" "${SANDBOX}/second.log"; then
    pass "refusal message names the running instance's pid"
else
    cat "${SANDBOX}/second.log"
    fail_test "refusal message does not name the running instance"
fi
teardown_sandbox

echo "=== test: lock is released once the running instance exits ==="
setup_sandbox
start_watcher_bg || { cat "${SANDBOX}/bg.log"; fail_test "setup: background instance failed to start"; teardown_sandbox; }
kill "$BG_PID" 2>/dev/null
wait "$BG_PID" 2>/dev/null
RELEASED=0
for _ in 1 2 3 4 5 6 7 8 9 10; do
    if [ ! -f "$LOCK_FILE" ]; then
        RELEASED=1
        break
    fi
    sleep 0.3
done
if [ "$RELEASED" -eq 1 ]; then
    pass "lock file removed after the owning instance exits"
else
    fail_test "lock file still present after the owning instance exited"
fi
BG_PID=""
teardown_sandbox

echo "=== test: a lock left by a dead pid (stale) is reclaimed, not refused ==="
setup_sandbox
# 999999999 is out of any real pid range on Linux/macOS — kill -0 on it
# fails, so the script must treat this lock as stale and reclaim it.
echo "999999999" > "$LOCK_FILE"
if start_watcher_bg; then
    pass "instance starts despite a stale lock file"
else
    cat "${SANDBOX}/bg.log"
    fail_test "instance refused to start against a stale lock"
fi
if grep -q "stale lock" "${SANDBOX}/bg.log"; then
    pass "stale-lock reclaim is logged"
else
    fail_test "no stale-lock reclaim message logged"
fi
if [ -f "$LOCK_FILE" ] && [ "$(cat "$LOCK_FILE")" = "$BG_PID" ]; then
    pass "lock file now contains the new instance's pid"
else
    fail_test "lock file not reclaimed with the new instance's pid"
fi
teardown_sandbox

echo ""
echo "=== summary: ${PASS} passed, ${FAIL} failed ==="
if [ "$FAIL" -gt 0 ]; then
    exit 1
fi
exit 0
