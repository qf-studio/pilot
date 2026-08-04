#!/bin/bash
# Ban argument-discarding Backend.Execute mocks in test files.
#
# Incident: GH-4702/#4706/#4707 — mockSelfReviewBackend.Execute(_
# context.Context, _ ExecuteOptions) discarded every argument it received,
# so no test could ever have caught the runSelfReview path-identity bug
# (self-review ran against the daemon's shared repo root instead of the
# worktree, undetected for months). Root-caused and fixed in TASK-441 Leg 1
# (GH-4708).
#
# A test double that receives a call across the Backend.Execute seam and
# immediately throws every argument away certifies nothing about the call —
# it can pass whether the caller sent the right ProjectPath or a completely
# wrong one. See internal/executor/backend_execute_guard_test.go's
# guardRecordingBackend for the pattern to use instead: record what crossed
# the seam, assert on it.
#
# Scope: deliberately narrow to the Backend interface's Execute(ctx
# context.Context, opts ExecuteOptions) shape (internal/executor/backend.go),
# not "any mock method with discarded params" — the codebase has many
# legitimate fixed-response test doubles (fake stores, fake classifiers,
# error-injection stubs) where the caller's input genuinely doesn't matter
# to the test's assertion. The Backend.Execute seam is different: it is the
# exact chokepoint TASK-323/GH-3577/GH-4702/GH-4703 all recurred at, where
# *what* crosses the seam (particularly ProjectPath) is the thing under
# test. Broadening this check would produce false positives against dozens
# of unrelated, correct test doubles and train contributors to ignore it.
#
# Used by CI (.github/workflows/ci.yml step: "Check Argument-Discarding
# Mocks") and by `make check-mocks`.

set -e

echo "Scanning *_test.go files for argument-discarding Backend.Execute mocks..."

# Matches a method with a receiver named Execute whose entire parameter
# list is "_ context.Context, _ ExecuteOptions" — i.e. both parameters of
# the Backend.Execute seam are discarded.
PATTERN='func \([a-zA-Z0-9_]+ \*?[A-Za-z0-9_]+\) Execute\(_ context\.Context, _ ExecuteOptions\)'

MATCHES=$(git ls-files '*_test.go' | xargs grep -HnE "$PATTERN" 2>/dev/null || true)

if [ -n "$MATCHES" ]; then
    echo ""
    echo "❌ ERROR: found Backend.Execute mock(s) that discard every argument"
    echo ""
    echo "$MATCHES"
    echo ""
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo "CI CHECK FAILED: argument-discarding Backend.Execute mock(s)"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
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
    exit 1
fi

echo "✓ No argument-discarding Backend.Execute mocks found"
exit 0
