#!/bin/bash
# Pilot Stop Gate: verify build + tests before Claude finishes
# Exit code 2 tells Claude to continue fixing issues

set -euo pipefail

cd "$CLAUDE_PROJECT_DIR"

echo "🔍 Pilot Stop Gate: Verifying build and tests..."

# Go project
if [ -f go.mod ]; then
    echo "📦 Running go build..."
    if ! go build ./... 2>&1; then
        echo "❌ Build failed. Fix compilation errors before finishing." >&2
        exit 2
    fi

    echo "🧪 Running go test..."
    if ! go test ./... -count=1 -timeout 120s 2>&1; then
        echo "❌ Tests failed. Fix failing tests before finishing." >&2
        exit 2
    fi

    echo "✅ Go build and tests passed"
    exit 0
fi

# Node.js project
if [ -f package.json ]; then
    # Check if npm test script exists
    if npm run | grep -q "test"; then
        echo "🧪 Running npm test..."
        if ! npm test 2>&1; then
            echo "❌ Tests failed. Fix failing tests before finishing." >&2
            exit 2
        fi

        echo "✅ npm tests passed"
    else
        echo "ℹ️  No npm test script found, skipping tests"
    fi

    # Try npm run build if available
    if npm run | grep -q "build"; then
        echo "📦 Running npm run build..."
        if ! npm run build 2>&1; then
            echo "❌ Build failed. Fix build errors before finishing." >&2
            exit 2
        fi

        echo "✅ npm build passed"
    else
        echo "ℹ️  No npm build script found, skipping build"
    fi

    exit 0
fi

# Python project
if [ -f requirements.txt ] || [ -f pyproject.toml ] || [ -f setup.py ]; then
    # Try pytest first, then python -m pytest, then skip
    if command -v pytest >/dev/null 2>&1; then
        echo "🧪 Running pytest..."
        if ! pytest 2>&1; then
            echo "❌ Tests failed. Fix failing tests before finishing." >&2
            exit 2
        fi
        echo "✅ pytest passed"
    elif python -m pytest --version >/dev/null 2>&1; then
        echo "🧪 Running python -m pytest..."
        if ! python -m pytest 2>&1; then
            echo "❌ Tests failed. Fix failing tests before finishing." >&2
            exit 2
        fi
        echo "✅ pytest passed"
    else
        echo "ℹ️  No pytest found, skipping Python tests"
    fi

    exit 0
fi

# Rust project
if [ -f Cargo.toml ]; then
    echo "📦 Running cargo build..."
    if ! cargo build 2>&1; then
        echo "❌ Build failed. Fix compilation errors before finishing." >&2
        exit 2
    fi

    echo "🧪 Running cargo test..."
    if ! cargo test 2>&1; then
        echo "❌ Tests failed. Fix failing tests before finishing." >&2
        exit 2
    fi

    echo "✅ Rust build and tests passed"
    exit 0
fi

# Makefile project
if [ -f Makefile ] || [ -f makefile ]; then
    # Try common make targets
    if make -n test >/dev/null 2>&1; then
        echo "🧪 Running make test..."
        if ! make test 2>&1; then
            echo "❌ Tests failed. Fix failing tests before finishing." >&2
            exit 2
        fi
        echo "✅ make test passed"
    elif make -n build >/dev/null 2>&1; then
        echo "📦 Running make build..."
        if ! make build 2>&1; then
            echo "❌ Build failed. Fix build errors before finishing." >&2
            exit 2
        fi
        echo "✅ make build passed"
    else
        echo "ℹ️  No test or build targets found in Makefile"
    fi

    exit 0
fi

echo "ℹ️  No recognized project type found, skipping quality gate"
exit 0