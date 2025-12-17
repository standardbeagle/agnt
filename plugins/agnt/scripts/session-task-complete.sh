#!/bin/bash
# agnt Session Task Complete hook - handles task completion events
# Can trigger scheduled follow-ups and browser notifications

# Read stdin
INPUT=$(cat)

# Check if agnt is available
if ! command -v agnt &> /dev/null; then
  exit 0
fi

# Extract task details
TASK_DESC=$(echo "$INPUT" | jq -r '.description // empty' 2>/dev/null)
TASK_STATUS=$(echo "$INPUT" | jq -r '.status // "completed"' 2>/dev/null)
TASK_OUTPUT=$(echo "$INPUT" | jq -r '.output // empty' 2>/dev/null)

# Skip if no task description
if [ -z "$TASK_DESC" ]; then
  exit 0
fi

# Build task data
TASK_DATA=$(jq -n \
  --arg desc "$TASK_DESC" \
  --arg status "$TASK_STATUS" \
  --arg output "$TASK_OUTPUT" \
  '{description: $desc, status: $status, output: $output}')

# Broadcast task completion to browser
agnt notify --type "task-complete" --title "Task $TASK_STATUS" --message "$TASK_DESC" --data "$TASK_DATA" 2>/dev/null || true

exit 0
