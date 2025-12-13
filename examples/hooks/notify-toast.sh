#!/bin/bash
# Claude Code hook to send toast notifications to the browser indicator
#
# This hook sends toast notifications via the agnt overlay when Claude
# completes certain actions.
#
# Usage: Configure in .claude/settings.json:
# {
#   "hooks": {
#     "Stop": [{
#       "hooks": [{
#         "type": "command",
#         "command": "/path/to/notify-toast.sh"
#       }]
#     }]
#   }
# }

# Read hook input from stdin
INPUT=$(cat)

# Parse the event type (for logging/debugging)
EVENT_TYPE=$(echo "$INPUT" | jq -r '.hook_event_name // "unknown"')

# Get the overlay socket path
OVERLAY_SOCKET="${XDG_RUNTIME_DIR:-/tmp}/devtool-overlay.sock"

# Function to send toast via HTTP to Unix socket
send_toast() {
    local type="$1"
    local title="$2"
    local message="$3"

    # Use curl to send to the Unix socket
    curl -s --unix-socket "$OVERLAY_SOCKET" \
        -X POST \
        -H "Content-Type: application/json" \
        -d "{\"type\": \"$type\", \"title\": \"$title\", \"message\": \"$message\"}" \
        "http://localhost/toast" >/dev/null 2>&1 || true
}

# Handle different event types
case "$EVENT_TYPE" in
    "Stop")
        # Claude finished responding
        send_toast "success" "Claude" "Response complete"
        ;;
    "SubagentStop")
        # A subagent finished
        send_toast "info" "Agent" "Task completed"
        ;;
    "PostToolUse")
        TOOL_NAME=$(echo "$INPUT" | jq -r '.tool_name // "unknown"')
        case "$TOOL_NAME" in
            "Edit"|"Write")
                send_toast "info" "File Changed" "Modified by Claude"
                ;;
            "Bash")
                send_toast "info" "Command" "Bash command executed"
                ;;
        esac
        ;;
esac

# Always exit successfully (don't block Claude)
exit 0
