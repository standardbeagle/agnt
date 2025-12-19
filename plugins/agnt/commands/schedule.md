---
description: "Schedule a message to be sent to your AI agent at a future time"
allowed-tools: ["mcp__agnt__session"]
---

Schedule a message to be delivered to an AI coding agent session at a specified time.

## Usage

The user can specify:
- **Session code**: Target session (e.g., "claude-1", "dev"). If omitted, list available sessions.
- **Duration**: When to send (e.g., "5m", "1h", "30s", "2h30m")
- **Message**: What to send to the agent

## Steps

1. If no arguments provided, list available sessions:
   ```
   session {action: "list"}
   ```

2. If session code provided but no duration/message, show session details:
   ```
   session {action: "get", code: "<session_code>"}
   ```

3. To schedule a message:
   ```
   session {action: "schedule", code: "<session_code>", duration: "<duration>", message: "<message>"}
   ```

4. To list pending scheduled tasks:
   ```
   session {action: "tasks"}
   ```

5. To cancel a scheduled task:
   ```
   session {action: "cancel", task_id: "<task_id>"}
   ```

## Examples

**Schedule a verification reminder:**
```
/schedule claude-1 5m "Please verify the build completed successfully"
```

**Schedule a test check:**
```
/schedule dev 10m "Run the test suite and report any failures"
```

**List all sessions:**
```
/schedule
```

**View pending tasks:**
```
/schedule --tasks
```

## Duration Format

Durations use Go duration syntax:
- `30s` - 30 seconds
- `5m` - 5 minutes
- `1h` - 1 hour
- `1h30m` - 1 hour 30 minutes
- `2h45m30s` - 2 hours 45 minutes 30 seconds

## Notes

- Sessions are created by `agnt run <command>` (e.g., `agnt run claude`)
- Each session has a unique code like "claude-1" or "dev"
- Scheduled messages are persisted and survive daemon restarts
- Messages are delivered as synthetic user input to the target session
