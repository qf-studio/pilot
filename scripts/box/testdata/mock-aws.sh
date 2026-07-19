#!/bin/bash
# Mock `aws` CLI used by pilot-backup-s3_test.sh (PATH-shim). Never talks to
# real AWS. Behavior is controlled entirely via env vars set by the test:
#
#   MOCK_AWS_CALL_LOG    (required) file to append the full argv to
#   MOCK_AWS_CAPTURE_DIR (optional) dir to copy the `s3 cp` source file into,
#                        so the test can inspect the archive the real script built
#   MOCK_AWS_FAIL_CP     (optional) if set, `aws s3 cp` exits 1
#   MOCK_AWS_FAIL_HEAD   (optional) if set, `aws s3api head-object` exits 1

set -euo pipefail

: "${MOCK_AWS_CALL_LOG:?MOCK_AWS_CALL_LOG not set}"
echo "$*" >> "$MOCK_AWS_CALL_LOG"

case "${1:-}-${2:-}" in
    "s3-cp")
        if [ -n "${MOCK_AWS_FAIL_CP:-}" ]; then
            echo "mock aws: simulated s3 cp failure" >&2
            exit 1
        fi
        SRC="${3:?missing s3 cp source arg}"
        if [ -n "${MOCK_AWS_CAPTURE_DIR:-}" ]; then
            mkdir -p "$MOCK_AWS_CAPTURE_DIR"
            cp "$SRC" "$MOCK_AWS_CAPTURE_DIR/"
        fi
        exit 0
        ;;
    "s3api-head-object")
        if [ -n "${MOCK_AWS_FAIL_HEAD:-}" ]; then
            exit 1
        fi
        exit 0
        ;;
    *)
        echo "mock aws: unhandled command: $*" >&2
        exit 1
        ;;
esac
