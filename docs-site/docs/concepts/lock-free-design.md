---
sidebar_position: 2
---

# Lock-Free Design

agnt prioritizes lock-free concurrency patterns to maximize performance and simplify reasoning about correctness.

## Why Lock-Free?

Traditional mutex-based synchronization has drawbacks:

- **Contention** - Threads block waiting for locks
- **Deadlocks** - Complex lock ordering can cause hangs
- **Priority inversion** - Low-priority threads hold locks needed by high-priority ones
- **Performance** - Lock/unlock overhead on every access

Lock-free designs avoid these by using atomic operations that complete without blocking.

## Patterns Used

### sync.Map for Registries

Both ProcessManager and ProxyManager use `sync.Map`:

```go
type ProcessManager struct {
    processes sync.Map  // map[string]*ManagedProcess
}

type ProxyManager struct {
    proxies sync.Map    // map[string]*ProxyServer
}
```

`sync.Map` provides:
- Lock-free reads (the common case)
- Amortized lock-free writes
- Safe concurrent access without external synchronization

```go
// Store is lock-free in common case
pm.processes.Store(id, process)

// Load is always lock-free
if p, ok := pm.processes.Load(id); ok {
    return p.(*ManagedProcess)
}

// Range is lock-free snapshot iteration
pm.processes.Range(func(key, value any) bool {
    // Process each entry
    return true
})
```

### Atomics for State

Process state uses atomic operations:

```go
type ManagedProcess struct {
    state     atomic.Uint32        // Process state
    pid       atomic.Int32         // OS process ID
    exitCode  atomic.Int32         // Exit code
    startTime atomic.Pointer[time.Time]
    endTime   atomic.Pointer[time.Time]
}
```

State transitions use Compare-And-Swap:

```go
func (p *ManagedProcess) CompareAndSwapState(old, new ProcessState) bool {
    return p.state.CompareAndSwap(uint32(old), uint32(new))
}

// Usage: atomic state transition
if !proc.CompareAndSwapState(StateRunning, StateStopping) {
    return errors.New("process not running")
}
```

### Atomic Counters

Statistics use atomic counters:

```go
type ProcessManager struct {
    activeCount  atomic.Int64
    totalStarted atomic.Int64
    totalFailed  atomic.Int64
}

// Increment is atomic
pm.activeCount.Add(1)
pm.totalStarted.Add(1)

// Read is atomic
count := pm.activeCount.Load()
```

### Atomic Flags

Shutdown coordination uses atomic flags:

```go
type ProcessManager struct {
    shuttingDown atomic.Bool
}

// Check before registering new processes
if pm.shuttingDown.Load() {
    return ErrShuttingDown
}

// Set during shutdown
pm.shuttingDown.Store(true)
```

## Where Locks Are Still Used

### RingBuffer

The ring buffer uses a single mutex because writes must be atomic with wraparound:

```go
type RingBuffer struct {
    mu       sync.Mutex
    buffer   []byte
    writePos int
    overflow atomic.Bool
}

func (rb *RingBuffer) Write(p []byte) (n int, err error) {
    rb.mu.Lock()
    defer rb.mu.Unlock()
    // Write with wraparound
}
```

The mutex is only held during writes, and reads take a consistent snapshot.

### TrafficLogger

Uses `sync.RWMutex` for read-heavy workloads:

```go
type TrafficLogger struct {
    mu      sync.RWMutex
    entries []LogEntry
}

// Writes acquire write lock
func (tl *TrafficLogger) Log(entry LogEntry) {
    tl.mu.Lock()
    defer tl.mu.Unlock()
    // Add entry
}

// Reads acquire read lock (concurrent reads allowed)
func (tl *TrafficLogger) Query(filter Filter) []LogEntry {
    tl.mu.RLock()
    defer tl.mu.RUnlock()
    // Query entries
}
```

## State Machine Correctness

The process state machine uses atomics to ensure correctness:

```
StatePending → StateStarting → StateRunning → StateStopping → StateStopped
                     ↓              ↓              ↓
                 StateFailed ←──────┴──────────────┘
```

Each transition is atomic:

```go
func (p *ManagedProcess) transitionToRunning() error {
    // Only Starting → Running is valid
    if !p.CompareAndSwapState(StateStarting, StateRunning) {
        currentState := p.State()
        return fmt.Errorf("invalid state transition: %v → Running", currentState)
    }
    return nil
}
```

This prevents race conditions where two goroutines might try to transition the same process.

## Shutdown Coordination

Graceful shutdown uses multiple atomic flags:

```go
func (pm *ProcessManager) Shutdown(ctx context.Context) error {
    // Prevent new registrations
    pm.shuttingDown.Store(true)

    // Signal health check to stop
    close(pm.shutdownCh)

    // Stop all processes (lock-free iteration)
    pm.processes.Range(func(key, value any) bool {
        proc := value.(*ManagedProcess)
        proc.Stop()
        return true
    })

    return nil
}
```

## Performance Implications

### Read-Heavy Workloads

Most operations are reads (status checks, output retrieval):

| Operation | Lock Type | Performance |
|-----------|-----------|-------------|
| Get process status | sync.Map Load | ~10ns |
| List processes | sync.Map Range | ~100ns + N×10ns |
| Read output | RingBuffer Lock | ~1µs |
| Query traffic | RWMutex RLock | ~1µs |

### Write Operations

Writes are less frequent and acceptable:

| Operation | Lock Type | Performance |
|-----------|-----------|-------------|
| Register process | sync.Map Store | ~50ns |
| Write output | Mutex Lock | ~1µs |
| Log traffic | RWMutex Lock | ~1µs |

## Guidelines for Extension

When adding new features:

1. **Prefer atomics** for counters and flags
2. **Use sync.Map** for registries with string keys
3. **Use CAS** for state machines
4. **Use RWMutex** when reads dominate writes
5. **Minimize lock scope** when locks are necessary

Example of good extension:

```go
type NewFeature struct {
    items     sync.Map       // Lock-free registry
    count     atomic.Int64   // Lock-free counter
    enabled   atomic.Bool    // Lock-free flag
}
```

## Testing Concurrent Code

Use `-race` flag to detect data races:

```bash
go test -race ./...
```

Write tests that exercise concurrent access:

```go
func TestConcurrentAccess(t *testing.T) {
    pm := NewProcessManager(config)

    var wg sync.WaitGroup
    for i := 0; i < 100; i++ {
        wg.Add(1)
        go func(id int) {
            defer wg.Done()
            // Concurrent operations
            pm.Register(fmt.Sprintf("proc-%d", id), proc)
            pm.Get(fmt.Sprintf("proc-%d", id))
            pm.List()
        }(i)
    }
    wg.Wait()
}
```

## Next Steps

- Understand [Graceful Shutdown](/concepts/graceful-shutdown)
- See [Architecture Overview](/concepts/architecture)
