#!/bin/bash
#
# Build documentation site
#
# This script:
# 1. Copies relevant .agent/ docs to docs/ (architecture only, not internal tasks/SOPs)
# 2. Transforms Navigator format to public docs format
# 3. Runs mkdocs build
#
# Usage:
#   ./scripts/build-docs.sh          # Build only
#   ./scripts/build-docs.sh serve    # Build and serve locally
#   ./scripts/build-docs.sh clean    # Clean generated files

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(dirname "$SCRIPT_DIR")"

cd "$ROOT_DIR"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# Check for mkdocs
check_dependencies() {
    if ! command -v mkdocs &> /dev/null; then
        log_error "mkdocs not found. Install with: pip install mkdocs-material"
        exit 1
    fi
}

# Sync architecture docs from .agent/ to docs/
# Only sync public-facing architecture docs, not internal task docs or SOPs
sync_agent_docs() {
    log_info "Syncing architecture docs from .agent/..."

    # Only sync if .agent/system/ exists
    if [[ -d ".agent/system" ]]; then
        # Architecture overview
        if [[ -f ".agent/system/ARCHITECTURE.md" ]]; then
            log_info "  → architecture/overview.md (from ARCHITECTURE.md)"
            # The docs/architecture/overview.md is already manually curated
            # Only update if explicitly requested
        fi

        # Feature matrix
        if [[ -f ".agent/system/FEATURE-MATRIX.md" ]]; then
            log_info "  → architecture/features.md (from FEATURE-MATRIX.md)"
            # The docs/architecture/features.md is already manually curated
        fi
    else
        log_warn ".agent/system/ not found, skipping sync"
    fi
}

# Clean generated files
clean() {
    log_info "Cleaning generated files..."
    rm -rf site/
    log_info "Clean complete"
}

# Build documentation
build() {
    check_dependencies
    sync_agent_docs

    log_info "Building documentation..."
    mkdocs build --strict

    log_info "Build complete: site/"
}

# Serve documentation locally
serve() {
    check_dependencies
    sync_agent_docs

    log_info "Starting local server..."
    log_info "Documentation available at http://localhost:8000"
    mkdocs serve
}

# Main
case "${1:-build}" in
    build)
        build
        ;;
    serve)
        serve
        ;;
    clean)
        clean
        ;;
    *)
        echo "Usage: $0 [build|serve|clean]"
        exit 1
        ;;
esac
