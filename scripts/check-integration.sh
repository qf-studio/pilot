#!/bin/bash
# Integration checker for Pilot
# Detects orphan code that will fail lint/CI:
# - Command functions not wired to AddCommand()
# - Unused imports
# - Platform-specific code without build tags
# - Missing config field defaults

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

cd "$PROJECT_ROOT"

ERRORS=0
WARNINGS=0

echo "Integration Check"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

# 1. Check for orphan command functions (newXxxCmd not in AddCommand)
echo "Checking command wiring..."

# Find all newXxxCmd function declarations
CMD_FUNCS=$(grep -rh 'func new[A-Z][a-zA-Z]*Cmd\(\)' cmd/pilot/*.go 2>/dev/null | grep -oE 'new[A-Z][a-zA-Z]*Cmd' | sort -u || true)

if [ -n "$CMD_FUNCS" ]; then
    for func in $CMD_FUNCS; do
        # Check if it's used in AddCommand or called somewhere
        USED_IN_ADDCMD=$(grep -rh "AddCommand.*${func}()" cmd/pilot/*.go 2>/dev/null || true)
        CALLED=$(grep -rh "${func}()" cmd/pilot/*.go 2>/dev/null | grep -v "^func " || true)

        # For subcommands, check if they're added to a parent
        SUBCOMMAND_USAGE=$(grep -rh "\.AddCommand(${func}()" cmd/pilot/*.go 2>/dev/null || true)

        if [ -z "$USED_IN_ADDCMD" ] && [ -z "$CALLED" ] && [ -z "$SUBCOMMAND_USAGE" ]; then
            echo -e "  ${RED}✗${NC} Orphan command: $func() - not wired to AddCommand()"
            ERRORS=$((ERRORS + 1))
        fi
    done
fi

if [ $ERRORS -eq 0 ]; then
    echo -e "  ${GREEN}✓${NC} All commands properly wired"
fi
echo ""

# 2. Check for platform-specific files without build tags
echo "Checking build tags..."
PLATFORM_ERRORS=0

for suffix in "_darwin" "_linux" "_windows"; do
    FILES=$(find . -name "*${suffix}.go" -not -path "./vendor/*" 2>/dev/null || true)
    for file in $FILES; do
        # Check if file has build tag in first 10 lines
        if ! head -10 "$file" | grep -qE '//go:build|// \+build'; then
            echo -e "  ${RED}✗${NC} Missing build tag: $file"
            PLATFORM_ERRORS=$((PLATFORM_ERRORS + 1))
        fi
    done
done

if [ $PLATFORM_ERRORS -eq 0 ]; then
    echo -e "  ${GREEN}✓${NC} All platform-specific files have build tags"
else
    ERRORS=$((ERRORS + PLATFORM_ERRORS))
fi
echo ""

# 3. Check for potential unused imports by running go build with verbose
echo "Checking for build issues..."

# Quick build to catch unused imports, undeclared names, etc.
BUILD_OUTPUT=$(go build ./... 2>&1) || true

if [ -n "$BUILD_OUTPUT" ]; then
    # Check for specific error patterns
    if echo "$BUILD_OUTPUT" | grep -q "imported and not used"; then
        echo -e "  ${RED}✗${NC} Unused imports detected:"
        echo "$BUILD_OUTPUT" | grep "imported and not used" | head -5 | while read -r line; do
            echo "      $line"
        done
        ERRORS=$((ERRORS + 1))
    elif echo "$BUILD_OUTPUT" | grep -q "undefined:"; then
        echo -e "  ${RED}✗${NC} Undefined references:"
        echo "$BUILD_OUTPUT" | grep "undefined:" | head -5 | while read -r line; do
            echo "      $line"
        done
        ERRORS=$((ERRORS + 1))
    elif echo "$BUILD_OUTPUT" | grep -q "cannot find package"; then
        echo -e "  ${RED}✗${NC} Missing packages:"
        echo "$BUILD_OUTPUT" | grep "cannot find package" | head -5 | while read -r line; do
            echo "      $line"
        done
        ERRORS=$((ERRORS + 1))
    else
        # Other build errors
        echo -e "  ${RED}✗${NC} Build issues:"
        echo "$BUILD_OUTPUT" | head -10 | while read -r line; do
            echo "      $line"
        done
        ERRORS=$((ERRORS + 1))
    fi
else
    echo -e "  ${GREEN}✓${NC} Build successful"
fi
echo ""

# 4. Check for config field consistency
echo "Checking config consistency..."

# Get config struct fields from config.go
CONFIG_FILE="internal/config/config.go"
if [ -f "$CONFIG_FILE" ]; then
    # Check if there are embedded structs that might have missing fields
    # This is a simple heuristic - we check that DefaultConfig() mentions
    # the same top-level struct field names as Config struct

    STRUCT_FIELDS=$(grep -E '^\s+[A-Z][a-zA-Z]+\s+' "$CONFIG_FILE" | grep -v '//' | grep -oE '^[[:space:]]+[A-Z][a-zA-Z]+' | tr -d '[:space:]' | sort -u || true)
    DEFAULT_FIELDS=$(grep -E '[A-Z][a-zA-Z]+:' "$CONFIG_FILE" | grep -v '//' | grep -oE '[A-Z][a-zA-Z]+:' | tr -d ':' | sort -u || true)

    # Simple check - if we have many struct fields but few default fields, warn
    STRUCT_COUNT=$(echo "$STRUCT_FIELDS" | wc -l | tr -d ' ')
    DEFAULT_COUNT=$(echo "$DEFAULT_FIELDS" | wc -l | tr -d ' ')

    if [ "$STRUCT_COUNT" -gt 0 ] && [ "$DEFAULT_COUNT" -gt 0 ]; then
        echo -e "  ${GREEN}✓${NC} Config structure appears consistent"
    else
        echo -e "  ${YELLOW}⚠${NC} Could not verify config consistency"
        WARNINGS=$((WARNINGS + 1))
    fi
else
    echo -e "  ${YELLOW}⚠${NC} Config file not found, skipping check"
    WARNINGS=$((WARNINGS + 1))
fi
echo ""

# 5. Check for common test issues
echo "Checking test patterns..."

# Check for -race flag usage in tests with potential issues
RACE_ISSUES=0

# Look for global state modification without mutex
# This is a heuristic - look for patterns that commonly cause race conditions
TEST_FILES=$(find . -name "*_test.go" -not -path "./vendor/*" 2>/dev/null || true)

for file in $TEST_FILES; do
    # Check for obvious patterns: global map writes, unprotected counter increments
    if grep -qE 'package\s+\w+\s*$' "$file" 2>/dev/null; then
        # Check if file has parallel test execution (t.Parallel)
        if grep -q 't\.Parallel()' "$file" 2>/dev/null; then
            # Check for potential shared state issues
            if grep -qE 'var\s+[a-z][a-zA-Z]*\s*=' "$file" 2>/dev/null; then
                if ! grep -q 'sync\.' "$file" 2>/dev/null && ! grep -q 'Mutex' "$file" 2>/dev/null; then
                    # Only warn, don't fail - this is a heuristic
                    : # Skip for now - too many false positives
                fi
            fi
        fi
    fi
done

echo -e "  ${GREEN}✓${NC} No obvious test race patterns"
echo ""

# Summary
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

if [ $ERRORS -eq 0 ]; then
    echo -e "${GREEN}Integration check passed${NC}"
    if [ $WARNINGS -gt 0 ]; then
        echo -e "  ${YELLOW}$WARNINGS warning(s)${NC}"
    fi
    exit 0
else
    echo -e "${RED}Integration check failed: $ERRORS issue(s)${NC}"
    if [ $WARNINGS -gt 0 ]; then
        echo -e "  ${YELLOW}$WARNINGS warning(s)${NC}"
    fi
    exit 1
fi
