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
    local version
    # Try GitHub API first
    version=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" 2>/dev/null | grep '"tag_name"' | sed -E 's/.*"v([^"]+)".*/\1/')

    # If API fails (rate limit), try scraping releases page
    if [ -z "$version" ]; then
        version=$(curl -fsSL "https://github.com/$REPO/releases/latest" 2>/dev/null | grep -oE '/releases/tag/v[0-9]+\.[0-9]+\.[0-9]+' | head -1 | sed 's|.*/v||')
    fi

    # Validate version format
    if [ -z "$version" ] || ! echo "$version" | grep -qE '^[0-9]+\.[0-9]+\.[0-9]+$'; then
        echo "Error: Could not determine latest version. Set AGNT_VERSION manually." >&2
        exit 1
    fi

    echo "$version"
}

# download fetches URL $1 to file $2.
download() {
    if command -v curl &> /dev/null; then
        curl -fsSL "$1" -o "$2"
    elif command -v wget &> /dev/null; then
        wget -q "$1" -O "$2"
    else
        echo "Error: curl or wget is required" >&2
        exit 1
    fi
}

# sha256_of prints the SHA-256 of file $1: sha256sum on Linux, shasum on macOS.
sha256_of() {
    if command -v sha256sum &> /dev/null; then
        sha256sum "$1" | cut -d' ' -f1
    elif command -v shasum &> /dev/null; then
        shasum -a 256 "$1" | cut -d' ' -f1
    else
        echo "Error: neither sha256sum nor shasum is installed, so the download cannot be verified" >&2
        exit 1
    fi
}

main() {
    local platform=$(detect_platform)
    local arch=$(detect_arch)
    local version=${AGNT_VERSION:-$(get_latest_version)}
    local install_dir="${AGNT_INSTALL_DIR:-$HOME/.local/bin}"

    # Validate version
    if [ -z "$version" ]; then
        echo "Error: Version not set" >&2
        exit 1
    fi

    echo "Installing agnt v$version..."
    echo "  Platform: $platform"
    echo "  Architecture: $arch"
    echo "  Install directory: $install_dir"

    # Create install directory
    mkdir -p "$install_dir"

    # Download URLs
    local asset="$BINARY_NAME-$platform-$arch"
    local base="https://github.com/$REPO/releases/download/v$version"
    local url="$base/$asset"
    local binary_path="$install_dir/$BINARY_NAME"

    # Stage in the install directory (same filesystem, so the final mv is a
    # rename); nothing reaches binary_path until its checksum is verified.
    TMP_DIR=$(mktemp -d "$install_dir/.agnt-install.XXXXXX")
    trap 'rm -rf "$TMP_DIR"' EXIT

    # The release's SHA256SUMS must list the asset; without it, refuse.
    if ! download "$base/SHA256SUMS" "$TMP_DIR/SHA256SUMS"; then
        echo "Error: could not fetch SHA256SUMS for v$version; refusing to install an unverified binary" >&2
        exit 1
    fi
    local expected
    expected=$(awk -v f="$asset" '$2 == f || $2 == "*" f { print tolower($1); exit }' "$TMP_DIR/SHA256SUMS")
    if ! echo "$expected" | grep -qE '^[0-9a-f]{64}$'; then
        echo "Error: SHA256SUMS for v$version lists no valid entry for $asset; refusing to install" >&2
        exit 1
    fi

    echo "  Downloading from: $url"
    download "$url" "$TMP_DIR/$asset"

    local actual
    actual=$(sha256_of "$TMP_DIR/$asset")
    if [ "$actual" != "$expected" ]; then
        echo "Error: checksum mismatch for $asset: downloaded file has sha256 $actual, SHA256SUMS lists $expected; not installed" >&2
        exit 1
    fi
    echo "  Verified sha256: $actual"

    # Make executable and move into place
    chmod +x "$TMP_DIR/$asset"
    mv "$TMP_DIR/$asset" "$binary_path"

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
