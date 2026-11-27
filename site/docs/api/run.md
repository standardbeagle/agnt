---
sidebar_position: 2
---

# run

Execute project scripts or raw commands with process management.

## Synopsis

```json
// Run a project script
run {script_name: "<name>"}

// Run a raw command
run {raw: true, command: "<executable>", args: ["arg1", "arg2"]}
```

## Parameters

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `script_name` | string | Yes* | Script name from detect (e.g., "test", "build") |
| `raw` | boolean | No | Set to true to run raw command instead of script |
| `command` | string | Yes** | Executable to run (when `raw: true`) |
| `args` | string[] | No | Arguments for raw command |
| `id` | string | No | Custom process ID (auto-generated if omitted) |
| `path` | string | No | Working directory (defaults to current) |
| `mode` | string | No | Execution mode: `background`, `foreground`, `foreground-raw` |

\* Required if `raw` is not true
\** Required if `raw` is true

## Execution Modes

### Background (Default)

Returns immediately with process ID. Process runs asynchronously.

```json
run {script_name: "dev"}
→ {
    "process_id": "dev",
    "state": "running",
    "message": "Process started in background"
  }
```

Use `proc` tool to monitor.

### Foreground

Waits for completion. Returns exit code and runtime.

```json
run {script_name: "build", mode: "foreground"}
→ {
    "process_id": "build",
    "state": "stopped",
    "exit_code": 0,
    "runtime": "12.3s"
  }
```

Output accessible via `proc {action: "output"}`.

### Foreground-Raw

Waits for completion. Includes stdout/stderr in response.

```json
run {script_name: "test", mode: "foreground-raw"}
→ {
    "process_id": "test",
    "state": "stopped",
    "exit_code": 0,
    "runtime": "5.2s",
    "stdout": "✓ 42 tests passed\n",
    "stderr": ""
  }
```

Best for short commands where output is needed immediately.

## Examples

### Run Project Scripts

```json
// Run tests
run {script_name: "test"}

// Run build and wait
run {script_name: "build", mode: "foreground"}

// Run lint with immediate output
run {script_name: "lint", mode: "foreground-raw"}
```

### Run Raw Commands

```json
// Go module tidy
run {raw: true, command: "go", args: ["mod", "tidy"], mode: "foreground-raw"}

// cURL request
run {raw: true, command: "curl", args: ["-s", "https://api.example.com/health"]}

// Docker command
run {raw: true, command: "docker", args: ["ps"]}
```

### Custom Process IDs

```json
// Named dev servers for multi-project
run {script_name: "dev", id: "frontend-dev", path: "./apps/web"}
run {script_name: "dev", id: "backend-dev", path: "./apps/api"}

// Check status by ID
proc {action: "status", process_id: "frontend-dev"}
```

### Working Directory

```json
// Run in subdirectory
run {script_name: "test", path: "./packages/core"}

// Run in monorepo package
run {script_name: "build", path: "./apps/web", id: "web-build"}
```

## Response

### Background Mode

```typescript
interface RunBackgroundResponse {
  process_id: string;
  state: "starting" | "running";
  message: string;
}
```

### Foreground Mode

```typescript
interface RunForegroundResponse {
  process_id: string;
  state: "stopped" | "failed";
  exit_code: number;
  runtime: string;
}
```

### Foreground-Raw Mode

```typescript
interface RunForegroundRawResponse {
  process_id: string;
  state: "stopped" | "failed";
  exit_code: number;
  runtime: string;
  stdout: string;
  stderr: string;
}
```

## Error Responses

### Script Not Found

```json
{
  "error": "script not found",
  "script_name": "nonexistent"
}
```

### ID Already Exists

```json
{
  "error": "process already exists",
  "process_id": "dev"
}
```

### Command Failed to Start

```json
{
  "error": "command not found: unknowncmd",
  "command": "unknowncmd"
}
```

## Process Lifecycle

After starting with `run`:

1. **Monitor**: `proc {action: "status", process_id: "..."}`
2. **View Output**: `proc {action: "output", process_id: "..."}`
3. **Stop**: `proc {action: "stop", process_id: "..."}`

## See Also

- [proc](/api/proc) - Process management
- [detect](/api/detect) - Discover available scripts
- [Process Management Feature](/features/process-management)
