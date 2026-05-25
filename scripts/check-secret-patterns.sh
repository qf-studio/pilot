#!/bin/bash
# Check for realistic-looking secret patterns across all tracked files.
#
# History:
#   - Original (TASK-41): scanned only *_test.go files. That was the scope of
#     the original incident (push protection blocked 9 branches because
#     tests used realistic fake tokens).
#   - Broadened (TASK-299, 2026-05-25 audit): the test-only scope let any
#     non-test file (source, docs, configs) leak past this check. The
#     2026-05-25 sweep found xoxb-format examples in CLAUDE.md,
#     CONTRIBUTING.md, internal/testutil/tokens.go — those are intentional
#     educational content, so they're explicitly allowlisted below. All
#     other tracked files are now scanned.
#
# Used by CI (.github/workflows/ci.yml step: "Check Secret Patterns") AND by
# the pre-commit hook installed via `make install-hooks`.

set -e

echo "Scanning all tracked files for realistic secret patterns..."

# Patterns that look like real secrets (will trigger GitHub push protection).
# Add new patterns here when GitHub adds support for new providers.
PATTERNS=(
    'xoxb-[0-9]{10,}-[0-9]{10,}'                  # Slack bot token
    'xoxa-[0-9]{10,}-[0-9]{10,}'                  # Slack app token
    'xoxp-[0-9]{10,}-[0-9]{10,}'                  # Slack user token
    'sk-[a-zA-Z0-9]{32,}'                         # OpenAI / Anthropic-style API key
    'ghp_[a-zA-Z0-9]{36}'                         # GitHub PAT
    'gho_[a-zA-Z0-9]{36}'                         # GitHub OAuth token
    'ghu_[a-zA-Z0-9]{36}'                         # GitHub user-to-server token
    'ghs_[a-zA-Z0-9]{36}'                         # GitHub server-to-server token
    'github_pat_[a-zA-Z0-9]{22}_[a-zA-Z0-9]{59}'  # GitHub fine-grained PAT
    'AKIA[0-9A-Z]{16}'                            # AWS access key ID
    'lin_api_[a-zA-Z0-9]{40}'                     # Linear API key
)

# Files allowlisted from the scan because they intentionally show secret
# patterns for educational purposes (teach contributors what NOT to use).
# DO NOT add files here without a clear "this is educational content"
# justification — every entry weakens the check.
#
# Paths are matched as exact lines against `git ls-files` output (so they
# must be repo-root-relative, no leading "./").
ALLOWLIST=(
    'CLAUDE.md'                                       # forbidden-pattern examples in §"Test Token Guidelines"
    'CONTRIBUTING.md'                                 # same examples for external contributors
    'internal/testutil/tokens.go'                     # safe-token constants module, comments show what NOT to do
    '.agent/tasks/archive/TASK-41-test-secret-patterns.md'  # postmortem documenting the original incident
    'scripts/check-secret-patterns.sh'                # this script literally contains the patterns it detects
)

# Build a temp file of files to scan: tracked files minus the allowlist.
TMPFILE=$(mktemp)
trap 'rm -f "$TMPFILE"' EXIT
git ls-files | grep -vFx -f <(printf '%s\n' "${ALLOWLIST[@]}") > "$TMPFILE"

# Sanity: bail loudly if the file list is suspiciously short — the allowlist
# probably matched too much or git ls-files returned nothing (broken cwd).
SCAN_COUNT=$(wc -l < "$TMPFILE" | tr -d ' ')
if [ "$SCAN_COUNT" -lt 50 ]; then
    echo "❌ ERROR: only $SCAN_COUNT files queued for scan — allowlist or cwd misconfigured"
    exit 2
fi

FOUND_SECRETS=0

for pattern in "${PATTERNS[@]}"; do
    # xargs respects ARG_MAX and splits the file list if necessary.
    MATCHES=$(xargs grep -HnE "$pattern" < "$TMPFILE" 2>/dev/null || true)
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
    echo "  ✅ test-github-token"
    echo ""
    echo "Or use the constants in internal/testutil/tokens.go:"
    echo "  import \"github.com/qf-studio/pilot/internal/testutil\""
    echo "  token := testutil.FakeSlackBotToken"
    echo ""
    echo "If the match is intentional educational content (showing what NOT"
    echo "to use), add the file to the ALLOWLIST in scripts/check-secret-patterns.sh."
    echo ""
    exit 1
fi

echo "✓ No realistic secret patterns found in $SCAN_COUNT scanned files"
exit 0
