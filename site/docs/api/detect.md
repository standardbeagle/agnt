---
sidebar_position: 1
---

# detect

Detect project type, package manager, and available scripts.

## Synopsis

```json
detect {path: "<directory>"}
```

## Parameters

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `path` | string | No | `.` | Directory to analyze |

## Response

```typescript
interface DetectResponse {
  type: "go" | "node" | "python" | "unknown";
  name: string;              // Project name from manifest
  framework?: string;        // Detected framework (e.g. next, vite, django)
  package_manager?: string;  // npm, pnpm, yarn, bun, pip, poetry, etc.
  scripts: DetectScript[];   // Full script descriptors
  script_names: string[];    // Convenience list of script names
  metadata?: Record<string, string>;
  summary?: string;          // Human-readable detection summary
}

interface DetectScript {
  name: string;
  command: string;
  proc_run: string;          // Suggested `run` command for proc/run
  proc_wait: string;         // Readiness wait hint
  likely_signals: string[];  // URL/error signals this script tends to emit
}
```

## Examples

### Basic Detection

```json
detect {path: "."}
```

Response:
```json
{
  "type": "node",
  "package_manager": "pnpm",
  "name": "my-react-app",
  "framework": "vite",
  "script_names": ["dev", "build", "test", "lint", "typecheck"],
  "scripts": [
    {
      "name": "dev",
      "command": "vite",
      "proc_run": "pnpm run dev",
      "proc_wait": "Local:\\s+{url}",
      "likely_signals": ["url", "http_error"]
    }
  ]
}
```

### Detect Subdirectory

```json
detect {path: "./packages/api"}
```

### Go Project

```json
detect {path: "./backend"}
```

Response:
```json
{
  "type": "go",
  "name": "github.com/user/myproject",
  "script_names": ["build", "test", "lint", "vet", "fmt"]
}
```

### Python Project

```json
detect {path: "./scripts"}
```

Response:
```json
{
  "type": "python",
  "name": "my-python-app",
  "script_names": ["test", "lint", "format", "type-check"]
}
```

## Detection Logic

### Priority Order

1. **Go** - Checks for `go.mod`
2. **Node.js** - Checks for `package.json`
3. **Python** - Checks for `pyproject.toml` → `setup.py` → `setup.cfg` → `requirements.txt`

### Package Manager Detection (Node.js)

| Lockfile | Package Manager |
|----------|-----------------|
| `pnpm-lock.yaml` | pnpm |
| `yarn.lock` | yarn |
| `bun.lockb` | bun |
| `package-lock.json` | npm |

## Default Scripts

### Go

| Script | Command |
|--------|---------|
| `build` | `go build ./...` |
| `test` | `go test ./...` |
| `lint` | `golangci-lint run` |
| `vet` | `go vet ./...` |
| `fmt` | `go fmt ./...` |

### Node.js

Scripts from `package.json` are used directly.

### Python

| Script | Command |
|--------|---------|
| `test` | `pytest` |
| `lint` | `flake8` or `ruff check` |
| `format` | `black .` |
| `type-check` | `mypy .` |

## Error Responses

### No Project Detected

```json
{
  "error": "no project detected",
  "path": "/some/empty/directory"
}
```

### Invalid Path

```json
{
  "error": "path does not exist",
  "path": "/nonexistent/path"
}
```

## See Also

- [run](/api/run) - Execute detected scripts
- [Project Detection Feature](/features/project-detection)
