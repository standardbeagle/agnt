#!/bin/bash
# Notify browser when Claude finishes responding
# Receives Stop event JSON on stdin

# Read stdin
INPUT=$(cat)

# Extract stop reason
STOP_REASON=$(echo "$INPUT" | jq -r '.stop_reason // "completed"' 2>/dev/null)

# Notify via agnt
agnt notify --type "response-complete" --title "Claude Finished" --message "$STOP_REASON" 2>/dev/null || true

exit 0
