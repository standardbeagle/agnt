# Process Dependency Ordering Design

## Problem

Autostart scripts start in arbitrary map iteration order. Some processes need others to be accepting traffic before they can start successfully (e.g., a frontend dev server needs the API server listening).

## Design Decisions

- **Block startup** until dependencies are ready (with per-dependency timeout)
- **Multiple dependencies** supported per script
- **Readiness signal**: URL detection (primary) + TCP port probe (fallback)
- **No cascade restarts** on dependency restart (can be added later as opt-in)

## Configuration

```kdl
scripts {
    api {
        run "go run ./cmd/server"
        autostart true
        url-matchers "Listening on {url}"
    }
    frontend {
        run "npm run dev"
        autostart true
        depends-on "api" timeout=30
    }
    dashboard {
        run "npm run dashboard"
        autostart true
        depends-on "api" timeout=30 "frontend" timeout=15
    }
}
```

### ScriptDependency

```go
type ScriptDependency struct {
    Name    string        // Script name this depends on
    Timeout time.Duration // How long to wait for readiness (default 30s)
}
```

Added to `ScriptConfig` as `DependsOn []ScriptDependency`.

KDL parsing: `depends-on` node has script names as arguments. Each name can have a `timeout=N` property (seconds). Names without explicit timeout get 30s default.

### Validation at config load

- Circular dependency detection (topological sort fails) -> error
- Unknown script name in `depends-on` -> error
- Dependency on non-autostart script -> warning

## Startup Orchestration

### Topological Sort

Build a DAG from `depends-on` edges. Sort into layers:
- Layer 0: scripts with no dependencies (e.g., `api`, `redis`)
- Layer 1: scripts depending only on layer 0 (e.g., `frontend`)
- Layer 2: scripts depending on layer 1, etc.

Scripts within the same layer start in parallel.

### Per-Layer Execution

For each layer:
- Start all scripts in the layer concurrently (existing `autostartScript()`)
- For layer 0, no waiting needed
- For layer 1+, each script's goroutine blocks on a readiness channel for each dependency before calling `autostartScript()`

### ReadySignaler

```go
type ReadySignaler struct {
    signals map[string]chan struct{} // processID -> closed when ready
    mu      sync.Mutex
}
```

- `WaitReady(processID string, timeout time.Duration) error` - blocks until signal or timeout
- `SignalReady(processID string)` - closes the channel

### Readiness Detection (first wins)

1. URL tracker's `onURLDetected` callback calls `SignalReady()`
2. Background goroutine polls TCP connect on expected port every 500ms, calls `SignalReady()` on success

### Timeout Handling

If `WaitReady` times out:
- Log warning: `"Dependency 'api' not ready after 30s, starting 'frontend' anyway"`
- Start the dependent script regardless
- Don't fail entire autostart because of one slow dependency

## Implementation

### Files to modify

1. **`internal/config/agnt.go`** - Add `ScriptDependency`, `DependsOn` field, KDL parsing for `depends-on`, circular dependency validation
2. **`internal/daemon/ready_signaler.go`** (new) - `ReadySignaler` struct, TCP port polling
3. **`internal/daemon/daemon.go`** - Modify `RunAutostart()` for topological sort, layered start, wire `ReadySignaler`
4. **`internal/daemon/urltracker.go`** - Hook `onURLDetected` to call `SignalReady()`
5. **`internal/daemon/startup_resilience.go`** - Start TCP port probe after successful `StartScript()` if expected port known and no URL matchers

### New files

6. **`internal/daemon/ready_signaler_test.go`** - Unit tests for signaling, timeout, port probe
7. **`internal/config/agnt_deps_test.go`** - Config parsing, topo sort, cycle detection tests

### Not in scope

- Cascade restarts on dependency failure
- Non-autostart dependency ordering (manual `run` tool)
- Cross-project dependencies
