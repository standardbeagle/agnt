#!/bin/bash
# agnt SessionStart hook - reads .agnt.kdl and starts configured services
set -e

PROJECT_ROOT="${CLAUDE_PROJECT_DIR:-$(pwd)}"
CONFIG_FILE="$PROJECT_ROOT/.agnt.kdl"

# Exit gracefully if no config exists
if [ ! -f "$CONFIG_FILE" ]; then
  exit 0
fi

# Check if agnt is available
if ! command -v agnt &> /dev/null; then
  echo '{"error": "agnt not found in PATH"}' >&2
  exit 0  # Don't block session start
fi

# Parse .agnt.kdl and start services
# KDL parsing is done by agnt itself
agnt project-start --config "$CONFIG_FILE" 2>/dev/null || true

exit 0
