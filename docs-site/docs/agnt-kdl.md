---
sidebar_position: 3
title: ".agnt.kdl Configuration"
description: "Complete reference for .agnt.kdl — the configuration file that defines scripts, proxies, hooks, and alerts for agnt."
---

# .agnt.kdl Configuration

`.agnt.kdl` is a single file in your project root that tells agnt which scripts to run, how to link proxies to them, and what behavior to enable. It uses [KDL](https://kdl.dev) — a node-based document format that's easier to read and write than JSON or YAML for configuration.

agnt looks for `.agnt.kdl` in your project directory and walks up to the filesystem root if needed. If no file exists, agnt falls back to its defaults (no scripts, no proxies).

## File Structure

```kdl
project {
    // optional metadata
}

scripts {
    // one or more named script blocks
}

proxies {
    // one or more named proxy blocks
}

hooks {
    on-response {
        // notification behavior when AI responds
    }
}

toast {
    // notification display settings
}

alerts {
    // process output monitoring
}

ai {
    // AI agent configuration
}
```

## project

Optional metadata block. None of these fields change behavior — they're informational.

```kdl
project {
    name "my-api"
    type "go"
    port-conflict "prompt"
}
```

| Property | Type | Default | Description |
|----------|------|---------|-------------|
| `name` | string | — | Display name for the project |
| `type` | string | — | Project type hint (`go`, `node`, `python`, `dotnet-vite`, etc.) |
| `port-conflict` | string | `prompt` | Policy when a declared port is already in use. One of `prompt`, `auto-kill`, `skip`, `fail` |

### port-conflict policies

When agnt starts autostart scripts, it scans all declared `ports` for unmanaged processes already listening on them. The policy determines what happens:

| Policy | Behavior |
|--------|----------|
| `prompt` | Returns conflicts to the client and waits for instructions |
| `auto-kill` | Kills the blocking process tree automatically |
| `skip` | Logs a warning and starts the script anyway |
| `fail` | Aborts autostart entirely |

## scripts

Each named block under `scripts` defines a runnable process.

```kdl
scripts {
    dev {
        run "npm run dev"
        autostart true
        url-matchers "Local:\\s+{url}"
        ports 5173
        cwd "packages/frontend"
    }
}
```

### Command properties

| Property | Type | Description |
|----------|------|-------------|
| `run` | string | Shell command to execute. Run via `sh -c` on Unix, `cmd.exe /c` on Windows |
| `command` | string | Executable name (used with `args` instead of `run`) |
| `args` | string[] | Arguments for `command` |
| `shell` | string | Override the default shell (`bash`, `powershell`, `cmd.exe`, etc.) |
| `shell-args` | string[] | Override the default shell arguments |
| `cwd` | string | Working directory for the process. Relative to the `.agnt.kdl` file location |
| `env` | map | Environment variables set for this process |

Use `run` for shell commands. Use `command` + `args` when you need to invoke an executable directly without a shell wrapper.

```kdl
// Shell command (most common)
dev { run "npm run dev" }

// Direct executable — no shell involved
server { command "node"; args "server.js" "--port" "4000" }

// Custom shell (Git Bash on Windows)
dev { run "npm run dev"; shell "C:\\Program Files\\Git\\bin\\bash.exe" }
```

### Startup properties

| Property | Type | Default | Description |
|----------|------|---------|-------------|
| `autostart` | bool | `false` | Start this script automatically when a session opens |
| `depends-on` | string or block | — | Scripts that must be ready before this one starts |
| `auto-restart` | bool | `false` | Restart the process automatically when it exits |

### Readiness properties

These control how agnt knows a script is ready to serve requests. Readiness matters for `depends-on` ordering — a dependent script won't start until its dependency signals readiness.

There are two mutually exclusive mechanisms:

**`url-matchers`** — Pattern-match the process output for a URL. When a line matches, the extracted URL is used for proxy linking and the script is marked ready. This is the preferred approach for any server that prints a startup message.

**`probe-port`** — Poll a TCP port every 500ms until it accepts a connection. Use this for servers that bind a port but don't print a recognizable URL to stdout. Requires `ports` to be set. Mutually exclusive with `url-matchers`.

If neither is set, the script is marked ready when it starts or exits — no readiness gating occurs.

| Property | Type | Description |
|----------|------|-------------|
| `url-matchers` | string[] | Regex patterns to match against process output. Use `{url}` as a placeholder for localhost URLs |
| `probe-port` | bool | Enable TCP port polling for readiness. Requires `ports` |

```kdl
// Preferred: match the server's startup message
backend {
    run "dotnet watch run --project src/App.csproj"
    url-matchers "Now listening on {url}"
    ports 5000
}

// Fallback: server binds port silently, no startup message
worker {
    run "./background-worker"
    ports 8080
    probe-port true
}
```

### Port properties

| Property | Type | Description |
|----------|------|-------------|
| `ports` | int[] | Ports used by this process. Used for pre-flight conflict detection and EADDRINUSE recovery |

`ports` serves two purposes regardless of the readiness mechanism:
- **Pre-flight**: Before starting, agnt checks if something else is already listening on these ports
- **EADDRINUSE recovery**: If the process fails to start because a port is in use, agnt can retry after clearing it

### Health monitoring properties

| Property | Type | Description |
|----------|------|-------------|
| `error-pattern` | string | Regex that flags the process as unhealthy when matched in output |
| `healthy-pattern` | string | Regex that clears the unhealthy flag when matched in output |

If neither is set, agnt uses built-in patterns that cover common frameworks (Go, Node, Python, .NET).

### depends-on

Controls startup order between scripts. A script with `depends-on` waits for all listed dependencies to reach a ready state before starting.

```kdl
scripts {
    backend {
        run "go run ./cmd/server"
        url-matchers "Listening on {url}"
        autostart true
    }
    frontend {
        run "npm run dev"
        autostart true
        depends-on "backend"
        url-matchers "Local:\\s+{url}"
    }
}
```

The dependency is satisfied when the backend script either prints a URL matching its `url-matchers` or reaches a port probe success. If the backend crashes or exits before becoming ready, the frontend starts anyway with a warning.

**Per-dependency timeout** — default is 120 seconds, configurable per dependency:

```kdl
// All dependencies get the same timeout
frontend {
    depends-on "backend" "redis" timeout=60
}

// Per-dependency timeout
frontend {
    depends-on {
        api timeout=30
        worker timeout=120
    }
}
```

## proxies

Each named block under `proxies` defines a reverse proxy into a dev server.

```kdl
proxies {
    app {
        script "dev"
        fallback-port 5173
    }
}
```

### Proxy properties

| Property | Type | Default | Description |
|----------|------|---------|-------------|
| `autostart` | bool | `false` | Start automatically when the session opens |
| `script` | string | — | Link to a script — proxy target is auto-detected from script's URL output |
| `url-pattern` | string | — | Regex filter for which detected URLs to use. Filters on the full URL string |
| `fallback-port` | int | — | Port to use if URL detection from the script fails |
| `url` | string | — | Direct target URL (e.g., `http://localhost:3000`) |
| `port` | int | — | Direct target port (shorthand for `http://localhost:PORT`) |
| `target` | string | — | Deprecated. Use `url` instead |
| `host` | string | `localhost` | Target hostname |
| `bind` | string | `127.0.0.1` | Listen address. `0.0.0.0` to expose on all interfaces (for tunnel/mobile testing) |
| `websocket` | bool | `false` | Enable WebSocket proxying |
| `max-log-size` | int | `1000` | Maximum traffic log entries to keep |

### Script-linked proxies

When a proxy has `script` set, agnt creates it automatically when the linked script prints a URL matching its `url-matchers`. This is the most common setup — no hardcoded ports.

```kdl
scripts {
    dev {
        run "npm run dev"
        url-matchers "Local:\\s+{url}"
        autostart true
    }
}

proxies {
    app {
        script "dev"
        fallback-port 5173
    }
}
```

`fallback-port` is a safety net. If the dev server starts but its URL output doesn't match the pattern (or the process doesn't print a URL at all), agnt creates the proxy targeting `localhost:fallback-port` after a delay.

### Explicit target proxies

When a proxy has a direct `url` or `port` set and no `script`, it starts on its own:

```kdl
proxies {
    external-api {
        url "http://localhost:8080"
        autostart true
    }
}
```

### URL pattern filtering

Some servers print multiple URLs (e.g., Wails prints both a Vite frontend URL and a backend URL). `url-pattern` filters which one the proxy targets:

```kdl
proxies {
    // Only proxy the Wails backend, not the Vite frontend
    wails-app {
        script "wails-dev"
        url-pattern ":34115"
    }
}
```

## hooks

Controls what happens when the AI agent responds.

```kdl
hooks {
    on-response {
        toast true
        indicator true
        sound false
    }
}
```

| Property | Type | Default | Description |
|----------|------|---------|-------------|
| `toast` | bool | `true` | Show a toast notification in the browser |
| `indicator` | bool | `true` | Update the floating indicator |
| `sound` | bool | `false` | Play a notification sound |

## toast

Controls toast notification display.

```kdl
toast {
    duration 4000
    position "bottom-right"
    max-visible 3
}
```

| Property | Type | Default | Description |
|----------|------|---------|-------------|
| `duration` | int | `4000` | Display duration in milliseconds |
| `position` | string | `bottom-right` | One of `top-left`, `top-right`, `bottom-left`, `bottom-right` |
| `max-visible` | int | `3` | Maximum simultaneous toasts |

## alerts

Controls process output monitoring. agnt scans process stdout/stderr for error patterns and surfaces them through `get_incidents`.

```kdl
alerts {
    enabled true
    batch-window 3
    dedupe-window 60

    patterns {
        "db-connection" {
            pattern "ECONNREFUSED"
            severity "error"
        }
    }

    disable "connection-refused"
}
```

| Property | Type | Default | Description |
|----------|------|---------|-------------|
| `enabled` | bool | `true` | Enable or disable alert monitoring entirely |
| `batch-window` | int | `3` | Seconds to collect alerts before delivering (reduces noise) |
| `dedupe-window` | int | `60` | Seconds to suppress duplicate alerts |
| `patterns` | map | — | Custom alert patterns keyed by ID |
| `disable` | string[] | — | Built-in pattern IDs to disable |

Each custom pattern has:

| Property | Type | Description |
|----------|------|-------------|
| `pattern` | string | Regex matched against each output line |
| `severity` | string | `error`, `warning`, or `info` |

## ai

Controls how agnt configures AI agent sessions started with `agnt run` or `agnt ai`.

```kdl
ai {
    skill "code-review"
    env {
        ANTHROPIC_API_KEY "sk-..."
    }
    append-system-prompt "This project uses GraphQL. Always check the schema before writing queries."
}
```

| Property | Type | Description |
|----------|------|-------------|
| `skill` | string | Skill/persona name to apply to the session |
| `env` | map | Environment variables set for AI commands |
| `system-prompt` | string | Full system prompt replacement |
| `append-system-prompt` | string | Text appended to the default agnt system prompt |

Use `append-system-prompt` over `system-prompt` — the default prompt includes tool documentation for agnt's MCP tools, which `system-prompt` would discard entirely.

## Complete Examples

### Single Vite app

```kdl
scripts {
    dev {
        run "npm run dev"
        autostart true
        url-matchers "(Local|Network):\\s*{url}"
    }
}

proxies {
    app {
        script "dev"
    }
}

hooks {
    on-response {
        toast true
        indicator true
    }
}
```

### Full-stack with separate backend and frontend

```kdl
scripts {
    backend {
        run "go run ./cmd/server"
        autostart true
        url-matchers "Listening on {url}"
        ports 4000
    }
    frontend {
        run "npm run dev"
        cwd "web"
        autostart true
        depends-on "backend"
        url-matchers "Local:\\s+{url}"
        ports 5173
    }
}

proxies {
    app {
        script "frontend"
        fallback-port 5173
    }
}

hooks {
    on-response {
        toast true
        indicator true
    }
}
```

### .NET backend with silent worker process

```kdl
scripts {
    api {
        run "dotnet watch run --project src/Api.csproj"
        autostart true
        url-matchers "Now listening on {url}"
        ports 5000
    }
    worker {
        run "./worker --port 8080"
        autostart true
        ports 8080
        probe-port true
    }
}

proxies {
    app {
        script "api"
        fallback-port 5000
    }
}
```
