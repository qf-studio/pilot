#!/bin/bash
# Pilot installer script
# Usage: curl -fsSL https://get.pilot.dev | sh

set -e

REPO="alekspetrov/pilot"
BINARY_NAME="pilot"
INSTALL_DIR="/usr/local/bin"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

error() {
    echo -e "${RED}[ERROR]${NC} $1"
    exit 1
}

# Detect OS and architecture
detect_platform() {
    OS=$(uname -s | tr '[:upper:]' '[:lower:]')
    ARCH=$(uname -m)

    case "$ARCH" in
        x86_64|amd64)
            ARCH="amd64"
            ;;
        arm64|aarch64)
            ARCH="arm64"
            ;;
        *)
            error "Unsupported architecture: $ARCH"
            ;;
    esac

    case "$OS" in
        darwin)
            OS="darwin"
            ;;
        linux)
            OS="linux"
            ;;
        *)
            error "Unsupported OS: $OS"
            ;;
    esac

    PLATFORM="${OS}-${ARCH}"
    info "Detected platform: $PLATFORM"
}

# Get latest version from GitHub
get_latest_version() {
    info "Fetching latest version..."
    VERSION=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | sed -E 's/.*"([^"]+)".*/\1/')

    if [ -z "$VERSION" ]; then
        VERSION="v0.1.0"  # Fallback
        warn "Could not fetch latest version, using $VERSION"
    fi

    info "Latest version: $VERSION"
}

# Download and install
install() {
    DOWNLOAD_URL="https://github.com/${REPO}/releases/download/${VERSION}/${BINARY_NAME}-${PLATFORM}"

    info "Downloading from $DOWNLOAD_URL..."

    # Create temp file
    TMP_FILE=$(mktemp)
    trap "rm -f $TMP_FILE" EXIT

    if ! curl -fsSL "$DOWNLOAD_URL" -o "$TMP_FILE"; then
        error "Failed to download binary"
    fi

    chmod +x "$TMP_FILE"

    # Install
    info "Installing to $INSTALL_DIR..."

    if [ -w "$INSTALL_DIR" ]; then
        mv "$TMP_FILE" "$INSTALL_DIR/$BINARY_NAME"
    else
        sudo mv "$TMP_FILE" "$INSTALL_DIR/$BINARY_NAME"
    fi

    info "✅ Pilot installed successfully!"
    echo ""
    echo "Get started:"
    echo "  pilot init     # Initialize configuration"
    echo "  pilot start    # Start the daemon"
    echo ""
}

# Check dependencies
check_dependencies() {
    if ! command -v curl &> /dev/null; then
        error "curl is required but not installed"
    fi

    if ! command -v python3 &> /dev/null; then
        warn "python3 not found - orchestrator features may not work"
    fi

    if ! command -v claude &> /dev/null; then
        warn "Claude Code CLI not found - install from https://github.com/anthropics/claude-code"
    fi
}

main() {
    echo "🚀 Pilot Installer"
    echo "=================="
    echo ""

    check_dependencies
    detect_platform
    get_latest_version
    install
}

main "$@"
