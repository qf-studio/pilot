#!/bin/bash
# Refresh Claude Code OAuth token every 30 minutes
# Run in background: ./refresh-token.sh &

TOKEN_FILE="/tmp/pilot-bench-token"

refresh() {
    TOKEN=$(security find-generic-password -s "Claude Code-credentials" -w 2>/dev/null \
        | python3 -c "import sys,json; print(json.load(sys.stdin)['claudeAiOauth']['accessToken'])" 2>/dev/null)
    if [ -n "$TOKEN" ]; then
        echo -n "$TOKEN" > "$TOKEN_FILE"
        chmod 644 "$TOKEN_FILE"
        echo "[$(date)] Token refreshed (${#TOKEN} chars)"
    else
        echo "[$(date)] WARNING: Failed to refresh token"
    fi
}

# Initial write
refresh

# Refresh loop
while true; do
    sleep 1800  # 30 minutes
    refresh
done
