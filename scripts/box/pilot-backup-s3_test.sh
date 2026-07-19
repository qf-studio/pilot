#!/bin/bash
# Plain-sh test suite for pilot-backup-s3.sh — no bats dependency, runs
# anywhere bash + sqlite3 + tar are available.
#
# Exercises: VACUUM INTO snapshot, tar layout, mocked S3 upload asserting
# --sse aws:kms and the backups/YYYY/MM/DD/ key layout, head-object
# verification, and non-zero exit on failure paths. Uses a PATH-shimmed mock
# `aws` (scripts/box/testdata/mock-aws.sh) — never talks to real AWS.
#
# Usage: ./scripts/box/pilot-backup-s3_test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BACKUP_SCRIPT="${SCRIPT_DIR}/pilot-backup-s3.sh"
MOCK_AWS="${SCRIPT_DIR}/testdata/mock-aws.sh"

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
    DATA_DIR="${SANDBOX}/data"
    STUB_BIN="${SANDBOX}/bin"
    CAPTURE_DIR="${SANDBOX}/captured"
    CALL_LOG="${SANDBOX}/aws-calls.log"

    mkdir -p "$DATA_DIR" "$STUB_BIN"
    : > "$CALL_LOG"

    cp "$MOCK_AWS" "${STUB_BIN}/aws"
    chmod +x "${STUB_BIN}/aws"

    sqlite3 "${DATA_DIR}/pilot.db" \
        "CREATE TABLE executions (id INTEGER PRIMARY KEY, status TEXT); INSERT INTO executions (status) VALUES ('done');"
    echo '{"patterns": []}' > "${DATA_DIR}/knowledge.json"
    echo '{"global": []}' > "${DATA_DIR}/global_patterns.json"
}

teardown_sandbox() {
    rm -rf "$SANDBOX"
}

# Runs the backup script against the current sandbox. Extra args are passed
# through to `env` as additional VAR=value assignments (e.g. MOCK_AWS_FAIL_CP=1).
run_backup() {
    local extra=("$@")
    env \
        PATH="${STUB_BIN}:${PATH}" \
        PILOT_DATA_DIR="$DATA_DIR" \
        BACKUP_BUCKET="test-bucket" \
        BACKUP_PREFIX="backups" \
        MOCK_AWS_CALL_LOG="$CALL_LOG" \
        MOCK_AWS_CAPTURE_DIR="$CAPTURE_DIR" \
        "${extra[@]}" \
        "$BACKUP_SCRIPT"
}

echo "=== test: happy path — snapshot, tar layout, sse-kms, head-object verify ==="
setup_sandbox
if run_backup >"${SANDBOX}/stdout.log" 2>&1; then
    pass "script exits 0 on success"
else
    cat "${SANDBOX}/stdout.log"
    fail_test "script exited non-zero on success path"
fi

if grep -q -- "--sse aws:kms" "$CALL_LOG"; then
    pass "s3 cp invoked with --sse aws:kms"
else
    fail_test "missing --sse aws:kms in aws s3 cp invocation"
fi

EXPECTED_DATE_PREFIX="$(date -u +%Y/%m/%d)"
EXPECTED_DATE_STAMP="$(date -u +%Y%m%d)"
if grep -q "s3://test-bucket/backups/${EXPECTED_DATE_PREFIX}/pilot-backup-" "$CALL_LOG"; then
    pass "s3 key uses date-partitioned backups/YYYY/MM/DD/ layout"
else
    fail_test "s3 key layout does not match backups/YYYY/MM/DD/"
fi

if grep -q "s3api head-object --bucket test-bucket --key backups/${EXPECTED_DATE_PREFIX}" "$CALL_LOG"; then
    pass "head-object verification called with matching bucket/key"
else
    fail_test "head-object verification not called with expected bucket/key"
fi

ARCHIVE="$(find "$CAPTURE_DIR" -name 'pilot-backup-*.tar.gz' 2>/dev/null | head -1)"
if [ -n "$ARCHIVE" ]; then
    CONTENTS="$(tar tzf "$ARCHIVE" | sort)"
    EXPECTED="$(printf 'global_patterns.json\nknowledge.json\npilot-%s.db\n' "$EXPECTED_DATE_STAMP")"
    if [ "$CONTENTS" = "$EXPECTED" ]; then
        pass "tar archive contains db snapshot + knowledge.json + global_patterns.json"
    else
        fail_test "unexpected tar contents: $CONTENTS"
    fi
else
    fail_test "no archive captured by mock aws"
fi
teardown_sandbox

echo "=== test: missing optional json files — script still succeeds ==="
setup_sandbox
rm -f "${DATA_DIR}/knowledge.json" "${DATA_DIR}/global_patterns.json"
if run_backup >"${SANDBOX}/stdout.log" 2>&1; then
    pass "script exits 0 when optional json files are missing"
else
    cat "${SANDBOX}/stdout.log"
    fail_test "script failed when optional json files were missing"
fi
ARCHIVE="$(find "$CAPTURE_DIR" -name 'pilot-backup-*.tar.gz' 2>/dev/null | head -1)"
if [ -n "$ARCHIVE" ]; then
    CONTENTS="$(tar tzf "$ARCHIVE" | sort)"
    EXPECTED="pilot-$(date -u +%Y%m%d).db"
    if [ "$CONTENTS" = "$EXPECTED" ]; then
        pass "tar archive contains only the db snapshot"
    else
        fail_test "unexpected tar contents with missing json files: $CONTENTS"
    fi
else
    fail_test "no archive captured by mock aws"
fi
teardown_sandbox

echo "=== test: missing ledger db — script fails before touching S3 ==="
setup_sandbox
rm -f "${DATA_DIR}/pilot.db"
if run_backup >"${SANDBOX}/stdout.log" 2>&1; then
    fail_test "script should have exited non-zero for missing pilot.db"
else
    pass "script exits non-zero when pilot.db is missing"
fi
if [ -s "$CALL_LOG" ]; then
    fail_test "aws should not have been invoked when pilot.db is missing"
else
    pass "aws not invoked when pilot.db is missing"
fi
teardown_sandbox

echo "=== test: upload failure — script propagates non-zero exit ==="
setup_sandbox
if run_backup MOCK_AWS_FAIL_CP=1 >"${SANDBOX}/stdout.log" 2>&1; then
    fail_test "script should have exited non-zero on s3 cp failure"
else
    pass "script exits non-zero when s3 cp fails"
fi
teardown_sandbox

echo "=== test: head-object verification failure — script propagates non-zero exit ==="
setup_sandbox
if run_backup MOCK_AWS_FAIL_HEAD=1 >"${SANDBOX}/stdout.log" 2>&1; then
    fail_test "script should have exited non-zero when head-object check fails"
else
    pass "script exits non-zero when head-object check fails"
fi
teardown_sandbox

echo ""
echo "=== summary: ${PASS} passed, ${FAIL} failed ==="
if [ "$FAIL" -gt 0 ]; then
    exit 1
fi
exit 0
