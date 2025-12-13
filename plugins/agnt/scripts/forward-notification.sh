#!/bin/bash
# Forward Claude notifications to the browser
# Receives Notification event JSON on stdin

# Read stdin
INPUT=$(cat)

# Extract notification details
TITLE=$(echo "$INPUT" | jq -r '.title // "Notification"' 2>/dev/null)
MESSAGE=$(echo "$INPUT" | jq -r '.message // empty' 2>/dev/null)

if [ -z "$MESSAGE" ]; then
  exit 0
fi

# Forward via agnt
agnt notify --type "claude-notification" --title "$TITLE" --message "$MESSAGE" 2>/dev/null || true

exit 0
