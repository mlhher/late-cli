#!/bin/bash
set -e

echo "=> Installing Late Orchestrator..."

# Detect OS
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$OS" in
    linux*)     OS="linux" ;;
    darwin*)    OS="darwin" ;;
    msys*|cygwin*|mingw*) OS="windows" ;;
    *)          echo "Unsupported OS: $OS"; exit 1 ;;
esac

# Detect Architecture
ARCH="$(uname -m)"
case "$ARCH" in
    x86_64|amd64) ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    *)             echo "Unsupported Architecture: $ARCH"; exit 1 ;;
esac

# Formulate the binary name
BINARY_NAME="late-${OS}-${ARCH}"
if [ "$OS" = "windows" ]; then
    BINARY_NAME="${BINARY_NAME}.exe"
fi

# Get the latest release download URL
DOWNLOAD_URL=$(curl -sfL https://api.github.com/repos/mlhher/late-cli/releases/latest | grep "browser_download_url" | grep "$BINARY_NAME\"" | cut -d '"' -f 4 | head -n 1)

if [ -z "$DOWNLOAD_URL" ]; then
    echo "Error: Could not find a release for $OS ($ARCH)."
    echo "Check the releases page: https://github.com/mlhher/late-cli/releases"
    exit 1
fi

# Force user-space installation
INSTALL_DIR="$HOME/.local/bin"
mkdir -p "$INSTALL_DIR"

echo "=> Downloading $BINARY_NAME..."
curl -sfL "$DOWNLOAD_URL" -o "$INSTALL_DIR/late.tmp"

echo "=> Making binary executable..."
chmod +x "$INSTALL_DIR/late.tmp"

# Rename atomically to avoid workspace pollution or half-written binaries
mv "$INSTALL_DIR/late.tmp" "$INSTALL_DIR/late"

echo "=> Success! Late is installed at $INSTALL_DIR/late"

# Crucial friction check: Is it actually in their PATH?
if [[ ":$PATH:" != *":$INSTALL_DIR:"* ]]; then
    echo ""
    echo "⚠️  WARNING: $INSTALL_DIR is not in your PATH."
    echo "To use 'late' from anywhere, add this line to your ~/.bashrc or ~/.zshrc:"
    echo ""
    echo "    export PATH=\"\$PATH:$INSTALL_DIR\""
    echo ""
    echo "Then restart your terminal or run: source ~/.bashrc (or ~/.zshrc)"
else
    echo "=> Run 'late' to get started."
fi