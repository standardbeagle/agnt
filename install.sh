#!/bin/bash
# agnt installer for Unix-like systems (Linux, macOS)
# Usage: curl -fsSL https://raw.githubusercontent.com/standardbeagle/agnt/main/install.sh | bash

set -e

REPO="standardbeagle/agnt"
BINARY_NAME="agnt"

# Detect platform
detect_platform() {
    local os=$(uname -s | tr '[:upper:]' '[:lower:]')
    case "$os" in
        linux*) echo "linux" ;;
        darwin*) echo "darwin" ;;
        *) echo "Unsupported OS: $os" >&2; exit 1 ;;
    esac
}

# Detect architecture
detect_arch() {
    local arch=$(uname -m)
    case "$arch" in
        x86_64|amd64) echo "amd64" ;;
        arm64|aarch64) echo "arm64" ;;
        *) echo "Unsupported architecture: $arch" >&2; exit 1 ;;
    esac
}

# Get latest version from GitHub
get_latest_version() {
    curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" | grep '"tag_name"' | sed -E 's/.*"v([^"]+)".*/\1/'
}

main() {
    local platform=$(detect_platform)
    local arch=$(detect_arch)
    local version=${AGNT_VERSION:-$(get_latest_version)}
    local install_dir="${AGNT_INSTALL_DIR:-$HOME/.local/bin}"

    echo "Installing agnt v$version..."
    echo "  Platform: $platform"
    echo "  Architecture: $arch"
    echo "  Install directory: $install_dir"

    # Create install directory
    mkdir -p "$install_dir"

    # Download URL
    local url="https://github.com/$REPO/releases/download/v$version/$BINARY_NAME-$platform-$arch"
    local binary_path="$install_dir/$BINARY_NAME"

    echo "  Downloading from: $url"

    # Download binary
    if command -v curl &> /dev/null; then
        curl -fsSL "$url" -o "$binary_path"
    elif command -v wget &> /dev/null; then
        wget -q "$url" -O "$binary_path"
    else
        echo "Error: curl or wget is required" >&2
        exit 1
    fi

    # Make executable
    chmod +x "$binary_path"

    echo ""
    echo "Successfully installed agnt to $binary_path"
    echo ""

    # Check if install_dir is in PATH
    if [[ ":$PATH:" != *":$install_dir:"* ]]; then
        echo "Add the following to your shell profile (.bashrc, .zshrc, etc.):"
        echo ""
        echo "  export PATH=\"$install_dir:\$PATH\""
        echo ""
    fi

    # Verify installation
    "$binary_path" --version
}

main "$@"
