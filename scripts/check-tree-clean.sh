#!/usr/bin/env bash
#
# check-tree-clean.sh — standing guard: fail the build if the test suite leaves
# the git working tree dirty.
#
# WHY THIS EXISTS
#   The `cmd/agnt/AGENTS.md` leak (worktrack 01KYQEGBK1XWS4TKVCYCR6YYFY) was
#   misdiagnosed three times in a row, and its fix shipped incomplete once,
#   because the property "tests do not write into the working tree" was trivial
#   to assert and asserted NOWHERE. This is that mechanical assertion. See
#   .claude/rules/testing-parallel-package-flakes.md
#     § "Sibling class: a test that writes into the SOURCE TREE ..."
#   (referred to elsewhere as § source-tree-pollution).
#
# TWO SUBTLETIES THIS GUARD HANDLES (do not remove without reading them)
#   1. A TTY-LESS RUN GIVES A FALSE CLEAN. The historically-leaking test
#      (cmd/agnt/shell_resolve_test.go's TestShellResolve_E2E_Binary) SKIPS
#      without a controlling terminal, so a CI runner with no PTY certifies the
#      wrong thing. This guard runs the suite under a PTY (`script -qec`) so the
#      PTY-gated tests actually EXECUTE, and additionally reports the skipped
#      test count from each run so a false clean stays visible.
#   2. IDEMPOTENT, NOT MERELY CLEAN-ONCE. A leaked artifact makes a *new*
#      regression test fail spuriously on the SECOND run with a message that
#      misdirects blame. This guard runs the full suite twice from a clean tree
#      and requires the tree to be empty after BOTH.
#
# NOT SATISFIABLE BY GITIGNORE. The point is that tests do not write into the
# tree, not that we stop noticing. This guard reads `git status --porcelain`,
# which lists untracked-but-not-ignored files; adding a .gitignore entry to
# silence it is a defect, not a fix.
#
# USAGE
#   scripts/check-tree-clean.sh            # default: 2 runs of `go test -p 1 -v ./...`
#   TREE_CLEAN_RUNS=1 scripts/check-tree-clean.sh
#   TREE_CLEAN_TEST_CMD='go test -p 1 ./cmd/agnt/...' scripts/check-tree-clean.sh
#
# EXIT CODES
#   0  every run passed and left the tree empty.
#   1  a run left the tree DIRTY (the guard's own verdict) — pollution message.
#   2  the tree stayed empty across all runs, but a suite run itself was RED
#      (e.g. an unrelated flake). The tree-clean gate PASSED; the suite did not.

set -uo pipefail

RULE_DOC=".claude/rules/testing-parallel-package-flakes.md"
# Default mirrors `make test` (go test -p 1 -v ./...) with one addition: -count=1.
#   -p 1     matches the repo's serial-package contract (avoids cross-package
#            port contention — see the same rule doc).
#   -v       makes per-test SKIP lines countable (false-clean visibility).
#   -count=1 DEFEATS THE TEST CACHE. Without it, the second idempotency run is
#            served from cache, never re-executes, and therefore can neither
#            leak nor prove the tree survives a real second run — silently
#            gutting subtlety #2. A cached run is not a run.
TEST_CMD="${TREE_CLEAN_TEST_CMD:-go test -p 1 -count=1 -v ./...}"
RUNS="${TREE_CLEAN_RUNS:-2}"

porcelain() { git status --porcelain; }

print_dirty_paths() { git status --porcelain | sed 's/^/    /'; }

fail_dirty() {
  local phase="$1"
  echo ""
  echo "=================================================================="
  echo "TREE-CLEAN GUARD FAILED — the test suite left the working tree DIRTY"
  echo "  ($phase)"
  echo ""
  echo "Offending path(s) (git status --porcelain):"
  print_dirty_paths
  echo ""
  echo "A test wrote into the repository working tree. This is the"
  echo "source-tree-pollution class documented in:"
  echo "    $RULE_DOC"
  echo "    § \"Sibling class: a test that writes into the SOURCE TREE ...\""
  echo ""
  echo "Fix the TEST, not the symptom:"
  echo "  - pass an explicit t.TempDir() destination into the code under test;"
  echo "  - set cmd.Dir on any binary the test spawns (a child re-resolves cwd,"
  echo "    so a parent's os.Chdir / t.TempDir does NOT contain it);"
  echo "  - do NOT gitignore the artifact — that only hides the next leak."
  echo "=================================================================="
  exit 1
}

# --- Precondition: the tree must be clean before we start ---------------------
# Otherwise a pre-existing dirty file would be misattributed to the suite.
if [ -n "$(porcelain)" ]; then
  echo "TREE-CLEAN GUARD: refusing to run — the working tree is ALREADY dirty:"
  print_dirty_paths
  echo "Commit, stash, or clean before running the guard so any dirtiness the"
  echo "suite introduces can be attributed to it."
  exit 1
fi

# --- Run the suite under a PTY so PTY-gated tests EXECUTE ---------------------
run_under_pty() {
  local logfile="$1"
  if command -v script >/dev/null 2>&1; then
    # util-linux: `script -q -e -c CMD FILE`. -e returns the child's exit code;
    # -q suppresses start/stop noise; -c runs CMD with a PTY allocated.
    script -qec "$TEST_CMD" "$logfile"
    return $?
  fi
  echo "WARNING: 'script' not found — running WITHOUT a PTY. PTY-gated tests"
  echo "         (e.g. TestShellResolve_E2E_Binary) will SKIP, so a 'clean'"
  echo "         result here is NOT trustworthy. Install util-linux 'script'."
  # Tee so the skip count below is still countable.
  set -o pipefail
  $TEST_CMD 2>&1 | tee "$logfile"
  return $?
}

suite_failed=0

for i in $(seq 1 "$RUNS"); do
  echo "=== tree-clean guard: full suite run $i/$RUNS (under PTY) ==="
  log="$(mktemp)"
  run_under_pty "$log"
  status=$?

  # False-clean visibility: report how many tests skipped this run. Under a PTY
  # the PTY-gated tests execute (skip count for them should be 0); a nonzero
  # jump here on a TTY-less runner is the tell that the guard certified nothing.
  skips=$(grep -c -- '--- SKIP:' "$log" 2>/dev/null || true)
  skips=${skips:-0}
  echo "run $i: suite exit=$status, tests skipped=$skips"
  rm -f "$log"

  # The tree-dirty verdict is primary and checked on EVERY run, pass or fail.
  if [ -n "$(porcelain)" ]; then
    fail_dirty "after suite run $i of $RUNS"
  fi
  echo "run $i: working tree clean OK"

  if [ "$status" -ne 0 ]; then
    suite_failed="$status"
    echo "run $i: NOTE — the suite itself exited $status (unrelated to tree"
    echo "        cleanliness). Continuing the tree-clean gate."
  fi
done

if [ "$suite_failed" -ne 0 ]; then
  echo ""
  echo "tree-clean gate PASSED: $RUNS consecutive full runs left the tree empty."
  echo "But the suite itself is RED (last non-zero exit $suite_failed) — see the"
  echo "output above. That is a test failure to fix separately, not tree pollution."
  exit 2
fi

echo ""
echo "tree-clean gate PASSED: $RUNS consecutive full runs left the tree empty,"
echo "and the suite is green."
exit 0
