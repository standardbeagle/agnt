#!/bin/bash
# agnt Session Connect hook - registers Claude Code session with daemon
# Called on SessionStart to enable bidirectional communication
set -e

PROJECT_ROOT="${CLAUDE_PROJECT_DIR:-$(pwd)}"
SESSION_ID="${CLAUDE_SESSION_ID:-}"

# Check if agnt is available
if ! command -v agnt &> /dev/null; then
  exit 0  # Don't block session start
fi

# Get or generate a session code
if [ -n "$SESSION_ID" ]; then
  # Use Claude's session ID to create a deterministic code
  SESSION_CODE="claude-${SESSION_ID:0:8}"
else
  # Fallback: let daemon generate a code
  SESSION_CODE=""
fi

# Register this session with the daemon
# The session tool will auto-generate a code if not provided
RESULT=$(agnt session register \
  --project "$PROJECT_ROOT" \
  ${SESSION_CODE:+--code "$SESSION_CODE"} \
  2>/dev/null) || true

# Extract the session code from result for user reference
if [ -n "$RESULT" ]; then
  CODE=$(echo "$RESULT" | jq -r '.code // empty' 2>/dev/null)
  if [ -n "$CODE" ]; then
    echo "Session registered: $CODE"
  fi
fi

exit 0
