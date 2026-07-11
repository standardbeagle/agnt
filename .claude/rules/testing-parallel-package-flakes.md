---
written_at: 2026-07-11T00:00:00Z
source_event: task-09b-loop-session-2026-07-11
---

# Lesson: full-repo `go test ./...` must serialize packages (`-p 1`)

Discovered while running worktrack task `09b` (no code change — its acceptance
criteria were already satisfied by commit `87240533`). While validating the
task's test gate, the worktrack `slice-go` template's gate command —
`go test ./...` — ran all package test binaries in **parallel** (Go's
default `-p` = `GOMAXPROCS`). This intermittently failed in `internal/daemon`
(e.g. `TestIsRunning` — "IsRunning() returned true after socket closed",
and `TestStartupPortCleanup`-class tests) and in browser-e2e packages
(`internal/proxy`, `internal/chromedp`).

## Root cause

Not CPU/memory load — **cross-package port/resource contention**. Multiple
`daemon.test`-spawned proxies across different package binaries race for
explicit listen-ports at the same time, producing:

```
explicit listen-port NNNNN is in use (owner: daemon.test pid=...)
bind: address already in use
```

The failing daemon tests pass 10/10 in isolation, and pass 5x under `stress`
when the package runs **alone**. The trigger is specifically *other packages'
test binaries running concurrently*, not resource pressure on the single
package under test. A dedicated determinization attempt could not reproduce
the failure in isolation for exactly this reason — it tested the package
alone, which is precisely the condition that never fails.

## Fix

`go test -p 1 ./...` is green on the same tree where `go test ./...` failed.
This matches the repo's own pre-commit hook contract (`.git/hooks/pre-commit`,
documented in `AGENTS.md` § Testing: `go test -count=1 -race -p 1` on staged
packages) — the pre-commit hook already serializes packages precisely to
avoid this class of contention. The `slice-go` worktrack template's gate had
diverged from that contract.

A new template, `slice-go-p1` (identical to `slice-go` but with `-p 1` in its
test-gate command), now owns the `slice` auto-attach tag. All agnt worktrack
tasks should attach through it rather than `slice-go`.

## Generalize

Any full-repo `go test ./...` gate in this project — CI, pre-commit,
worktrack templates, ad hoc verification — must serialize packages (`-p 1`,
or equivalent) to match the pre-commit contract. Parallel-package runs surface
cross-package port-binding contention that *reads* as flaky daemon/browser-e2e
tests but is actually a harness-concurrency artifact, not a product bug.

This is an instance of the flake-attribution trap already tracked in
`feedback_flaky_test_hunt` (isolate + intensify the *suspected* test) and
`project_flake_class_2026_07` (per-user memory, `.claude/projects/-home-beagle-work-core-agnt/memory/`):
flakes that only appear under whole-repo parallelism will never reproduce by
determinizing the named test in isolation, because isolation removes the
actual trigger (concurrent sibling test binaries). Before spending effort
isolating a "flaky" daemon/proxy/chromedp test, first re-run the failure
under `-p 1` — if it goes green, the bug is harness concurrency, not the
test.

## See also

- `AGENTS.md` § Testing — pre-commit hook's `-p 1` contract
- `.claude/rules/daemon-architecture.md` § Port-Kill Guard, § Session Containment — why daemon tests bind real ports
- per-user memory `feedback_flaky_test_hunt`, `project_flake_class_2026_07`
