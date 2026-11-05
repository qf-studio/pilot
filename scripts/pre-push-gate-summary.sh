#!/bin/bash
# Failure-output helpers for scripts/pre-push-gate.sh (GH-6093).
#
# Sourced by the gate and by scripts/test-prepush-gate-output.sh. Defines
# functions only — sourcing it runs nothing.
#
# Why: `head -20` of a `go test -short -race ./...` run is 20 `ok` lines, so
# a failure deep in the output was invisible to the operator. Show the lines
# that matter instead and keep the full log on disk.

GATE_LOG_DIR="/tmp/pilot-gate"

# Lines that identify a failure in go test / race detector output.
GATE_FAIL_REGEX='^(--- FAIL|FAIL|panic:|.*DATA RACE|ok .*\(timeout\))'

# gate_slugify <check name> — "go test -short" -> "go-test-short"
gate_slugify() {
    printf '%s' "$1" | tr '[:upper:]' '[:lower:]' | sed -E 's/[^a-z0-9]+/-/g; s/^-+//; s/-+$//'
}

# gate_failure_summary <output> — print the failing lines of a check's output:
# every line matching GATE_FAIL_REGEX plus up to 40 lines of context after the
# first `--- FAIL` line. With no match, the last 40 lines (never the head).
gate_failure_summary() {
    local output="$1"
    # Here-string, not a pipe: `grep -q` exits early and a pipe would SIGPIPE
    # the writer, failing the test under `set -o pipefail`.
    if grep -qE "$GATE_FAIL_REGEX" <<<"$output"; then
        printf '%s\n' "$output" | GATE_RE="$GATE_FAIL_REGEX" awk '
            BEGIN { re = ENVIRON["GATE_RE"] }
            {
                show = ($0 ~ re)
                if (!seen && $0 ~ /^--- FAIL/) { seen = 1; ctx = 40; show = 1 }
                else if (ctx > 0) { ctx--; show = 1 }
                if (show) print
            }'
    else
        printf '%s\n' "$output" | tail -40
    fi
}

# gate_write_failure_log <check name> <output> — write the full output to
# /tmp/pilot-gate/<slug>-<timestamp>.log and print the path.
gate_write_failure_log() {
    local slug log
    slug="$(gate_slugify "$1")"
    mkdir -p "$GATE_LOG_DIR"
    log="$GATE_LOG_DIR/${slug:-check}-$(date +%Y%m%d-%H%M%S)-$$.log"
    printf '%s\n' "$2" > "$log"
    printf '%s\n' "$log"
}
