#!/bin/bash
# agnt Session Disconnect hook - unregisters session on stop
# Called when Claude Code session ends

PROJECT_ROOT="${CLAUDE_PROJECT_DIR:-$(pwd)}"
SESSION_CODE="${AGNT_SESSION_CODE:-}"

# Check if agnt is available
if ! command -v agnt &> /dev/null; then
  exit 0
fi

# Unregister session
if [ -n "$SESSION_CODE" ]; then
  agnt session unregister --code "$SESSION_CODE" 2>/dev/null || true
else
  # Try to find and unregister by project path
  agnt session unregister --project "$PROJECT_ROOT" 2>/dev/null || true
fi

# Notify browser of disconnect
agnt notify --type "session-disconnect" --title "Session Ended" --message "Claude Code session disconnected" 2>/dev/null || true

exit 0
