---
description: Ratified repo-wide testing conventions — node-driven JS runtime test tier, and the cross-package test-harness file shape. Reference only.
---

# Testing Conventions

Two de facto conventions, ratified 2026-08-19 (task `01KYR0XXXQJVF5SWS86N97RW3Y`),
after two earlier tasks gave opposite answers on each and the tree has since
converged. This doc records the converged shape; it is not a place to
re-litigate the underlying fixes.

## 1. Node-driven JS runtime test tier (env-gated, source-guarded)

Some shipped browser JS carries behavior the Go source of the test cannot
express — grapheme-cluster / surrogate-pair boundaries, `TextEncoder` byte
counting, DOM-stub interaction. A test that must assert on that behavior may
**extract the real production function and execute it under `node`**. This tier
is **permitted**, and its canonical shape has four required parts:

1. **Env-gate `AGNT_JS_RUNTIME_TESTS=1`.** The default `go test` suite must not
   depend on `node` being installed. When the env var is unset the node case
   `t.Skip`s **loudly** — the skip message names the always-on source guard
   that still ran, so a node-less box loses the node assertion but is never
   left silently uncovered.
2. **A paired always-on source guard.** Every node case has a sibling test that
   runs unconditionally (no env-gate, no node) and asserts the basic invariant
   directly against the source or a pure-Go reimplementation. Losing node is
   losing *one* assertion, never losing *all* coverage of the property.
3. **Extraction and execution failure must be LOUD.** `extractJSFunc`
   `t.Fatalf`s on unbalanced braces; a malformed-but-balanced extraction that
   is invalid JS surfaces as a `node` `SyntaxError` through `CombinedOutput`
   → `t.Fatalf("node driver failed: …")`. A skipped-because-no-node run is the
   only silent path, and only when the env var is unset.
4. **Extract the real production code, do not rewrite it.** `extractJSFunc`
   pulls the actual shipped function body out of the bundle by header +
   brace-matching. A hand-copied reimplementation would test the copy, not the
   artifact.

**Env-gate is canonical, not a build tag.** The 2026-07-29 task snapshot
proposed a build tag; the de facto convention that shipped uses an env-gate,
and it achieves the same goal (default suite independent of node) while being
easier to flip on locally for a single ad-hoc run
(`AGNT_JS_RUNTIME_TESTS=1 go test ./internal/proxy/scripts/`) than editing a
`-tags` invocation. Ratify the env-gate as the canonical form.

Instances on the current tree (`internal/proxy/scripts/`):

| Node case (env-gated) | Always-on source guard |
|---|---|
| `TestPlayerByteTruncationIsClusterSafe` (`walkthrough_player_test.go`) | `TestPlayerCapsAndClampsAreByteDenominated` |
| `TestPlayerRevealNoSplitSurrogateFrames` (`walkthrough_reveal_boundary_test.go`) | `TestPlayerRevealAdvancesOnClusterBoundary` |
| `TestLiveWalkthroughGestureLabelByteRefused` (`walkthrough_live_gesture_label_test.go`) | `TestLiveWalkthroughGestureLabelCapIsByteDenominated` |
| `TestDemoIndicatorExhaustionAndPlacement` (`demo_indicator_test.go`) | `TestDemoIndicatorExhaustedBudgetStillCarriesTheBadge`, `…FirstChildPlacementIsGatedOnStyling` |

All four are already env-gated with a paired guard as of ratification; no gate
alignment was required.

## 2. A cross-package test harness is a plain `.go` file fenced by `*testing.T`

A helper that must be importable from another package's `_test.go` but must
never be reachable from production is a **plain `.go` file** (not `_test.go`,
which cannot be imported across packages), fenced by a `*testing.T` parameter.
Production code cannot construct a `*testing.T`, so the parameter is a
compile-time fence against production import — no build tag needed.

This ratifies the "flagged for promotion, not yet promoted" note in
`lessons-ssh-transport.md` § 9. Three independent instances established the
shape before ratification:

| Harness | File |
|---|---|
| `NewForTest(t, cfg)` | `internal/daemon/test_helpers.go` |
| shared test utilities | `internal/testutil/testutil.go` |
| `SSHDFreezeHarness` | `internal/sshclient/testharness_reconnect.go` |

The daemon case additionally documents its own startup contract in
`daemon-architecture.md` § Test startup contract; that section stays the
source of truth for *what* `NewForTest` skips, while this entry owns the
general file-shape rule.

## See also

- `AGENTS.md` § Testing — the `-p 1` full-suite contract and the `--no-verify`
  sanctions (RED-first and the GREEN `-race`-flake case).
- `.claude/rules/testing-parallel-package-flakes.md` — both sanctioned
  `--no-verify` cases (RED-first TDD, GREEN `-race`-flake) in one place.
- `.claude/rules/daemon-architecture.md` § Test startup contract — the daemon
  harness's skip list.

<!-- provenance: written_at 2026-08-19; source_event task
     01KYR0XXXQJVF5SWS86N97RW3Y (convention ratification).
     Two-sides-disagreement origin commits: 22ba23f2, ccc2466b.
     Current-pattern commits: 0359b637 (surrogate-pair fix + env-gate),
     fbc3ca10 (GREEN-past-`-race`-flake harvest), 2cae85df (the GREEN
     `--no-verify` case in testing-parallel-package-flakes.md). -->
