#!/bin/bash
# agnt Workflow Engine - Self-transitioning state machine for task completion
# Enforces multi-phase workflows with review cycles before allowing completion

PROJECT_ROOT="${CLAUDE_PROJECT_DIR:-$(pwd)}"
WORKFLOW_DIR="$PROJECT_ROOT/.agnt"
WORKFLOW_FILE="$WORKFLOW_DIR/workflow.json"
STATE_FILE="$WORKFLOW_DIR/workflow-state.json"

# Read stdin for event context
INPUT=$(cat)

# Ensure workflow directory exists
mkdir -p "$WORKFLOW_DIR"

# Check if agnt is available
if ! command -v agnt &> /dev/null; then
  exit 0
fi

# Check if workflow exists
if [ ! -f "$WORKFLOW_FILE" ]; then
  exit 0
fi

# Initialize state file if missing
if [ ! -f "$STATE_FILE" ]; then
  echo '{"current_state": "init", "history": [], "attempts": {}, "started_at": null}' > "$STATE_FILE"
fi

# Load workflow and state
WORKFLOW=$(cat "$WORKFLOW_FILE")
STATE=$(cat "$STATE_FILE")

CURRENT_STATE=$(echo "$STATE" | jq -r '.current_state')
HISTORY=$(echo "$STATE" | jq -r '.history')

# Extract event info
EVENT_TYPE="${AGNT_EVENT_TYPE:-response}"
TOOL_NAME=$(echo "$INPUT" | jq -r '.tool_name // empty' 2>/dev/null)
RESPONSE_TEXT=$(echo "$INPUT" | jq -r '.response // .message // empty' 2>/dev/null)

# Get current state definition
STATE_DEF=$(echo "$WORKFLOW" | jq -r ".states[\"$CURRENT_STATE\"] // empty")

if [ -z "$STATE_DEF" ] || [ "$STATE_DEF" = "null" ]; then
  exit 0
fi

# Check for completion signals in response
COMPLETION_SIGNALS=("complete" "done" "finished" "all tasks" "implemented" "ready for review")
FOUND_COMPLETION=false

for signal in "${COMPLETION_SIGNALS[@]}"; do
  if echo "$RESPONSE_TEXT" | grep -qi "$signal" 2>/dev/null; then
    FOUND_COMPLETION=true
    break
  fi
done

# Get state properties
STATE_TYPE=$(echo "$STATE_DEF" | jq -r '.type // "work"')
NEXT_STATE=$(echo "$STATE_DEF" | jq -r '.next // empty')
ON_COMPLETE=$(echo "$STATE_DEF" | jq -r '.on_complete // empty')
PROMPT=$(echo "$STATE_DEF" | jq -r '.prompt // empty')
REVIEW_PROMPT=$(echo "$STATE_DEF" | jq -r '.review_prompt // empty')
MAX_ATTEMPTS=$(echo "$STATE_DEF" | jq -r '.max_attempts // 3')
REQUIRES=$(echo "$STATE_DEF" | jq -r '.requires // [] | join(",")')

# Get attempt count for current state
ATTEMPTS=$(echo "$STATE" | jq -r ".attempts[\"$CURRENT_STATE\"] // 0")

# Function to transition state
transition_state() {
  local new_state="$1"
  local reason="$2"

  # Update state file
  NEW_STATE=$(echo "$STATE" | jq \
    --arg state "$new_state" \
    --arg prev "$CURRENT_STATE" \
    --arg reason "$reason" \
    --arg time "$(date -Iseconds)" \
    '.current_state = $state | .history += [{"from": $prev, "to": $state, "reason": $reason, "time": $time}]')

  echo "$NEW_STATE" > "$STATE_FILE"

  # Get new state's entry prompt
  local entry_prompt=$(echo "$WORKFLOW" | jq -r ".states[\"$new_state\"].prompt // empty")

  if [ -n "$entry_prompt" ] && [ "$entry_prompt" != "null" ]; then
    # Send the prompt to the session
    agnt session send --project "$PROJECT_ROOT" --message "$entry_prompt" 2>/dev/null || true
  fi

  # Notify browser
  agnt notify --type "workflow-transition" --title "Workflow: $new_state" --message "$reason" 2>/dev/null || true
}

# Function to increment attempts
increment_attempts() {
  STATE=$(echo "$STATE" | jq --arg s "$CURRENT_STATE" '.attempts[$s] = ((.attempts[$s] // 0) + 1)')
  echo "$STATE" > "$STATE_FILE"
  ATTEMPTS=$((ATTEMPTS + 1))
}

# Handle based on state type
case "$STATE_TYPE" in
  "work")
    # Work state - check if claiming completion
    if [ "$FOUND_COMPLETION" = "true" ]; then
      increment_attempts

      if [ -n "$ON_COMPLETE" ] && [ "$ON_COMPLETE" != "null" ]; then
        # Transition to next state (usually review)
        transition_state "$ON_COMPLETE" "Work phase reported complete, moving to review"
      elif [ -n "$NEXT_STATE" ] && [ "$NEXT_STATE" != "null" ]; then
        transition_state "$NEXT_STATE" "Work phase complete"
      fi
    fi
    ;;

  "review")
    # Review state - enforce thoroughness
    if [ "$FOUND_COMPLETION" = "true" ]; then
      increment_attempts

      if [ "$ATTEMPTS" -lt "$MAX_ATTEMPTS" ]; then
        # Not enough review cycles - send review prompt
        if [ -n "$REVIEW_PROMPT" ] && [ "$REVIEW_PROMPT" != "null" ]; then
          agnt session send --project "$PROJECT_ROOT" --message "$REVIEW_PROMPT" 2>/dev/null || true
          agnt notify --type "workflow-review" --title "Review Required" --message "Attempt $ATTEMPTS/$MAX_ATTEMPTS" 2>/dev/null || true
        fi
      else
        # Enough attempts, allow transition
        if [ -n "$ON_COMPLETE" ] && [ "$ON_COMPLETE" != "null" ]; then
          transition_state "$ON_COMPLETE" "Review complete after $ATTEMPTS attempts"
        elif [ -n "$NEXT_STATE" ] && [ "$NEXT_STATE" != "null" ]; then
          transition_state "$NEXT_STATE" "Review complete"
        fi
      fi
    fi
    ;;

  "gate")
    # Gate state - check requirements before allowing passage
    # This would check if tests pass, build succeeds, etc.
    if [ "$FOUND_COMPLETION" = "true" ]; then
      # For now, just transition - could add requirement checking
      if [ -n "$NEXT_STATE" ] && [ "$NEXT_STATE" != "null" ]; then
        transition_state "$NEXT_STATE" "Gate passed"
      fi
    fi
    ;;

  "fix")
    # Fix state - loop back to review after fixes
    if [ "$FOUND_COMPLETION" = "true" ]; then
      if [ -n "$ON_COMPLETE" ] && [ "$ON_COMPLETE" != "null" ]; then
        transition_state "$ON_COMPLETE" "Fixes applied, returning to review"
      fi
    fi
    ;;

  "final")
    # Final state - actually complete
    if [ "$FOUND_COMPLETION" = "true" ]; then
      agnt notify --type "workflow-complete" --title "Workflow Complete" --message "All phases finished successfully" 2>/dev/null || true
      # Reset for next workflow
      echo '{"current_state": "init", "history": [], "attempts": {}, "completed_at": "'$(date -Iseconds)'"}' > "$STATE_FILE"
    fi
    ;;
esac

exit 0
