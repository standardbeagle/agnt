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
  package_manager?: string;  // npm, pnpm, yarn, bun, pip, poetry, etc.
  name?: string;             // Project name from manifest
  version?: string;          // Project version
  scripts: string[];         // Available script names
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
  "version": "1.0.0",
  "scripts": ["dev", "build", "test", "lint", "typecheck"]
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
  "scripts": ["build", "test", "lint", "vet", "fmt"]
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
  "scripts": ["test", "lint", "format", "type-check"]
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
