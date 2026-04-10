#!/bin/bash
# Quick-run script for Pilot Terminal-Bench agent.
#
# Usage:
#   ./run.sh                    # Run 1 task (smoke test)
#   ./run.sh 10                 # Run 10 tasks
#   ./run.sh all                # Run all tasks
#   ./run.sh all 8              # Run all tasks with 8 concurrent
#
# Prerequisites:
#   - Harbor installed: pip install harbor
#   - Docker running
#   - Claude Code auth (ANTHROPIC_API_KEY or CLAUDE_CODE_OAUTH_TOKEN)

set -euo pipefail

N_TASKS="${1:-1}"
N_CONCURRENT="${2:-1}"
MODEL="${MODEL:-anthropic/claude-opus-4-6}"

cd "$(dirname "$0")"

# Auto-extract OAuth token from Claude Code keychain if no API key set
if [ -z "${ANTHROPIC_API_KEY:-}" ] && [ -z "${CLAUDE_CODE_OAUTH_TOKEN:-}" ]; then
    CREDS=$(security find-generic-password -s "Claude Code-credentials" -w 2>/dev/null || true)
    if [ -n "$CREDS" ]; then
        export CLAUDE_CODE_OAUTH_TOKEN=$(echo "$CREDS" | python3 -c "import sys,json; print(json.load(sys.stdin)['claudeAiOauth']['accessToken'])" 2>/dev/null || true)
        [ -n "$CLAUDE_CODE_OAUTH_TOKEN" ] && echo "[pilot] OAuth token extracted from keychain"
    fi
fi

echo "=== Pilot Terminal-Bench Agent ==="
echo "Tasks:      ${N_TASKS}"
echo "Concurrent: ${N_CONCURRENT}"
echo "Model:      ${MODEL}"
echo ""

TASK_ARGS=""
if [ "$N_TASKS" != "all" ]; then
    TASK_ARGS="-l $N_TASKS"
fi

harbor run \
    -d terminal-bench@2.0 \
    --agent-import-path "pilot_agent:PilotAgent" \
    -m "$MODEL" \
    -e docker \
    -n "$N_CONCURRENT" \
    $TASK_ARGS \
    --ae "CLAUDE_CODE_OAUTH_TOKEN=${CLAUDE_CODE_OAUTH_TOKEN:-}" \
    --ae "ANTHROPIC_API_KEY=${ANTHROPIC_API_KEY:-}" \
    --debug
