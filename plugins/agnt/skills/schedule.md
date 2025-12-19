---
description: "Schedule messages to AI agent sessions with time delays"
allowed-tools: ["mcp__agnt__session"]
---

You are a scheduling assistant that helps users schedule messages to be delivered to AI coding agent sessions at specified times.

## When to Use This Skill

Use this skill when the user wants to:
- Schedule a reminder or follow-up message to an agent
- Set up a delayed verification check
- Queue a message for delivery after a certain time
- Manage scheduled tasks (view, cancel)

## Capabilities

1. **List Sessions**: Show all active agent sessions that can receive scheduled messages
2. **Schedule Messages**: Queue a message for future delivery to a specific session
3. **View Tasks**: List all pending scheduled tasks
4. **Cancel Tasks**: Remove a scheduled task before it executes

## Workflow

### Step 1: Identify Target Session

First, list available sessions to find the target:
```
session {action: "list"}
```

Sessions are identified by codes like "claude-1", "claude-2", "dev", etc.

### Step 2: Schedule the Message

Once you have the session code:
```
session {action: "schedule", code: "claude-1", duration: "5m", message: "Please verify the deployment completed successfully"}
```

### Step 3: Confirm Scheduling

After scheduling, the tool returns a task ID. Inform the user:
- Task ID for future reference
- Scheduled delivery time
- Target session

## Duration Format

Parse user intent into Go duration format:
- "in 5 minutes" → "5m"
- "in an hour" → "1h"
- "in 30 seconds" → "30s"
- "in 2 hours and 30 minutes" → "2h30m"
- "in 90 minutes" → "90m" or "1h30m"

## Common Use Cases

### Verification Reminder
User: "Remind me to check if tests pass in 10 minutes"
```
session {action: "schedule", code: "claude-1", duration: "10m", message: "Please check if the test suite passed and report any failures"}
```

### Deployment Check
User: "Schedule a deployment verification in 5 minutes"
```
session {action: "schedule", code: "claude-1", duration: "5m", message: "Verify the deployment completed successfully. Check the health endpoint and report status."}
```

### Build Completion
User: "Let me know when 15 minutes have passed so I can review the build"
```
session {action: "schedule", code: "claude-1", duration: "15m", message: "15 minutes have passed. Please review the build output and summarize any issues."}
```

## Managing Tasks

### List Pending Tasks
```
session {action: "tasks"}
```

### Cancel a Task
```
session {action: "cancel", task_id: "<task_id>"}
```

## Important Notes

- Sessions must be running (`agnt run` active) to receive scheduled messages
- If a session disconnects, the message will retry up to 3 times
- Tasks are persisted per-project in `.agnt/scheduled-tasks.json`
- The scheduler runs in the daemon, so tasks survive client disconnections
