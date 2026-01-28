#!/bin/bash
# Install git hooks for the pilot project
# Hooks installed:
#   - pre-commit: Secret pattern detection
#   - pre-push: Full validation gate (build, lint, test, secrets, integration)

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
HOOKS_DIR="$PROJECT_ROOT/.git/hooks"

echo "Installing git hooks..."

# Create hooks directory if it doesn't exist
mkdir -p "$HOOKS_DIR"

# Install pre-commit hook
cat > "$HOOKS_DIR/pre-commit" << 'EOF'
#!/bin/bash
# Pre-commit hook to prevent realistic-looking secrets in test files
# This helps avoid GitHub push protection blocks

set -e

echo "Checking for realistic secret patterns in staged files..."

# Get list of staged Go test files
STAGED_FILES=$(git diff --cached --name-only --diff-filter=ACM | grep '_test\.go$' || true)

if [ -z "$STAGED_FILES" ]; then
    exit 0
fi

# Patterns that look like real secrets (will trigger GitHub push protection)
# These are regex patterns for grep -E
PATTERNS=(
    'xoxb-[0-9]{10,}-[0-9]{10,}'           # Slack bot token
    'xoxa-[0-9]{10,}-[0-9]{10,}'           # Slack app token
    'xoxp-[0-9]{10,}-[0-9]{10,}'           # Slack user token
    'sk-[a-zA-Z0-9]{32,}'                   # OpenAI API key
    'ghp_[a-zA-Z0-9]{36}'                   # GitHub PAT
    'gho_[a-zA-Z0-9]{36}'                   # GitHub OAuth token
    'ghu_[a-zA-Z0-9]{36}'                   # GitHub user-to-server token
    'ghs_[a-zA-Z0-9]{36}'                   # GitHub server-to-server token
    'github_pat_[a-zA-Z0-9]{22}_[a-zA-Z0-9]{59}'  # GitHub fine-grained PAT
    'AKIA[0-9A-Z]{16}'                      # AWS access key ID
    'lin_api_[a-zA-Z0-9]{40}'               # Linear API key
)

FOUND_SECRETS=0

for file in $STAGED_FILES; do
    if [ -f "$file" ]; then
        for pattern in "${PATTERNS[@]}"; do
            if grep -qE "$pattern" "$file" 2>/dev/null; then
                echo ""
                echo "❌ ERROR: Found realistic-looking secret pattern in: $file"
                echo "   Pattern: $pattern"
                echo ""
                grep -nE "$pattern" "$file" | head -3
                echo ""
                FOUND_SECRETS=1
            fi
        done
    fi
done

if [ $FOUND_SECRETS -eq 1 ]; then
    echo ""
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo "COMMIT BLOCKED: Realistic secret patterns detected"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo ""
    echo "GitHub's push protection will block these patterns."
    echo ""
    echo "Instead, use obviously fake tokens:"
    echo "  ✅ test-slack-bot-token"
    echo "  ✅ fake-api-key"
    echo "  ✅ Constants from internal/testutil/tokens.go"
    echo ""
    echo "To bypass this check (not recommended):"
    echo "  git commit --no-verify"
    echo ""
    exit 1
fi

echo "✓ No realistic secret patterns found"
exit 0
EOF

chmod +x "$HOOKS_DIR/pre-commit"

echo "✓ Pre-commit hook installed"

# Install pre-push hook
cat > "$HOOKS_DIR/pre-push" << 'EOF'
#!/bin/bash
# Pre-push hook - runs full validation gate before push
# This ensures CI will pass on first attempt

set -e

# Find the project root (where .git is)
HOOK_DIR="$(dirname "$0")"
PROJECT_ROOT="$(cd "$HOOK_DIR/../.." && pwd)"
GATE_SCRIPT="$PROJECT_ROOT/scripts/pre-push-gate.sh"

# Check if gate script exists
if [ ! -x "$GATE_SCRIPT" ]; then
    echo "⚠️  Pre-push gate script not found: $GATE_SCRIPT"
    echo "   Run 'make install-hooks' to reinstall"
    exit 0  # Allow push if script missing (don't block on missing script)
fi

echo ""
echo "🚦 Running pre-push validation gate..."
echo ""

# Run the gate
if ! "$GATE_SCRIPT"; then
    echo ""
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo "❌ PUSH BLOCKED: Gate validation failed"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo ""
    echo "Fix the issues above before pushing."
    echo ""
    echo "Quick fixes:"
    echo "  make auto-fix     # Auto-fix common issues"
    echo "  make gate         # Re-run gate manually"
    echo ""
    echo "To bypass (not recommended):"
    echo "  git push --no-verify"
    echo ""
    exit 1
fi

echo ""
echo "✅ Gate passed - proceeding with push"
echo ""
EOF

chmod +x "$HOOKS_DIR/pre-push"

echo "✓ Pre-push hook installed"
echo ""
echo "Hooks installed:"
echo "  • pre-commit: Checks for realistic secrets in test files"
echo "  • pre-push:   Runs full validation gate (build, lint, test, secrets)"
echo ""
echo "Use '--no-verify' to bypass hooks (not recommended)."
