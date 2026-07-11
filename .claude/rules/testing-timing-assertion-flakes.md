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
