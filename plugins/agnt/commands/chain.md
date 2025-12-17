---
description: "Chain commands to run automatically when events occur"
allowed-tools: ["mcp__devtool__session", "Read", "Write"]
---

Set up command chains that execute automatically when specific events occur.

## Usage

Chain commands to trigger on:
- **Tool completion**: Run after specific tools finish (Write, Edit, Bash, Task, etc.)
- **Task completion**: Run when a Task agent completes
- **Any success/failure**: Conditional execution based on outcome

## Chain Configuration

Chains are stored in `.agnt/chains.json` in your project:

```json
{
  "chains": [
    {
      "id": "review-after-edit",
      "trigger": "tool:Edit",
      "condition": "success",
      "command": "/code-review",
      "oneshot": false
    }
  ]
}
```

## Steps

### 1. List Current Chains

Read the chains file to see what's configured:
```
Read .agnt/chains.json
```

### 2. Add a New Chain

Create or update the chains file:

**Example: Code review after editing:**
```json
{
  "chains": [
    {
      "id": "auto-review",
      "trigger": "tool:Edit",
      "condition": "success",
      "command": "/code-review the changes I just made",
      "oneshot": false
    }
  ]
}
```

**Example: Run tests after writing files:**
```json
{
  "chains": [
    {
      "id": "auto-test",
      "trigger": "tool:Write",
      "condition": "success",
      "command": "Please run the test suite to verify my changes",
      "oneshot": false
    }
  ]
}
```

**Example: One-shot chain (runs once then removes itself):**
```json
{
  "chains": [
    {
      "id": "deploy-check",
      "trigger": "task:complete",
      "condition": "success",
      "command": "/schedule claude-1 5m 'Verify the deployment completed successfully'",
      "oneshot": true
    }
  ]
}
```

### 3. Remove a Chain

Edit the chains file to remove unwanted chains.

## Trigger Types

| Trigger | Description |
|---------|-------------|
| `tool:Edit` | After Edit tool completes |
| `tool:Write` | After Write tool completes |
| `tool:Bash` | After Bash tool completes |
| `tool:Task` | After Task agent completes |
| `tool:*` | After any tool completes |
| `task:complete` | After a Task agent finishes |

## Conditions

| Condition | Description |
|-----------|-------------|
| `success` | Only run if tool succeeded (default) |
| `failure` | Only run if tool failed |
| `always` | Run regardless of outcome |

## Chain Properties

| Property | Required | Description |
|----------|----------|-------------|
| `id` | Yes | Unique identifier for the chain |
| `trigger` | Yes | Event that triggers the chain |
| `command` | Yes | Message/command to send to session |
| `condition` | No | When to execute (default: "success") |
| `session` | No | Target session code (default: current project) |
| `oneshot` | No | Remove after first execution (default: false) |

## Example Workflows

### Auto Code Review Pipeline
```json
{
  "chains": [
    {
      "id": "review-edits",
      "trigger": "tool:Edit",
      "condition": "success",
      "command": "Please review the code I just edited for any issues"
    },
    {
      "id": "test-after-review",
      "trigger": "tool:Task",
      "condition": "success",
      "command": "Run the relevant tests for the files I modified"
    }
  ]
}
```

### Build Verification Pipeline
```json
{
  "chains": [
    {
      "id": "build-check",
      "trigger": "tool:Bash",
      "condition": "success",
      "command": "/schedule claude-1 2m 'Check if the build completed and report any errors'"
    }
  ]
}
```

## Notes

- Chains are project-specific (stored in `.agnt/chains.json`)
- The chain hook runs on PostToolUse events
- Commands can be plain messages or slash commands
- Use oneshot for temporary chains that should auto-remove
- Chains can trigger other chains (be careful of loops!)
