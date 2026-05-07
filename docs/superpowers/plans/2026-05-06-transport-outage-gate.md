# Transport-Signal Outage Gate Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop the proxy from spamming connection / browser-JS errors during dev-server restarts that the existing process-state-driven `OutageClassifier` cannot detect (e.g. `dotnet watch` keeps the supervisor process `Running` while the embedded HTTP/WS server bounces; `.bat` files spawn the real web server as a child the daemon does not manage). Replace drop-on-suppression with hold-and-emit-on-no-recovery so genuine outages still surface, just delayed.

**Architecture:** Extend `HealthTracker` with per-proxy transport-signal tracking (`RecordTransportError`, `RecordRecoverySignal`) keyed by `proxyID`. Extend `OutageClassifier` with `ClassifyProxy(proxyID)` that takes the worse of process-state outage and proxy-transport outage. Refactor `proxyBroadcastGate` from drop-during-window into a hold-buffer-and-cancel-on-recovery gate. Wire the gate into both the legacy AlertHub `BroadcastLogEntry` path AND the incident bus by calling adapter functions inside the same `wireProxyLogger` callback after the gate decision. Drop browser-JS network-cascade messages during outage; emit other browser-JS errors. Config lives in `.agnt.kdl` `alerts.outage-hold`.

**Tech Stack:** Go 1.24.2, `internal/daemon`, `internal/incident`, `internal/proxy`, KDL config via `kdl-go`

---

## File Map

| Action | File | Responsibility |
|--------|------|----------------|
| Modify | `internal/daemon/health_tracker.go` | Per-proxy transport-error / recovery-signal bookkeeping; new outage state |
| Modify | `internal/daemon/outage_classifier.go` | `ClassifyProxy(proxyID)` combining process and proxy outage |
| Create | `internal/daemon/hold_buffer.go` | Per-proxy held-event buffer, cancel-on-recovery, JS cascade pattern matching |
| Modify | `internal/daemon/hub_helpers.go` | `wireProxyLogger`: replace drop with hold-buffer; fan to AlertHub + incident bus on emit |
| Modify | `internal/config/agnt.go` | `OutageHoldConfig` struct on `AlertsConfig` |
| Modify | `internal/daemon/daemon.go` | Construct hold-buffer + transport tracker; pass config |
| Create | `internal/daemon/health_tracker_transport_test.go` | Transport signal tracking tests |
| Create | `internal/daemon/hold_buffer_test.go` | Hold-buffer cancel/emit/cascade tests |
| Modify | `internal/daemon/outage_classifier_test.go` | Proxy classify tests |
| Modify | `internal/config/agnt_test.go` | Parse `outage-hold` block |

---

## Design Contracts

### Transport-signal outage detection

`HealthTracker` gains a per-proxy side table `transportState[proxyID]`:

```go
type proxyTransportState struct {
    mu              sync.Mutex
    firstErrAt      time.Time   // start of current transport burst
    errCount        int          // errs seen since firstErrAt
    lastRecoveryAt  time.Time   // most recent 2xx/3xx or WS open
    inOutage        atomic.Bool  // true while burst active
}
```

A proxy enters synthetic outage when:
- `errCount >= TransportErrThreshold` (default 1) within `TransportErrWindow` (default 1s) AND
- `now - lastRecoveryAt >= TransportRecoveryDebounce` (default 500ms — guards against single races)

Outage exits when a recovery signal arrives. Recovery signals:
- HTTP 2xx/3xx response on the proxy (any path)
- WebSocket connection opens on the proxy
- Process state edge to Running with grace expired (existing path)

### Hold-buffer semantics

When the gate decides "suppress this entry", instead of dropping it, push it into the `HoldBuffer`:

```go
type HoldEntry struct {
    Entry      proxy.LogEntry
    ProxyID    string
    Fingerprint string         // (proxyID, type, error-canonical)
    EnqueuedAt time.Time
    EmitAt     time.Time       // EnqueuedAt + HoldWindow
}
```

Buffer is keyed by `(proxyID, fingerprint)` so duplicate errors during one burst collapse to one hold entry with `count++`. A single goroutine per buffer drives a min-heap on `EmitAt`:

- Timer fires → if proxy is no longer in outage → drop entry (was rebuild noise); else emit the entry exactly once with the accumulated count.
- Recovery signal arrives → drop ALL entries for that proxy whose fingerprint matches a transport-cascade or browser-JS-cascade pattern. Emit non-cascade entries immediately (genuine errors that happened during outage).

### JS cascade patterns

Configurable substring list (default below). A browser-JS error matching ANY pattern is treated as a transport-cascade and dropped on recovery:

```
"Failed to fetch"
"NetworkError"
"WebSocket"
"ERR_CONNECTION_REFUSED"
"ERR_NETWORK_CHANGED"
"ERR_INTERNET_DISCONNECTED"
"net::ERR_"
"Load failed"
```

Match is case-insensitive substring on `Summary` (which already includes message + first stack line).

### Dual-path wiring

`wireProxyLogger` already runs on every `LogEntry` from `TrafficLogger`. After the gate decision:

- **Pass through (no outage)** → `AlertHub.BroadcastLogEntry` (existing) AND adapter → `incidentBus.Fire`.
- **Hold** → push to `HoldBuffer`. On emit, the buffer's emit callback runs the same fan-out (AlertHub + bus).
- **Drop on recovery** → never reaches either consumer.

This guarantees `get_errors` (legacy) and `get_incidents` (new) see the same stream, gated identically.

---

## Task 1: Config — `OutageHoldConfig`

**Files:**
- Modify: `internal/config/agnt.go`
- Modify: `internal/config/agnt_test.go`

- [ ] **Step 1: Add KDL parse test**

```go
func TestOutageHold_Parse(t *testing.T) {
    kdl := `
alerts {
    outage-hold {
        enabled true
        window-ms 3000
        transport-err-threshold 1
        transport-err-window-ms 1000
        recovery-debounce-ms 500
        js-cascade-patterns "Failed to fetch" "NetworkError" "WebSocket"
    }
}`
    // assert AlertsConfig.OutageHold populated
}
```

- [ ] **Step 2: Add struct + parser**

```go
type OutageHoldConfig struct {
    Enabled               bool
    WindowMs              int      // hold duration before forced emit
    TransportErrThreshold int      // errs to trigger outage
    TransportErrWindowMs  int      // window for threshold
    RecoveryDebounceMs    int      // ignore recovery signals within this window after entering outage
    JSCascadePatterns     []string
}
```

Defaults via `DefaultAgntConfig`: enabled=true, window=3000, threshold=1, errWindow=1000, debounce=500, patterns=[8 defaults].

---

## Task 2: HealthTracker — transport-signal tracking

**Files:**
- Modify: `internal/daemon/health_tracker.go`
- Create: `internal/daemon/health_tracker_transport_test.go`

- [ ] **Step 1: Failing test — RecordTransportError sets outage**

```go
func TestHealthTracker_TransportError_TriggersOutage(t *testing.T) {
    tracker := NewHealthTracker(stubLookup, nil)
    tracker.SetTransportConfig(TransportConfig{
        Threshold: 1, Window: time.Second, RecoveryDebounce: 500*time.Millisecond,
    })
    tracker.RecordTransportError("proxy1", time.Now())
    require.True(t, tracker.IsProxyInTransportOutage("proxy1"))
}
```

- [ ] **Step 2: Add `proxyTransportState` + methods**

`RecordTransportError(proxyID, ts)`, `RecordRecoverySignal(proxyID, ts)`, `IsProxyInTransportOutage(proxyID) bool`, `Forget(proxyID)`. Sliding-window err count using a small ring (`crashHistorySize` reuse pattern).

- [ ] **Step 3: Recovery signal exits outage**

```go
func TestHealthTracker_RecoverySignal_ExitsOutage(t *testing.T) {
    // record err → in outage; record recovery after debounce → not in outage
}
```

---

## Task 3: OutageClassifier — `ClassifyProxy`

**Files:**
- Modify: `internal/daemon/outage_classifier.go`
- Modify: `internal/daemon/outage_classifier_test.go`

- [ ] **Step 1: Failing test**

```go
func TestClassifier_ProxyTransportOutage(t *testing.T) {
    // proxy in transport outage, process Running healthy → ClassifyProxy returns Rebuild
}
```

- [ ] **Step 2: Implement**

```go
func (c *OutageClassifier) ClassifyProxy(proxyID, linkedProcessID string) OutageType {
    procOutage := c.Classify(linkedProcessID)
    if c.tracker.IsProxyInTransportOutage(proxyID) {
        // Treat transport outage as Rebuild for window logic
        return maxOutageType(procOutage, OutageRebuild)
    }
    return procOutage
}
```

`maxOutageType` orders by suppression severity: Healthy < Rebuild < LongRebuild < ExpiredRebuild < Crash.

`SuppressionModeProxy(proxyID, processID)` mirrors `SuppressionMode`.

---

## Task 4: HoldBuffer

**Files:**
- Create: `internal/daemon/hold_buffer.go`
- Create: `internal/daemon/hold_buffer_test.go`

- [ ] **Step 1: Failing tests**

```go
func TestHoldBuffer_RecoveryCancelsTransportErr(t *testing.T) { ... }
func TestHoldBuffer_TimerExpiryEmitsOnce(t *testing.T) { ... }
func TestHoldBuffer_JSCascadeDroppedOnRecovery(t *testing.T) { ... }
func TestHoldBuffer_NonCascadeJSEmittedOnRecovery(t *testing.T) { ... }
func TestHoldBuffer_DuplicateFingerprintCoalesces(t *testing.T) { ... }
```

- [ ] **Step 2: Implement**

```go
type HoldBuffer struct {
    cfg      OutageHoldConfig
    emit     func(proxy.LogEntry, string)  // AlertHub + bus fan-out
    nowFn    func() time.Time
    entries  sync.Map  // (proxyID|fp) → *HoldEntry
    timer    *time.Timer  // single timer driven by min-heap
}

func (b *HoldBuffer) Hold(entry proxy.LogEntry, proxyID, fp string)
func (b *HoldBuffer) OnRecovery(proxyID string)  // walk entries, emit non-cascade, drop cascade
```

Single goroutine drives emission. Recovery signals arrive on a channel → goroutine processes synchronously to avoid races.

- [ ] **Step 3: JS cascade matching**

`isJSCascade(entry, patterns)` — case-insensitive substring on `entry.FrontendError.Message + Stack` or fallback to `Summary` on the wrapped IncidentEvent path.

---

## Task 5: Wire gate to dual paths

**Files:**
- Modify: `internal/daemon/hub_helpers.go`

- [ ] **Step 1: Refactor `proxyBroadcastGate`**

Return a tri-state: `gateAccept`, `gateHold`, `gateDrop`.

- `gateAccept` → call `AlertHub.BroadcastLogEntry` AND fire incident-bus adapter.
- `gateHold` → push to `HoldBuffer`.
- `gateDrop` → drop (used only for diagnostic suppression markers we don't want re-emitted).

- [ ] **Step 2: Add transport signal hooks**

Inside `wireProxyLogger` callback, before gate:
- If `entry.Type == LogTypeDiagnostic && entry.Diagnostic.Category == "transport"` → `tracker.RecordTransportError(proxyID, now)`.
- If `entry.Type == LogTypeHTTP && entry.HTTP.StatusCode < 500` → `tracker.RecordRecoverySignal(proxyID, now)`.
- If `entry.Type == LogTypeWebSocket` (or however WS connect is logged) → `RecordRecoverySignal`.

Recovery handler then calls `holdBuffer.OnRecovery(proxyID)`.

- [ ] **Step 3: Adapter fan-out helper**

```go
func (d *Daemon) fireToIncidentBus(entry proxy.LogEntry, proxyID string) {
    if d.incidentBus == nil { return }
    switch entry.Type {
    case proxy.LogTypeDiagnostic:
        if ev, ok := incident.FromProxyDiagnostic(*entry.Diagnostic, proxyID); ok {
            d.incidentBus.Publish(ev)
        }
    case proxy.LogTypeHTTP:
        if ev, ok := incident.FromHTTPEntry(*entry.HTTP, proxyID); ok {
            d.incidentBus.Publish(ev)
        }
    case proxy.LogTypeError:
        if entry.FrontendError != nil {
            ev := incident.FromFrontendError(*entry.FrontendError, proxyID)
            d.incidentBus.Publish(ev)
        }
    }
}
```

This is the FIRST production wire of incident adapters. Verify session attach is required: `incidentBus.Publish` is a no-op if no session pipeline exists, so unwired sessions stay correct.

---

## Task 6: Daemon wiring

**Files:**
- Modify: `internal/daemon/daemon.go`

- [ ] **Step 1: Construct hold buffer**

After `incBus := incident.NewMPSCBus(...)`:

```go
holdCfg := cfg.Alerts.OutageHold
holdBuffer := NewHoldBuffer(holdCfg, d.fireGatedFanOut)
d.holdBuffer = holdBuffer
```

`fireGatedFanOut` is the closure that runs `AlertHub.BroadcastLogEntry` + `fireToIncidentBus`. Used both as direct gate-accept path and as hold-buffer emit callback.

- [ ] **Step 2: Pass transport config to tracker**

`d.healthTracker.SetTransportConfig(...)` from cfg.

---

## Task 7: Integration test — dotnet-watch sim

**Files:**
- Create: `internal/daemon/transport_outage_integration_test.go`

- [ ] **Step 1: Sim**

Process state stays Running. Inject 5 transport_err diagnostics through `TrafficLogger.LogDiagnostic`. Inject 3 frontend errors with "Failed to fetch". After 1s, inject HTTP 200 entry (recovery).

Assert:
- AlertHub stream sinks see ZERO error entries during outage
- After recovery: zero transport_err emitted (canceled), zero "Failed to fetch" emitted (cascade drop)
- One non-cascade browser-JS error injected during outage IS emitted on recovery

- [ ] **Step 2: No-recovery path**

Same setup but no recovery within 3s window. Assert exactly one transport_err and one cascade browser error reach sinks (collapsed by fingerprint, count > 1).

---

## Task 8: Documentation

**Files:**
- Modify: `CLAUDE.md` (Configuration § Alert Push Channels)
- Modify: `.claude/rules/daemon-architecture.md` (add Outage Hold contract)

- [ ] Document `outage-hold` config keys, semantics, and the cascade-pattern list.
- [ ] Document the contract: hold-buffer emit goes through both AlertHub and incident bus.

---

## Out of Scope

- Process-driven outage classification (already exists in `OutageClassifier`).
- Multi-tier hold windows (one fixed window per proxy is enough; per-proxy override added later if needed).
- Streaming partial buffers via `agnt monitor` (held entries are invisible until emitted; a future enhancement could expose pending count).
- Cross-proxy outage correlation (per-proxy state is sufficient for v1).
