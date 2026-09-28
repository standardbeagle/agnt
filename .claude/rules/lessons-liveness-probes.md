---
paths:
  - "internal/sshclient/**"
  - "internal/daemon/startup_resilience.go"
  - "internal/daemon/health/**"
  - "internal/daemon/hub_stream.go"
  - "internal/daemon/doctor.go"
  - "internal/daemonclient/**"
  - "internal/proxy/ws_handler.go"
  - "cmd/agnt/overlay.go"
---

# Lessons: liveness and readiness signals

## 1. A liveness probe must be time-bounded

Any call used as a liveness signal (keepalive, heartbeat, health ping) needs its own deadline, shorter than the polling interval around it. On a black-holed transport (packet loss, no RST) an unbounded call blocks forever: the miss counter never advances and reconnect/failover never fires. When the final miss trips, also close the transport so the still-blocked call unblocks instead of leaking.

Reference: `internal/sshclient/client.go` `startKeepalive` races each `SendRequest` against 5s (interval 15s); a timeout counts as a miss; the 3rd miss closes `c.SSH` and fires `Dead()`.

## 2. Consume the signal, and branch on why the loop ended

Downstream consumers (reconnect, alerting, failover) use the existing `Dead()` signal; never re-derive a second keepalive. A loop gated on it must distinguish "signal fired → transport died → reconnect" from "ended without the signal → clean cancel or remote exit → stop". Conflating them resurrects sessions the user deliberately ended (`internal/sshclient/reconnect.go`).

## 3. Readiness must be affirmative, self-emitted health

An early-success exit from a watch window needs a positive signal emitted by the subject itself, such as a URL or ready line in its own output. Never use "has not crashed yet" (`state == Running`), and never use an ambient probe a third party can satisfy (TCP connect, socket-table presence).

Reference: `internal/daemon/startup_resilience.go` `monitorStartupFailure` exits early on `urlTracker.GetURLs`. Order: failure checks, then readiness, then deadline. Test it by injecting the ready signal with a never-firing deadline and asserting the outcome, not latency. Keep the mutation guard: a `sleep 0.3; exit 1` process must fail if the predicate is relaxed to absence-of-failure.
