---
name: messaging-queue
description: Coordinate alert/event delivery to AI agents — single-queue rule, layered gates (dedup, batch, activity-defer, outage-hold), and the forbidden parallel-path pattern that causes spam
---

# Messaging Queue Coordination

Use this skill when touching any code that delivers events to an AI agent — PTY stdin injection, MCP `Log()` notifications, channel pings, overlay toasts, hold buffers, alert hubs, or auto-forward paths. Every "spamming the agent" bug we have shipped traces back to violating one of the rules below.

## The Single-Queue Rule

There is exactly one ordered pipeline from "raw event" to "agent's input". Anything that writes to the agent surface MUST flow through it. Parallel writes are bugs, even when they "work" most of the time — they bypass dedup, activity-deferral, outage-holds, and ordering, and they are the canonical cause of repeated-message spam.

Currently in this project the canonical queue is `internal/overlay/alerts.go::AlertScanner` (process-output side) plus the daemon-side gate chain in `internal/daemon/hub_helpers.go::wireProxyLogger` (proxy-side). The browser-error / auto-forward path in `cmd/agnt/overlay.go::processAutoForwardEvent` is a parallel write that does NOT honor the queue. **That is a bug, not an architecture choice.**

## Required Pipeline Layers

Any new delivery path must compose these layers in this order. Skipping a layer is a bug; reordering a layer is a bug.

1. **Source classification.** Where did the event come from (process output, browser JS, HTTP, transport diag, hook). Keep the source tag through the whole pipeline — downstream gates need it.

2. **Fingerprinting.** Every event gets a stable fingerprint `(source, proxyID/scriptID, canonical-message)`. Existing helper: `FingerprintForEntry` in `internal/daemon/hub_helpers.go`. New paths reuse it; do not invent a parallel hashing scheme.

3. **Dedup window.** Same fingerprint within `DedupeWindow` collapses to one. Existing impl: `AlertScanner.dedupe` map + `pruneDedup`. **No global single-timestamp debounce** — `o.lastForwardNs` in `cmd/agnt/overlay.go` is the anti-pattern: it makes "any event in last 10s" suppress "any other event", so different errors mask each other and identical errors retrigger forever once the window slides.

4. **Batch window.** Bursts coalesce. Existing impl: `AlertScanner.batchTimer` + `pending` slice. The pending slice IS the wait-for-ready buffer — see § Layering Hold Buffers below.

5. **Outage-hold gate.** If the proxy is in transport outage, hold the entry instead of emitting. Existing impl: `internal/daemon/hold_buffer.go::HoldBuffer`. Must layer on top of the batch buffer (§ below), not run in parallel.

6. **Cascade match.** Inside the hold window, transport-cascade messages drop on recovery; genuine errors emit. Existing impl: `HoldBuffer.MatchesJSCascade` + `DefaultJSCascadePatterns` in `internal/config/agnt.go`. Pattern list MUST cover dev-server reconnect noise (vite: `send was called before connect`, `@vite/client`, `ViteHotContext`; webpack: `[HMR] Waiting for update signal`).

7. **Activity-deferral.** Do NOT inject into a PTY while the child is producing output. Existing impl: `AlertScanner` checks `ActivityState() == ActivityActive` and defers up to `maxRetries` × `retryInterval`. Every emit-to-PTY caller must check this — `processAutoForwardEvent` does not, which is why it can land mid-response.

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
| Parallel write to delivery surface | Bypasses every gate | `ws_handler.go:246` → `NotifyBrowserError` → overlay HTTP, parallel to `Logger().SetOnLogEntry` |
| Global single-timestamp debounce | Different events mask each other; identical events re-fire each window | `cmd/agnt/overlay.go::lastForwardNs` |
| Per-feature fingerprinting | Same event hashes differently in different gates → dedup breaks across layers | `HoldBuffer` reinvented `fingerprint` separate from `AlertScanner.fingerprint` |
| Sink-side dedup | Each sink dedupes independently → consumers see different streams | None today; do not add |
| Skipping activity gate on "high priority" | "Important" errors interrupt the agent mid-response, derail responses | `processAutoForwardEvent` |
| Cascade pattern list with only generic tokens | Vite/webpack/turbo HMR errors slip through | `DefaultJSCascadePatterns` missing vite-specific tokens |

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
| Canonical pending+dedup+batch+activity-defer | `internal/overlay/alerts.go` |
| Outage hold buffer (currently parallel; needs layering) | `internal/daemon/hold_buffer.go` |
| Daemon-side proxy gate chain | `internal/daemon/hub_helpers.go::wireProxyLogger` |
| Outage classification | `internal/daemon/outage_classifier.go` |
| Transport-signal tracking | `internal/daemon/health_tracker.go` |
| Cascade pattern list | `internal/config/agnt.go::DefaultJSCascadePatterns` |
| Sink interfaces | `internal/daemon/alert_hub.go` |
| Forbidden parallel write | `cmd/agnt/overlay.go::processAutoForwardEvent` (do not extend; refactor onto pipeline) |
| Fingerprint helper | `internal/daemon/hub_helpers.go::FingerprintForEntry` |
