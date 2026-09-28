---
paths:
  - "internal/sshclient/**"
  - "internal/sessionhost/**"
  - "cmd/agnt/ssh*"
  - "cmd/agnt/attach*"
  - "cmd/agnt/push*"
---

# Lessons: agnt ssh transport

## 1. E2E tests that spawn agnt build it fresh

`findAgntBinary()` (cmd/agnt) builds from current source once per test binary (`sync.Once`), never trusts a pre-built binary. Why: a stale binary's appVersion trips the client/daemon version-mismatch auto-upgrade mid-test and eats the deadline, which reads as a flake. Any new binary-spawn helper must guarantee same-build provenance for both client and daemon.

## 2. In real-Chrome tests, poll the DOM signal you assert on

`location.href` updates when navigation commits, before the document loads. Poll for the target element and its content (`waitContentElementText`), not the URL.

## 3. Carry reviewer follow-ups forward

A `pass_with_changes` verdict's `new_acceptance_criteria` are follow-up debt for the parent epic (`01KWMARXTVWKC33EPHZZJ43JT9`). File them as tasks; do not leave them in closed workflow output.

## 4. Cobra child-command tests execute the root

`sshCmd.SetArgs(...); sshCmd.Execute()` silently runs `rootCmd` with the root's args. Drive flag tests through the root: `rootCmd.SetArgs([]string{"ssh", "myhost", "--flag"}); rootCmd.Execute()` (see `cmd/agnt/ssh_test.go::TestSSHToolFlagRejected`). A parsed-but-ignored flag is a Config Authority bug; if the capability does not exist yet, remove the flag rather than wiring it speculatively.

## 5. Atomic remote install over a bare exec channel

`internal/sshclient/bootstrap_upload.go` `UploadFile`: exec `mkdir -p <dir> && cat > <tmp>`, stream stdin, then a second exec does sha256 verify → `chmod 0755` → same-dir `mv`. No SFTP dependency. Shell-quote every interpolated value, and normalize remote `uname -sm` against the fixed whitelist (linux/darwin × amd64/arm64) before it reaches a command string.

## 6. Verify before rename-to-activate

The integrity check gates the rename. Verifying after activation is too late. `cmd/agnt/upgrade.go` follows the same order; keep them consistent.

## 7. Non-interactive install needs explicit consent

`agnt ssh` never installs on a remote host silently: `--no-bootstrap` skips, scripted runs require `--bootstrap=yes`, only an interactive terminal gets a y/N prompt. Anything that mutates remote or shared state without a human present needs its own consent flag.

## 8. Platform-gated subcommands get a loud stub

A `//go:build !windows` command under `cmd/agnt/` needs a `<cmd>_windows.go` registering the same command whose `RunE` errors with what is unsupported, the tracking task, and the workaround (see `ssh_windows.go`, `attach_windows.go`). Never let it fall through to cobra's "unknown command".

## 9. Freezing a forking daemon: whole tree, settled scans

`SSHDFreezeHarness` (`internal/sshclient/testharness_reconnect.go`) SIGSTOPs every descendant via `/proc`, not one PID, and requires three consecutive agreeing scans before acting (sshd forks a post-auth worker after the client sees handshake success). Tolerate `ESRCH`. Readiness probes must assert the protocol signal (read the `SSH-2.0-` banner), not bare TCP accept. Applies to any fixture that pauses or kills a real forking subprocess.

## 10. Remote exec drives the daemon protocol, not CLI strings

Reconnect reattach (`internal/sshclient/reconnect.go`) calls `SESSION-HOST LIST/CREATE` over the forwarded socket. Do not bake flags into a remote `agnt attach ...` command line: the remote flag set drifts independently and breaks only at runtime. Open follow-up: `RemoteAttachCommand` in `session.go` still bakes `--create-if-missing`/`--cwd` (`01KX973H37DZ716WCS27FVAZPT`).

## 11. Backoff tests inject the clock

`BackoffConfig.Delay` (`reconnect.go`) takes injectable base delay and jitter source. Tests assert growth ratio, cap and jitter range with zero real sleeps. Every retry/backoff component must expose those seams.

## 12. SFTP remote-path traversal guard: all four parts

`PushToInbox` (`internal/sshclient/sftp.go`) is the canonical validator for an untrusted remote path:

1. Reject absolute paths and any `path.Clean` result that is or starts with `..`.
2. Containment via normalized `root+"/"` prefix, never raw `HasPrefix(candidate, root)` (`/proj` vs `/proj-evil`).
3. Resolve symlinks on every intermediate segment (`Lstat`/`ReadLink`), re-check containment per hop, fail closed on depth-cap overflow.
4. Re-run the symlink check immediately before the activating `PosixRename`.

A sub-ms TOCTOU window remains (SFTP has no `openat`); it is accepted only because exploitation needs prior write access to the project root, and the check function's doc comment says so. Minor debt: `validateDestRelPath` should explicitly reject `fileName` of `.`/`..` instead of relying on the rename failing.

## 13. Two remote-write paths, keep both

`UploadFile` (§5, exec channel, no deps) bootstraps a bare host. `PushToInbox` (§12, `pkg/sftp`) pushes arbitrary files. Same temp-write → verify → rename shape; do not merge the transports.

## 14. Control-socket discovery: fail loud, reclaim stale, never hijack live

`internal/sshclient/control.go` (`~/.agnt/ssh/<host>.ctl`): `DialControl` wraps `ErrNoActiveSession` with the host and a "start `agnt ssh <host>`" hint. Both `ListenControl` and `DiscoverActiveHosts` reclaim a socket nothing answers; a socket that answers is never reclaimed. `pingControl` is time-bounded (see `lessons-liveness-probes.md`).
