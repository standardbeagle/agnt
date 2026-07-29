---
sidebar_position: 7
title: Detachable & Remote Sessions
description: Daemon-owned terminal sessions that survive disconnects, plus agnt ssh for driving a remote host's agnt with local ports and file push.
---

# Detachable & Remote Sessions

An `agnt run` session normally lives and dies with your terminal. Detachable
sessions invert that: the **daemon** owns the PTY, so the shell (and the AI agent
running in it) survives the client going away — you close the laptop, the SSH link
drops, the terminal crashes, and the session keeps running.

## Detach and reattach locally

```bash
agnt session hosts          # list detachable sessions
agnt attach my-session      # attach to one
agnt session kill my-session
```

Inside `agnt attach`, press <kbd>Ctrl-\\</kbd> twice (the default
`session.detach-key`) to detach without killing the session. Reattaching replays
the retained scrollback, then resumes live output.

A detachable session is only reaped by an explicit `session kill`, by the shell
exiting on its own, or by daemon shutdown. A dropped attach stream never kills
it — that is the entire point.

## Remote development over SSH

`agnt ssh` opens a local terminal on a **daemon-owned session on a remote host**,
using your existing OpenSSH configuration.

```bash
agnt ssh my-host:/path/to/project
```

The argument is `host[:remote-project-path]` — the first colon separates host
from path, it is **not** a port. Set `Port`, `User`, `IdentityFile`, and
`ProxyJump` in `~/.ssh/config` as usual. Host keys are verified against
`~/.ssh/known_hosts`; there is no insecure bypass flag. Authentication tries
ssh-agent, then configured identity files, then interactive password /
keyboard-interactive.

The session name defaults to your local working-directory basename; override it
with `--attach NAME`.

### Reconnect

The remote daemon owns the PTY, so transport loss does not kill your shell. The
client reconnects with bounded exponential backoff and reattaches to the *same*
session.

| Flag | Effect |
|------|--------|
| `--reconnect-max N` | Cap reconnect attempts (`0` = unlimited) |
| `--create-if-missing` | Allow creating a replacement if the session vanished |
| `--new` | Force a fresh session |

If the session disappeared and you passed neither flag, reconnect **fails loudly**
rather than silently replacing your work. Ctrl-C stops reconnecting.

### Bootstrap

The client checks the remote `agnt` version before attaching. An interactive
terminal prompts before installing or upgrading. For scripted use, pass
`--bootstrap=yes` to consent explicitly, or `--no-bootstrap` to skip provisioning
entirely — automation never installs silently.

### Forwarded daemon socket and proxy ports

On connect, `agnt ssh` prints a local endpoint plus an `AGNT_DAEMON_SOCKET` value,
so local commands talk to the remote daemon:

```bash
export AGNT_DAEMON_SOCKET=…      # POSIX shells
$env:AGNT_DAEMON_SOCKET='\\.\pipe\…'   # PowerShell
agnt monitor
```

Remote reverse-proxy ports are forwarded to loopback automatically — the same
port number is preferred, and a replacement is chosen and reported if it is
occupied. `--status` prints the active mappings. Forwards are rebuilt from
remote state after a reconnect. The overlay port (`19191`) is not forwarded.

### Push files to a live session

```bash
agnt push ./logo.png
agnt push ./a.js ./b.css assets
agnt push --host my-host ./artifact.zip
```

Files land in `<remote-project>/.agnt-inbox/<basename>` by default; a trailing
non-file argument selects a project-relative destination directory. Uploads
reject absolute, traversing, and symlink-escaping destinations, verify the
uploaded bytes, and activate the final file atomically — an interrupted upload
leaves no partial file. Keep `agnt ssh` running for `agnt push` to find the
session, and use `--host` when several are active.

## Platform notes

| Platform | `agnt ssh` | `agnt attach` |
|----------|-----------|---------------|
| Linux / macOS | Unix sockets, SIGWINCH resize | Raw-terminal relay |
| WSL | Same as Linux (the supported path for Windows hosts) | Same as Linux |
| Native Windows | Owner-only `\\.\pipe\agnt-ssh-*` named pipes, polled console resize | ConPTY relay; restores console modes on exit |

On native Windows, `agnt push --host` works, but enumerating sessions without
`--host` is intentionally unsupported (named pipes cannot be safely listed) and
fails with an actionable error rather than guessing.

## Troubleshooting

- Fix host, user, port, identities, and jump hosts in `~/.ssh/config`. `host:path`
  does not accept a port number.
- Update `known_hosts` through normal OpenSSH procedures.
- For CI or other non-interactive use, pass `--bootstrap=yes` or preinstall a
  matching remote version.
- Use `--create-if-missing` only when replacing a lost session is acceptable.
- Forwarding failures are loud but may leave the PTY usable — check stderr and
  verify the remote daemon and the local socket or port.

Remote SSH has no `.agnt.kdl` keys today; use the flags above. SSH config
examples in archived design specs are aspirational, not shipped.
