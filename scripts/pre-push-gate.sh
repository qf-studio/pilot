#!/bin/bash
# Pre-push validation gate for Pilot
# Runs all checks before allowing a push: build, lint, test, secrets, integration
#
# Full gate: 224-287s measured (dominated by `go test -short -race ./...`;
# see GH-4771 issue body for the per-step breakdown). Pushes whose diff
# touches no *.go/go.mod/go.sum path (docs-only) take a fast path instead —
# only check-secrets + check-graph run, target <10s. The pre-push
# git hook (installed via install-hooks.sh) decides this using
# scripts/pre-push-classify.sh and sets PILOT_GATE_DOCS_ONLY=1 accordingly.
# `make gate` always runs the full gate (no stdin to classify from). See
# .agent/sops/quality/pre-push-gate.md.

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Track timing
GATE_START=$(date +%s)
FAILURES=0
WARNINGS=0

# Print header
echo ""
echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${BLUE}           PILOT PRE-PUSH GATE                      ${NC}"
echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo ""

cd "$PROJECT_ROOT"

# Helper function to run a check
run_check() {
    local name="$1"
    local cmd="$2"
    local start=$(date +%s)

    echo -n "  [$name] "

    # Capture output
    local output
    local exit_code
    output=$(eval "$cmd" 2>&1) && exit_code=0 || exit_code=$?

    local end=$(date +%s)
    local duration=$((end - start))

    if [ $exit_code -eq 0 ]; then
        echo -e "${GREEN}✓${NC} (${duration}s)"
        return 0
    else
        echo -e "${RED}✗${NC} (${duration}s)"
        echo ""
        echo -e "${RED}$output${NC}" | head -20
        echo ""
        return 1
    fi
}

# Helper for warnings (non-blocking)
run_check_warn() {
    local name="$1"
    local cmd="$2"
    local start=$(date +%s)

    echo -n "  [$name] "

    local output
    local exit_code
    output=$(eval "$cmd" 2>&1) && exit_code=0 || exit_code=$?

    local end=$(date +%s)
    local duration=$((end - start))

    if [ $exit_code -eq 0 ]; then
        echo -e "${GREEN}✓${NC} (${duration}s)"
        return 0
    else
        echo -e "${YELLOW}⚠${NC} (${duration}s)"
        WARNINGS=$((WARNINGS + 1))
        return 0  # Don't fail on warnings
    fi
}

# Docs-only fast path: the pre-push hook classifies the push (via
# scripts/pre-push-classify.sh, fed git's stdin ref updates) and exports
# PILOT_GATE_DOCS_ONLY=1 when the diff touches no *.go/go.mod/go.sum path.
# `make gate` never sets this, so a manual invocation always runs the full
# gate below. Only check-secrets + check-graph run here — see the "Must
# always run" rule in .agent/sops/quality/pre-push-gate.md.
if [ "${PILOT_GATE_DOCS_ONLY:-0}" = "1" ]; then
    echo -e "${YELLOW}⚡ Docs-only push detected — fast path engaged${NC}"
    echo "   Skipping:  build, lint, test (short), mocks, integration"
    echo "   Reason:    diff contains no *.go / go.mod / go.sum paths"
    echo "   Running:   check-secrets, check-graph (always run regardless of diff)"
    echo ""

    echo -e "${BLUE}[1/2] Secret Patterns${NC}"
    if [ -x "$SCRIPT_DIR/check-secret-patterns.sh" ]; then
        if ! run_check "check-secrets" "$SCRIPT_DIR/check-secret-patterns.sh"; then
            FAILURES=$((FAILURES + 1))
        fi
    else
        echo -e "  [check-secrets] ${YELLOW}skipped (script not found)${NC}"
    fi
    echo ""

    echo -e "${BLUE}[2/2] Knowledge Graph${NC}"
    if ! run_check "check-graph" "python3 $SCRIPT_DIR/check-graph.py"; then
        FAILURES=$((FAILURES + 1))
    fi
    echo ""

    GATE_END=$(date +%s)
    GATE_DURATION=$((GATE_END - GATE_START))

    echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    if [ $FAILURES -eq 0 ]; then
        echo -e "${GREEN}  GATE PASSED (fast path)${NC} (${GATE_DURATION}s)"
        echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
        echo ""
        exit 0
    else
        echo -e "${RED}  GATE FAILED (fast path)${NC} (${GATE_DURATION}s)"
        echo -e "  ${RED}$FAILURES check(s) failed${NC}"
        echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
        echo ""
        echo "Fix the issues above before pushing."
        echo "To bypass (not recommended): git push --no-verify"
        echo ""
        exit 1
    fi
fi

# 1. BUILD
echo -e "${BLUE}[1/8] Build${NC}"
if ! run_check "go build" "go build -o /dev/null ./cmd/pilot"; then
    FAILURES=$((FAILURES + 1))
fi
echo ""

# 2. LINT
echo -e "${BLUE}[2/8] Lint${NC}"
if command -v golangci-lint >/dev/null 2>&1; then
    if ! run_check "golangci-lint" "golangci-lint run --timeout 60s"; then
        FAILURES=$((FAILURES + 1))
    fi
else
    echo -e "  [golangci-lint] ${YELLOW}skipped (not installed)${NC}"
    WARNINGS=$((WARNINGS + 1))
fi
echo ""

# 3. TEST (short mode for speed)
echo -e "${BLUE}[3/8] Test (short)${NC}"
if ! run_check "go test -short" "go test -short -race ./..."; then
    FAILURES=$((FAILURES + 1))
fi
echo ""

# 4. SECRETS
echo -e "${BLUE}[4/8] Secret Patterns${NC}"
if [ -x "$SCRIPT_DIR/check-secret-patterns.sh" ]; then
    if ! run_check "check-secrets" "$SCRIPT_DIR/check-secret-patterns.sh"; then
        FAILURES=$((FAILURES + 1))
    fi
else
    echo -e "  [check-secrets] ${YELLOW}skipped (script not found)${NC}"
fi
echo ""

# 5. MOCKS
echo -e "${BLUE}[5/8] Argument-Discarding Mocks${NC}"
if [ -x "$SCRIPT_DIR/check-mocks.sh" ]; then
    if ! run_check "check-mocks" "$SCRIPT_DIR/check-mocks.sh"; then
        FAILURES=$((FAILURES + 1))
    fi
else
    echo -e "  [check-mocks] ${YELLOW}skipped (script not found)${NC}"
fi
echo ""

# 6. DESTRUCTIVE-CALL GATE
echo -e "${BLUE}[6/8] Destructive-Call Gate${NC}"
if [ -x "$SCRIPT_DIR/check-destructive-calls.sh" ]; then
    # Self-test first: proves the gate's own detection logic (seeded
    # violations caught, allowlisted files not flagged) still works on this
    # machine's grep before trusting its "clean" verdict below. This is what
    # would have caught GH-4831 — BSD grep on macOS silently no-op'd Check 2
    # (bare Verdict{} literals), so the plain run reported false-green while
    # the self-test failed outright.
    if ! run_check "check-destructive --self-test" "$SCRIPT_DIR/check-destructive-calls.sh --self-test"; then
        FAILURES=$((FAILURES + 1))
    fi
    if ! run_check "check-destructive" "$SCRIPT_DIR/check-destructive-calls.sh"; then
        FAILURES=$((FAILURES + 1))
    fi
else
    echo -e "  [check-destructive] ${YELLOW}skipped (script not found)${NC}"
fi
echo ""

# 7. KNOWLEDGE GRAPH
echo -e "${BLUE}[7/8] Knowledge Graph${NC}"
if ! run_check "check-graph" "python3 $SCRIPT_DIR/check-graph.py"; then
    FAILURES=$((FAILURES + 1))
fi
echo ""

# 8. INTEGRATION
echo -e "${BLUE}[8/8] Integration${NC}"
if [ -x "$SCRIPT_DIR/check-integration.sh" ]; then
    if ! run_check "integration" "$SCRIPT_DIR/check-integration.sh"; then
        FAILURES=$((FAILURES + 1))
    fi
else
    # Run inline integration checks if script doesn't exist
    echo -n "  [orphan-commands] "
    # Check for newXxxCmd functions not in AddCommand
    ORPHAN_CMDS=0
    for cmd_func in $(grep -rh 'func new.*Cmd\(\)' cmd/pilot/*.go 2>/dev/null | grep -oE 'new[A-Za-z]+Cmd' || true); do
        if ! grep -q "AddCommand.*${cmd_func}" cmd/pilot/*.go 2>/dev/null; then
            if ! grep -q "${cmd_func}()" cmd/pilot/*.go 2>/dev/null; then
                ORPHAN_CMDS=$((ORPHAN_CMDS + 1))
            fi
        fi
    done
    if [ $ORPHAN_CMDS -eq 0 ]; then
        echo -e "${GREEN}✓${NC}"
    else
        echo -e "${RED}✗ Found $ORPHAN_CMDS orphan commands${NC}"
        FAILURES=$((FAILURES + 1))
    fi

    # Check for platform-specific files without build tags
    echo -n "  [build-tags] "
    MISSING_TAGS=0
    for file in $(find . -name '*_darwin.go' -o -name '*_linux.go' 2>/dev/null | grep -v vendor || true); do
        if ! head -5 "$file" | grep -q '//go:build\|// +build'; then
            MISSING_TAGS=$((MISSING_TAGS + 1))
            echo ""
            echo -e "    ${RED}Missing build tag: $file${NC}"
        fi
    done
    if [ $MISSING_TAGS -eq 0 ]; then
        echo -e "${GREEN}✓${NC}"
    else
        FAILURES=$((FAILURES + 1))
    fi
fi
echo ""

# Calculate total time
GATE_END=$(date +%s)
GATE_DURATION=$((GATE_END - GATE_START))

# Summary
echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"

if [ $FAILURES -eq 0 ]; then
    echo -e "${GREEN}  GATE PASSED${NC} (${GATE_DURATION}s)"
    if [ $WARNINGS -gt 0 ]; then
        echo -e "  ${YELLOW}$WARNINGS warning(s)${NC}"
    fi
    echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo ""
    exit 0
else
    echo -e "${RED}  GATE FAILED${NC} (${GATE_DURATION}s)"
    echo -e "  ${RED}$FAILURES check(s) failed${NC}"
    if [ $WARNINGS -gt 0 ]; then
        echo -e "  ${YELLOW}$WARNINGS warning(s)${NC}"
    fi
    echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo ""
    echo "Fix the issues above before pushing."
    echo "To bypass (not recommended): git push --no-verify"
    echo ""
    exit 1
fi
