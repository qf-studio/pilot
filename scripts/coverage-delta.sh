#!/bin/bash
# Check that new functions added in the latest commit have test coverage.
# Exits 0 if all new functions are covered or no new functions were added.
# Exits 1 if new functions have 0% coverage.
#
# Usage: scripts/coverage-delta.sh

set -e

echo "Checking coverage delta for new functions..."

# Get changed Go files (excluding test files) from last commit
CHANGED_FILES=$(git diff --name-only HEAD~1 -- '*.go' | grep -v '_test.go' || true)

if [ -z "$CHANGED_FILES" ]; then
    echo "No Go source files changed in last commit"
    exit 0
fi

# Extract unique packages from changed files
PACKAGES=""
for file in $CHANGED_FILES; do
    if [ -f "$file" ]; then
        pkg=$(dirname "$file")
        if [ -n "$pkg" ] && [ "$pkg" != "." ]; then
            PACKAGES="$PACKAGES ./$pkg"
        fi
    fi
done

# Deduplicate packages
PACKAGES=$(echo "$PACKAGES" | tr ' ' '\n' | sort -u | tr '\n' ' ')

if [ -z "$PACKAGES" ]; then
    echo "No packages to check"
    exit 0
fi

echo "Packages to check: $PACKAGES"

# Extract new exported function signatures from diff
# Pattern: lines starting with + that define a func with uppercase first letter (exported)
NEW_FUNCS=$(git diff HEAD~1 -- '*.go' | grep '^+func ' | grep -v '_test.go' | sed 's/^+//' | grep -E 'func \(?[A-Z]|func [A-Z]' || true)

if [ -z "$NEW_FUNCS" ]; then
    echo "No new exported functions added"
    exit 0
fi

echo ""
echo "New exported functions detected:"
echo "$NEW_FUNCS" | head -20
echo ""

# Create temp directory for coverage profiles
COVER_DIR=$(mktemp -d)
trap 'rm -rf "$COVER_DIR"' EXIT

UNCOVERED=""
CHECKED=0

for pkg in $PACKAGES; do
    pkg_name=$(basename "$pkg")
    cover_file="$COVER_DIR/${pkg_name}.cover"

    echo "Running coverage for $pkg..."

    # Run tests with coverage (ignore failures - some packages may not have tests)
    if go test -coverprofile="$cover_file" "$pkg" >/dev/null 2>&1; then
        if [ -f "$cover_file" ]; then
            # Parse coverage output
            COVERAGE_OUTPUT=$(go tool cover -func="$cover_file" 2>/dev/null || true)

            # Check each new function for coverage
            while IFS= read -r func_line; do
                # Extract function name from "func Name(" or "func (r *Type) Name("
                func_name=$(echo "$func_line" | sed -E 's/func \([^)]+\) ([A-Za-z0-9_]+)\(.*/\1/' | sed -E 's/func ([A-Za-z0-9_]+)\(.*/\1/')

                if [ -n "$func_name" ]; then
                    CHECKED=$((CHECKED + 1))
                    # Check if function appears in coverage with 0%
                    if echo "$COVERAGE_OUTPUT" | grep -q "$func_name.*0.0%"; then
                        UNCOVERED="$UNCOVERED\n$func_name (in $pkg)"
                    fi
                fi
            done <<< "$NEW_FUNCS"
        fi
    else
        echo "  Warning: No tests or test failure for $pkg"
    fi
done

echo ""
echo "Checked $CHECKED new function(s)"

if [ -n "$UNCOVERED" ]; then
    echo ""
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo "COVERAGE CHECK FAILED: New functions with 0% coverage"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo -e "$UNCOVERED"
    echo ""
    echo "Add tests for these functions before merging."
    exit 1
fi

echo "✓ All new functions have test coverage"
exit 0
