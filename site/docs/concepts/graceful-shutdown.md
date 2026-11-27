---
sidebar_position: 3
---

# Graceful Shutdown

agnt implements sophisticated shutdown handling to ensure clean process termination and resource cleanup.

## Shutdown Flow

When agnt receives a termination signal:

```
SIGINT/SIGTERM
      │
      ▼
┌─────────────────┐
│ Signal Handler  │
└────────┬────────┘
         │
         ▼
┌─────────────────┐
│ Set Deadline    │ (2 seconds for Ctrl+C)
└────────┬────────┘
         │
    ┌────┴────┐
    │         │
    ▼         ▼
ProcessMgr  ProxyMgr
(parallel)  (parallel)
    │         │
    ▼         ▼
All Done ────────► Exit
```

## Shutdown Modes

### Aggressive Mode (Ctrl+C)

When deadline is less than 3 seconds:

- **Immediate SIGKILL** to all processes
- No graceful termination period
- Completes in under 500ms typically

```go
// Ctrl+C triggers 2-second deadline
ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
pm.Shutdown(ctx)
```

### Normal Mode (Programmatic)

When deadline is 3+ seconds:

1. Send SIGTERM to all processes
2. Wait up to 5 seconds for graceful exit
3. Send SIGKILL to remaining processes

```go
// Programmatic shutdown with 30-second deadline
ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
pm.Shutdown(ctx)
```

## Process Group Handling

agnt uses process groups to ensure child processes are terminated:

```go
cmd := exec.Command(name, args...)
cmd.SysProcAttr = &syscall.SysProcAttr{
    Setpgid: true,  // Create new process group
}
```

When stopping:

```go
// Signal entire process group
syscall.Kill(-pid, syscall.SIGTERM)

// After timeout
syscall.Kill(-pid, syscall.SIGKILL)
```

This handles cases where your dev server spawns child processes (watchers, compilers, etc.).

## Shutdown Coordination

### Preventing New Work

```go
func (pm *ProcessManager) Register(id string, proc *ManagedProcess) error {
    if pm.shuttingDown.Load() {
        return ErrShuttingDown
    }
    // ... registration logic
}
```

### Health Check Termination

```go
func (pm *ProcessManager) Shutdown(ctx context.Context) error {
    // Stop health check goroutine
    close(pm.shutdownCh)

    // Wait for it to exit
    <-pm.healthDone

    // ... continue shutdown
}
```

### sync.Once Protection

```go
var shutdownOnce sync.Once

func (pm *ProcessManager) Shutdown(ctx context.Context) error {
    var err error
    shutdownOnce.Do(func() {
        err = pm.doShutdown(ctx)
    })
    return err
}
```

Prevents duplicate shutdown if signal received multiple times.

## Real-World Scenarios

### Dev Server with Watchers

```bash
# Your dev command spawns multiple processes:
pnpm dev
  ├── next dev (PID 1234)
  │     └── webpack --watch (PID 1235)
  └── tsc --watch (PID 1236)
```

Without process groups, only the parent dies. With agnt:

```
Ctrl+C
  │
  ▼
SIGKILL to process group (-1234)
  │
  ├── PID 1234 killed
  ├── PID 1235 killed
  └── PID 1236 killed
```

### Build in Progress

```bash
# Long-running build
go build -o app ./...
```

Behavior depends on mode:

**Aggressive (Ctrl+C)**:
- Build immediately killed
- Partial output files may remain
- Fast exit

**Normal**:
- SIGTERM sent
- Go compiler handles gracefully
- Clean exit within grace period

### Multiple Proxies

```go
// ProxyManager shuts down all proxies in parallel
func (pm *ProxyManager) Shutdown(ctx context.Context) error {
    var wg sync.WaitGroup

    pm.proxies.Range(func(key, value any) bool {
        wg.Add(1)
        go func(p *ProxyServer) {
            defer wg.Done()
            p.Stop()
        }(value.(*ProxyServer))
        return true
    })

    wg.Wait()
    return nil
}
```

## Context Cancellation

All operations respect context cancellation:

```go
func (p *ManagedProcess) Stop(ctx context.Context) error {
    // Send SIGTERM
    p.signal(syscall.SIGTERM)

    select {
    case <-p.doneCh:
        return nil  // Process exited gracefully
    case <-ctx.Done():
        // Context cancelled - force kill
        p.signal(syscall.SIGKILL)
        return ctx.Err()
    case <-time.After(gracefulTimeout):
        // Grace period expired - force kill
        p.signal(syscall.SIGKILL)
    }
}
```

## Error Handling

Shutdown errors are collected but don't prevent other shutdowns:

```go
func (pm *ProcessManager) Shutdown(ctx context.Context) error {
    var errs []error

    pm.processes.Range(func(key, value any) bool {
        if err := value.(*ManagedProcess).Stop(ctx); err != nil {
            errs = append(errs, err)
        }
        return true
    })

    return errors.Join(errs...)
}
```

## Timing Configuration

```go
const (
    // Ctrl+C gets aggressive shutdown
    AggressiveThreshold = 3 * time.Second

    // Grace period before SIGKILL
    GracefulTimeout = 5 * time.Second

    // Total shutdown timeout for Ctrl+C
    ShutdownTimeout = 2 * time.Second
)
```

## Testing Shutdown

```go
func TestGracefulShutdown(t *testing.T) {
    pm := NewProcessManager(config)

    // Start a process
    proc := pm.Start("test", cmd)

    // Shutdown with short timeout
    ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
    defer cancel()

    start := time.Now()
    err := pm.Shutdown(ctx)
    elapsed := time.Since(start)

    // Should complete quickly in aggressive mode
    assert.Less(t, elapsed, 200*time.Millisecond)

    // Process should be stopped
    assert.Equal(t, StateStopped, proc.State())
}
```

## Platform Considerations

### Linux/macOS

Process groups work as expected:
- `Setpgid: true` creates new group
- `kill(-pid, signal)` signals entire group

### Windows

Process groups work differently:
- Uses job objects instead
- `Setpgid` not available
- agnt handles this transparently

## Best Practices

1. **Use 2-second Ctrl+C timeout** - Fast response for interactive use
2. **Use longer timeouts for CI** - Allow graceful cleanup
3. **Always signal process group** - Catch child processes
4. **Check shuttingDown flag** - Prevent work during shutdown
5. **Use sync.Once** - Prevent duplicate shutdown

## Debugging Shutdown Issues

### Hanging on Shutdown

```bash
# Check for orphan processes
ps aux | grep your-script

# Force cleanup
proc {action: "cleanup_port", port: 3000}
```

### Zombie Processes

The health check goroutine detects zombies:

```go
func (pm *ProcessManager) healthCheck() {
    ticker := time.NewTicker(10 * time.Second)
    for {
        select {
        case <-ticker.C:
            pm.processes.Range(func(key, value any) bool {
                proc := value.(*ManagedProcess)
                if proc.IsZombie() {
                    proc.MarkFailed()
                }
                return true
            })
        case <-pm.shutdownCh:
            return
        }
    }
}
```

## Next Steps

- Understand the [Architecture](/concepts/architecture)
- Learn about [Lock-Free Design](/concepts/lock-free-design)
