#!/bin/bash
# Auto-fix common issues that cause CI failures
# Only applies SAFE fixes - no breaking changes

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

cd "$PROJECT_ROOT"

FIXED=0
SUGGESTED=0

echo ""
echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${BLUE}           AUTO-FIX COMMON ISSUES                   ${NC}"
echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo ""

# 1. Run go fmt
echo "Running go fmt..."
FMT_OUTPUT=$(go fmt ./... 2>&1)
if [ -n "$FMT_OUTPUT" ]; then
    echo -e "  ${GREEN}✓${NC} Formatted files:"
    echo "$FMT_OUTPUT" | while read -r line; do
        echo "      $line"
    done
    FIXED=$((FIXED + 1))
else
    echo -e "  ${GREEN}✓${NC} Already formatted"
fi
echo ""

# 2. Run goimports to fix import ordering and remove unused imports
echo "Running goimports..."
if command -v goimports >/dev/null 2>&1; then
    IMPORT_CHANGES=$(goimports -l . 2>/dev/null | grep -v vendor || true)
    if [ -n "$IMPORT_CHANGES" ]; then
        goimports -w . 2>/dev/null
        echo -e "  ${GREEN}✓${NC} Fixed imports in:"
        echo "$IMPORT_CHANGES" | head -10 | while read -r line; do
            echo "      $line"
        done
        FIXED=$((FIXED + 1))
    else
        echo -e "  ${GREEN}✓${NC} Imports already clean"
    fi
else
    echo -e "  ${YELLOW}⚠${NC} goimports not installed"
    echo "      Install: go install golang.org/x/tools/cmd/goimports@latest"
fi
echo ""

# 3. Run go mod tidy
echo "Running go mod tidy..."
TIDY_BEFORE=$(cat go.sum 2>/dev/null | wc -l || echo 0)
go mod tidy 2>/dev/null
TIDY_AFTER=$(cat go.sum 2>/dev/null | wc -l || echo 0)

if [ "$TIDY_BEFORE" != "$TIDY_AFTER" ]; then
    echo -e "  ${GREEN}✓${NC} Tidied go.mod (deps changed: $TIDY_BEFORE -> $TIDY_AFTER)"
    FIXED=$((FIXED + 1))
else
    echo -e "  ${GREEN}✓${NC} Dependencies already tidy"
fi
echo ""

# 4. Check for and suggest missing build tags on platform-specific files
echo "Checking platform-specific files..."
for suffix in "_darwin" "_linux" "_windows"; do
    FILES=$(find . -name "*${suffix}.go" -not -path "./vendor/*" 2>/dev/null || true)
    for file in $FILES; do
        if ! head -10 "$file" | grep -qE '//go:build|// \+build'; then
            # Extract platform from suffix
            PLATFORM=${suffix#_}

            echo -e "  ${YELLOW}⚠${NC} Missing build tag: $file"
            echo -e "      Suggestion: Add at top of file:"
            echo -e "      ${BLUE}//go:build $PLATFORM${NC}"
            echo -e "      ${BLUE}// +build $PLATFORM${NC}"
            echo ""
            SUGGESTED=$((SUGGESTED + 1))
        fi
    done
done

if [ $SUGGESTED -eq 0 ]; then
    echo -e "  ${GREEN}✓${NC} All platform files have build tags"
fi
echo ""

# 5. Check for orphan commands and suggest wiring
echo "Checking command wiring..."
CMD_FUNCS=$(grep -rh 'func new[A-Z][a-zA-Z]*Cmd\(\)' cmd/pilot/*.go 2>/dev/null | grep -oE 'new[A-Z][a-zA-Z]*Cmd' | sort -u || true)

ORPHANS=""
if [ -n "$CMD_FUNCS" ]; then
    for func in $CMD_FUNCS; do
        USED_IN_ADDCMD=$(grep -rh "AddCommand.*${func}()" cmd/pilot/*.go 2>/dev/null || true)
        CALLED=$(grep -rh "${func}()" cmd/pilot/*.go 2>/dev/null | grep -v "^func " || true)
        SUBCOMMAND_USAGE=$(grep -rh "\.AddCommand(${func}()" cmd/pilot/*.go 2>/dev/null || true)

        if [ -z "$USED_IN_ADDCMD" ] && [ -z "$CALLED" ] && [ -z "$SUBCOMMAND_USAGE" ]; then
            if [ -z "$ORPHANS" ]; then
                ORPHANS="$func"
            else
                ORPHANS="$ORPHANS $func"
            fi
        fi
    done
fi

if [ -n "$ORPHANS" ]; then
    echo -e "  ${YELLOW}⚠${NC} Found orphan commands. Add to rootCmd.AddCommand():"
    for orphan in $ORPHANS; do
        echo -e "      ${BLUE}$orphan(),${NC}"
    done
    SUGGESTED=$((SUGGESTED + 1))
else
    echo -e "  ${GREEN}✓${NC} All commands wired"
fi
echo ""

# 6. Run golangci-lint with --fix if available
echo "Running golangci-lint --fix..."
if command -v golangci-lint >/dev/null 2>&1; then
    # Only run fixable linters
    LINT_FIX_OUTPUT=$(golangci-lint run --fix --timeout 60s 2>&1) || true
    if echo "$LINT_FIX_OUTPUT" | grep -q "issues found"; then
        echo -e "  ${YELLOW}⚠${NC} Some issues remain (not auto-fixable)"
        SUGGESTED=$((SUGGESTED + 1))
    else
        echo -e "  ${GREEN}✓${NC} Lint issues auto-fixed"
        FIXED=$((FIXED + 1))
    fi
else
    echo -e "  ${YELLOW}⚠${NC} golangci-lint not installed"
fi
echo ""

# Summary
echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "  Auto-fixed:  ${GREEN}$FIXED item(s)${NC}"
echo -e "  Suggestions: ${YELLOW}$SUGGESTED item(s)${NC}"
echo -e "${BLUE}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo ""

if [ $SUGGESTED -gt 0 ]; then
    echo "💡 Some issues require manual fixes. See suggestions above."
    exit 1
fi

exit 0
