#!/bin/bash
# Ban argument-discarding mocks on high-value test-double seams.
#
# Incident: GH-4702/#4706/#4707 — mockSelfReviewBackend.Execute(_
# context.Context, _ ExecuteOptions) discarded every argument it received,
# so no test could ever have caught the runSelfReview path-identity bug
# (self-review ran against the daemon's shared repo root instead of the
# worktree, undetected for months). Root-caused and fixed in TASK-441 Leg 1
# (GH-4708).
#
# A test double that receives a call across a seam and immediately throws
# every argument away certifies nothing about the call — it can pass
# whether the caller sent the right input or a completely wrong one. See
# internal/executor/backend_execute_guard_test.go's guardRecordingBackend
# for the pattern to use instead: record what crossed the seam, assert on
# it.
#
# Scope: deliberately narrow to specific interface seams that have already
# produced a real, undetected-for-months bug when mocked this way — not
# "any mock method with discarded params". The codebase has many legitimate
# fixed-response test doubles (fake stores, fake classifiers, error-
# injection stubs) where the caller's input genuinely doesn't matter to the
# test's assertion. Broadening either check below to "any Check(...)" or
# "any method" would produce false positives against dozens of unrelated,
# correct test doubles and train contributors to ignore this gate.
#
# Used by CI (.github/workflows/ci.yml step: "Check Argument-Discarding
# Mocks") and by `make check-mocks`.
#
# Check 2 (QualityChecker.Check) added TASK-460 / GH-5062: GH-5060's root
# cause was the same test-blindness class recurring on a second seam —
# sleepThenFailQualityChecker.Check(_ context.Context) discarded its ctx
# unconditionally, so the fresh-ctx regression tests built on it structurally
# could not see that the quality-gate retry-pass re-check still ran on the
# exhausted attempt ctx. That bug shipped undetected until a *second*,
# hand-written mock (ctxRespectingQualityChecker, runner_gh5060_test.go) was
# added specifically because sleepThenFailQualityChecker couldn't cover it.
#
# ExecutionSaver was considered for a third check and deliberately left out:
# cmd/pilot's sdkCore.ExecutionSaver/ExecutionSaverV2 has exactly one
# implementation, storeExecutionSaver (cmd/pilot/main.go) — a production
# adapter pinned by a `var _ sdkCore.ExecutionSaverV2 = storeExecutionSaver{}`
# build-time assertion (cmd/pilot/execution_saver_test.go), not a hand-rolled
# test-double mock. There's no argument-discarding double of it to catch, so
# there's no seam here for this gate to cover.

set -e

echo "Scanning *_test.go files for argument-discarding mocks..."

FAILED=0

# ---------------------------------------------------------------------------
# Check 1: Backend.Execute
# ---------------------------------------------------------------------------
#
# Matches a method with a receiver named Execute whose entire parameter
# list is "_ context.Context, _ ExecuteOptions" — i.e. both parameters of
# the Backend.Execute seam are discarded.
BACKEND_PATTERN='func \([a-zA-Z0-9_]+ \*?[A-Za-z0-9_]+\) Execute\(_ context\.Context, _ ExecuteOptions\)'

BACKEND_MATCHES=$(git ls-files '*_test.go' | xargs grep -HnE "$BACKEND_PATTERN" 2>/dev/null || true)

if [ -n "$BACKEND_MATCHES" ]; then
    FAILED=1
    echo ""
    echo "❌ ERROR: found Backend.Execute mock(s) that discard every argument"
    echo ""
    echo "$BACKEND_MATCHES"
    echo ""
    echo "A mock's Execute(_ context.Context, _ ExecuteOptions) certifies"
    echo "nothing about the call it received — it cannot catch bugs in what"
    echo "crosses the seam (wrong ProjectPath, wrong options). This is how"
    echo "the GH-4702 runSelfReview bug survived undetected for months."
    echo ""
    echo "Fix: name the second parameter, record what you care about, and"
    echo "assert on it in the test. See"
    echo "internal/executor/backend_execute_guard_test.go's"
    echo "guardRecordingBackend for the pattern:"
    echo ""
    echo "  func (b *fooBackend) Execute(_ context.Context, opts ExecuteOptions) (*BackendResult, error) {"
    echo "      b.gotProjectPath = opts.ProjectPath"
    echo "      return &BackendResult{Success: true}, nil"
    echo "  }"
    echo ""
    echo "...and in the test:"
    echo "  if backend.gotProjectPath != wantPath {"
    echo "      t.Errorf(\"backend received ProjectPath %q, want %q\", backend.gotProjectPath, wantPath)"
    echo "  }"
    echo ""
fi

# ---------------------------------------------------------------------------
# Check 2: QualityChecker.Check
# ---------------------------------------------------------------------------
#
# Matches a method named Check whose sole parameter (the ctx) is discarded,
# on a receiver type name ending in QualityChecker/qualityChecker — the
# naming convention every checker double in internal/executor follows
# (ctxRespectingQualityChecker, sleepThenFailQualityChecker,
# statefulQualityChecker, ...). Scoped to that suffix, not "any Check(...)",
# so this doesn't false-positive against the package's other *Checker
# interfaces (TeamChecker, IssueStateChecker, LiveWorkerChecker,
# BasePresenceProbe), whose Check methods have unrelated signatures and
# semantics.
QC_PATTERN='func \([a-zA-Z0-9_]+ \*?[A-Za-z0-9_]*[Qq]ualityChecker\) Check\(_ context\.Context\)'

# Pre-existing fixed-response QualityChecker test doubles that legitimately
# discard ctx: each always returns the same QualityOutcome regardless of
# what's passed in, and the test built on it asserts on retry-loop mechanics
# (does a retry fire, does the worktree get reused, does terminal failure
# clean up) — never on ctx freshness or propagation. That's the same
# "fixed-response test double" carve-out described in this script's header
# for Backend.Execute.
#
# sleepThenFailQualityChecker (runner_gh4876_test.go) is the GH-5060
# offender by name in the issue, but it remains legitimate for the one test
# it still drives (TestQualityGateRetry_UsesFreshContextForResetAndReinvoke,
# which asserts on the reset/re-invoke path, not the gate re-check).
# GH-5060's fix added a *second*, ctx-respecting mock
# (ctxRespectingQualityChecker, runner_gh5060_test.go) precisely because
# this one structurally can't cover the re-check path — that new mock is
# NOT on this allowlist and must keep passing this check.
#
# Do not add an entry here without the same "always the same result, ctx
# genuinely irrelevant to what the test asserts" justification. If a new
# mock's test cares whether ctx is fresh/stale/cancelled, don't discard the
# parameter — follow ctxRespectingQualityChecker's pattern instead: name it
# and consume ctx.Err() (or equivalent) on the call(s) that matter.
QC_ALLOWLIST=(
    "internal/executor/gh4129_test.go"                                # statefulQualityChecker — GH-4129 retry-loop mechanics, not ctx
    "internal/executor/runner_gh4876_test.go"                         # sleepThenFailQualityChecker — GH-4876 reset/reinvoke fresh-ctx check; see ctxRespectingQualityChecker (runner_gh5060_test.go) for the ctx-respecting sibling covering the re-check path
    "internal/executor/runner_retry_worktree_test.go"                 # failingQualityChecker — GH-3577 worktree-retry mechanics, not ctx
    "internal/executor/runner_terminal_failure_clean_gh4594_test.go"  # terminallyFailingQualityChecker — GH-4594 terminal-failure cleanup, not ctx
)

QC_MATCHES=$(git ls-files '*_test.go' | grep -vFx -f <(printf '%s\n' "${QC_ALLOWLIST[@]}") | xargs grep -HnE "$QC_PATTERN" 2>/dev/null || true)

if [ -n "$QC_MATCHES" ]; then
    FAILED=1
    echo ""
    echo "❌ ERROR: found QualityChecker.Check mock(s) that discard ctx"
    echo ""
    echo "$QC_MATCHES"
    echo ""
    echo "A mock's Check(_ context.Context) certifies nothing about the ctx"
    echo "it received — it structurally cannot tell a fresh ctx from a stale"
    echo "one. This is how the GH-5060 quality-gate retry-pass re-check bug"
    echo "(reusing an exhausted attempt ctx) survived undetected until a"
    echo "second, hand-written mock was added to catch it."
    echo ""
    echo "Fix: name the ctx parameter and consume it where it matters. See"
    echo "internal/executor/runner_gh5060_test.go's ctxRespectingQualityChecker"
    echo "for the pattern:"
    echo ""
    echo "  func (c *fooQualityChecker) Check(ctx context.Context) (*QualityOutcome, error) {"
    echo "      if err := ctx.Err(); err != nil {"
    echo "          return nil, err"
    echo "      }"
    echo "      return &QualityOutcome{Passed: true}, nil"
    echo "  }"
    echo ""
    echo "If ctx is genuinely irrelevant to this test's assertion (a fixed-"
    echo "response double whose test doesn't care about ctx freshness), add"
    echo "the file to QC_ALLOWLIST in scripts/check-mocks.sh with a comment"
    echo "explaining why."
    echo ""
fi

if [ "$FAILED" -eq 1 ]; then
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo "CI CHECK FAILED: argument-discarding mock(s) found"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    exit 1
fi

echo "✓ No argument-discarding mocks found"
exit 0
