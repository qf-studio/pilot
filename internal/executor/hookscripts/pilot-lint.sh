#!/bin/bash
# Pilot Lint: auto-format/lint files after Edit/Write tools (PostToolUse hook)
# This is an opt-in feature (lint_on_save: true)

set -euo pipefail

cd "$CLAUDE_PROJECT_DIR"

# Read the hook input to get the file path
INPUT=$(cat)

# Extract file path from tool input
if command -v jq >/dev/null 2>&1; then
    FILE_PATH=$(echo "$INPUT" | jq -r '.tool_input.file_path // empty' 2>/dev/null || echo "")
else
    # Fallback without jq
    FILE_PATH=$(echo "$INPUT" | grep -o '"file_path"[[:space:]]*:[[:space:]]*"[^"]*"' | sed 's/.*"file_path"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/' || echo "")
fi

if [ -z "$FILE_PATH" ]; then
    # No file path found, nothing to lint
    exit 0
fi

# Make path relative to project directory if absolute
if [[ "$FILE_PATH" == /* ]]; then
    RELATIVE_PATH=$(realpath --relative-to="$CLAUDE_PROJECT_DIR" "$FILE_PATH" 2>/dev/null || echo "$FILE_PATH")
else
    RELATIVE_PATH="$FILE_PATH"
fi

# Only lint if file exists
if [ ! -f "$RELATIVE_PATH" ]; then
    exit 0
fi

echo "🧹 Pilot Lint: Auto-formatting $RELATIVE_PATH"

# Determine file type and apply appropriate formatter
FILE_EXT="${RELATIVE_PATH##*.}"

case "$FILE_EXT" in
    go)
        if command -v gofmt >/dev/null 2>&1; then
            gofmt -w "$RELATIVE_PATH"
            echo "✅ Formatted Go file with gofmt"
        fi
        if command -v goimports >/dev/null 2>&1; then
            goimports -w "$RELATIVE_PATH"
            echo "✅ Organized imports with goimports"
        fi
        ;;

    js|jsx|ts|tsx)
        # Try prettier first, then eslint
        if command -v prettier >/dev/null 2>&1; then
            prettier --write "$RELATIVE_PATH" 2>/dev/null && echo "✅ Formatted JavaScript/TypeScript with prettier"
        elif command -v eslint >/dev/null 2>&1; then
            eslint --fix "$RELATIVE_PATH" 2>/dev/null && echo "✅ Formatted JavaScript/TypeScript with eslint --fix"
        fi
        ;;

    py)
        # Try black first, then autopep8
        if command -v black >/dev/null 2>&1; then
            black "$RELATIVE_PATH" 2>/dev/null && echo "✅ Formatted Python with black"
        elif command -v autopep8 >/dev/null 2>&1; then
            autopep8 --in-place "$RELATIVE_PATH" 2>/dev/null && echo "✅ Formatted Python with autopep8"
        fi
        ;;

    rs)
        if command -v rustfmt >/dev/null 2>&1; then
            rustfmt "$RELATIVE_PATH" && echo "✅ Formatted Rust with rustfmt"
        fi
        ;;

    json)
        if command -v jq >/dev/null 2>&1; then
            temp_file=$(mktemp)
            if jq . "$RELATIVE_PATH" > "$temp_file" 2>/dev/null; then
                mv "$temp_file" "$RELATIVE_PATH"
                echo "✅ Formatted JSON with jq"
            else
                rm -f "$temp_file"
            fi
        elif command -v prettier >/dev/null 2>&1; then
            prettier --write "$RELATIVE_PATH" 2>/dev/null && echo "✅ Formatted JSON with prettier"
        fi
        ;;

    yaml|yml)
        if command -v prettier >/dev/null 2>&1; then
            prettier --write "$RELATIVE_PATH" 2>/dev/null && echo "✅ Formatted YAML with prettier"
        fi
        ;;

    *)
        echo "ℹ️  No formatter available for $FILE_EXT files"
        ;;
esac

exit 0