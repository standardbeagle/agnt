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

## Sibling class: a test that writes into the SOURCE TREE poisons every worktree-reading gate

Same family — a harness artifact that costs real diagnostic time because it
*reads* as someone's product change. `cmd/agnt/AGENTS.md` gets written into the
repository working tree during the `cmd/agnt` suite (tracked as
`01KYQEGBK1XWS4TKVCYCR6YYFY`; the real writer is the `agnt` binary spawned by
`shell_resolve_test.go`'s `TestShellResolve_E2E_Binary` — see the
misdiagnosis history below). The file
is untracked and is not in any task's `fileScope`, but **scope-check evaluates
the whole tree including untracked files**, so every full-suite run dirtied the
tree for whatever task happened to be in flight.

Observed cost across the `pubserve` epic tail: deleted by hand three times, and
**two implementers plus a reviewer each independently had to stop and establish
that a file was not theirs** before they could trust their own diff. That is the
same wasted-attribution tax as the `-p 1` class above, just paid in review
cycles instead of reruns.

The mechanism to watch for is a **cwd-relative write in production code**
(`phaseCmdArgsAndPrompt`/`writePersistentContext` write `AGENTS.md` next to the
project directory, which `run.go` resolved from `os.Getwd()`) reached either
in-process by a test fenced only with `os.Chdir`, or — the part that took three
tries to find — **by a child process the test spawns**. `t.TempDir()` is real
isolation; `os.Chdir` is **process-global** and therefore is not; and neither
one reaches a subprocess at all.

This bug was misdiagnosed three times in a row, each time plausibly:

1. Blamed the test for not using `t.TempDir()` — it did use it.
2. Blamed `support_matrix_test.go` — it `Chdir`'d into a temp dir correctly, so
   its write never landed in the repo.
3. Fixed the in-process path only (threading `projectDir` through
   `phaseCmdArgsAndPrompt`, commits `3d17edf4`/`d50c28f6` — correct and worth
   keeping) while the subprocess path kept leaking.

The actual writer was `TestShellResolve_E2E_Binary` spawning the real `agnt`
binary with **no `cmd.Dir`**: the child inherited cwd=`cmd/agnt` and re-resolved
the destination itself. Provenance that settled it: the artifact's body contains
`test-e2e-cmd`, a string unique to that one spawn. Note the leak fires *even
when the test reports SKIP*, because the write precedes `pty.Start`; and it only
fires where the test process has a controlling terminal, so a TTY-less CI run
skips the test and yields a **false clean**. Verify this class under
`script -qec '<go test …>' /dev/null`, and prefer a positive assertion that the
write landed under `cmd.Dir` over merely asserting the repo stayed clean — the
latter is also satisfied by a child that never ran.

**Rules**:

1. A test must never be able to write into the repository working tree. Pass an
   explicit destination directory (`t.TempDir()`) into the code under test;
   treat reliance on process-global `os.Chdir` as a defect, not isolation.
1a. **A test that spawns your own binary must set `cmd.Dir`.** `t.TempDir()` and
   `os.Chdir` in the PARENT do not constrain a CHILD process — the child
   re-resolves every cwd-relative path itself. De-cwd'ing the in-process call
   path does nothing for a spawned one. Audit every `exec.Command(agntPath, …)`
   for `cmd.Dir` (`cmd/agnt/run_test.go` already sets it; `shell_resolve_test.go`
   did not).
2. If a stray untracked file appears mid-task, do not fold it into a close-out
   commit and do not adjudicate it from scratch — check the known-strays list
   here first, delete it, and move on.
3. When production code writes to a cwd-relative path, that path is a
   parameter waiting to be extracted. Extract it the first time a test needs to
   `Chdir` to contain it.

## See also

- `AGENTS.md` § Testing — pre-commit hook's `-p 1` contract
- `.claude/rules/daemon-architecture.md` § Port-Kill Guard, § Session Containment — why daemon tests bind real ports
- per-user memory `feedback_flaky_test_hunt`, `project_flake_class_2026_07`
- `.claude/rules/publish-security-review-lessons.md` — the epic whose tail paid
  the source-tree-pollution tax documented above

<!-- provenance: the source-tree-pollution section —
     written_at 2026-07-29;
     source_event pubserve epic 01KYJBPVCX5BKD7YS03E4MTX87 tail (S7/S8/S9/S10:
       01KYJC0CWY6DBN91V86CMDYFRX, 01KYJC0CZVVC0TTYT720KWYSYX,
       01KYJC0D3FDSG4A05W9058YNR0, 01KYJC0D6K1NAA9Q9J68T4CW7B);
       observed independently by S9's implementer, S9's reviewer, and S10's
       reviewer (advisory on cmd/agnt/AGENTS.md);
     tracking task 01KYQEGBK1XWS4TKVCYCR6YYFY;
     attribution corrected 2026-07-29 (attempt 2 of the same task, after
       correctness-review reproduced the leak at HEAD d50c28f6 in an isolated
       worktree under a real PTY): the writer is the no-cmd.Dir binary spawn in
       shell_resolve_test.go, not support_matrix_test.go -->
