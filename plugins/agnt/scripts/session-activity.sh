#!/bin/bash
# agnt Session Activity hook - broadcasts tool/task activity to browser
# Receives PostToolUse or task events on stdin

# Read stdin
INPUT=$(cat)

# Check if agnt is available
if ! command -v agnt &> /dev/null; then
  exit 0
fi

# Extract activity details
TOOL_NAME=$(echo "$INPUT" | jq -r '.tool_name // empty' 2>/dev/null)
TOOL_INPUT=$(echo "$INPUT" | jq -r '.tool_input // {}' 2>/dev/null)
TOOL_RESULT=$(echo "$INPUT" | jq -r '.tool_result.success // true' 2>/dev/null)

# Skip if no tool name
if [ -z "$TOOL_NAME" ]; then
  exit 0
fi

# Determine activity type and summary
case "$TOOL_NAME" in
  "Read")
    FILE=$(echo "$TOOL_INPUT" | jq -r '.file_path // "file"' 2>/dev/null)
    SUMMARY="Reading $(basename "$FILE")"
    ;;
  "Write")
    FILE=$(echo "$TOOL_INPUT" | jq -r '.file_path // "file"' 2>/dev/null)
    SUMMARY="Writing $(basename "$FILE")"
    ;;
  "Edit")
    FILE=$(echo "$TOOL_INPUT" | jq -r '.file_path // "file"' 2>/dev/null)
    SUMMARY="Editing $(basename "$FILE")"
    ;;
  "Bash")
    CMD=$(echo "$TOOL_INPUT" | jq -r '.command // ""' 2>/dev/null | head -c 50)
    SUMMARY="Running: ${CMD}..."
    ;;
  "Glob"|"Grep")
    PATTERN=$(echo "$TOOL_INPUT" | jq -r '.pattern // ""' 2>/dev/null)
    SUMMARY="Searching: $PATTERN"
    ;;
  "Task")
    DESC=$(echo "$TOOL_INPUT" | jq -r '.description // "task"' 2>/dev/null)
    SUMMARY="Task: $DESC"
    ;;
  "TodoWrite")
    SUMMARY="Updating task list"
    ;;
  *)
    SUMMARY="$TOOL_NAME"
    ;;
esac

# Build activity data
ACTIVITY_DATA=$(jq -n \
  --arg tool "$TOOL_NAME" \
  --arg summary "$SUMMARY" \
  --arg success "$TOOL_RESULT" \
  '{tool: $tool, summary: $summary, success: ($success == "true")}')

# Broadcast activity to browser via agnt proxy
agnt notify --type "session-activity" --title "Agent Activity" --message "$SUMMARY" --data "$ACTIVITY_DATA" 2>/dev/null || true

# Also send heartbeat to keep session alive
agnt session heartbeat --project "${CLAUDE_PROJECT_DIR:-$(pwd)}" 2>/dev/null || true

exit 0
