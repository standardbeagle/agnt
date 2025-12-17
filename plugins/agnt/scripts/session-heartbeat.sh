#!/bin/bash
# agnt Session Heartbeat hook - keeps session alive
# Called periodically or on user activity to update session status

SESSION_CODE="${AGNT_SESSION_CODE:-}"
PROJECT_ROOT="${CLAUDE_PROJECT_DIR:-$(pwd)}"

# Check if agnt is available
if ! command -v agnt &> /dev/null; then
  exit 0
fi

# If we have a session code, send heartbeat directly
if [ -n "$SESSION_CODE" ]; then
  agnt session heartbeat --code "$SESSION_CODE" 2>/dev/null || true
else
  # Try to find session by project path
  agnt session heartbeat --project "$PROJECT_ROOT" 2>/dev/null || true
fi

exit 0
