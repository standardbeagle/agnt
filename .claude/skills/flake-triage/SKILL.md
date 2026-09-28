---
name: flake-triage
description: "Diagnose a failing, flaky, or timing-out Go/chromedp test before changing it. Use when: test flake, intermittent failure, timeout, passes alone fails in suite."
---

# Flake triage

Classify the failure before touching code or deadlines. Most "flakes" in this repo are harness artifacts or data-loss races, not scheduler load.

## 0. Read what already ran

Read `.tman/<alias>.fail.log` (e.g. `.tman/test.fail.log`) at the repo root. Do not rerun the whole suite to find the failure.

## 1. Where did it fail?

| Signal | Class | Next step |
|---|---|---|
| Failed under parallel `go test ./...` only | Cross-package port/socket contention | Rerun under `-p 1`. Green → harness artifact, not a product bug. |
| Fails at 0.00s: `bootstrap failed`, `daemon already running`, `address already in use` | Shared global resource (socket, port, lock file) falling through to a production default | Reproduce with **two concurrent test binaries**. `-p 1` and isolation both hide it. Fix the helper's hermetic default; never widen a deadline. |
| Empty or missing result for the full deadline | Data-loss race (e.g. fd closed before reader drained) | Add a completion barrier, then lower the deadline. |
| Late result, ordering/count assertion off | Wall-clock premise | Rewrite the assertion against recorded timestamps (`testing-timing-assertion-flakes.md`). |
| `cmd/agnt` red on a task that never touched it | Known strays: `cmd/agnt/AGENTS.md` written by a spawned binary, port 5173 contention | Delete the stray, retry once. |
| Real-Chrome step stalls | Injection bug or renderer starvation | Step 3. |

## 2. Reproduce the real trigger

- Isolate and intensify the *suspected* test: tight loop (`-count=N -run '^Name$'`) plus induced CPU load (`stress`).
- A flake that needs sibling test binaries never reproduces in isolation. Test the condition that actually differs.
- Validate one `go test` at a time; concurrent runs pollute each other.
- Verify a fix under the same induced load that reproduced it. One clean run proves nothing.
- Spawned-binary tests that only misbehave with a controlling terminal: run under `script -qec '<go test …>' /dev/null`. A TTY-less run can skip and give a false clean.

## 3. Real-Chrome stalls: fixable or starvation?

Attach to the target and log browser-process CDP events (`Target.TargetInfoChanged`), which do not depend on page JS.

- Navigation commits late or the relay attaches late → injection/lifecycle ordering bug. Fix it.
- Navigation commits in under 1s but the renderer does not run inline script for seconds, and a second trace under the same load stalls at a different stage → CPU starvation.
- Doubling the budget: a real ordering bug converges to ~0% failures; starvation does not.

Starvation outcome: record it in `docs/testing-flake-registry.md` with the CDP evidence, keep the test `chromee2e`-tagged, never weaken assertions, never cherry-pick a clean run.

## 4. Before calling it fixed

- Mutation-check: revert the fix, confirm the test fails on the behavioural assertion.
- If not reproduced, only a defensive fix that mirrors a proven sibling pattern is allowed, labelled non-reproduced-defensive.
- A `-race`-only hook failure on a GREEN commit may bypass the hook only under Case B in `testing-parallel-package-flakes.md`.
