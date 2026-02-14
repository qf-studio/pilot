#!/bin/bash

# Pilot Development Setup Script
# This script helps set up a development environment for Pilot

set -e

echo "🚀 Setting up Pilot development environment..."

# Check if Go is installed
if ! command -v go &> /dev/null; then
    echo "❌ Go is not installed. Please install Go 1.24+ from https://golang.org/dl/"
    exit 1
fi

# Check Go version
GO_VERSION=$(go version | awk '{print $3}' | sed 's/go//')
MIN_VERSION="1.24"

if [ "$(printf '%s\n' "$MIN_VERSION" "$GO_VERSION" | sort -V | head -n1)" != "$MIN_VERSION" ]; then
    echo "❌ Go version $GO_VERSION is too old. Please install Go $MIN_VERSION or later."
    exit 1
fi

echo "✅ Go $GO_VERSION detected"

# Create config directory
CONFIG_DIR="$HOME/.pilot"
if [ ! -d "$CONFIG_DIR" ]; then
    echo "📁 Creating config directory: $CONFIG_DIR"
    mkdir -p "$CONFIG_DIR"
    mkdir -p "$CONFIG_DIR/logs"
fi

# Copy example config if it doesn't exist
CONFIG_FILE="$CONFIG_DIR/config.yaml"
if [ ! -f "$CONFIG_FILE" ]; then
    echo "⚙️  Copying example configuration..."
    cp example-config.yaml "$CONFIG_FILE"
    echo "✅ Configuration file created at $CONFIG_FILE"
    echo "📝 Please edit this file to add your API tokens and preferences"
else
    echo "⚙️  Configuration file already exists at $CONFIG_FILE"
fi

# Build Pilot
echo "🔨 Building Pilot..."
if make build; then
    echo "✅ Pilot built successfully"
else
    echo "❌ Build failed. Please check the output above."
    exit 1
fi

# Check if GitHub CLI is installed
if ! command -v gh &> /dev/null; then
    echo "⚠️  GitHub CLI (gh) not found. Install it for GitHub integration:"
    echo "   - macOS: brew install gh"
    echo "   - Ubuntu: sudo apt install gh"
    echo "   - Other: https://cli.github.com/manual/installation"
else
    echo "✅ GitHub CLI detected"
fi

# Check if git is configured
if ! git config user.name &> /dev/null; then
    echo "⚠️  Git user.name not configured. Run:"
    echo "   git config --global user.name 'Your Name'"
fi

if ! git config user.email &> /dev/null; then
    echo "⚠️  Git user.email not configured. Run:"
    echo "   git config --global user.email 'your.email@example.com'"
fi

echo ""
echo "🎉 Setup complete!"
echo ""
echo "Next steps:"
echo "1. Edit $CONFIG_FILE with your API tokens"
echo "2. Run: ./bin/pilot init"
echo "3. Start Pilot: ./bin/pilot start --github --dashboard"
echo ""
echo "For detailed documentation, visit: https://pilot.quantflow.studio"