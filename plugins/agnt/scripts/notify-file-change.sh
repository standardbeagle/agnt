#!/bin/bash
# Notify browser when Claude writes/edits a file
# Receives PostToolUse JSON on stdin

# Read stdin
INPUT=$(cat)

# Extract file path from tool_input
FILE_PATH=$(echo "$INPUT" | jq -r '.tool_input.file_path // empty' 2>/dev/null)
TOOL_NAME=$(echo "$INPUT" | jq -r '.tool_name // "Edit"' 2>/dev/null)

if [ -z "$FILE_PATH" ]; then
  exit 0
fi

# Get just the filename for display
FILENAME=$(basename "$FILE_PATH")

# Notify via agnt (broadcasts to all active proxies)
agnt notify --type "file-change" --title "File ${TOOL_NAME}ed" --message "$FILENAME" --data "{\"path\": \"$FILE_PATH\", \"tool\": \"$TOOL_NAME\"}" 2>/dev/null || true

exit 0
