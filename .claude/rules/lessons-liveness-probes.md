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
  - task:01M0B18S8K5D68FMJPF0QYS6V8 (startup-monitor early exit, readiness extension)
  - commit:2cae85df601e4841a04dce2122b8b10a961e9c52
  - reviewer_verdict:01M0C0HYFT4GCY26Q1F463Z0TN (pass, high confidence, no rewinds)
written_at_last_update: 2026-08-18T00:00:00Z
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

## Extension: the readiness/early-SUCCESS predicate is the mirror image — it must be affirmative self-emitted health, never absence-of-failure and never an ambient probe

Task `01M0B18S8K5D68FMJPF0QYS6V8` (commit `2cae85df`, reviewer verdict
`pass`, high confidence, no rewinds) is the success-side sibling of the two
lessons above. `internal/daemon/startup_resilience.go` `monitorStartupFailure`
watched a process for a crash window (default 3s) but had **no positive-exit
path**: its only way to report "healthy" was the deadline expiring, so every
SUCCESSFUL autostart blocked the caller for the whole window and stacked ~3s
per dependency layer. The fix added an early return, and the whole lesson is
in *which* signal was chosen for it:

- **Chosen: a URL detected in the process's OWN captured output**
  (`urlTracker.GetURLs`). This is unambiguous affirmative health — a URL
  cannot be printed by a process that is mid-crash, and (unlike a socket
  probe) it cannot be attributed to a *pre-existing* port holder because it
  comes from the process's own stdout/stderr, not the socket table.
- **Rejected: `state == Running` ("has not crashed yet").** This is the
  not-yet-crashed trap — the absence of a negative is not the presence of a
  positive. A `sleep 0.3 && exit 1` process is "still running" for its first
  ticks yet is unhealthy. The `delayed_exit` regression test is the
  mutation-killer: relaxing the predicate to `state==Running => ready` makes
  it return `nil` at ~100ms, and the review **mutation-verified** exactly that
  before accepting.
- **Rejected: a bare expected-port TCP probe.** A probe can succeed against a
  pre-existing/protected holder while our own process is mid-EADDRINUSE-crash
  — the same ambient-signal ambiguity §1's "a URL cannot be attributed to a
  pre-existing holder" avoids — which would regress the in-monitor EADDRINUSE
  recovery path.
- **Ordering is load-bearing**: the affirmative-health check runs AFTER the
  failure (EADDRINUSE/crash) checks and BEFORE the deadline check, so a
  failure on the same tick is still classified as failure first.

Generalize: whenever you add an early-SUCCESS/ready exit to a watch/health
window, the exit predicate must be a **positive signal emitted by the subject
itself** (a readiness line in its own output, an explicit ready RPC/callback
it sends), never "no error has arrived yet" and never an ambient probe a third
party can satisfy (TCP connect, socket-table presence, a shared health
endpoint). This is symmetric with §1: §1 says a *failure/liveness* probe must
be time-bounded so a hang cannot masquerade as "still fine"; this says a
*readiness* predicate must be affirmative so "not yet failed" cannot
masquerade as "healthy." Test it deterministically the way this task did —
inject the ready signal and set a never-firing deadline, then assert the
OUTCOME (returned via readiness) rather than any wall-clock latency bound, and
keep a mutation-guard crash case (`sleep 0.3; exit 1`) that fails the moment
the predicate is relaxed to absence-of-failure.
