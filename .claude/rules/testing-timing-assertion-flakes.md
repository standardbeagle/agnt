---
written_at: 2026-07-11T20:41:00Z
source_event: task-F-daemon-01KX8R68X97Z0H0M1W7K9WWQQX-commit-19807765
---

# Lesson: judge time-driven event assertions by observed timestamps, not nominal schedule

Discovered while determinizing worktrack task `F-daemon`
(`01KX8R68X97Z0H0M1W7K9WWQQX`, package `internal/daemon`), fixed in commit
`19807765` (`TestEventHub_KeepaliveHeartbeat`), reviewer verdict
`pass_with_changes`.

## Root cause

The test's burst phase slept a fixed `synthInterval/4` between sends and
asserted no extra keepalive fired, assuming the real send-to-send gap would
track that nominal value. Under scheduler pressure in a loaded serial suite,
the sending goroutine can be descheduled long enough that the *actual* gap
exceeds the ticker interval — at which point a keepalive firing inside that
now-longer gap is correct ticker behavior, not a `Reset()` suppression
regression. The assertion was a fixed-wall-clock invariant that a busy host
can simply outrun; an "extra" tick is not a bug, it's the test's premise
going stale.

## Fix

Record each send's actual timestamp and each tick's actual timestamp, then
judge the invariant against what really happened: a keepalive is only a bug
if it lands strictly between two sends whose *own observed* gap was shorter
than the interval. `require.Eventually` is retained only as generous
headroom for the idle phase, never as the primary invariant. This makes the
test immune to scheduling jitter instead of requiring a longer fixed sleep,
which would only move the flake window rather than remove it. Reviewer
confirmed the fix still has teeth: a genuine `Reset()`-suppression
regression still fails the rewritten assertion.

## Generalize

Any test in this repo asserting on the **count or spacing of time-driven
events** (ticker fires, keepalive sends, heartbeats, polling intervals) must
derive its expectation from the **recorded actual timestamps** of the events
it observed, never from `configured interval × assumed scheduler
promptness`. Under a loaded serial suite (this repo's whole-package gate
runs `-p 1`, see `testing-parallel-package-flakes.md`), inter-event gaps
routinely drift beyond nominal — treating that drift as a failure produces a
flake on a false premise, not a real regression signal.

This is the deterministic-timing counterpart to the parallel-package flake
class already documented in `.claude/rules/testing-parallel-package-flakes.md`
(cross-package port contention under `go test ./...`) and to the
ETXTBSY write-then-exec class in per-user memory
(`project_flake_class_2026_07`): same family root cause — no absolute
wall-clock value may serve as a *primary* test invariant in this codebase.
`require.Eventually` for generous headroom is fine; a tight fixed-sleep
count/ordering assertion is not.

## Secondary: non-reproduced defensive fix, when it's legitimate

Same task, different test (`TestStartupPortCleanup_KillsOrphan`, commit
`8ee562fb`): after 90+ induced-load attempts the flake could not be
reproduced. A defensive fix was still applied — `startupPortCleanup` gated
its entire kill decision on a single `FindPIDsByPortTagged` `/proc` scan; a
sibling code path (`killPortHoldersGuarded`, see
`.claude/rules/daemon-architecture.md` § Port-Kill Guard) already fails open
into a kill-time re-scan rather than trusting one snapshot.
`findPortHoldersWithRetry` (4 attempts, 50ms backoff) brings
`startupPortCleanup` in line with that proven sibling pattern.

Generalize: a non-reproduced flake may still warrant a defensive fix **only
when** (a) there is exactly one code mechanism plausibly matching the
symptom, and (b) the fix mirrors an existing, proven sibling pattern in the
same codebase rather than a fabricated root cause. Document the fix
explicitly as non-reproduced-defensive (not "root-caused"), and file any
remaining latency/regression-test advisories as follow-up tasks rather than
asserting the investigation is closed. (Follow-up here: `TestIsRunning` →
task `01KX9EFFWG...`.)

## See also

- `.claude/rules/testing-parallel-package-flakes.md` — sibling flake class
  (cross-package port contention, `-p 1` fix)
- `.claude/rules/daemon-architecture.md` § Port-Kill Guard — the
  fail-open re-scan pattern `findPortHoldersWithRetry` mirrors
- per-user memory `feedback_flaky_test_hunt`, `project_flake_class_2026_07`

## CDP browser-process signal distinguishes a fixable injection bug from unfixable host renderer-starvation (task F-proxy)

Discovered while determinizing worktrack task `F-proxy`
(`01KX8R68VM9AY8CXSE2AKQKQHX`, real-Chrome AuthBreakout popup-relay e2e),
fixed/documented in commit `1f0c152c`, reviewer verdict `pass_with_changes`.

### The diagnostic technique

For a real-Chrome (chromedp) e2e flake where a page-JS relay appears not to
run in time, don't trust page-level signals alone — attach to the popup
target and log **browser-process-level** CDP events (`Target.TargetInfoChanged`
/ target lifecycle), which are independent of the page's own JS execution.
This separates two look-alike failure modes:

- **Hypothesis A (fixable)**: an injection/lifecycle-ordering bug — the
  relay script is attached too late, or the poll misses the window.
- **Hypothesis B (unfixable environment artifact)**: the browser-level
  navigation *commits* promptly (<1s, seen via the Target event) but the
  target's own renderer never gets scheduled to execute its inline script
  for many seconds — real CPU-scheduling starvation, not a bug in this
  repo's code.

Corroborate with a second trace under the same load hitting a *different*
stall stage (cross-scenario) — if both stall in different places under the
same CPU pressure, that's starvation, not a reproducible ordering defect.
The tell-tale signature of unbounded starvation: **doubling the timeout
budget does not proportionally reduce the failure rate** — a real ordering
bug converges to ~0% failures well before 2x headroom; starvation does not.

### Fix / accepted resolution

When the CDP evidence points to hypothesis B, the honest outcome is to
**document it as non-determinizable-with-CDP-evidence** and widen the
budget as a practical (non-guaranteeing) mitigation — never weaken the
actual assertions, never cherry-pick a clean run to call it "fixed." This is
an **accepted outcome**, not a failure to root-cause, once corroborated by
the browser-process-level signal and the doubling-budget non-response
above.

### Generalize

1. A fixed wall-clock timeout guarding an **eventually/liveness** property
   (does the round-trip eventually happen at all) is a generous ceiling and
   is acceptable; this is distinct from a **latency SLO** (is it fast),
   which must be baseline-calibrated instead of hand-picked (see task
   `F-hook`, `01KX8R68YZECQ7HP1YKG5VXR4Z`).
2. Real-Chrome e2e tests that starve under CPU oversubscription should be
   build-tag/load-tier gated out of the default `go test -p 1 ./...` suite
   so the suite stays reliably green, and run separately on an unloaded
   box. (Feeds task `F-gate`, `01KX8R68ZMEJPQYY4BX4B2A38C`.)
3. Ties to `.claude/rules/lessons-ssh-transport.md` #2 (poll the actual DOM
   signal you're asserting on, not a proxy signal like `location.href` that
   fires earlier in the lifecycle) — same family: know exactly which layer
   (browser process vs. page JS vs. URL) a signal actually reflects before
   trusting it as evidence.
