---
written_at: 2026-07-11T03:38:47Z
source_event: task:01KX5AW5HV66SJ8MY3C9FJAT6X
sources:
  - task:01KX5AW5HV66SJ8MY3C9FJAT6X (task 04b, original lesson)
  - commit:f16b3a5b
  - task:01KX8QVVKSRGYEQE0JFJSYJKDV (task 09c, extension)
  - commit:59973ea7451a2a59402859052deb6ab20192f77d
  - commit:90a6244277ccfab175c55b2da5756d280103b7e3
  - commit:d6a8ab062a0fe819e8f519ae8676d18bc7d23b04
  - commit:f5d65a9a89c8f92a921f04ea9ed3aa74de47c550
  - reviewer_verdict:01KX972C67V05EPEB5CPEJ7YNC (pass_with_changes, no rewinds)
written_at_last_update: 2026-07-11T18:45:00Z
---

# Lesson: an indefinite-blocking liveness probe is a liveness lie

`internal/sshclient/client.go` (`startKeepalive`) called
`c.SSH.SendRequest("keepalive@openssh.com", true, nil)` on a 15s ticker with
no per-call deadline. On a clean disconnect this fails fast and the 3-miss
counter advances normally. But on a true network black-hole (packet loss,
no RST — as opposed to a reset connection) `SendRequest` blocks forever: the
miss counter never advances, `Dead()` never fires, and the reconnect logic
that depends on `Dead()` never triggers. The liveness probe was itself not
live.

Fixed in commit `f16b3a5b`: race each `SendRequest` against a timeout
shorter than the ticker interval (5s vs 15s here); a timeout counts as a
miss identically to a transport error. On the final miss, close the
underlying connection (`c.SSH.Close()`) in addition to firing `Dead()`, so
any goroutine still blocked on the black-holed reply unblocks instead of
leaking for the process lifetime — bounding the call alone is not enough if
nothing then interrupts the still-blocked prior attempt.

## Generalize

Any indefinite-blocking call used as a liveness/health signal (keepalives,
heartbeats, health-check pings, "is the peer still there" probes) must be
individually time-bounded, strictly shorter than the polling interval that
wraps it. Two failure modes without this bound:

1. **Silent failure**: the probe hangs, the failure counter never
   increments, downstream logic gated on the probe (reconnect, failover,
   alerting) never fires — worse than no probe, because callers believe
   liveness is being checked.
2. **Goroutine/resource leak**: even after the bound trips and the caller
   gives up waiting, the original blocked call is still in flight unless
   something (closing the transport, cancelling a context) actively
   unblocks it.

Applies to any future probe over a transport that can black-hole rather
than cleanly reset — TCP/SSH/WebSocket keepalives, HTTP health checks
without a client timeout, RPC pings without a deadline context.

## Extension: consumers of the signal must not reimplement it, and must not conflate "died" with "ended cleanly"

Task 09c (`01KX8QVVKSRGYEQE0JFJSYJKDV`, commits `59973ea7`/`90a62442`/
`d6a8ab06`/`f5d65a9a`, reviewer `pass_with_changes`, no rewinds) built a
reconnect state machine (`internal/sshclient/reconnect.go`) gated on this
same `Dead()` channel from task 04b. Its correctness depended on getting one
branch right: **`Dead()` fired → transport died → reconnect** is a
different case from **the relay loop ended without `Dead()` firing → a
clean cancel or the remote side exiting on its own → exit normally, do not
reconnect**. Collapsing that distinction resurrects a session the user or
remote process deliberately ended, which is worse than a missed reconnect.

Generalize: (a) once a bounded liveness probe like `Dead()` exists, every
downstream consumer (reconnect loops, alerting, failover) must consume that
existing signal rather than re-deriving its own keepalive/health-check
logic — reimplementing invites exactly the indefinite-block bug this lesson
documents, a second time, in a second place. (b) any loop gated on a
liveness signal must explicitly branch on *why* the underlying operation
ended — signal fired (dead) vs. ended without the signal (clean/cooperative
exit) — rather than treating "the loop returned" as a single case that
always means "reconnect."
