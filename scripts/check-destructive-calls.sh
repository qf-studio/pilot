#!/bin/bash
# Gate new bypasses of the TASK-459 `Verdict` contract.
#
# TASK-459 Phases 1-3 (#4796/PR#4802, #4811/PR#4812, #4817/PR#4821) migrated
# every destructive autopilot action (PR close, branch delete, fix-issue
# spawn, merge) behind `Verdict.AuthorizesDestructive()` — a positive-
# evidence gate that fails closed (hold, not act) on `FailureClassUnknown`
# or empty evidence. That contract is only as good as its two invariants:
#
#   1. Every call to a destructive API happens inside one of the small set
#      of gated call sites that construct and check a `Verdict` first — not
#      from some new, ungated call site added later.
#   2. Every `Verdict` is built through `NewVerdict`/`NewUnknownVerdict`
#      (internal/autopilot/failure_class.go) — never via a bare `Verdict{}`
#      composite literal elsewhere, which PR#4802 review finding 2 flagged
#      as possible from any file in package autopilot (unexported fields
#      don't restrict intra-package construction, only cross-package).
#
# Neither invariant is enforced by the Go compiler. This script is TASK-459
# Phase 4's grep gate keeping both true by construction instead of by
# convention: it fails CI if a new call site or a stray composite literal
# appears anywhere the allowlists below don't already account for.
#
# Used by CI (.github/workflows/ci.yml step: "Check Destructive-Call Gate"),
# `make check-destructive`, and scripts/pre-push-gate.sh.
#
# Run `./scripts/check-destructive-calls.sh --self-test` to verify the
# gate's own detection logic (seeded violations must be caught, allowlisted
# production files must not be flagged) without touching repo files.

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
cd "$PROJECT_ROOT"

# ---------------------------------------------------------------------------
# Check 1: destructive API calls outside their gated call sites.
# ---------------------------------------------------------------------------
#
# Scope: production (non-_test.go) files only. Test files that call these
# methods directly (internal/adapters/github/client_test.go,
# internal/autopilot/feedback_loop_test.go, spawned_fix_test.go) are testing
# the gated helper's own implementation, not adding a new decision path —
# the hazard this check exists for is a *new production call site* that
# skips the Verdict gate, not test coverage of the existing ones.
DESTRUCTIVE_METHOD_PATTERN='\.(ClosePullRequest|DeleteBranch|CreateFailureIssue|MergePullRequest)\('

# Files allowed to call the destructive methods above directly. Every entry
# needs a comment saying which inventory family (.agent/system/
# irreversible-actions.md) it belongs to and what gates it. Adding a new
# destructive call site means adding a row to that inventory *and* an entry
# here — see .agent/sops/quality/irreversible-actions.md.
DESTRUCTIVE_CALL_ALLOWLIST=(
    # Families 1/2/3/6: the autopilot decision ladder. Every ClosePullRequest/
    # DeleteBranch/CreateFailureIssue/MergePullRequest-adjacent(*) call here is
    # reached only after a Verdict.AuthorizesDestructive() check (CI-failure
    # rungs) or an equivalent re-read/counter gate documented per-row in the
    # inventory. (*MergePullRequest itself is called by auto_merger.go, not
    # here — controller.go calls AutoMerger.MergePR, which is gated below.)
    "internal/autopilot/controller.go"
    # Family 6: AutoMerger.MergePR, the sole production MergePullRequest call.
    # Gated by a live CheckCI re-validation immediately before merging
    # (handleMerging, controller.go) — a definitive CIFailure here rescinds
    # approval instead of merging.
    "internal/autopilot/auto_merger.go"
    # Family 2 (N/A row): GitOperations.DeleteBranch here deletes the local
    # git worktree branch only — not a GitHub API call, not PR/branch-adjacent,
    # out of the Verdict contract's scope entirely. Kept on the allowlist so
    # this script stays a pure "did a new site appear" gate, not a value
    # judgment about this specific already-reviewed local cleanup.
    "internal/executor/git.go"
)

# ---------------------------------------------------------------------------
# Check 2: bare `Verdict{}` composite-literal construction outside its
# owning file.
# ---------------------------------------------------------------------------
#
# Uses a negative lookbehind so a *qualified* identifier of the same name in
# an unrelated package (e.g. cmd/pilot/poller_github.go's sdkcore.Verdict)
# doesn't false-positive, and a leading \b so embedded-suffix type names
# (PreFlightVerdict{}, JudgeVerdict{} in internal/executor/intent_judge.go)
# don't either.
VERDICT_LITERAL_PATTERN='(?<!\.)\bVerdict\{'

VERDICT_LITERAL_ALLOWLIST=(
    # The owning file: NewVerdict/NewUnknownVerdict construct Verdict{} here,
    # and only here, in production code.
    "internal/autopilot/failure_class.go"
    # TestVerdict_ZeroValue / TestVerdict_AuthorizesDestructive deliberately
    # construct a bare Verdict{} to pin PR#4802 review finding 1 (a zero-value
    # Verdict must read as FailureClassUnknown/empty-evidence and never
    # authorize a destructive action) — this is the regression test *for* the
    # exact hazard this script's Check 2 guards against, so it's the one test
    # file allowed to construct one.
    "internal/autopilot/failure_class_test.go"
)

is_allowlisted() {
    local target="$1"
    shift
    local entry
    for entry in "$@"; do
        if [ "$target" = "$entry" ]; then
            return 0
        fi
    done
    return 1
}

# scan_destructive_calls FILE...
# Prints grep -HnE-format matches for destructive-API call sites in the
# given files, skipping anything on DESTRUCTIVE_CALL_ALLOWLIST. Empty output
# means clean.
scan_destructive_calls() {
    local f hit
    for f in "$@"; do
        [ -z "$f" ] && continue
        is_allowlisted "$f" "${DESTRUCTIVE_CALL_ALLOWLIST[@]}" && continue
        hit=$(grep -HnE "$DESTRUCTIVE_METHOD_PATTERN" "$f" 2>/dev/null || true)
        [ -n "$hit" ] && echo "$hit"
    done
    # Explicit success: this is a collector, not a pass/fail predicate — the
    # last loop iteration's exit status (e.g. a "no match" test outcome)
    # must never become this function's own return code, or `VAR=$(...)`
    # callers would trip `set -e` on a clean scan.
    return 0
}

# scan_verdict_literals FILE...
# Prints grep -PHn-format matches for bare Verdict{} composite literals in
# the given files, skipping anything on VERDICT_LITERAL_ALLOWLIST. Empty
# output means clean.
scan_verdict_literals() {
    local f hit
    for f in "$@"; do
        [ -z "$f" ] && continue
        is_allowlisted "$f" "${VERDICT_LITERAL_ALLOWLIST[@]}" && continue
        hit=$(grep -PHn "$VERDICT_LITERAL_PATTERN" "$f" 2>/dev/null || true)
        [ -n "$hit" ] && echo "$hit"
    done
    return 0
}

run_self_test() {
    echo "Running check-destructive-calls.sh self-test..."
    local tmp ok=1
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' RETURN

    # 1. A destructive call in a file NOT on the allowlist must be caught.
    cat >"$tmp/seeded_bad_caller.go" <<'EOF'
package fakepkg

func doThing(c *Client, owner, repo string, n int) error {
	return c.ClosePullRequest(nil, owner, repo, n)
}
EOF
    if [ -z "$(scan_destructive_calls "$tmp/seeded_bad_caller.go")" ]; then
        echo "  FAIL: seeded destructive-call violation was not detected"
        ok=0
    else
        echo "  OK: seeded destructive-call violation detected"
    fi

    # 2. A bare Verdict{} composite literal outside its owning file must be
    #    caught, and a qualified/embedded-suffix look-alike must not
    #    false-positive.
    cat >"$tmp/seeded_bad_verdict.go" <<'EOF'
package autopilot

func sneaky() Verdict {
	return Verdict{}
}

func fine() sdkcore.Verdict {
	return sdkcore.Verdict{}
}

func alsoFine() *PreFlightVerdict {
	return &PreFlightVerdict{}
}
EOF
    hits=$(scan_verdict_literals "$tmp/seeded_bad_verdict.go")
    if [ -z "$hits" ]; then
        echo "  FAIL: seeded Verdict{} violation was not detected"
        ok=0
    elif [ "$(echo "$hits" | wc -l)" -ne 1 ]; then
        echo "  FAIL: expected exactly 1 match (bare Verdict{} only), got:"
        echo "$hits" | sed 's/^/    /'
        ok=0
    else
        echo "  OK: seeded Verdict{} violation detected, qualified/suffix look-alikes ignored"
    fi

    # 3. The real, already-reviewed production files on both allowlists must
    #    NOT be flagged (proves the allowlist actually suppresses, using the
    #    live repo files as fixtures rather than a synthetic repo tree).
    if [ -z "$(scan_destructive_calls "internal/autopilot/controller.go")" ]; then
        echo "  OK: allowlisted internal/autopilot/controller.go not flagged"
    else
        echo "  FAIL: allowlisted internal/autopilot/controller.go was flagged"
        ok=0
    fi
    if [ -z "$(scan_verdict_literals "internal/autopilot/failure_class.go")" ]; then
        echo "  OK: allowlisted internal/autopilot/failure_class.go not flagged"
    else
        echo "  FAIL: allowlisted internal/autopilot/failure_class.go was flagged"
        ok=0
    fi

    rm -rf "$tmp"
    trap - RETURN

    if [ "$ok" -eq 1 ]; then
        echo "Self-test passed."
        return 0
    fi
    echo "Self-test FAILED."
    return 1
}

if [ "${1:-}" = "--self-test" ]; then
    run_self_test
    exit $?
fi

echo "Scanning for destructive-call-gate bypasses..."

PROD_GO_FILES=$(git ls-files '*.go' | grep -v '_test\.go$' || true)
ALL_GO_FILES=$(git ls-files '*.go' || true)

# shellcheck disable=SC2086
DESTRUCTIVE_HITS=$(scan_destructive_calls $PROD_GO_FILES)
# shellcheck disable=SC2086
VERDICT_HITS=$(scan_verdict_literals $ALL_GO_FILES)

FAILED=0

if [ -n "$DESTRUCTIVE_HITS" ]; then
    FAILED=1
    echo ""
    echo "❌ ERROR: destructive API call(s) outside their gated call site(s)"
    echo ""
    echo "$DESTRUCTIVE_HITS"
    echo ""
    echo "ClosePullRequest / DeleteBranch / CreateFailureIssue / MergePullRequest"
    echo "must only be called from a site that has already checked a Verdict (or"
    echo "an equivalent re-read/counter gate documented in"
    echo ".agent/system/irreversible-actions.md) before reaching this line."
    echo ""
    echo "Fix: route the call through an existing gated site instead of a new"
    echo "one, or if this genuinely is a new legitimate destructive call site:"
    echo "  1. Add a row to .agent/system/irreversible-actions.md describing"
    echo "     its evidence and reversibility."
    echo "  2. Add the file to DESTRUCTIVE_CALL_ALLOWLIST in this script with a"
    echo "     comment explaining what gates it."
    echo "See .agent/sops/quality/irreversible-actions.md."
    echo ""
fi

if [ -n "$VERDICT_HITS" ]; then
    FAILED=1
    echo ""
    echo "❌ ERROR: Verdict{} composite literal outside failure_class.go"
    echo ""
    echo "$VERDICT_HITS"
    echo ""
    echo "Verdict's fields are unexported, but that only stops cross-package"
    echo "construction — any file in package autopilot can still write a bare"
    echo "Verdict{} and bypass NewVerdict/NewUnknownVerdict's invariants"
    echo "(PR#4802 review finding 2)."
    echo ""
    echo "Fix: construct the Verdict via NewVerdict(...) or NewUnknownVerdict(...)"
    echo "instead of a composite literal. See"
    echo ".agent/sops/quality/irreversible-actions.md."
    echo ""
fi

if [ "$FAILED" -eq 1 ]; then
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo "CI CHECK FAILED: destructive-call-gate bypass"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    exit 1
fi

echo "✓ No destructive-call-gate bypasses found"
exit 0
