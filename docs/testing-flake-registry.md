# Testing: Flake Registry & Non-Determinizable Tests

Source of truth for the flake-eradication epic (`01KX5M99X1A1B6Z5KMRKGEAF9G`,
closed by gate task `F-gate`, `01KX8R68ZMEJPQYY4BX4B2A38C`). This document is
the durable record; the epic's task comments are the working log.

## The repo test contract

**General suite = `go test -p 1 ./...`** (serial packages), not the Go
default `go test ./...` (parallel packages, `-p = GOMAXPROCS`). This matches
the pre-commit hook (`.git/hooks/pre-commit`, `AGENTS.md` § Testing).

Why: parallel-package runs surface **cross-package port/resource
contention** (multiple `daemon.test` binaries racing for explicit listen
ports simultaneously) that reads as flaky daemon/proxy/chromedp tests but is
actually a harness-concurrency artifact, not a product bug. The affected
tests pass 10/10 in isolation and 5x under `stress` when their package runs
*alone* — the trigger is specifically *other packages' test binaries
running concurrently*. See `.claude/rules/testing-parallel-package-flakes.md`
for the full root-cause writeup and repro method.

**Loaded-machine acceptance bar**: `stress -c $(nproc)` (moderate, ~1x core
count) alongside the serial suite — not 3x-oversubscription. F-proxy proved
CPU oversubscription beyond ~1x core count induces real host-renderer
starvation in real-Chrome e2e tests that is an environment artifact, not
something this repo's code can fix (see below). Moderate load is the
achievable, meaningful bar for the general suite; oversubscription-only
failures belong to the non-determinizable registry, not the acceptance gate.

## Determinized / fixed flakes

These were root-caused and fixed during the 2026-07-11 flake-epic loop
session. They should be green in the general `-p 1` suite going forward.

| Test | Package | Root cause | Fix commit |
|---|---|---|---|
| `TestCommandWithArgs_DirectPATH` | `cmd/agnt` | ETXTBSY fork/exec race (write+exec of the same path) | `691ce42` (`writeExecutable` via child `sh -c cat>`) |
| `TestEventHub_KeepaliveHeartbeat` | `internal/daemon` | Assertion compared *nominal* send/tick schedule instead of *observed* timestamps; scheduler jitter under load made an on-time keepalive look like a spurious one | `19807765` (judge against recorded actual timestamps) |
| `TestStartupPortCleanup_KillsOrphan` | `internal/daemon` | Never reproduced after 90+ induced-load attempts; defensive fix applied anyway because it mirrors a proven sibling pattern (`killPortHoldersGuarded`'s fail-open re-scan) | `8ee562fb` (`findPortHoldersWithRetry`, 4 attempts / 50ms backoff) — non-reproduced-defensive, not root-caused |
| Hook dispatcher p99 ≤5ms contract | `cmd/agnt` (`hook_latency_test.go`) | The contract's only guard (`TestHook_LatencyAgainstWarmDaemon`) had been widened to a 1s smoke test because its wall-clock budget flaked under load; the real 5ms guarantee had no asserting test | `3691ce42` (`TestHook_LatencyWithinBaselineFactor` — calibrates against a same-run baseline of 300 round trips over a trivial echo socket, so load inflates baseline and measurement together and the derived bound stays load-tolerant) |
| `TestIsRunning` | `internal/daemon` (`socket_test.go:256`) | Fixed-instant assertion (`IsRunning()==false` at one point post-`Close()`) with no tolerance for late goroutine scheduling deep in a loaded serial suite; the OS dial-after-close race provably does not exist (0/20000) | `2f462fcf` (bounded 2s poll-to-terminal-false) |

Fix pattern shared across all of these (see
`.claude/rules/testing-timing-assertion-flakes.md`): **judge time-driven
correctness by observed/relative signals, never by a fixed wall-clock value
compared against a nominal schedule or a hand-picked absolute latency.**
`require.Eventually`/polling is fine as a *generous ceiling* on an
eventually-true property; it is never acceptable as the sole primary
invariant for a *latency* claim (that needs a same-run baseline instead).

## Genuinely non-determinizable (registered, not chased)

### Real-Chrome e2e tests under CPU oversubscription

- `TestE2E_AuthBreakout_PopupRoundTrip` (`internal/proxy`)
- `TestE2E_CurrentPage_FrameworkWarningsForwarded` (`internal/proxy`)

F-proxy (commit `1f0c152c`) attached CDP to the popup target and traced
browser-process-level events independent of page JS. Evidence: the browser
**commits** the popup navigation in under 1s (seen via
`Target.TargetInfoChanged`), but the target's renderer is never scheduled to
execute the relay's inline script for 80+ seconds under 3x CPU
oversubscription. Doubling the timeout budget did not proportionally reduce
the failure rate (~20-25% residual) — the documented signature of
scheduling starvation, not a reproducible ordering defect (a real bug
converges toward 0% well before 2x headroom).

**Load boundary**: green at idle / moderate load (~1x core count, the
`stress -c $(nproc)` bar this repo's general suite uses); flaky specifically
under 3x-oversubscription. This is an environment property, not a code
defect — no assertion change in this repo can fix a renderer that never gets
CPU time.

**Recommendation (follow-up, not yet actioned — filed as
`01KX9RQHW5SQ8NDMQNB4ERG058`)**: build-tag/load-tier gate these and other
real-Chrome (chromedp) e2e tests out of the general `go test -p 1 ./...`
suite, mirroring the existing `procisolation` two-tier precedent (`make
test` vs `make test-isolated`) — e.g. a `chromee2e` tag + `make
test-chrome-e2e` target — so the general suite can be reliably 5x-green
under moderate load, while the chrome-e2e tier runs separately on an
unloaded box.

### Fixable residual (not in the non-determinizable registry)

- `TestCreate_SpawnsAndCapturesOutput` (`internal/sessionhost/sessionhost_test.go:78`)
  — a fixed 15s PTY-first-output deadline. Observed once during a loaded
  serial-suite run (~11 minutes in, after `internal/daemon` 117s +
  `internal/proxy` 37s + `internal/chromedp` 22s + `internal/overlay` 20s of
  prior packages had already run) with "timed out waiting for output; got
  ''"; passes 5/5 isolated at 0.02s. Same load-accumulation /
  fixed-deadline-on-a-real-process-signal class as `TestIsRunning` and
  `TestEventHub_KeepaliveHeartbeat` above, not yet given the same
  poll-to-terminal treatment. This is fixable and therefore is explicitly
  **excluded** from the non-determinizable registry. Filed as follow-up
  `01KX9RQHCK0T2CB88P31B2EW18`; F-gate is documentation-only and must not
  change `internal/sessionhost/`. It passed in all five loaded acceptance
  runs below, which is evidence against an immediate suite blocker, not a
  claim that the fixed-deadline design has been determinized.

## Wall-clock-invariant sweep

Sweep command (re-run this to extend the audit):

```
grep -rn "time.Since\|Sub(start)\|elapsed\|< .*time.Millisecond\|Less(.*time\." \
  --include=*_test.go internal/ cmd/
```

~50 hits as of 2026-07-11. Most are acceptable: generous ceilings on an
eventually/liveness property (e.g. "graceful stop respects its 500ms
deadline", "resolves within 2s"), or tests deliberately verifying an
injected-delay mechanism's own correctness (chaos-injection tests, retry
backoff). Those are fine per the F-proxy lesson: a fixed timeout guarding
*whether something eventually happens* is a ceiling, not a latency SLO.

The 2026-07-12 resweep found **zero remaining primary absolute wall-clock
assertions**. Follow-up `01KX9RQJ2D76QE0GCRK1Q9VZ5F` completed the seven
previously identified conversions in commits `e558f807` and `11f4224c`:

- hot-path performance checks now compare against same-run baselines;
- router, DNS, session registration, and WebSocket isolation checks use
  observed state or ordering rather than elapsed-time budgets; and
- the router fixture synchronizes explicitly on its slow handler entering a
  test-controlled blocked state.

The resweep repeated the command above and manually classified its remaining
hits. They are relative same-run comparisons, observed-order assertions,
generous deadlock/liveness ceilings, or tests of deliberately injected
delays. None uses absolute wall-clock latency as its primary invariant.

## Acceptance evidence

On 2026-07-12, six CPU stress workers (`stress -c 6`, where `nproc` returned
6) ran continuously while five consecutive uncached serial suites executed.
Each run invoked `go clean -testcache` immediately before
`go test -count=1 -p 1 ./...`. Full logs are attached to the F-gate task
(`01KX8R68ZMEJPQYY4BX4B2A38C`); this compact table is the durable audit.

| Run | Start (America/Chicago) | Exit | Duration | Load before | Load after |
|---:|---|---:|---:|---|---|
| 1 | 2026-07-12 00:44:23 -05:00 | 0 | 261s | 0.95 1.42 1.19 | 11.35 7.69 3.90 |
| 2 | 2026-07-12 00:48:44 -05:00 | 0 | 247s | 11.35 7.69 3.90 | 9.08 8.45 5.10 |
| 3 | 2026-07-12 00:52:51 -05:00 | 0 | 247s | 9.08 8.45 5.10 | 11.09 9.16 6.14 |
| 4 | 2026-07-12 00:56:58 -05:00 | 0 | 249s | 11.09 9.16 6.14 | 9.20 9.44 7.01 |
| 5 | 2026-07-12 01:01:07 -05:00 | 0 | 247s | 9.20 9.44 7.01 | 10.05 10.04 7.85 |

The same stress PID remained live across all five runs (elapsed 00:00 at run
1 start and 16:44 at run 5 start). No cached package result appears in the
logs, and all five runs include `internal/sessionhost` green.

## See also

- `.claude/rules/testing-parallel-package-flakes.md` — why the general suite
  must serialize packages (`-p 1`)
- `.claude/rules/testing-timing-assertion-flakes.md` — judge time-driven
  assertions by observed timestamps, not nominal schedule; the CDP
  browser-process-signal technique for distinguishing a fixable ordering bug
  from unfixable renderer starvation
- `.claude/rules/daemon-architecture.md` § Test startup contract — related
  test-speed discipline (`NewForTest` vs `Start()`)
