---
name: messaging-queue
description: Coordinate alert/event delivery to AI agents — single-queue rule, layered gates (dedup, batch, activity-defer, outage-hold), and the forbidden parallel-path pattern that causes spam
---

# Messaging Queue Coordination

Use this skill when touching any code that delivers events to an AI agent — PTY stdin injection, MCP `Log()` notifications, channel pings, overlay toasts, hold buffers, alert hubs, or auto-forward paths. Every "spamming the agent" bug we have shipped traces back to violating one of the rules below.

## The Single-Queue Rule

There is exactly one ordered pipeline from "raw event" to "agent's input". Anything that writes to the agent surface MUST flow through it. Parallel writes are bugs, even when they "work" most of the time — they bypass dedup, activity-deferral, outage-holds, and ordering, and they are the canonical cause of repeated-message spam.

Currently in this project the canonical queue is `internal/overlay/alerts.go::AlertScanner` (process-output side) plus the daemon-side gate chain in `internal/daemon/hub_helpers.go::wireProxyLogger` (proxy-side).

**Resolved (PTY side):** the browser-error / auto-forward path
`cmd/agnt/overlay.go::processAutoForwardEvent` no longer writes the PTY
directly. It cascade-filters then calls `AlertScanner.Inject`, so browser-JS
and HTTP errors now share the same dedup / batch / overload-throttle /
activity-defer queue as process-output alerts. The old `lastForwardNs` global
single-timestamp debounce has been removed. The remaining direct `typeText`
in `processProxyEvent` is for non-error UI/automation events (panel messages,
sketch, design) only — error sources all flow through the queue.

**Still parallel (cross-process, low incidence):** the daemon incident
pipeline delivers to MCP-native agents via `incident_digest` → `session.Log()`
(consumed only by `agnt mcp`, `serve.go`), while PTY-wrapped agents get stdin
injection via the overlay socket (consumed by `agnt run`). These serve
*different consumer processes / different agents* (MCP-native vs PTY-only), so
the same agent does not normally get both. A single agent that is *both*
MCP-connected and under `agnt run` could double-receive; per-session
delivery-mode routing (adapter identity → mcp|stdin) is the intended fix and
is not yet wired.

## Required Pipeline Layers

Any new delivery path must compose these layers in this order. Skipping a layer is a bug; reordering a layer is a bug.

1. **Source classification.** Where did the event come from (process output, browser JS, HTTP, transport diag, hook). Keep the source tag through the whole pipeline — downstream gates need it.

2. **Fingerprinting.** Every event gets a stable fingerprint `(source, proxyID/scriptID, canonical-message)`. Existing helper: `FingerprintForEntry` in `internal/daemon/hub_helpers.go`. New paths reuse it; do not invent a parallel hashing scheme.

3. **Dedup window.** Same fingerprint within `DedupeWindow` collapses to one. Existing impl: `AlertScanner.dedupe` map + `pruneDedup`. **No global single-timestamp debounce** — the old `o.lastForwardNs` in `cmd/agnt/overlay.go` was the anti-pattern (it made "any event in last 10s" suppress "any other event", so different errors masked each other and identical errors retriggered forever once the window slid); it has been removed.

4. **Batch window.** Bursts coalesce. Existing impl: `AlertScanner.batchTimer` + `pending` slice. The pending slice IS the wait-for-ready buffer — see § Layering Hold Buffers below.

4b. **Overload throttle.** The pending batch is bounded by `MaxPending` (default 50). While the agent is busy, activity-deferral holds the flush and distinct (non-duplicate) alerts accumulate; without a cap a burst would dump everything into the agent's input the moment it goes idle — agent overload. On overflow the scanner evicts the oldest *lowest-severity* entry (so errors survive longest) and counts it; `AlertBatch.Suppressed` carries the dropped count and `Format` appends a one-line "N more alerts suppressed" note so the throttle is visible, not silently lossy. Depth is observable via `AlertScanner.DepthSnapshot()` and surfaced in the overlay status bar (`📮N ⏸ +N`) and the overview panel "alert queue" section. Impl: `AlertScanner.addMatch` cap + `evictOneLocked` in `internal/overlay/alerts.go`.

   **Protected content is never dropped.** `AlertMatch.Protected` (set for `AlertSourceUser` — explicit user actions: panel messages, sketches, design interactions) is exempt from BOTH dedup (a repeated user action is intentional, deliver each) and eviction (`evictOneLocked` skips protected; if the queue is entirely protected it exceeds the cap rather than drop user content). Protected entries still honor activity-deferral, and use a short `protectedBatchWindow` (150ms) so interactive messages aren't delayed by the error-oriented batch window when the agent is idle. **Invariant: user messages and output from explicit user action are never dropped — only auto-generated error/diagnostic floods are subject to the cap.** User actions are routed in via `cmd/agnt/overlay.go::processProxyEvent` → `AlertScanner.Inject` with `Protected: true`, and are excluded from the daemon alert mirror (they are not errors).

5. **Outage-hold gate.** If the proxy is in transport outage, hold the entry instead of emitting. Existing impl: `internal/daemon/hold_buffer.go::HoldBuffer`. Must layer on top of the batch buffer (§ below), not run in parallel.

6. **Cascade match.** Inside the hold window, transport-cascade messages drop on recovery; genuine errors emit. Existing impl: `HoldBuffer.MatchesJSCascade` + `DefaultJSCascadePatterns` in `internal/config/agnt.go`. Pattern list MUST cover dev-server reconnect noise (vite: `send was called before connect`, `@vite/client`, `ViteHotContext`; webpack: `[HMR] Waiting for update signal`).

7. **Activity-deferral.** Do NOT inject into a PTY while the child is producing output. Existing impl: `AlertScanner` checks `ActivityState() == ActivityActive` and defers up to `maxRetries` × `retryInterval`. Every emit-to-PTY caller must check this. `processAutoForwardEvent` now honors it transitively by routing through `AlertScanner.Inject` (which shares the deferred `flush`); direct `typeText` callers (non-error UI events) still bypass it by design.

8. **Sink fan-out.** After all gates pass, dispatch to the registered sinks (MCP, channel, overlay/PTY, stream). Existing impl: `AlertHub.BroadcastLogEntry`, `fireGatedFanOut` in hub_helpers.go. Sinks themselves are dumb — all gating happens upstream.

## Layering Hold Buffers on the Wait-for-Ready Buffer

The hold buffer (outage gate) and the batch buffer (`AlertScanner.pending`) are **the same kind of object** — a per-fingerprint pending table with a deferred flush. They must compose, not duplicate.

The intended structure:

```
event → fingerprint → batch buffer (coalesce N ms)
                          │
                          ▼
                   activity gate (defer if agent active)
                          │
                          ▼
                   hold buffer (defer if proxy in outage,
                                drop on cascade+recovery)
                          │
                          ▼
                       sinks
```

Concretely: `HoldBuffer` should not own a separate channel + map + goroutine + fingerprint scheme. It should be a *predicate* and *cancel-token* layered on the existing pending buffer:

- The buffer's flush callback consults `OutageClassifier.SuppressionModeProxy` before emitting.
- If `ModeFull` or `ModeDiagnosticOnly`-and-error: keep the entry in `pending`, restart the batch timer with `HoldWindowMs`, increment retry counter.
- On recovery signal: walk `pending`, drop entries whose fingerprint matches a cascade pattern, emit the rest immediately.
- On hold-window expiry: emit whatever survives.

This collapses two independent goroutines + two fingerprint hashes + two retry counters into one pipeline. It also makes the activity gate and the outage gate compose correctly today they're both "hold and retry" but they don't see each other, so a message can pass the activity gate, get held by the outage gate, and the activity state at emission time is unchecked.

## Forbidden Patterns

| Anti-pattern | Why it's wrong | Where we did it |
|--------------|---------------|----------------|
| Parallel write to delivery surface | Bypasses every gate | `ws_handler.go:246` → `NotifyBrowserError` → overlay HTTP; now feeds the queue via `processAutoForwardEvent` → `AlertScanner.Inject` (RESOLVED — it is a *source* into the queue, not a write to the agent surface) |
| Global single-timestamp debounce | Different events mask each other; identical events re-fire each window | `cmd/agnt/overlay.go::lastForwardNs` (RESOLVED — removed) |
| Unbounded pending during deferral | Burst piles up while agent busy, then floods stdin on idle | Fixed by the `MaxPending` overload throttle (layer 4b) |
| Per-feature fingerprinting | Same event hashes differently in different gates → dedup breaks across layers | `HoldBuffer` reinvented `fingerprint` separate from `AlertScanner.fingerprint` |
| Sink-side dedup | Each sink dedupes independently → consumers see different streams | None today; do not add |
| Skipping activity gate on "high priority" | "Important" errors interrupt the agent mid-response, derail responses | `processAutoForwardEvent` (RESOLVED — routes through `Inject` → deferred `flush`) |
| Cascade pattern list with only generic tokens | Vite/webpack/turbo HMR errors slip through | `DefaultJSCascadePatterns` now includes vite (`@vite/client`, `ViteHotContext`, `send was called before connect`) + webpack (`[HMR]`) tokens — keep it covering new dev-server reconnect noise |

## Fingerprint Contract

Same logical event → same fingerprint, regardless of which layer produced it. Different events with similar text MUST hash differently. The canonical key:

```
sha1(source_tag + "\x00" + scope_id + "\x00" + canonical(message))
```

- `source_tag`: `browser_js`, `http_5xx`, `transport_err`, `process_output`, `process_alert`, etc.
- `scope_id`: `proxyID` for proxy-scoped events, `scriptID` for process-scoped, `""` for daemon-global.
- `canonical(message)`: lowercase, collapse whitespace, strip trailing line numbers / column numbers / timestamp prefixes.

Existing `FingerprintForEntry` is the reference implementation. Extend it, do not fork it.

## When Adding a New Event Type

Checklist before merging:

- [ ] Routes through the canonical pipeline (no new direct write to a sink).
- [ ] Reuses `FingerprintForEntry` — extend if needed, do not parallel-implement.
- [ ] Cascade-pattern coverage if the event can be transport noise.
- [ ] Activity-deferral honored before any PTY-injecting sink.
- [ ] Outage-hold honored if scoped to a proxy.
- [ ] Test: ten identical events in 100ms produce one delivery.
- [ ] Test: event during outage + recovery within hold window → dropped if cascade, emitted otherwise.
- [ ] Test: event during agent activity → deferred, delivered after `IdleTimeout`.

## Telemetry

Every gate decision is observable. When debugging "why is X spamming":

1. Daemon debug log (`debug.Log("alerts", ...)`) — gate decision per entry.
2. `agnt monitor --types hook,error,diagnostic --format json` — what reached the sinks.
3. `HoldBuffer.entries` length — how many in flight.
4. `AlertScanner.pending` length — pre-emit batch.
5. `OutageClassifier.SuppressionModeProxy` — current mode for the proxy.

If event count from source > delivery count to sink and the spam continues, the gate is being bypassed — look for a parallel write path.

## Reference Files

| Concern | File |
|---------|------|
| Canonical pending+dedup+batch+overload-throttle+activity-defer | `internal/overlay/alerts.go` |
| Overload throttle (MaxPending cap, evictOneLocked, DepthSnapshot, AlertBatch.Suppressed) | `internal/overlay/alerts.go` |
| Queue-depth UI (status bar `📮`, overview "alert queue" section) | `internal/overlay/render.go`; provider wired in `cmd/agnt/pty_common.go` |
| Outage hold buffer (currently parallel; needs layering) | `internal/daemon/hold_buffer.go` |
| Daemon-side proxy gate chain | `internal/daemon/hub_helpers.go::wireProxyLogger` |
| Outage classification | `internal/daemon/outage_classifier.go` |
| Transport-signal tracking | `internal/daemon/health_tracker.go` |
| Cascade pattern list | `internal/config/agnt.go::DefaultJSCascadePatterns` |
| Sink interfaces | `internal/daemon/alert_hub.go` |
| Auto-forward source into the queue (was parallel; now routes via `AlertScanner.Inject`) | `cmd/agnt/overlay.go::processAutoForwardEvent` |
| Fingerprint helper | `internal/daemon/hub_helpers.go::FingerprintForEntry` |
