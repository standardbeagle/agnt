# Unified Message Queue: Layer HoldBuffer on AlertScanner Pending

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` or `superpowers:executing-plans` to implement this plan task-by-task. REQUIRED CONTEXT: read `.claude/skills/messaging-queue/SKILL.md` before any change to alert/event delivery code.

**Status:** PENDING — depends on `2026-05-06-transport-outage-gate.md` landing first.

**Goal:** Eliminate the parallel-queue duplication between `internal/daemon/hold_buffer.go::HoldBuffer` and `internal/overlay/alerts.go::AlertScanner`. Both maintain pending tables, fingerprints, retry counters, and emit goroutines; they don't compose, so an event held by the outage gate is never re-checked against the activity gate at emit time, and dedup keys disagree across layers.

**Architecture:** Extract the `pending + dedup + batch-timer + retry` primitive into a shared package (`internal/msgqueue/`). Rewrite `HoldBuffer` and `AlertScanner` as thin wrappers that compose flush-time predicates (activity-active → defer; proxy-in-outage → defer; cascade-on-recovery → drop). One goroutine per queue, one fingerprint helper, one retry counter.

**Driving rule:** `.claude/skills/messaging-queue/SKILL.md` § Layering Hold Buffers on the Wait-for-Ready Buffer. The skill is the single source of truth for the target shape; this plan is the execution sequence.

**Tech Stack:** Go 1.24.2.

---

## Why deferred

The `2026-05-06-transport-outage-gate.md` plan introduced `HoldBuffer` to ship the transport-cascade fix on a tight timeline. It uses its own goroutine + map intentionally: the outage gate landed before the unification was scoped. Refactoring on top of in-flight WIP risks merge conflicts. Land transport-outage gate fully, **then** start this plan.

The user-visible spam bug ("vite send-was-called-before-connect spamming Kimi") is **already fixed** via:

- `2026-05-07` extension to `DefaultJSCascadePatterns` (vite/HMR tokens).
- `2026-05-07` rewrite of `cmd/agnt/overlay.go::processAutoForwardEvent` to route through `AlertScanner.Inject`, dropping the global `lastForwardNs` debounce.

This plan is therefore a debt-paydown, not a bug fix.

---

## File Map

| Action | File | Responsibility |
|--------|------|----------------|
| Create | `internal/msgqueue/queue.go` | `Queue[T]` primitive: pending slice, dedup map, batch timer, flush callback with retry. |
| Create | `internal/msgqueue/fingerprint.go` | `Fingerprint(source, scope, message)` — single canonical key. |
| Create | `internal/msgqueue/queue_test.go` | Pending, dedup, batch, retry, cancel-on-predicate tests. |
| Modify | `internal/daemon/hold_buffer.go` | Rewrite as `Queue[HoldEntry]` consumer. Outage gate becomes flush-time predicate; recovery becomes cancel-token walk. Drop the channel + map + min-heap. |
| Modify | `internal/daemon/hold_buffer_test.go` | Adjust to new API. Same invariants asserted. |
| Modify | `internal/overlay/alerts.go` | Rewrite `pending`/`addMatch`/`flush` on top of `Queue[AlertMatch]`. Activity-defer becomes flush-time predicate. |
| Modify | `internal/overlay/alerts_test.go` | API adjustments. |
| Modify | `internal/daemon/hub_helpers.go` | `wireProxyLogger`: gate decision returns predicate-keyed disposition. |

---

## Design Contracts

### Queue primitive

```go
package msgqueue

type Queue[T any] struct {
    // Pending is the wait-for-ready buffer. Entries with the same
    // Fingerprint coalesce. Flush is called when the batch timer fires;
    // the callback decides accept (deliver), defer (re-arm timer),
    // or drop (discard) per entry.
}

type Disposition int
const (
    Deliver Disposition = iota
    Defer
    Drop
)

type FlushDecision[T any] struct {
    Entry T
    Disposition
}

func (q *Queue[T]) Push(key string, v T) // dedup by key
func (q *Queue[T]) Cancel(predicate func(T) bool) // drop matching pending entries (cascade-on-recovery)
func (q *Queue[T]) Flush(ctx context.Context, decide func(T) Disposition) []T
```

### HoldBuffer rewrite

`HoldBuffer.Hold(entry, proxyID, fp, cascade)` becomes:

```go
b.queue.Push(proxyID + "|" + fp, HoldEntry{Entry: entry, Cascade: cascade, ProxyID: proxyID})
```

`HoldBuffer.OnRecovery(proxyID)`:

```go
b.queue.Cancel(func(e HoldEntry) bool {
    return e.ProxyID == proxyID && e.Cascade
})
// remaining queued entries for proxyID emit on next flush
```

Flush predicate consults outage classifier:

```go
b.queue.Flush(ctx, func(e HoldEntry) Disposition {
    if classifier.IsProxyInTransportOutage(e.ProxyID) {
        return Defer
    }
    return Deliver
})
```

### AlertScanner rewrite

Activity-defer becomes flush-time predicate:

```go
s.queue.Flush(ctx, func(m *AlertMatch) Disposition {
    if s.actState != nil && s.actState() == ActivityActive {
        return Defer
    }
    return Deliver
})
```

### Composability

Both gates re-evaluated at every flush. An entry held by the outage gate that becomes activity-deferable later in the same window will defer for the activity reason instead of emitting blind. **This is the core skill requirement and the reason the unification is worth doing.**

---

## Tasks

### Task 1: Extract Queue primitive
- [ ] Failing test in `internal/msgqueue/queue_test.go` — push two with same key, dedup; flush returns single entry.
- [ ] Failing test — flush predicate returning `Defer` re-arms timer, second flush emits.
- [ ] Failing test — `Cancel(predicate)` walks pending and drops matching entries.
- [ ] Implement `Queue[T]`.

### Task 2: Migrate HoldBuffer
- [ ] Rewrite `hold_buffer.go` to use `Queue[HoldEntry]`. All existing tests must pass unchanged externally; internal mocks adjust.
- [ ] Verify `TestHoldBuffer_RecoveryCancelsTransportErr`, `TestHoldBuffer_TimerExpiryEmitsOnce`, `TestHoldBuffer_NonCascadeJSEmittedOnRecovery`, `TestHoldBuffer_DuplicateFingerprintCoalesces` still green.

### Task 3: Migrate AlertScanner
- [ ] Rewrite `alerts.go::pending` machinery on top of `Queue[*AlertMatch]`.
- [ ] Verify `TestAlertScannerActivityDeferral` and the `TestAlertScanner_Inject_*` suite still green.

### Task 4: Single fingerprint
- [ ] Replace `internal/overlay/alerts.go::fingerprint` and `internal/daemon/hold_buffer.go::FingerprintForEntry` with calls to `msgqueue.Fingerprint`.
- [ ] Keep type-specific scope helpers (`scopeForLogEntry`, `scopeForAlertMatch`) as thin adapters.

### Task 5: Compose gates
- [ ] Wire `HoldBuffer` flush predicate to also check activity state — proxy-side held entries must not interrupt mid-response delivery.
- [ ] Add a regression test: queue with both gates active flushes only when both gates open.

---

## Acceptance

1. Single goroutine per queue (no separate hold-buffer loop).
2. Single fingerprint helper used by both subsystems.
3. Activity gate and outage gate compose — re-checked at every flush.
4. All existing tests pass without weakening assertions.
5. `.claude/skills/messaging-queue/SKILL.md` § Forbidden Patterns updated to remove the "parallel goroutine" entry.
