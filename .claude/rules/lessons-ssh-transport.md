---
written_at: 2026-07-11T04:15:44Z
source_event: task-01KWMAX9AS4W2ARNAAQ89JJNFZ
sources:
  - task:01KWMAX9A4TCGKE6X0KDCB1QEY (task 04, lessons 1-4)
  - commit:47a4a9f68854f6eec1aa0b70c87f94eca7f01824
  - commit:36af71664d914a385c65248e363a176701191721
  - workflow_attempt:01KX58P85FZ5SFXSZZFBHJKD76 (go-test attempt1 failed, attempt2 passed)
  - reviewer_verdict:01KX5AP9KDC7V05KACBWMS5PS0 (pass_with_changes)
  - task:01KWMAX9AS4W2ARNAAQ89JJNFZ (task 05, lessons 5-7 + follow-up 05a)
  - commit:f6be07ad
  - commit:6a88afd4
  - commit:88539584
  - commit:4a3887d3
  - commit:fe117ff2
  - reviewer_verdict:01KX7MY77RE43EX1CMQ3YZ5PJD (pass_with_changes, no rewinds)
  - task:01KX5E02KJM70TJHM1RJFWE8H7 (task 06a, lesson 8)
  - commit:d1b0825d
  - reviewer_verdict:01KX7P9FVQ92KWK68JS594KJJ5 (pass, no rewinds)
  - task:01KX8QVVF2HC6AY3RVFSMYK8C4 (task 09a, lesson 9)
  - commit:6bcc791c9fdfae8abf12a5b98264318450ce6548
  - commit:a7b300d80deb82c35c0b05caf382aa78bb6ddf19
  - reviewer_verdict:01KX8YKHFVBVD28R64W3PYZY7Z (pass, no rewinds)
  - task:01KX8QVVKSRGYEQE0JFJSYJKDV (task 09c, lessons 10-11)
  - commit:59973ea7451a2a59402859052deb6ab20192f77d
  - commit:90a6244277ccfab175c55b2da5756d280103b7e3
  - commit:d6a8ab062a0fe819e8f519ae8676d18bc7d23b04
  - commit:f5d65a9a89c8f92a921f04ea9ed3aa74de47c550
  - reviewer_verdict:01KX972C67V05EPEB5CPEJ7YNC (pass_with_changes, no rewinds — single-attempt pass on every step)
  - task:01KX973H37DZ716WCS27FVAZPT (follow-up bug filed from 09c review, referenced by lesson 10)
written_at_last_update: 2026-07-11T18:45:00Z
---

# Lessons: agnt ssh transport (task 04)

## 1. E2E subprocess tests must build the binary fresh, not trust a stale one
`findAgntBinary()` (cmd/agnt) trusted a pre-built repo-root `agnt` binary.
When its baked-in appVersion diverged from current source, the client/daemon
version-mismatch auto-upgrade path fired mid-test and ate the deadline —
`TestShellResolve_E2E_Binary` timed out intermittently, looking like a flake
but actually a stale-fixture bug. Fix: build from current source once per
test-binary process via `sync.Once`, shared by all `findAgntBinary()` callers.
Generalize: any E2E test that shells out to a self-built binary and expects
it to talk to a self-spawned daemon must guarantee same-build provenance for
both sides, or a version-skew path (that the harness itself defines) will
silently steal test time. Applies to any future `agnt <subcmd>` E2E adding
its own binary-spawn helper.

## 2. chromedp content-iframe navigation: href-updated != document-loaded
`waitContentHref` polled only `location.href`, which updates the instant
navigation commits — before the new document finishes loading. Reading
`textContent` right after raced the load and flaked ~20% of runs (null or
empty string). Fix: `waitContentElementText` polls for the target element
by id **and** non-empty textContent, not just the URL. Generalize: this
project's real-chrome e2e tests must never gate on `location.href` alone
when the next assertion reads element content in the destination frame —
poll for the actual DOM signal being asserted on, not a proxy signal that
fires earlier in the navigation lifecycle.

## 3. Reviewer pass_with_changes still needs its non-blocking asks tracked
Task closed clean (no rewind), but `correctness-review` surfaced two
follow-up-worthy items as `new_acceptance_criteria` rather than blockers:
(a) `--tool` CLI flag parsed but not wired to anything yet — an inert flag
left in `cmd/agnt/ssh.go`; (b) no per-request timeout around the
`keepalive@openssh.com` SendRequest, so a network black-hole (packet loss,
no RST) could block the keepalive goroutine forever and silently prevent
`Dead()` from ever firing. Neither blocked this task (scope reductions were
declared, not silent), but both are exactly the kind of thing that
disappears once the task closes if not carried forward explicitly. Pattern:
when a reviewer returns `pass_with_changes` with `new_acceptance_criteria`,
those items are follow-up debt for the parent epic (`01KWMARXTVWKC33EPHZZJ43JT9`,
remote-ssh) — check they get turned into sub-tasks (e.g. later "06 port
forwarding" / hardening pass), not just left in a closed workflow's outcome
JSON where nobody will read them again.

**Item (b) RESOLVED** — task `04b` (`01KX5AW5HV66SJ8MY3C9FJAT6X`, commit
`f16b3a5b`) bounded `SendRequest` with a 5s timeout (< 15s interval); a
timeout now counts as a miss, and the 3rd consecutive miss also closes the
underlying `c.SSH` connection so a still-blocked `SendRequest` goroutine
unblocks instead of leaking. Regression test: in-process ssh server that
accepts the handshake then silently drops every global request, asserting
`Dead()` closes within a few keepalive intervals. Generalized lesson
registered separately: `.claude/rules/lessons-liveness-probes.md`.

**Item (a) RESOLVED** — task `04a` (`01KX5AW5BQGZS5Y38V3N43FGNZ`, commit
`97beed6e`) dropped the inert `--tool` flag rather than speculatively wiring
it, per lesson 4 below.

## 4. Cobra child-command tests must Execute() the root, not the child
`cobra.Command.ExecuteC()` redirects execution to `Root()` and uses the
**root's** configured args whenever the command being executed has a parent.
A test that does `sshCmd.SetArgs([...]); sshCmd.Execute()` silently ignores
its own `SetArgs` call — cobra runs `rootCmd` with whatever args were last
set on `rootCmd` (often none), and the test can spuriously pass without ever
exercising the flag-parsing path it thinks it's testing. Fix: drive
CLI/flag-rejection tests in `cmd/agnt` through the root —
`rootCmd.SetArgs([]string{"ssh", "myhost", "--tool", "claude"});
rootCmd.Execute()`. Reference: `cmd/agnt/ssh_test.go::TestSSHToolFlagRejected`.
Applies to any `cmd/agnt` test asserting flag-parse behavior (rejection,
default values, required-flag errors) on a subcommand — not just ssh.

Secondary: parsed-but-ignored CLI flags are Config Authority bugs, same as
unacted `.agnt.kdl` fields (`.claude/rules/daemon-architecture.md` § Config
Authority). `agnt ssh --tool` was parsed but only warned-and-discarded; the
backing capability (tool selection in `agnt attach`) doesn't exist yet, so
the correct minimal fix was **removal**, not speculative wiring — cobra's
own unknown-flag rejection then gives a real, loud error instead of a silent
no-op. Re-add the flag when the capability lands (tracked under epic
`01KWMARXTVWKC33EPHZZJ43JT9`).

## 5. Atomic remote-file install over a bare ssh exec channel (no SFTP dep)
Task 05 (`01KWMAX9AS4W2ARNAAQ89JJNFZ`, commits `f6be07ad`/`6a88afd4`/`88539584`/
`4a3887d3`/`fe117ff2`) shipped `internal/sshclient/bootstrap_upload.go`'s
`UploadFile` without adding an SFTP dependency: open one exec session running
`mkdir -p <dir> && cat > <tmp-path>`, stream the binary over stdin, then a
**second** exec session does `sha256sum` verify, `chmod 0755`, and a same-dir
`mv` rename-to-activate — all in that order. Generalize: this project's
pattern for "atomically install an executable on a remote host via ssh" is
temp-write → verify → chmod → same-dir-rename, with no SFTP subsystem
required; `cat >` on an exec channel is sufficient for the write half.
Injection defense is load-bearing here: every interpolated path/value in the
shell command string must be shell-quoted, and remote `uname -sm` output
(os/arch) must be normalized against a fixed whitelist (linux/darwin ×
amd64/arm64) before it ever reaches a command string — untrusted remote
output must not flow into a shell command unvalidated.

## 6. Integrity check must precede rename-to-activate, not follow it
Same `UploadFile`: sha256 verification of the uploaded bytes happens
**before** the same-dir `mv` that makes the new binary live at its final
path. Generalize: for any "upload then activate" sequence (temp-write →
verify → rename), the verify step is only meaningful if it gates the
rename — verifying after activation is already too late, since a corrupt
binary would already be executable at the target path. Applies to any
future remote-install or self-upgrade path reusing this atomic-write shape
(`cmd/agnt/upgrade.go`'s local self-upgrade already follows this order;
keep it consistent when the two paths are eventually unified).

## 7. Scripted/non-interactive install must require explicit opt-in consent
`agnt ssh`'s bootstrap wiring (`cmd/agnt/ssh.go`, commit `fe117ff2`) never
silently installs a binary on a remote host: `--no-bootstrap` skips the
check entirely, a non-interactive/scripted invocation requires explicit
`--bootstrap=yes`, and only an interactive terminal falls back to a y/N
prompt. Generalize: any feature that can install/modify state on a remote
or shared resource without a human present to interrupt it needs an
explicit non-interactive consent flag distinct from the interactive
default — "runs in CI" and "silently mutates infrastructure" must never be
the same code path.

## 12. SFTP remote-path traversal guard: containment must survive four adversarial angles at once
Task 08a (`01KX8R68J0RNYGMTWQ38G85F94`, commits `29d30691`/`b49b3c09`/`c6e58c81`)
added `PushToInbox` (`internal/sshclient/sftp.go`) for arbitrary-file push over
`github.com/pkg/sftp`, guarded by `correctness-review` (verdict
`pass_with_changes`, 0 blockers). The traversal guard that survived
adversarial review has four parts, all required together — any one alone is
bypassable:

1. Reject absolute paths and any `path.Clean` result that is or starts with
   `..`.
2. Segment-safe containment via `root+"/"` prefix — **not** raw
   `strings.HasPrefix(candidate, root)`, which a sibling directory
   (`/proj` vs `/proj-evil`) defeats. Normalize `root` first.
3. Resolve symlinks on **every intermediate path segment**, not just the
   leaf, via `Lstat`/`ReadLink`, with a depth cap that fails closed (errors
   out) on overflow rather than silently truncating the walk. Re-check
   containment against each resolved target as you go.
4. Re-run the same symlink-chain check immediately before the activating
   `PosixRename` — the check at validation time and the check at rename
   time are not the same instant.

Residual: this narrows but does not eliminate TOCTOU — there is a sub-ms
window between the final re-check and the rename syscall, and SFTP has no
`openat`-relative-to-verified-fd equivalent to close it completely.
Judged acceptable-advisory (not a blocker) only because the threat model
already requires the attacker to have pre-existing write access to the
project root to exploit it — and the acceptance is conditional on disclosing
it honestly in a doc comment on the check function, not silently. Generalize:
this four-part shape (clean/absolute reject, segment-safe prefix, walk-every-
segment symlink resolution with fail-closed depth cap, re-check-before-
activate) is this project's canonical pattern for validating an untrusted
remote path before any privileged remote file operation — reuse it rather
than re-deriving a subset next time.

One non-blocking gap the same review surfaced: the filename check
(`fileName != path.Base(fileName)`) does not explicitly reject
`fileName == "."` or `".."` — `path.Base("..")` returns `".."` unchanged, so
`finalPath` can resolve to `destDir` or its parent. No actual bypass results
(destDir is always pre-created via `MkdirAll`, so `PosixRename(tmpFile,
existingDirectory)` fails at the syscall level on the file-vs-directory type
mismatch), but relying on that incidental failure instead of an explicit
`.`/`..` reject is inelegant follow-up debt for whoever next touches
`validateDestRelPath`.

## 13. Two sanctioned remote-file-write paths — same atomic shape, different transport, do not collapse
This project now has two intentionally distinct ways to write a file onto a
remote host over `agnt ssh`:

- **Task 05's `UploadFile`** (bare exec channel, no SFTP dependency):
  single-binary bootstrap install, `mkdir -p && cat >` → verify → chmod →
  rename. Use when the payload is one known binary and you want zero new
  dependencies.
- **Task 08a's `PushToInbox`** (`github.com/pkg/sftp` client): arbitrary
  user-supplied files at arbitrary caller-chosen destinations, needing the
  traversal guard in lesson 9 above (a single fixed bootstrap path never
  needed one).

Both use the identical temp-write → verify(hash readback) → rename
atomicity shape (lessons #5/#6) — that convergence is intentional and should
stay convergent — but the transport choice is deliberate per use case.
Do not merge them into one code path: the no-dependency exec-channel
installer is load-bearing for bootstrapping a host that has nothing yet,
which is exactly the case where you can't assume an SFTP subsystem is
wanted/available server-side either.

## 14. Second-terminal control-socket discovery: fail loud, reclaim stale, never hijack live
Task 08a's control socket (`~/.agnt/ssh/<host>.ctl`, `internal/sshclient/control.go`)
lets a second terminal (`agnt push`) find the connection an already-running
`agnt ssh <host>` owns. Two rules the adversarial review confirmed held:

1. **Fail loud, not silent, when no session is active.** `DialControl`
   wraps a sentinel `ErrNoActiveSession` with the host name and an actionable
   hint ("start `agnt ssh <host>`") rather than returning a bare connection
   error or timing out unexplained — same Silent Failure Prohibition this
   project applies to daemon operations.
2. **Reclaim a stale socket left by a crashed owner, but never steal a live
   one.** Both `ListenControl` (the owning side, on startup) and
   `DiscoverActiveHosts` (the discovering side) must independently detect
   "socket file exists but nothing answers" and clean it up, while a socket
   that does answer is never treated as reclaimable — corrupting that
   distinction would let a second `agnt ssh` to the same host hijack the
   first one's live connection instead of erroring "already connected."

The liveness ping backing this (`pingControl`) is bounded-timeout per
`.claude/rules/lessons-liveness-probes.md` — an indefinite-blocking probe
here would reproduce that same "probe that never fires" failure mode for
stale-socket detection specifically.

Provenance: written_at 2026-07-11T19:22:00Z; source_event task-08a
(`01KX8R68J0RNYGMTWQ38G85F94`, commits `29d30691`, `b49b3c09`, `c6e58c81`).

## Follow-up filed: task 05a (writeSession leak)
`correctness-review` (pass_with_changes, step `01KX7MY77RE43EX1CMQ3YZ5PJD`)
flagged that `UploadFile`'s `writeSession` (opened for the `mkdir -p && cat >`
exec) is closed on every error path but never closed on the success path
after `Wait()` succeeds — a minor per-call SSH session/channel leak, cleaned
up only when the whole `*ssh.Client` closes. Filed as follow-up `05a` per the
task-05 pattern this project already tracks in lesson 3 above: a
`pass_with_changes` verdict's non-blocking advisories are follow-up debt for
the parent epic (`01KWMARXTVWKC33EPHZZJ43JT9`), not something to leave
buried in a closed workflow's outcome JSON. Reviewer also noted (as
accepted, non-blocking scope reductions, not filed as follow-ups): the
release-download source path skips sha256 verification (no published
release checksums to verify against) and `RemoteAgntVersion` can't
distinguish "genuinely missing" from "errored this once" (both safely
resolve to the same bootstrap-and-retry path).

## 8. Platform-unsupported subcommand: register a loud stub, never let the command silently not exist

Task 06a (`01KX5E02KJM70TJHM1RJFWE8H7`, commit `d1b0825d`) closed clean
(single-attempt pass on every step, reviewer verdict `pass`, no rewinds).
`cmd/agnt/ssh.go` was `//go:build !windows`, so on Windows `agnt ssh` simply
did not exist as a cobra command — an undocumented absence that reads as a
silent skip under `.claude/rules/daemon-architecture.md` § Silent Failure
Prohibition ("unknown command" gives the user no actionable signal that the
feature is deliberately unsupported vs. a typo/misconfiguration). Fix:
`cmd/agnt/ssh_windows.go` (`//go:build windows`) registers the **same**
command name/parent/`Args` shape as the unix version, but its `RunE` returns
an explicit, actionable error naming the tracking task and the workaround
(WSL). Build tags are mutually exclusive (`!windows` vs `windows`) so exactly
one registration wins per platform — no double-registration, no silent
exclusion.

This is now a **repeated** pattern, not a one-off: `cmd/agnt/attach_windows.go`
was the precedent this task explicitly generalized from. Generalize further:
whenever a `cmd/agnt` subcommand is `//go:build !windows` (or otherwise
platform-gated) with no equivalent implementation on the excluded
platform(s), it needs a matching `<cmd>_windows.go` (or platform-appropriate)
stub file whose `RunE` fails loud with a message naming (a) what's
unsupported, (b) the tracking task/doc, and (c) a workaround if one exists —
never leave a platform-gated command to fall through to cobra's generic
"unknown command". Apply this check whenever adding a new `!windows`-tagged
(or `darwin`/`linux`-only) subcommand under `cmd/agnt/`.

## 9. Freezing a real forking daemon must catch the whole privsep tree, including a post-handshake fork race

Task 09a (`01KX8QVVF2HC6AY3RVFSMYK8C4`, commits `6bcc791c`/`a7b300d8`) built
`SSHDFreezeHarness` (`internal/sshclient/testharness_reconnect.go`) to
simulate a "TCP established, nothing answers" black-hole against a *real*
sshd subprocess — a case `HardCloseHarness`'s hard TCP close cannot
reproduce. First pass SIGSTOPed only the PIDs it could see; `a7b300d8`
(same task, no rewind — caught under the loop's own `-race`/repeat stress,
not by a reviewer) hardened two races:

1. **Signal the whole descendant tree, not the listener PID.** sshd forks a
   monitor process and, after auth completes, a second privilege-dropped
   worker to service the connection — each potentially its own pgid. A
   freeze that only stops the process the harness dialed first misses the
   post-auth worker, which keeps answering "frozen" traffic. Fix: `Freeze()`
   walks `/proc` for every descendant, not a single known PID.
2. **The post-auth fork can land after the client already sees handshake
   success — a settle window is required.** Freeze fired the instant a
   `/proc` scan looked stable; a worker that forked in the gap between
   "scan looked settled" and "SIGSTOP sent" went unfrozen. Fix: require
   **three consecutive agreeing scans** (widened to 500ms/25ms) before
   treating the descendant set as settled, and tolerate `ESRCH` throughout
   signaling (a short-lived child legitimately exiting between enumeration
   and signal is not a harness failure). A companion readiness fix in the
   same commit closed a related gap: `waitForListen` only connect-and-closed
   a probe socket (proves the listen backlog exists, not that a handler is
   scheduled to service it); it now reads the `SSH-2.0-...` banner, which
   only succeeds once a real handler answers end to end.

Generalize: this is the **process-tree sibling** of
`.claude/rules/lessons-liveness-probes.md` — that lesson bounds an
indefinite-blocking *probe*; this one bounds an indefinite-*settling* control
action (freeze/pause) against a subject that forks. Any future test fixture
that pauses or kills a real subprocess to simulate a stuck/frozen peer — not
just sshd — must (a) enumerate the full descendant set at signal time, never
a single captured PID, and (b) require multiple consecutive stable scans
before acting, because "the process tree looks quiescent" and "the process
tree is quiescent" are different instants when forks are involved. A
readiness probe for such a fixture must also assert the same signal the test
body will act on (protocol banner, not bare TCP accept) — see lesson 2's
`href-updated != document-loaded` for the same shape of bug in a different
transport.

**Convention note (flagged for promotion, not yet promoted):** the harness
is a non-`_test.go` file (`testharness_reconnect.go`) using a `*testing.T`
parameter purely as a compile-time fence against production import, mirroring
`internal/daemon/test_helpers.go`'s `NewForTest`. This is now the **third**
in-repo instance of that shape — `internal/testutil/testutil.go` is the
second. Three independent instances of the same convention with no shared
naming/location rule is a candidate for a repo-wide steering note (e.g. a
short `.claude/rules/testing-conventions.md` entry: "a cross-package test
harness is a plain `.go` file, not `_test.go`, fenced by a `*testing.T`
parameter"), but the right home is ambiguous between `daemon-architecture.md`
§ Test startup contract (where the pattern is already documented for the
daemon case) and a new standalone doc — left for operator decision rather
than silently promoted here.

## 10. Remote-exec paths must drive the daemon protocol directly, not bake CLI flags into a remote command line

Task 09c (`01KX8QVVKSRGYEQE0JFJSYJKDV`, commits `59973ea7`/`90a62442`/
`d6a8ab06`/`f5d65a9a`) closed clean — every step (scope-check, go-build,
go-test, correctness-review) passed on its first attempt, reviewer verdict
`pass_with_changes`, no rewinds. The reconnect state machine's reattach step
(`internal/sshclient/reconnect.go`) drives `SESSION-HOST LIST/CREATE`
directly over the forwarded daemon socket — a structured protocol call, not
a string-built remote command line. The review flagged that the *sibling*
initial-connect path, `RemoteAttachCommand` (`internal/sshclient/session.go`),
does the opposite: it bakes `--create-if-missing`/`--cwd` flags into a
remote `agnt attach ...` command line, but `cmd/agnt/attach.go`'s
`attachCmd` defines neither flag — a latent round-trip break (cobra rejects
the unknown flags, or silently ignores them) filed as follow-up
`01KX973H37DZ716WCS27FVAZPT`. Reconnect's own reattach step never has this
problem, because it never asks a remote shell to parse anything.

Generalize: whenever a remote-exec command line and the remote subcommand's
actual flag set can drift independently (they are edited in different
files, sometimes different tasks), prefer a structured protocol call
(existing daemon verbs, RPC, typed IPC) over building a CLI string and
shelling it to the far side. A baked flag the remote binary can't parse is a
silent-or-loud round-trip break that only surfaces at real-network runtime,
never at compile time. This is a sibling of lesson 4's Config-Authority
principle (parsed-but-unwired flag = bug) applied to the *remote* side of a
two-binary protocol instead of the local CLI.

## 11. Deterministic backoff/jitter testing: inject the clock, never sleep for real

Same task 09c. `BackoffConfig.Delay` (`internal/sshclient/reconnect.go`) —
`min(base*2^(n-1), cap) × jitter∈[0.8,1.2)` — is a pure function of an
injectable base delay and jitter source. Its tests assert multiplicative
growth and jitter bounds at microsecond scale with **zero wall-clock
sleeps**: shrink the base delay parameter for the test, don't wait out a
real 1s–30s backoff window. Generalize: any reconnect/retry/backoff
component in this project must expose its base delay and jitter source as
injectable parameters (or a seam), so tests assert the *shape* of the
schedule (growth ratio, cap, jitter range) in microseconds rather than
asserting real elapsed time — real-sleep backoff tests are slow, and
absolute-latency assertions are load-sensitive on shared CI runners (the
same class of flake this project already tracks in
`.claude/rules/testing-parallel-package-flakes.md`, though that lesson is
about test-binary parallelism rather than timing assertions — the shared
principle is "don't make a real clock a load-bearing part of a fast test").
