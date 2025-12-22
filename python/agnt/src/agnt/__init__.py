"""
agnt - MCP server for AI coding agents

This package provides the agnt binary for use as an MCP server,
offering process management, reverse proxy with traffic logging,
browser instrumentation, and sketch mode.

Usage:
    Add to your MCP client configuration:
    {
        "mcpServers": {
            "agnt": {
                "command": "agnt",
                "args": ["mcp"]
            }
        }
    }

    Or run with PTY wrapper:
        agnt run claude --dangerously-skip-permissions

For more information, see:
    https://standardbeagle.github.io/agnt/
"""

__version__ = "0.7.12"
__all__ = ["main", "get_binary_path", "run"]

import os
import platform
import stat
import subprocess
import sys
import tempfile
from pathlib import Path
from typing import Optional

import httpx

REPO = "standardbeagle/agnt"
VERSION = __version__
BINARY_NAME = "agnt"


def get_platform() -> str:
    """Get the platform name for download."""
    system = platform.system().lower()
    if system == "darwin":
        return "darwin"
    elif system == "linux":
        return "linux"
    elif system == "windows":
        return "windows"
    else:
        raise RuntimeError(f"Unsupported platform: {system}")


def get_arch() -> str:
    """Get the architecture name for download."""
    machine = platform.machine().lower()
    if machine in ("x86_64", "amd64"):
        return "amd64"
    elif machine in ("arm64", "aarch64"):
        return "arm64"
    else:
        raise RuntimeError(f"Unsupported architecture: {machine}")


def get_binary_name() -> str:
    """Get the binary name for the current platform."""
    if platform.system().lower() == "windows":
        return f"{BINARY_NAME}.exe"
    return BINARY_NAME


def get_cache_dir() -> Path:
    """Get the cache directory for storing the binary."""
    if platform.system().lower() == "windows":
        base = Path(os.environ.get("LOCALAPPDATA", Path.home() / "AppData" / "Local"))
    elif platform.system().lower() == "darwin":
        base = Path.home() / "Library" / "Caches"
    else:
        base = Path(os.environ.get("XDG_CACHE_HOME", Path.home() / ".cache"))

    cache_dir = base / "agnt" / VERSION
    cache_dir.mkdir(parents=True, exist_ok=True)
    return cache_dir


def get_download_url() -> str:
    """Get the download URL for the binary."""
    plat = get_platform()
    arch = get_arch()
    ext = ".exe" if plat == "windows" else ""
    return f"https://github.com/{REPO}/releases/download/v{VERSION}/{BINARY_NAME}-{plat}-{arch}{ext}"


def get_binary_path() -> Path:
    """Get the path to the agnt binary, downloading if necessary."""
    cache_dir = get_cache_dir()
    binary_path = cache_dir / get_binary_name()

    if binary_path.exists():
        return binary_path

    # Download the binary
    url = get_download_url()
    print(f"Downloading agnt v{VERSION}...", file=sys.stderr)
    print(f"  Platform: {get_platform()}", file=sys.stderr)
    print(f"  Architecture: {get_arch()}", file=sys.stderr)

    try:
        with httpx.Client(follow_redirects=True, timeout=60.0) as client:
            response = client.get(url)
            response.raise_for_status()

            # Write to temp file first, then move
            with tempfile.NamedTemporaryFile(
                delete=False, dir=cache_dir, suffix=".tmp"
            ) as tmp:
                tmp.write(response.content)
                tmp_path = Path(tmp.name)

            # Move to final location
            tmp_path.rename(binary_path)

            # Make executable on Unix
            if platform.system().lower() != "windows":
                binary_path.chmod(binary_path.stat().st_mode | stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH)

            print(f"Successfully installed agnt to {binary_path}", file=sys.stderr)

    except httpx.HTTPError as e:
        print(f"Failed to download agnt: {e}", file=sys.stderr)
        print("", file=sys.stderr)
        print("You can manually download the binary from:", file=sys.stderr)
        print(f"  https://github.com/{REPO}/releases/tag/v{VERSION}", file=sys.stderr)
        print("", file=sys.stderr)
        print("Or build from source:", file=sys.stderr)
        print("  git clone https://github.com/standardbeagle/agnt.git", file=sys.stderr)
        print("  cd agnt", file=sys.stderr)
        print("  make build", file=sys.stderr)
        sys.exit(1)

    return binary_path


def run(args: Optional[list[str]] = None) -> subprocess.CompletedProcess:
    """Run agnt with the given arguments."""
    binary_path = get_binary_path()
    return subprocess.run([str(binary_path)] + (args or []))


def main() -> None:
    """Entry point for the agnt command."""
    binary_path = get_binary_path()
    try:
        # Replace current process with the binary
        if platform.system().lower() == "windows":
            # Windows doesn't support exec, so use subprocess
            result = subprocess.run([str(binary_path)] + sys.argv[1:])
            sys.exit(result.returncode)
        else:
            os.execv(str(binary_path), [str(binary_path)] + sys.argv[1:])
    except FileNotFoundError:
        print(f"Error: Binary not found at {binary_path}", file=sys.stderr)
        sys.exit(1)
    except PermissionError:
        print(f"Error: Binary at {binary_path} is not executable", file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    main()
