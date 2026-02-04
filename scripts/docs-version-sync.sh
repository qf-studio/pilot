#!/bin/bash
# Updates version references in Navigator docs
# Usage: ./scripts/docs-version-sync.sh v0.13.0

set -e

VERSION="${1:-}"
if [ -z "$VERSION" ]; then
    echo "Usage: $0 <version>"
    exit 1
fi

# Strip 'v' prefix for display version
DISPLAY_VERSION="${VERSION#v}"
DATE=$(date +%Y-%m-%d)

echo "Syncing docs to version $VERSION ($DATE)"

# Files to update
FILES=(
    ".agent/DEVELOPMENT-README.md"
    ".agent/system/FEATURE-MATRIX.md"
)

updated=0

for file in "${FILES[@]}"; do
    if [ -f "$file" ]; then
        # macOS sed requires '' after -i, Linux doesn't
        # Use a temp file approach for cross-platform compatibility

        # Update "**Current Version:** vX.Y.Z" pattern
        if grep -q "Current Version:" "$file"; then
            if [[ "$OSTYPE" == "darwin"* ]]; then
                sed -i '' "s/\*\*Current Version:\*\* v[0-9]*\.[0-9]*\.[0-9]*/\*\*Current Version:\*\* $VERSION/g" "$file"
            else
                sed -i "s/\*\*Current Version:\*\* v[0-9]*\.[0-9]*\.[0-9]*/\*\*Current Version:\*\* $VERSION/g" "$file"
            fi
        fi

        # Update "**Last Updated:** YYYY-MM-DD" pattern
        if grep -q "Last Updated:" "$file"; then
            if [[ "$OSTYPE" == "darwin"* ]]; then
                sed -i '' "s/\*\*Last Updated:\*\* [0-9]\{4\}-[0-9]\{2\}-[0-9]\{2\}/\*\*Last Updated:\*\* $DATE/g" "$file"
            else
                sed -i "s/\*\*Last Updated:\*\* [0-9]\{4\}-[0-9]\{2\}-[0-9]\{2\}/\*\*Last Updated:\*\* $DATE/g" "$file"
            fi
        fi

        echo "  Updated $file"
        ((++updated))
    else
        echo "  Skipped $file (not found)"
    fi
done

if [ $updated -gt 0 ]; then
    echo "Version synced to $VERSION in $updated file(s)"
else
    echo "No files were updated"
    exit 1
fi
