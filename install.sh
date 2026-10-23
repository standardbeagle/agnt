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

# download URL DEST: fetch with curl or wget, failing on HTTP errors.
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

# fetch_status URL DEST: download URL to DEST and print the HTTP status
# ("000" when the request did not complete).
fetch_status() {
    local code
    if command -v curl &> /dev/null; then
        code=$(curl -sSL -w '%{http_code}' -o "$2" "$1" 2>/dev/null) || code="000"
        echo "${code:-000}"
    elif command -v wget &> /dev/null; then
        if wget -q "$1" -O "$2" 2>/dev/null; then echo 200
        elif wget -S --spider "$1" 2>&1 | grep -q ' 404 '; then echo 404
        else echo 000; fi
    else
        echo 000
    fi
}

# sha256_of FILE: print the hex SHA-256 of FILE.
sha256_of() {
    if command -v sha256sum &> /dev/null; then
        sha256sum "$1" | awk '{ print $1 }'
    else
        shasum -a 256 "$1" | awk '{ print $1 }'
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

    # Download URL
    local url="https://github.com/$REPO/releases/download/v$version/$BINARY_NAME-$platform-$arch"
    local binary_path="$install_dir/$BINARY_NAME"

    echo "  Downloading from: $url"

    # Download to a temp file beside the target and rename it into place, so a
    # failed or truncated download never replaces a working binary and an
    # installed (possibly running) binary is swapped atomically.
    TMP_FILES=()
    trap 'rm -f "${TMP_FILES[@]}"' EXIT
    local tmp_path
    tmp_path=$(mktemp "$install_dir/.$BINARY_NAME.XXXXXX")
    TMP_FILES+=("$tmp_path")

    download "$url" "$tmp_path"

    # Verify against the release's SHA256SUMS. Releases that predate the file
    # have none; AGNT_REQUIRE_CHECKSUM=1 refuses to install without it.
    local sums_path="$tmp_path.sums"
    TMP_FILES+=("$sums_path")
    local sums_status
    sums_status=$(fetch_status "https://github.com/$REPO/releases/download/v$version/SHA256SUMS" "$sums_path")
    if [ "$sums_status" = "200" ]; then
        local asset="$BINARY_NAME-$platform-$arch"
        local expected actual
        expected=$(awk -v f="$asset" '$2 == f { print $1 }' "$sums_path")
        if [ -z "$expected" ]; then
            echo "Error: no checksum for $asset in SHA256SUMS" >&2
            exit 1
        fi
        actual=$(sha256_of "$tmp_path")
        if [ "$expected" != "$actual" ]; then
            echo "Error: checksum mismatch for $asset (expected $expected, got $actual)" >&2
            exit 1
        fi
        echo "  Checksum verified"
    elif [ "$sums_status" != "404" ]; then
        # Only a definite "not published" may skip verification; a network
        # error or 5xx must not downgrade the install to unverified.
        echo "Error: could not fetch SHA256SUMS for v$version (status $sums_status)" >&2
        exit 1
    elif [ "${AGNT_REQUIRE_CHECKSUM:-}" = "1" ]; then
        echo "Error: SHA256SUMS is not published for v$version and AGNT_REQUIRE_CHECKSUM=1" >&2
        exit 1
    else
        echo "  Warning: no SHA256SUMS published for v$version; skipping checksum verification" >&2
    fi

    chmod +x "$tmp_path"
    mv -f "$tmp_path" "$binary_path"

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
