#!/bin/bash
# Pilot Bash Guard: block destructive commands in PreToolUse:Bash hook
# Reads JSON input from stdin, outputs permission decision

set -euo pipefail

# Read the entire stdin input
INPUT=$(cat)

# Extract the command from the tool input using jq
# Handle case where jq might not be available
if command -v jq >/dev/null 2>&1; then
    COMMAND=$(echo "$INPUT" | jq -r '.tool_input.command // empty' 2>/dev/null || echo "")
else
    # Fallback: extract command using grep/sed (less reliable but works without jq)
    COMMAND=$(echo "$INPUT" | grep -o '"command"[[:space:]]*:[[:space:]]*"[^"]*"' | sed 's/.*"command"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/' || echo "")
fi

# Check for dangerous patterns
DANGEROUS_PATTERNS=(
    'rm -rf /'
    'rm -rf \$'
    'rm -rf ~'
    'rm -rf \*'
    'git push --force'
    'git push -f'
    'DROP TABLE'
    'DROP DATABASE'
    'DELETE FROM.*WHERE 1=1'
    'git reset --hard'
    'git clean -fd'
    'sudo rm'
    'sudo dd'
    'mkfs\.'
    'fdisk'
    'cfdisk'
    '> /dev/sd[a-z]'
    'chmod -R 777 /'
    'chown -R .* /'
    'systemctl stop'
    'systemctl disable'
    'service.*stop'
    'killall -9'
    'pkill -f .*'
    'reboot'
    'shutdown'
    'halt'
    'init 0'
    'init 6'
)

# Check if command contains any dangerous patterns
for pattern in "${DANGEROUS_PATTERNS[@]}"; do
    if echo "$COMMAND" | grep -qiE "$pattern"; then
        # Block the command - output hook-specific JSON response
        if command -v jq >/dev/null 2>&1; then
            jq -n --arg reason "Destructive command blocked by Pilot safety guard: $pattern" '{
                hookSpecificOutput: {
                    hookEventName: "PreToolUse",
                    permissionDecision: "deny",
                    permissionDecisionReason: $reason
                }
            }'
        else
            # Fallback JSON without jq
            echo "{\"hookSpecificOutput\":{\"hookEventName\":\"PreToolUse\",\"permissionDecision\":\"deny\",\"permissionDecisionReason\":\"Destructive command blocked by Pilot safety guard: $pattern\"}}"
        fi
        exit 0
    fi
done

# Command is safe - allow it to proceed (exit 0 without output)
exit 0