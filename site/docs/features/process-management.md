---
sidebar_position: 2
---

# Process Management

agnt provides robust process management with output capture, graceful shutdown, and real-time monitoring.

## Overview

The process management system consists of two tools:

- **`run`** - Start processes (scripts or raw commands)
- **`proc`** - Monitor, query output, and stop processes

## Key Features

- **Lock-Free Design** - High-performance concurrent process management
- **Bounded Output** - Ring buffers prevent memory overflow (256KB per stream)
- **Graceful Shutdown** - SIGTERM with timeout, then SIGKILL
- **Process Groups** - Child processes are properly cleaned up
- **Output Filtering** - grep, head, tail support for large outputs

## Execution Modes

### Background Mode (Default)

Process runs asynchronously. Returns immediately with process ID.

```json
run {script_name: "dev"}
→ {
    "process_id": "dev",
    "state": "running",
    "message": "Process started in background"
  }
```

Use `proc` to monitor:

```json
proc {action: "status", process_id: "dev"}
→ {state: "running", runtime: "45s", pid: 12345}
```

### Foreground Mode

Waits for process completion. Good for short-lived commands.

```json
run {script_name: "build", mode: "foreground"}
→ {
    "process_id": "build",
    "state": "stopped",
    "exit_code": 0,
    "runtime": "12.3s"
  }
```

### Foreground-Raw Mode

Like foreground, but includes stdout/stderr in response.

```json
run {script_name: "test", mode: "foreground-raw"}
→ {
    "process_id": "test",
    "state": "stopped",
    "exit_code": 0,
    "stdout": "✓ 42 tests passed\n",
    "stderr": ""
  }
```

Best for commands where you need immediate output.

## Running Scripts

### Project Scripts

Run scripts detected from your project:

```json
// First, detect available scripts
detect {path: "."}
→ {scripts: ["dev", "build", "test", "lint"]}

// Then run them
run {script_name: "test"}
run {script_name: "build", mode: "foreground"}
```

### Raw Commands

Run arbitrary commands with `raw: true`:

```json
run {raw: true, command: "go", args: ["mod", "tidy"], mode: "foreground-raw"}
→ {exit_code: 0, stdout: "", stderr: ""}

run {raw: true, command: "curl", args: ["-s", "https://api.example.com/health"]}
→ {process_id: "curl-abc123", state: "running"}
```

### Custom Process IDs

```json
run {script_name: "dev", id: "frontend-dev"}
run {script_name: "dev", id: "backend-dev", path: "./apps/api"}
```

## Monitoring Processes

### List All Processes

```json
proc {action: "list"}
→ {
    "processes": [
      {"id": "dev", "state": "running", "runtime": "5m32s"},
      {"id": "build", "state": "stopped", "exit_code": 0}
    ],
    "active_count": 1,
    "total_count": 2
  }
```

### Check Status

```json
proc {action: "status", process_id: "dev"}
→ {
    "id": "dev",
    "state": "running",
    "pid": 12345,
    "runtime": "5m32s",
    "started_at": "2024-01-15T10:30:00Z"
  }
```

## Reading Output

### Combined Output

```json
proc {action: "output", process_id: "dev"}
→ {
    "output": "[10:30:01] Starting dev server...\n[10:30:02] Ready on http://localhost:3000\n...",
    "truncated": false
  }
```

### Separate Streams

```json
proc {action: "output", process_id: "build", stream: "stderr"}
→ {output: "Warning: unused variable 'x'\n", truncated: false}
```

### Tail Output

Get only the last N lines:

```json
proc {action: "output", process_id: "dev", tail: 20}
→ Last 20 lines of output
```

### Head Output

Get only the first N lines:

```json
proc {action: "output", process_id: "build", head: 50}
→ First 50 lines (useful for seeing startup messages)
```

### Filter with Grep

```json
proc {action: "output", process_id: "test", grep: "FAIL"}
→ Only lines containing "FAIL"

proc {action: "output", process_id: "dev", grep: "error", grep_v: true}
→ Only lines NOT containing "error"
```

### Combined Filters

Filters apply in order: grep → head → tail

```json
proc {action: "output", process_id: "test", grep: "PASS", tail: 10}
→ Last 10 lines that contain "PASS"
```

## Stopping Processes

### Graceful Stop

```json
proc {action: "stop", process_id: "dev"}
→ {
    "id": "dev",
    "state": "stopped",
    "exit_code": 0,
    "message": "Process stopped gracefully"
  }
```

Sends SIGTERM, waits 5 seconds, then SIGKILL if needed.

### Force Stop

```json
proc {action: "stop", process_id: "dev", force: true}
→ Immediate SIGKILL (no graceful period)
```

## Port Cleanup

Kill orphaned processes holding a port:

```json
proc {action: "cleanup_port", port: 3000}
→ {
    "port": 3000,
    "killed_pids": [12345, 12346],
    "message": "Killed 2 processes on port 3000"
  }
```

Essential for:
- Dev servers that crash without cleanup
- OAuth callback servers left running
- Multiple dev sessions on same machine

## Real-World Examples

### Development Workflow

```json
// Start frontend and backend
run {script_name: "dev", id: "frontend", path: "./apps/web"}
run {script_name: "dev", id: "backend", path: "./apps/api"}

// Monitor both
proc {action: "list"}

// Check backend startup
proc {action: "output", process_id: "backend", tail: 10}

// When done
proc {action: "stop", process_id: "frontend"}
proc {action: "stop", process_id: "backend"}
```

### Test Debugging

```json
// Run tests
run {script_name: "test", mode: "foreground"}
→ {exit_code: 1}  // Tests failed

// Find failures
proc {action: "output", process_id: "test", grep: "FAIL"}
→ "FAIL src/utils.test.ts > formatDate > handles null input"

// Get context around failure
proc {action: "output", process_id: "test", grep: "formatDate", tail: 20}
```

### Build Monitoring

```json
// Start build
run {script_name: "build", id: "prod-build"}

// Check progress (while running)
proc {action: "output", process_id: "prod-build", tail: 5}
→ "[12:00:05] Compiling TypeScript...
    [12:00:08] Bundling assets...
    [12:00:10] 75% complete..."

// Wait for completion
proc {action: "status", process_id: "prod-build"}
→ {state: "stopped", exit_code: 0, runtime: "45s"}
```

### Handling Stuck Processes

```json
// Process not responding
proc {action: "status", process_id: "dev"}
→ {state: "running", runtime: "2h30m"}

// Try graceful stop
proc {action: "stop", process_id: "dev"}
→ {error: "timeout waiting for process"}

// Force kill
proc {action: "stop", process_id: "dev", force: true}
→ {state: "stopped", message: "Process killed"}

// Clean up any orphaned children
proc {action: "cleanup_port", port: 3000}
```

## Process States

| State | Description |
|-------|-------------|
| `pending` | Created but not started |
| `starting` | Process spawn in progress |
| `running` | Actively executing |
| `stopping` | Graceful shutdown in progress |
| `stopped` | Completed successfully |
| `failed` | Exited with error or crashed |

## Output Truncation

Each stream (stdout/stderr) has a 256KB ring buffer:

- Old output is discarded when buffer is full
- `truncated: true` in response indicates data was lost
- Use `grep`/`tail` to find specific content in large outputs

## Error Handling

### Process Not Found

```json
proc {action: "status", process_id: "nonexistent"}
→ {error: "process not found", process_id: "nonexistent"}
```

### ID Already Exists

```json
run {script_name: "dev", id: "my-server"}
run {script_name: "dev", id: "my-server"}  // Second call
→ {error: "process already exists", process_id: "my-server"}
```

### Script Not Found

```json
run {script_name: "nonexistent"}
→ {error: "script not found", script_name: "nonexistent"}
```

## Best Practices

1. **Use Descriptive IDs** - `frontend-dev` not `dev1`
2. **Check Exit Codes** - Zero means success
3. **Monitor Long Processes** - Periodically check status
4. **Clean Up Ports** - Use `cleanup_port` before starting servers
5. **Use Foreground for Short Commands** - Builds, tests, one-shot scripts
6. **Use Background for Servers** - Dev servers, watchers, long-running tasks

## Next Steps

- Set up the [Reverse Proxy](/features/reverse-proxy) to debug frontend issues
- Learn about [Graceful Shutdown](/concepts/graceful-shutdown) behavior
