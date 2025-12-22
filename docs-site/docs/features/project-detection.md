---
sidebar_position: 1
---

# Project Detection

agnt automatically detects your project type, package manager, and available scripts without any configuration.

## How It Works

The `detect` tool analyzes your project directory to identify:

1. **Project Type** - Go, Node.js, or Python
2. **Package Manager** - npm, pnpm, yarn, bun (Node.js) or pip, poetry, pipenv (Python)
3. **Project Name** - From manifest files (package.json, go.mod, pyproject.toml)
4. **Available Scripts** - Commands you can run with the `run` tool

## Detection Hierarchy

Projects are detected in priority order:

1. **Go** - Presence of `go.mod`
2. **Node.js** - Presence of `package.json`
3. **Python** - Checks `pyproject.toml` → `setup.py` → `setup.cfg` → `requirements.txt`

If multiple project markers exist, the first match wins.

## Usage

### Basic Detection

```json
detect {path: "."}
```

Response for a Node.js project:

```json
{
  "type": "node",
  "package_manager": "pnpm",
  "name": "my-react-app",
  "version": "1.0.0",
  "scripts": ["dev", "build", "test", "lint", "preview"]
}
```

### Detect Specific Directory

```json
detect {path: "./packages/frontend"}
```

Useful for monorepos where different packages have different project types.

## Supported Project Types

### Go Projects

Detected by `go.mod` file. Extracts module name.

```json
{
  "type": "go",
  "name": "github.com/user/myproject",
  "scripts": ["build", "test", "lint", "vet", "fmt"]
}
```

Default Go scripts:
| Script | Command |
|--------|---------|
| `build` | `go build ./...` |
| `test` | `go test ./...` |
| `lint` | `golangci-lint run` |
| `vet` | `go vet ./...` |
| `fmt` | `go fmt ./...` |

### Node.js Projects

Detected by `package.json`. Package manager detected from lockfiles:

| Lockfile | Package Manager |
|----------|----------------|
| `pnpm-lock.yaml` | pnpm |
| `yarn.lock` | yarn |
| `bun.lockb` | bun |
| `package-lock.json` | npm |

```json
{
  "type": "node",
  "package_manager": "pnpm",
  "name": "my-app",
  "version": "2.1.0",
  "scripts": ["dev", "build", "test", "lint", "typecheck"]
}
```

Scripts are read directly from `package.json`.

### Python Projects

Detected by multiple markers in priority order:

1. `pyproject.toml` - Modern Python projects (Poetry, Hatch, etc.)
2. `setup.py` - Traditional setuptools
3. `setup.cfg` - Declarative setuptools
4. `requirements.txt` - Basic pip projects

```json
{
  "type": "python",
  "name": "my-django-app",
  "scripts": ["test", "lint", "format", "type-check"]
}
```

Default Python scripts:
| Script | Command |
|--------|---------|
| `test` | `pytest` |
| `lint` | `flake8` or `ruff check` |
| `format` | `black .` |
| `type-check` | `mypy .` |

## Real-World Examples

### Monorepo Detection

```bash
my-monorepo/
├── apps/
│   ├── web/           # Node.js (Next.js)
│   │   └── package.json
│   └── api/           # Go
│       └── go.mod
├── packages/
│   └── shared/        # Node.js (library)
│       └── package.json
└── scripts/           # Python (tooling)
    └── pyproject.toml
```

Detect each project:

```json
detect {path: "./apps/web"}
→ {type: "node", package_manager: "pnpm", scripts: ["dev", "build"]}

detect {path: "./apps/api"}
→ {type: "go", scripts: ["build", "test"]}

detect {path: "./scripts"}
→ {type: "python", scripts: ["lint", "format"]}
```

### CI/CD Integration

Use detection to run the right commands without hardcoding:

```yaml
# GitHub Actions example
- name: Detect Project
  run: |
    PROJECT_TYPE=$(agnt detect --path . | jq -r '.type')
    echo "PROJECT_TYPE=$PROJECT_TYPE" >> $GITHUB_ENV

- name: Run Tests
  run: |
    if [ "$PROJECT_TYPE" = "node" ]; then
      pnpm test
    elif [ "$PROJECT_TYPE" = "go" ]; then
      go test ./...
    fi
```

### IDE Integration

AI assistants can use detection to provide context-aware suggestions:

```
User: "How do I run tests?"

AI: [detect {path: "."}]
    → Node.js project with pnpm

AI: "Run tests with: pnpm test

    Or use the run tool:
    run {script_name: "test"}"
```

## Error Handling

### No Project Detected

If no project markers are found:

```json
{
  "error": "no project detected",
  "path": "/some/empty/directory"
}
```

### Invalid Path

If the path doesn't exist:

```json
{
  "error": "path does not exist",
  "path": "/nonexistent/path"
}
```

## Best Practices

1. **Use Relative Paths** - Start with `.` for the current directory
2. **Detect Before Running** - Always detect to know available scripts
3. **Handle Monorepos** - Detect individual packages, not the root
4. **Cache Results** - Detection is fast but results don't change during a session

## Next Steps

Once you've detected your project, use the [run tool](/api/run) to execute scripts and the [proc tool](/api/proc) to manage running processes.
