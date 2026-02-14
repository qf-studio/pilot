#!/bin/bash
# Pilot installer script
# Usage: curl -fsSL https://get.pilot.dev | sh

set -e

REPO="alekspetrov/pilot"
BINARY_NAME="pilot"
INSTALL_DIR="$HOME/.local/bin"

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
    # Determine archive extension based on OS
    if [ "$OS" = "windows" ]; then
        ARCHIVE_EXT="zip"
    else
        ARCHIVE_EXT="tar.gz"
    fi

    ARCHIVE_NAME="${BINARY_NAME}-${PLATFORM}.${ARCHIVE_EXT}"
    DOWNLOAD_URL="https://github.com/${REPO}/releases/download/${VERSION}/${ARCHIVE_NAME}"

    info "Downloading from $DOWNLOAD_URL..."

    # Create temp directory for extraction
    TMP_DIR=$(mktemp -d)
    trap "rm -rf $TMP_DIR" EXIT

    TMP_ARCHIVE="$TMP_DIR/$ARCHIVE_NAME"

    if ! curl -fsSL "$DOWNLOAD_URL" -o "$TMP_ARCHIVE"; then
        error "Failed to download archive"
    fi

    # Extract binary from archive
    info "Extracting..."
    if [ "$ARCHIVE_EXT" = "zip" ]; then
        unzip -q "$TMP_ARCHIVE" -d "$TMP_DIR"
    else
        tar -xzf "$TMP_ARCHIVE" -C "$TMP_DIR"
    fi

    # Find the pilot binary in extracted files
    EXTRACTED_BINARY="$TMP_DIR/$BINARY_NAME"
    if [ ! -f "$EXTRACTED_BINARY" ]; then
        error "Binary not found in archive"
    fi

    chmod +x "$EXTRACTED_BINARY"

    # Create install directory if needed (no sudo required for ~/.local/bin)
    if [ ! -d "$INSTALL_DIR" ]; then
        info "Creating $INSTALL_DIR..."
        mkdir -p "$INSTALL_DIR"
    fi

    # Install
    info "Installing to $INSTALL_DIR..."
    mv "$EXTRACTED_BINARY" "$INSTALL_DIR/$BINARY_NAME"
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

# Detect user's shell and return rc file path
detect_shell_rc() {
    SHELL_NAME=$(basename "$SHELL")

    case "$SHELL_NAME" in
        zsh)
            echo "$HOME/.zshrc"
            ;;
        bash)
            # Prefer .bashrc, fall back to .bash_profile on macOS
            if [ -f "$HOME/.bashrc" ]; then
                echo "$HOME/.bashrc"
            else
                echo "$HOME/.bash_profile"
            fi
            ;;
        fish)
            echo "$HOME/.config/fish/config.fish"
            ;;
        *)
            # Fallback to .profile for other shells
            echo "$HOME/.profile"
            ;;
    esac
}

# Configure PATH in shell rc file
configure_path() {
    RC_FILE=$(detect_shell_rc)
    SHELL_NAME=$(basename "$SHELL")
    PATH_LINE=""

    # Different syntax for fish vs other shells
    if [ "$SHELL_NAME" = "fish" ]; then
        PATH_LINE='set -gx PATH "$HOME/.local/bin" $PATH'
        PATH_CHECK='.local/bin'
    else
        PATH_LINE='export PATH="$HOME/.local/bin:$PATH"'
        PATH_CHECK='.local/bin'
    fi

    # Check if PATH already configured
    if [ -f "$RC_FILE" ] && grep -q "$PATH_CHECK" "$RC_FILE" 2>/dev/null; then
        info "PATH already configured in $RC_FILE"
        return 0
    fi

    # Create rc file directory if needed (for fish)
    RC_DIR=$(dirname "$RC_FILE")
    if [ ! -d "$RC_DIR" ]; then
        mkdir -p "$RC_DIR"
    fi

    # Add PATH configuration
    info "Adding PATH to $RC_FILE..."
    echo "" >> "$RC_FILE"
    echo "# Added by Pilot installer" >> "$RC_FILE"
    echo "$PATH_LINE" >> "$RC_FILE"

    PATH_CONFIGURED=true
}

# Verify installation
verify_installation() {
    # Add to current session PATH for verification
    export PATH="$INSTALL_DIR:$PATH"

    if command -v pilot &> /dev/null; then
        INSTALLED_VERSION=$(pilot version 2>/dev/null || echo "unknown")
        info "✅ Verified: pilot $INSTALLED_VERSION"
        return 0
    else
        warn "Could not verify installation"
        return 1
    fi
}

# Print post-install instructions
print_instructions() {
    RC_FILE=$(detect_shell_rc)
    SHELL_NAME=$(basename "$SHELL")

    echo ""
    info "✅ Pilot installed successfully!"
    echo ""

    if [ "$PATH_CONFIGURED" = true ]; then
        echo -e "${YELLOW}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
        echo -e "${YELLOW}  PATH was updated. To use pilot now, either:${NC}"
        echo ""
        echo -e "    ${GREEN}1.${NC} Run: source $RC_FILE"
        echo -e "    ${GREEN}2.${NC} Or open a new terminal"
        echo -e "${YELLOW}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
        echo ""
    fi

    echo "Get started:"
    echo "  pilot version  # Verify installation"
    echo "  pilot init     # Initialize configuration"
    echo "  pilot start    # Start the daemon"
    echo ""
}

main() {
    echo "🚀 Pilot Installer"
    echo "=================="
    echo ""

    PATH_CONFIGURED=false

    check_dependencies
    detect_platform
    get_latest_version
    install
    configure_path
    verify_installation
    print_instructions
}

main "$@"
