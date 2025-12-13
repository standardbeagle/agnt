#!/bin/bash
# Notify browser when Claude runs a bash command
# Receives PostToolUse JSON on stdin

# Read stdin
INPUT=$(cat)

# Extract command from tool_input
COMMAND=$(echo "$INPUT" | jq -r '.tool_input.command // empty' 2>/dev/null)

if [ -z "$COMMAND" ]; then
  exit 0
fi

# Truncate long commands for display
if [ ${#COMMAND} -gt 50 ]; then
  DISPLAY_CMD="${COMMAND:0:47}..."
else
  DISPLAY_CMD="$COMMAND"
fi

# Notify via agnt
agnt notify --type "command" --title "Command Executed" --message "$DISPLAY_CMD" 2>/dev/null || true

exit 0
