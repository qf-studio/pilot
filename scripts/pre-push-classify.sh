#!/bin/bash
# Classifies a set of git pre-push ref updates as "docs-only" (safe for the
# pre-push-gate.sh fast path) or "full" (run the entire gate).
#
# Contract:
#   - Reads <local_ref> <local_sha> <remote_ref> <remote_sha> lines from
#     stdin, exactly as git passes them to a pre-push hook.
#   - Prints exactly one word to stdout: "docs-only" or "full".
#   - Always exits 0 — classification itself never fails. Any uncertainty
#     (new branch, ref deletion, an errored/empty diff, no input at all)
#     biases toward "full", per GH-4771's "fail toward the full gate on any
#     doubt" rule.
#   - Diagnostics (why a decision was made) go to stderr, never stdout, so
#     stdout stays a clean single-word classification callers can capture
#     with `$(...)`.
#
# A push is docs-only iff the UNION of changed paths across every pushed ref
# contains zero paths matching *.go, go.mod, or go.sum. Must be run with the
# repo as the working directory (the pre-push hook and `git diff` both rely
# on this).
#
# See .agent/sops/quality/pre-push-gate.md for the full fast-path rationale.

set -u

NULL_SHA_RE='^0+$'
CODE_PATH_RE='(^|/)go\.mod$|(^|/)go\.sum$|\.go$'

saw_ref=0

# Prints "full" and exits immediately — used the moment any single ref
# update forces the whole push out of the fast path.
classify_full() {
    echo "full"
    exit 0
}

while read -r local_ref local_sha remote_ref remote_sha; do
    # Guard against blank/malformed lines (e.g. trailing newline from `cat`).
    if [ -z "${local_ref:-}" ] || [ -z "${local_sha:-}" ] || [ -z "${remote_ref:-}" ] || [ -z "${remote_sha:-}" ]; then
        continue
    fi
    saw_ref=1

    if [[ "$remote_sha" =~ $NULL_SHA_RE ]]; then
        echo "pre-push-classify: new branch (null remote OID) on ${remote_ref} -> full gate" >&2
        classify_full
    fi

    if [[ "$local_sha" =~ $NULL_SHA_RE ]]; then
        echo "pre-push-classify: ref deletion (${remote_ref}) -> full gate" >&2
        classify_full
    fi

    changed="$(git diff --name-only "${remote_sha}..${local_sha}" 2>/dev/null)"
    diff_status=$?

    if [ $diff_status -ne 0 ]; then
        echo "pre-push-classify: git diff errored for ${remote_sha}..${local_sha} -> full gate" >&2
        classify_full
    fi

    if [ -z "$changed" ]; then
        echo "pre-push-classify: empty diff for ${remote_sha}..${local_sha} -> full gate" >&2
        classify_full
    fi

    if echo "$changed" | grep -qE "$CODE_PATH_RE"; then
        echo "pre-push-classify: code path(s) touched on ${local_ref} -> full gate" >&2
        classify_full
    fi
done

if [ "$saw_ref" -eq 0 ]; then
    echo "pre-push-classify: no ref updates read from stdin -> full gate" >&2
    classify_full
fi

echo "docs-only"
