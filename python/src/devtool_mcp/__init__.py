"""
devtool-mcp - DEPRECATED: Use 'agnt' instead

This package has been renamed to 'agnt'. This wrapper package will
continue to work but will not receive updates. Please migrate to agnt:

    pip uninstall devtool-mcp
    pip install agnt

Then update your MCP configuration:

    Before:
    {
        "mcpServers": {
            "devtool": {
                "command": "devtool-mcp"
            }
        }
    }

    After:
    {
        "mcpServers": {
            "agnt": {
                "command": "agnt",
                "args": ["serve"]
            }
        }
    }

For more information, see:
    https://standardbeagle.github.io/agnt/
"""

import sys
import warnings

# Show deprecation warning on import
warnings.warn(
    "\n"
    "=" * 60 + "\n"
    "DEPRECATION WARNING: devtool-mcp has been renamed to 'agnt'\n"
    "=" * 60 + "\n"
    "\n"
    "This package will continue to work but will not receive updates.\n"
    "Please migrate to the new package:\n"
    "\n"
    "    pip uninstall devtool-mcp\n"
    "    pip install agnt\n"
    "\n"
    "Then update your MCP configuration to use 'agnt' command.\n"
    "See: https://standardbeagle.github.io/agnt/\n",
    DeprecationWarning,
    stacklevel=2
)

# Re-export everything from agnt
from agnt import (
    __version__,
    get_binary_path,
    run,
)

__all__ = ["main", "get_binary_path", "run"]


def main() -> None:
    """Entry point for the devtool-mcp command (forwards to agnt)."""
    # Show deprecation notice
    print(
        "\n"
        "=" * 60 + "\n"
        "DEPRECATION NOTICE: devtool-mcp has been renamed to 'agnt'\n"
        "=" * 60 + "\n"
        "\n"
        "This command will continue to work but will not receive updates.\n"
        "Please migrate to the new package:\n"
        "\n"
        "    pip uninstall devtool-mcp\n"
        "    pip install agnt\n"
        "\n"
        "Then use 'agnt' command instead:\n"
        "    agnt serve     (for MCP server)\n"
        "    agnt run ...   (for PTY wrapper)\n"
        "\n"
        "See: https://standardbeagle.github.io/agnt/\n"
        "=" * 60 + "\n",
        file=sys.stderr
    )

    # Import and run agnt's main
    from agnt import main as agnt_main
    agnt_main()


if __name__ == "__main__":
    main()
