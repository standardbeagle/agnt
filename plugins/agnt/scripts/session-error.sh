#!/bin/bash
# agnt Session Error hook - forwards errors to browser and logs
# Receives error events on stdin

# Read stdin
INPUT=$(cat)

# Check if agnt is available
if ! command -v agnt &> /dev/null; then
  exit 0
fi

# Extract error details
ERROR_TYPE=$(echo "$INPUT" | jq -r '.error_type // "error"' 2>/dev/null)
ERROR_MSG=$(echo "$INPUT" | jq -r '.error // .message // empty' 2>/dev/null)
TOOL_NAME=$(echo "$INPUT" | jq -r '.tool_name // empty' 2>/dev/null)

# Skip if no error message
if [ -z "$ERROR_MSG" ]; then
  exit 0
fi

# Build error data
ERROR_DATA=$(jq -n \
  --arg type "$ERROR_TYPE" \
  --arg message "$ERROR_MSG" \
  --arg tool "$TOOL_NAME" \
  '{type: $type, message: $message, tool: $tool}')

# Broadcast error to browser
agnt notify --type "session-error" --title "Agent Error" --message "$ERROR_MSG" --data "$ERROR_DATA" 2>/dev/null || true

exit 0
