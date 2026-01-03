# go-cli-server Migration Plan

## Overview

Rename `go-mcp-hub` → `go-cli-server` and fully migrate `agnt` to use it, removing ~10k lines of duplicated code.

**Current State:**
- `go-mcp-hub`: ~5.7k lines (protocol, process, socket, hub, client)
- `agnt internal/daemon/`: ~10k lines (much duplicated)
- `agnt internal/process/`: ~1.2k lines (fully duplicated)

**Target State:**
- `go-cli-server`: ~7.2k lines (add scheduler, pidtracker, resilient, autostart)
- `agnt`: Uses go-cli-server + agnt-specific handlers only

---

## Phase 1: Enhance go-cli-server (Add Missing Components)

### 1.1 Rename Repository
```bash
# In go-mcp-hub repo
git mv go-mcp-hub go-cli-server
# Update go.mod: module github.com/standardbeagle/go-cli-server
# Update all imports
```

### 1.2 Add Scheduler Package (~300 lines)
**Source:** `agnt/internal/daemon/scheduler.go`, `scheduler_state.go`

```
go-cli-server/
└── scheduler/
    ├── scheduler.go      # ScheduledTask, Scheduler, tick loop
    ├── state.go          # SchedulerStateManager for persistence
    └── scheduler_test.go
```

**Key Types:**
```go
type ScheduledTask struct {
    ID          string
    SessionCode string
    Message     string
    DeliverAt   time.Time
    Status      TaskStatus  // Pending, Delivered, Failed, Cancelled
    Attempts    int
    CreatedAt   time.Time
}

type Scheduler struct {
    tasks       sync.Map
    tickPeriod  time.Duration
    maxRetries  int
    deliverFunc func(sessionCode, message string) error
}
```

### 1.3 Add PIDTracker Implementation (~300 lines)
**Source:** `agnt/internal/daemon/pidtracker.go`, `*_unix.go`, `*_windows.go`

```
go-cli-server/
└── process/
    ├── pidtracker.go         # Interface + disk-based implementation
    ├── pidtracker_unix.go    # Unix signal handling
    └── pidtracker_windows.go # Windows Job Objects
```

**Key Interface:**
```go
type PIDTracker interface {
    Add(processID string, pid, pgid int, projectPath string) error
    Remove(processID string) error
    CleanupOrphans() error
    SetDaemonPID(pid int) error
}
```

### 1.4 Enhance Client Package (~600 lines)
**Source:** `agnt/internal/daemon/resilient.go`, `autostart.go`

```
go-cli-server/
└── client/
    ├── conn.go           # (existing) Basic connection
    ├── resilient.go      # NEW: Auto-reconnection, heartbeat
    ├── autostart.go      # NEW: Auto-start daemon wrapper
    └── resilient_test.go
```

**ResilientConn Features:**
- Automatic reconnection on disconnect
- Heartbeat monitoring (configurable interval)
- Exponential backoff (100ms → 30s)
- Connect/disconnect/reconnect-failed callbacks
- Version mismatch handling

**AutoStartConn Features:**
- Wraps ResilientConn
- Starts daemon if not running
- Startup lock to prevent race conditions
- Retry loop with backoff

### 1.5 Add Version Management (~100 lines)
```
go-cli-server/
└── hub/
    ├── version.go  # Version constant, comparison utilities
```

### 1.6 Register Scheduler Commands in Hub
Add to `hub/hub.go`:
```go
// In registerBuiltinCommands()
if h.scheduler != nil {
    h.commands.Register(CommandDefinition{
        Verb:     "SCHEDULE",
        SubVerbs: []string{"ADD", "CANCEL", "LIST", "STATUS"},
        Handler:  h.handleSchedule,
    })
}
```

---

## Phase 2: Migrate agnt to Use go-cli-server

### 2.1 Update go.mod
```go
require github.com/standardbeagle/go-cli-server v0.1.0

// Remove local replace directive after publishing
```

### 2.2 Delete Duplicated Packages

**DELETE entirely:**
```
internal/process/           # Use go-cli-server/process
├── manager.go
├── process.go
├── lifecycle.go
├── lifecycle_unix.go
├── lifecycle_windows.go
├── ringbuf.go
└── *_test.go
```

**DELETE from internal/daemon/:**
```
internal/daemon/
├── scheduler.go           # Use go-cli-server/scheduler
├── scheduler_state.go
├── pidtracker.go          # Use go-cli-server/process
├── pidtracker_unix.go
├── pidtracker_windows.go
├── resilient.go           # Use go-cli-server/client
├── autostart.go           # Use go-cli-server/client
├── autostart_unix.go
├── autostart_windows.go
├── conn.go                # Use go-cli-server/socket
├── socket.go
└── socket_windows.go
```

### 2.3 Refactor internal/daemon/ to Use go-cli-server

**KEEP but refactor:**
```
internal/daemon/
├── daemon.go       # Wrap go-cli-server/hub.Hub, add agnt-specific init
├── connection.go   # Embed go-cli-server/hub.Connection, add agnt methods
├── handler.go      # Keep all 58 handlers, register as custom commands
├── client.go       # Wrap go-cli-server/client.ResilientConn
├── session.go      # Keep agnt-specific session cleanup logic
├── state.go        # Keep proxy state persistence
├── upgrade.go      # Keep version upgrade logic
└── version.go      # Keep for agnt-specific version
```

### 2.4 Register agnt-Specific Commands

In `daemon.go`:
```go
func NewDaemon(config Config) *Daemon {
    // Create go-cli-server hub
    h := hub.New(hub.Config{
        SocketName:        "agnt",
        EnableProcessMgmt: true,
        EnableScheduler:   true,
        Version:           version.Version,
    })

    d := &Daemon{hub: h, ...}

    // Register agnt-specific commands
    d.registerAgntCommands()

    return d
}

func (d *Daemon) registerAgntCommands() {
    d.hub.RegisterCommand(hub.CommandDefinition{
        Verb:     "PROXY",
        SubVerbs: []string{"START", "STOP", "STATUS", "LIST", "EXEC", "TOAST"},
        Handler:  d.handleProxy,
    })

    d.hub.RegisterCommand(hub.CommandDefinition{
        Verb:     "PROXYLOG",
        SubVerbs: []string{"QUERY", "SUMMARY", "CLEAR", "STATS"},
        Handler:  d.handleProxyLog,
    })

    d.hub.RegisterCommand(hub.CommandDefinition{
        Verb:     "TUNNEL",
        SubVerbs: []string{"START", "STOP", "STATUS", "LIST"},
        Handler:  d.handleTunnel,
    })

    d.hub.RegisterCommand(hub.CommandDefinition{
        Verb:     "CHAOS",
        SubVerbs: []string{"ENABLE", "DISABLE", "STATUS", "SET", "PRESET", ...},
        Handler:  d.handleChaos,
    })

    d.hub.RegisterCommand(hub.CommandDefinition{
        Verb:     "CURRENTPAGE",
        SubVerbs: []string{"LIST", "GET", "SUMMARY", "CLEAR"},
        Handler:  d.handleCurrentPage,
    })

    d.hub.RegisterCommand(hub.CommandDefinition{
        Verb:     "OVERLAY",
        SubVerbs: []string{"SET", "GET", "CLEAR"},
        Handler:  d.handleOverlay,
    })

    d.hub.RegisterCommand(hub.CommandDefinition{
        Verb:    "DETECT",
        Handler: d.handleDetect,
    })
}
```

### 2.5 Update Client Usage

**Before:**
```go
import "github.com/standardbeagle/agnt/internal/daemon"

client := daemon.NewResilientClient(socketPath, daemon.ResilientConfig{...})
```

**After:**
```go
import "github.com/standardbeagle/go-cli-server/client"

conn := client.NewResilientConn(
    client.WithSocketPath(socketPath),
    client.WithHeartbeat(10*time.Second),
    client.WithAutoStart("agnt", "daemon", "start"),
)
```

---

## Phase 3: Update Protocol Package

### 3.1 Move agnt Verbs to Registration

**Before** (in `internal/protocol/commands.go`):
```go
const (
    VerbProxy     = "PROXY"
    VerbProxyLog  = "PROXYLOG"
    ...
)
```

**After** (register at daemon startup):
```go
func init() {
    // Register agnt-specific verbs with go-cli-server
    protocol.DefaultRegistry.RegisterVerb("PROXY", []string{"START", "STOP", ...})
    protocol.DefaultRegistry.RegisterVerb("PROXYLOG", []string{"QUERY", "CLEAR", ...})
    // etc.
}
```

### 3.2 Keep Only agnt-Specific Types
```
internal/protocol/
├── hub.go       # Re-exports from go-cli-server/protocol (keep)
├── commands.go  # ONLY agnt-specific constants and types
└── (delete parser.go, responses.go - already done)
```

---

## Phase 4: Testing & Validation

### 4.1 Unit Tests
- Run go-cli-server tests: `cd ~/work/go-cli-server && go test ./...`
- Run agnt tests: `cd ~/work/devtool && go test ./...`

### 4.2 Integration Tests
- Test daemon startup/shutdown
- Test process management (run, stop, output)
- Test proxy lifecycle
- Test scheduler (schedule, deliver, cancel)
- Test resilient client reconnection
- Test auto-start from cold

### 4.3 Manual Testing
```bash
# Start agnt daemon
agnt daemon start

# Test process commands
agnt mcp  # Start MCP server
# Use detect, run, proc tools

# Test proxy commands
# proxy start, exec, logs

# Test resilient reconnection
# Kill daemon, verify client reconnects
```

---

## File Inventory: Before & After

### go-cli-server (After Phase 1)

```
go-cli-server/                    # ~7,200 lines
├── go.mod
├── README.md
├── protocol/                     # ~700 lines
│   ├── commands.go
│   ├── responses.go
│   ├── parser.go
│   └── parser_test.go
├── process/                      # ~1,600 lines
│   ├── manager.go
│   ├── process.go
│   ├── lifecycle.go
│   ├── lifecycle_unix.go
│   ├── lifecycle_windows.go
│   ├── ringbuf.go
│   ├── pidtracker.go            # NEW
│   ├── pidtracker_unix.go       # NEW
│   ├── pidtracker_windows.go    # NEW
│   └── *_test.go
├── socket/                       # ~600 lines
│   ├── socket_unix.go
│   └── socket_windows.go
├── hub/                          # ~1,800 lines
│   ├── hub.go
│   ├── connection.go
│   ├── handlers.go
│   ├── registry.go
│   ├── version.go               # NEW
│   └── *_test.go
├── client/                       # ~1,200 lines
│   ├── conn.go
│   ├── resilient.go             # NEW
│   ├── autostart.go             # NEW
│   └── *_test.go
└── scheduler/                    # ~400 lines (NEW)
    ├── scheduler.go
    ├── state.go
    └── scheduler_test.go
```

### agnt (After Phase 2)

```
internal/daemon/                  # ~3,000 lines (down from ~10,000)
├── daemon.go                     # Wraps hub.Hub + agnt init
├── connection.go                 # Embeds hub.Connection + agnt methods
├── handler.go                    # 39 agnt-specific handlers only
├── client.go                     # Wraps client.ResilientConn
├── session.go                    # agnt-specific cleanup
├── state.go                      # Proxy state persistence
├── upgrade.go                    # Version upgrade logic
└── *_test.go

internal/process/                 # DELETED (0 lines, was ~1,200)

internal/protocol/                # ~200 lines (down from ~700)
├── hub.go                        # Re-exports
└── commands.go                   # agnt-specific constants only
```

**Net Reduction in agnt: ~8,000 lines**

---

## Implementation Order

| Step | Description | Est. Lines | Priority |
|------|-------------|------------|----------|
| 1.1 | Rename go-mcp-hub → go-cli-server | 0 | P0 |
| 1.2 | Add scheduler package | 400 | P1 |
| 1.3 | Add PIDTracker implementation | 300 | P1 |
| 1.4 | Add ResilientConn + AutoStartConn | 600 | P1 |
| 1.5 | Add version management | 100 | P2 |
| 2.1 | Update agnt go.mod | 5 | P0 |
| 2.2 | Delete internal/process/ | -1,200 | P0 |
| 2.3 | Delete duplicated daemon files | -4,000 | P1 |
| 2.4 | Refactor daemon.go to use hub.Hub | 200 | P1 |
| 2.5 | Register agnt commands | 100 | P1 |
| 2.6 | Update client usage | 50 | P1 |
| 3.1 | Move verbs to registration | 50 | P2 |
| 4.x | Testing & validation | 0 | P0 |

**Total new code in go-cli-server: ~1,400 lines**
**Total deleted from agnt: ~5,200 lines**
**Net project reduction: ~3,800 lines**

---

## Risks & Mitigations

| Risk | Impact | Mitigation |
|------|--------|------------|
| Breaking agnt during migration | High | Feature branch, comprehensive tests |
| go-cli-server API instability | Medium | Version pin, semantic versioning |
| Platform-specific bugs | Medium | Test on Linux, macOS, Windows |
| Performance regression | Low | Benchmark critical paths |
| Test coverage gaps | Medium | Require 80%+ coverage before merge |

---

## Success Criteria

1. **go-cli-server builds and tests pass** on Linux, macOS, Windows
2. **agnt builds and tests pass** with go-cli-server dependency
3. **No functional regression** in agnt features
4. **~8k lines removed** from agnt codebase
5. **LCI can use go-cli-server** to build its daemon

---

## Next Steps

1. Create `go-cli-server` repo (rename from go-mcp-hub)
2. Implement Phase 1 (add missing components)
3. Publish v0.1.0 to GitHub
4. Migrate agnt in feature branch
5. Test thoroughly
6. Merge and release agnt with go-cli-server dependency
