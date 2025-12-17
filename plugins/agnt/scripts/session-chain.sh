#!/bin/bash
# agnt Session Chain hook - executes chained commands on task/tool completion
# Reads pending chains from .agnt/chains.json and executes matching triggers

PROJECT_ROOT="${CLAUDE_PROJECT_DIR:-$(pwd)}"
CHAINS_FILE="$PROJECT_ROOT/.agnt/chains.json"

# Read stdin for event context
INPUT=$(cat)

# Check if agnt is available
if ! command -v agnt &> /dev/null; then
  exit 0
fi

# Check if chains file exists
if [ ! -f "$CHAINS_FILE" ]; then
  exit 0
fi

# Extract event details
EVENT_TYPE="${AGNT_EVENT_TYPE:-tool_complete}"
TOOL_NAME=$(echo "$INPUT" | jq -r '.tool_name // empty' 2>/dev/null)
TASK_STATUS=$(echo "$INPUT" | jq -r '.status // "completed"' 2>/dev/null)
TOOL_SUCCESS=$(echo "$INPUT" | jq -r '.tool_result.success // true' 2>/dev/null)

# Read chains and find matching triggers
CHAINS=$(cat "$CHAINS_FILE" 2>/dev/null)

# Process each chain
echo "$CHAINS" | jq -c '.chains[]?' 2>/dev/null | while read -r chain; do
  TRIGGER=$(echo "$chain" | jq -r '.trigger // empty')
  CONDITION=$(echo "$chain" | jq -r '.condition // "success"')
  COMMAND=$(echo "$chain" | jq -r '.command // empty')
  SESSION=$(echo "$chain" | jq -r '.session // empty')
  CHAIN_ID=$(echo "$chain" | jq -r '.id // empty')

  # Skip if no trigger or command
  [ -z "$TRIGGER" ] || [ -z "$COMMAND" ] && continue

  # Check if trigger matches
  MATCH=false
  case "$TRIGGER" in
    "tool:$TOOL_NAME"|"tool:*")
      MATCH=true
      ;;
    "task:complete")
      [ "$EVENT_TYPE" = "task_complete" ] && MATCH=true
      ;;
    "task:*")
      [ "$EVENT_TYPE" = "task_complete" ] && MATCH=true
      ;;
    *)
      # Check regex match
      if echo "$TOOL_NAME" | grep -qE "$TRIGGER" 2>/dev/null; then
        MATCH=true
      fi
      ;;
  esac

  [ "$MATCH" = "false" ] && continue

  # Check condition
  case "$CONDITION" in
    "success")
      [ "$TOOL_SUCCESS" != "true" ] && continue
      ;;
    "failure")
      [ "$TOOL_SUCCESS" = "true" ] && continue
      ;;
    "always")
      # Always execute
      ;;
  esac

  # Execute the chained command
  if [ -n "$SESSION" ]; then
    # Send to specific session
    agnt session send --code "$SESSION" --message "$COMMAND" 2>/dev/null || true
  else
    # Send to current project's session
    agnt session send --project "$PROJECT_ROOT" --message "$COMMAND" 2>/dev/null || true
  fi

  # Log execution
  agnt notify --type "chain-executed" --title "Chain Triggered" --message "$COMMAND" --data "{\"chain_id\": \"$CHAIN_ID\", \"trigger\": \"$TRIGGER\"}" 2>/dev/null || true

  # Remove one-shot chains
  ONESHOT=$(echo "$chain" | jq -r '.oneshot // false')
  if [ "$ONESHOT" = "true" ] && [ -n "$CHAIN_ID" ]; then
    # Remove this chain from the file
    jq "del(.chains[] | select(.id == \"$CHAIN_ID\"))" "$CHAINS_FILE" > "$CHAINS_FILE.tmp" && mv "$CHAINS_FILE.tmp" "$CHAINS_FILE"
  fi
done

exit 0
