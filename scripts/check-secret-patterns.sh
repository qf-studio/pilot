#!/bin/bash
# Check for realistic-looking secret patterns in test files
# Used by CI to catch patterns that might have bypassed pre-commit hook

set -e

echo "Scanning for realistic secret patterns in test files..."

# Patterns that look like real secrets (will trigger GitHub push protection)
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

for pattern in "${PATTERNS[@]}"; do
    # Search in all test files
    MATCHES=$(grep -rE "$pattern" --include='*_test.go' . 2>/dev/null || true)
    if [ -n "$MATCHES" ]; then
        echo ""
        echo "❌ ERROR: Found realistic-looking secret pattern"
        echo "   Pattern: $pattern"
        echo ""
        echo "$MATCHES" | head -10
        echo ""
        FOUND_SECRETS=1
    fi
done

if [ $FOUND_SECRETS -eq 1 ]; then
    echo ""
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo "CI CHECK FAILED: Realistic secret patterns detected"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo ""
    echo "GitHub's push protection will block these patterns."
    echo ""
    echo "Instead, use obviously fake tokens:"
    echo "  ✅ test-slack-bot-token"
    echo "  ✅ fake-api-key"
    echo "  ✅ Constants from internal/testutil/tokens.go"
    echo ""
    exit 1
fi

echo "✓ No realistic secret patterns found in test files"
exit 0
