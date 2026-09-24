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
#
# GH-5437: the pattern list itself now lives in a single canonical file
# (internal/executor/secretpatterns/patterns.txt) shared with the Go
# executor's acceptance-evidence output redaction, so the two never drift
# apart. Add new patterns there, not here.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PATTERNS_FILE="$SCRIPT_DIR/../internal/executor/secretpatterns/patterns.txt"
PATTERNS=()
while IFS= read -r pattern_line; do
    [[ -z "$pattern_line" || "$pattern_line" == \#* ]] && continue
    PATTERNS+=("$pattern_line")
done < "$PATTERNS_FILE"

if [ "${#PATTERNS[@]}" -eq 0 ]; then
    echo "❌ ERROR: no patterns loaded from $PATTERNS_FILE"
    exit 2
fi

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
    'internal/executor/secretpatterns/patterns.txt'   # canonical pattern list (GH-5437) — same rationale
    'internal/executor/secretpatterns/patterns_test.go'      # asserts regexes match known secret *shapes* (GH-5437)
    'internal/executor/acceptance_evidence_run_test.go'      # asserts redaction strips known secret *shapes* (GH-5437)
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

# ---------------------------------------------------------------------------
# Private denylist (2026-09-24): this repository is PUBLIC. Client-engagement
# identifiers (client names, their repos, ticket prefixes, prices) must never
# land here — they belong in the private client workspace. The list itself is
# private by construction: it lives OUTSIDE the repo and is read only if
# present. Override the path with PILOT_PRIVATE_DENYLIST.
# ---------------------------------------------------------------------------
DENYLIST="${PILOT_PRIVATE_DENYLIST:-$HOME/.config/quantflow/private-denylist.txt}"
if [ -f "$DENYLIST" ]; then
    DENY_HITS=0
    while IFS= read -r needle; do
        [[ -z "$needle" || "$needle" == \#* ]] && continue
        HITS=$(xargs grep -HniF -- "$needle" < "$TMPFILE" 2>/dev/null || true)
        if [ -n "$HITS" ]; then
            echo "❌ private-denylist hit (string not echoed):"
            echo "$HITS" | cut -d: -f1,2 | sed 's/^/   /' | head -20
            DENY_HITS=$((DENY_HITS + 1))
        fi
    done < "$DENYLIST"
    if [ "$DENY_HITS" -gt 0 ]; then
        echo "❌ ERROR: $DENY_HITS private-denylist string(s) found in tracked files. This repo is public; move that content to the private client workspace."
        exit 1
    fi
    echo "✓ No private-denylist strings found"
fi

echo "✓ No realistic secret patterns found in $SCAN_COUNT scanned files"
exit 0
