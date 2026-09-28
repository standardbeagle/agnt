---
paths:
  - "**/*_test.go"
  - "internal/testutil/**"
  - "internal/daemon/test_helpers.go"
  - "internal/sshclient/testharness_*.go"
  - "internal/proxy/scripts/**"
  - "Makefile"
---

# Testing Conventions

## 1. Node-driven JS runtime test tier (env-gated, source-guarded)

A Go test may extract a real shipped JS function and run it under `node` when the behaviour cannot be expressed in Go (grapheme/surrogate boundaries, `TextEncoder` byte counts). Required shape:

1. **Env-gate `AGNT_JS_RUNTIME_TESTS=1`**, not a build tag. Unset → loud `t.Skip` naming the paired guard that still ran. `make test` must never need node.
2. **Paired always-on source guard** asserting the basic invariant without node.
3. **Loud failure**: `extractJSFunc` `t.Fatalf`s on unbalanced braces; node errors surface via `CombinedOutput` → `t.Fatalf`.
4. **Extract the shipped function** by header + brace matching. Never hand-copy it.

Existing pairs live in `internal/proxy/scripts/` (`walkthrough_player_test.go`, `walkthrough_reveal_boundary_test.go`, `walkthrough_live_gesture_label_test.go`, `demo_indicator_test.go`).

### 1a. DOM-shaped assertions belong in the vitest + jsdom tier

Anything that reads a document (stylesheets, computed styles, attributes, element walks) goes in `internal/proxy/scripts/jstest` (`make test-js`), not a node driver with a stubbed `document`.

1. `load-audit.js` evaluates the shipped bytes. No wrapper, no copy.
2. Outside `make test`; `make test-js` loud-skips without npm.
3. The Go source guard keeps the contract (fields, scoring, caps) and says in its doc comment that behaviour is proven in the JS tier. `TestModernCSS_EveryFeatureHasDOMCoverage` fails when a detection ships with no JS case.
4. Pair every detection with a near-miss negative, and mutation-verify the negatives.
5. jsdom drops unparseable declarations and folds numeric `calc()`. Where that matters, assert the value survived before asserting on the result.

Layout, paint and renderer timing belong to the `chromee2e` tier.

## 2. A cross-package test harness is a plain `.go` file fenced by `*testing.T`

A helper imported by another package's tests but never by production is a plain `.go` file (not `_test.go`) taking a `*testing.T` parameter. Production cannot construct one, so no build tag is needed. Instances: `internal/daemon/test_helpers.go` (`NewForTest`), `internal/testutil/testutil.go`, `internal/sshclient/testharness_reconnect.go`. What `NewForTest` skips is owned by `daemon-architecture.md` § Test startup contract.

## 3. Hermetic tests

- **Never write into the repo tree.** Pass an explicit `t.TempDir()` destination into the code under test. `os.Chdir` is process-global and is not isolation; a cwd-relative write in production code is a parameter waiting to be extracted.
- **A test that spawns a binary sets `cmd.Dir`.** Parent `t.TempDir()`/`t.Setenv`/`os.Chdir` do not constrain a child. Assert positively that the child's write landed under `cmd.Dir`.
- **Build spawned binaries fresh**, into a temp dir, once per test binary (`sync.Once`). A stale prebuilt binary triggers the client/daemon version-skew upgrade path.
- **Helper resource fields default to a per-test isolate.** Any socket path, port, pid/lock file or state dir whose empty value falls through to a production default is a cross-binary collision (and fights a real dev daemon). `NewForTest` defaults `SocketPath` to `t.TempDir()/d.sock`.
- Tests that start real OS processes must not use `t.Parallel()` (PID-reuse race kills unrelated processes).

## 4. Time

- No absolute wall-clock value is a primary invariant. `require.Eventually` is fine as generous headroom.
- Assertions on count or spacing of time-driven events (tickers, keepalives, heartbeats) are judged against **recorded actual timestamps**, not `interval × assumed promptness`. A loaded `-p 1` suite routinely drifts past nominal.
- Backoff/retry/jitter: inject base delay and jitter source; assert the schedule's shape in microseconds, never sleep it out.
- A liveness bound ("eventually happens") may be a generous fixed ceiling. A latency SLO must be baseline-calibrated in the same run.
- Readiness tests: inject the ready signal with a never-firing deadline and assert the outcome, plus a mutation-guard crash case (`sleep 0.3; exit 1`).

## 5. Real browsers

Real-Chrome tests are `//go:build chromee2e` and loud-skip without a browser (`skipIfNoBrowser`). Gate on the DOM signal you assert on, never on `location.href` alone. Run via `make test-chrome-e2e` on an unloaded machine only.

## See also

- `testing-parallel-package-flakes.md` — `-p 1` gate and the sanctioned `--no-verify` cases.
- `flake-triage` skill — diagnosing a failing or flaky test.
