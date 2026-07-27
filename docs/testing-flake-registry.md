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
| Hook ring p99 ≤5ms target | `internal/daemon` (`TestHookRing_EnqueueP99Latency`) | Sequential idle mutex calibration was not shaped like the subsequent 8-producer measurement; scheduler stalls could make its derived guard tighter than the documented target | Concurrent, interleaved 8-producer mutex calibration diagnoses scheduler inflation, while an independent hard assertion preserves the absolute p99 <5ms production contract; a second relative assertion catches ring-specific divergence |
| `TestIsRunning` | `internal/daemon` (`socket_test.go:256`) | Fixed-instant assertion (`IsRunning()==false` at one point post-`Close()`) with no tolerance for late goroutine scheduling deep in a loaded serial suite; the OS dial-after-close race provably does not exist (0/20000) | `2f462fcf` (bounded 2s poll-to-terminal-false) |
| `TestCreate_SpawnsAndCapturesOutput` | `internal/sessionhost` | `waitLoop` published `StatusExited` and closed the PTY before `readLoop` had drained the child's final output; under load this permanently discarded bytes rather than merely delivering them late | `1f63ee23` (publish terminal status only after `readDone`, then assert scrollback) + `3c1c1f6a` (bound inherited-slave drain with `exitDrainGrace`); the test polls the terminal-after-drain invariant and uses 30s only as a deadlock ceiling, never a latency requirement |
| `TestSchedulerWriteBehind_*` (all 7; observed on `SlowPersister`) | `internal/daemon` (`scheduler_state_stress_test.go`) | Under a loaded full-suite `-p 1` run, `goleak.IgnoreCurrent()` snapshots the goroutine set at each test's *start*; sibling daemon tests spawn real OS subprocesses (agnt binary/sleep/echo) whose os/exec drain goroutines (`os/exec.(*Cmd).writerDescriptor.func1` pipe pump, `os/exec.(*Cmd).watchCtx`) are created *after* that snapshot and whose reap is delayed past this test's boundary, so they surface here as false-positive "leaks" even though this file uses only a stub persister and never touches os/exec | 2026-07-16 (`verifyNoSchedulerLeaks(t, goleak.IgnoreCurrent())` helper — appends two `IgnoreAnyFunction` filters scoped to the os/exec drain frames only; a genuine `SchedulerStateManager` writeLoop leak has no os/exec frame and still fails, verified by a skip-Close tooth check). Call sites must pass `IgnoreCurrent()` at the `defer` statement so the snapshot stays at test start, not test end |
| `TestClientSessionHost_FullCycle` | `internal/daemon` (`client_sessionhost_test.go`) | Every wait was a fixed wall-clock window: a 3s `time.After` for the post-`SESSION-HOST KILL` exit frame, plus 5s attach-stream contexts that silently ended the stream (and therefore all later frames) if the earlier phases ran long. Discovered 2026-07-27 draining S1 (`01KYJC0CE04TGVZTW67V2AZNK0`): passes 5/5 isolated, fails with package `-count=2` under `nproc` CPU burners. Shape is LATE-result, not empty-result, so it is the timing-assertion class, not the data-loss class | 2026-07-27 (`awaitClosed` + `require.Eventually` against a single generous `sessionHostLiveness` ceiling used for the attach contexts too). These are liveness waits — "does the frame ever arrive" — never latency SLOs, so the ceiling only bounds a hang. Teeth verified by deleting the `exit` broadcast in `sessionhost.go`: the test still fails |
| `TestHubProgressKeepsSilentRequestAlive` | `internal/daemon` (`hub_progress_test.go`) | Client `WithTimeout(25ms)` raced a fixed 65ms handler and relied on STATUS frames arriving on a *nominal* 10ms tick; under load a stalled tick blew the hardcoded idle deadline and the healthy request failed. Same 2026-07-27 discovery and repro as the row above | 2026-07-27 (client idle deadline derived from an observed same-run round-trip baseline via `measureRoundTripBaseline`, handler silence then derived as 6× that deadline). The invariant is now a load-invariant *ratio* — the request must outlive several of its own idle-deadline windows, which only a received STATUS frame can achieve — plus an observed-elapsed assertion that the request really did span multiple windows. Teeth verified by removing the STATUS deadline refresh in the vendored client `conn.go`: the test still fails |

`hub_progress_test.go` is now a **two-hit file**:
`TestHubProgressDoesNotContaminateChunkedPayload` in the same file is still a
registered open flake below (same 25ms client deadline against a 45ms chunk
gap). It was left untouched by the 2026-07-27 fix, which was scoped to the two
tests that failed in that run — but the baseline-derived deadline pattern
introduced here (`measureRoundTripBaseline`, `hubStatusInterval`) is the
ready-made fix shape when it is picked up.

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

**Tier isolation completed** in `32a351e1` and `2943e6ea` (follow-up
`01KX9RQHW5SQ8NDMQNB4ERG058`): real-Chrome tests are tagged `chromee2e`, are
excluded from `make test`, and run through `make test-chrome-e2e`. This
matches the four-tier contract in `AGENTS.md` and keeps renderer starvation
outside the host-safe general suite.

The Chrome tier is deliberately a **scheduled/manual unloaded-machine**
gate. The isolation commits and tag/Makefile checks prove suite composition;
they do not claim that real Chrome was runtime-validated under CPU
oversubscription. Running that tier concurrently with `stress` or a full
suite would contradict the diagnosed renderer-starvation boundary above.

## Observed open flakes (registered 2026-07-24)

Load-dependent failures observed during full-package `-race` runs of
`internal/daemon` on a busy machine. Both pass repeatedly in isolation
(`-run <name> -count=1 -race`); the trigger is suite-wide load, not a
reproduced ordering defect. Root cause not yet chased — if they recur,
investigate with the repro method in
`.claude/rules/testing-parallel-package-flakes.md`.

| Test | Signature | Notes |
|---|---|---|
| `TestRunAutostartAsync_DependencyWait` (`internal/daemon/autostart_async_test.go`) | `b` never entered dep wait within 15s; `context_cancel_during_dependency_wait` misses `PhaseDependencyWaitStart` | Failed 2/7 full-package runs on 2026-07-24; passes 2/2 isolated. Looks like a fixed-window timing assertion against a nominal schedule (the anti-pattern in `.claude/rules/testing-timing-assertion-flakes.md`). |
| `TestHubProgressDoesNotContaminateChunkedPayload` (`internal/daemon/hub_progress_test.go:141`) | unix socket read `i/o timeout` | Failed 1/7 full-package runs on 2026-07-24; passes isolated. Socket read deadline too tight under race-instrumented load. Still open: its sibling `TestHubProgressKeepsSilentRequestAlive` in the same file was fixed on 2026-07-27 (making this a two-hit file); apply the same baseline-derived-deadline shape here. |
| `TestProxy_PageTracking_Integration` (`internal/proxy/integration_test.go:127`) | `Expected 1 resource in session, got 0`; run also logged `bind: address already in use` on an ephemeral port | Failed 1/1 full-package pre-commit run on 2026-07-24 with the live daemon + a concurrent session loading the machine; passes 3/3 isolated (~0.6s). Resource attribution likely races under load, or the bind collision poisoned a sibling test's server. |

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
