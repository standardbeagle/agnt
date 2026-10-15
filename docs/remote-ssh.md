# Remote SSH

`agnt ssh` opens a local terminal on a daemon-owned session on a remote host. It uses OpenSSH configuration and `known_hosts`, keeps the remote session alive across transport loss, forwards remote proxy ports, and exposes the remote daemon through a local Unix-domain socket (AF_UNIX on every platform, native Windows included).

## Quick start

```bash
agnt ssh my-host:/path/to/project
```

The argument is `host[:remote-project-path]`; the first colon separates host from path, not a port. Configure `Port`, `User`, `IdentityFile`, and `ProxyJump` in `~/.ssh/config`. The default session name is the local working-directory basename; override it with `--attach NAME`. Native Windows and WSL are distinct: Windows puts the daemon forward at `%TEMP%\agnt\ssh-<hash>.sock`, serves the `agnt push` control endpoint on an owner-only `\\.\pipe\agnt-ssh-control-*` named pipe, and polls console resize; WSL retains the Linux Unix-socket and SIGWINCH behavior.

## Bootstrap and authentication

The client checks the remote `agnt` version before attaching. An interactive terminal prompts before an install or upgrade. In non-interactive use, pass `--bootstrap=yes`; `--no-bootstrap` skips provisioning. The install copies this client's own binary (SHA-256 verified on the remote) when the remote has the same OS and architecture; otherwise it runs `go install` of this client's exact version when the remote has Go (module integrity comes from the Go checksum database), and otherwise downloads that version's release asset. A go-installed or downloaded binary must report the client's version before it replaces `~/.local/bin/agnt`. Releases publish no checksums, so a downloaded asset is trusted on GitHub's TLS alone, as with `install.sh`; a client built from an unreleased version cannot use either remote-build path. Host keys are verified against `~/.ssh/known_hosts`. Authentication tries ssh-agent keys and the configured identity files (when `~/.ssh/config` names no `IdentityFile`, the existing ones of `~/.ssh/id_ed25519`, `id_ecdsa` and `id_rsa`), then interactive keyboard-interactive, then password authentication.

## Sessions and reconnect

Initial connection creates a missing named session and attaches to it. The remote daemon owns the PTY, so SSH loss does not kill the shell. The client reconnects with bounded exponential backoff and reattaches to the same session. If that session disappeared, reconnect fails loudly by default; `--create-if-missing` or `--new` permits a replacement. `--reconnect-max N` limits attempts (`0` is unlimited). Ctrl-C stops reconnecting.

Manual lifecycle commands use the forwarded daemon socket:

```bash
agnt session hosts
agnt attach my-session
agnt session kill my-session
```

`session hosts` lists detachable sessions. In `agnt attach`, press `Ctrl-\` then `Ctrl-\` again (the default `session.detach-key`) to detach without killing the session; a later attach replays retained scrollback and resumes live output. `session kill` explicitly terminates it. These manual actions are distinct from `agnt ssh` automatic transport reconnect, which reattaches after network loss.

Native Windows `agnt attach` relays the remote daemon-owned PTY through the local console, restores console modes on every exit, and polls for resize changes. Redirected stdout is supported as a byte stream; redirected stdin is rejected before the remote attach begins because detach chords and interactive console input cannot be preserved safely. WSL uses the Linux raw-terminal and SIGWINCH path. A daemon running on native Windows cannot host a session itself (the PTY library has no Windows backend), so attach there always targets a remote daemon.

## Daemon and proxy forwarding

On connection, `agnt ssh` prints a local endpoint and an `AGNT_DAEMON_SOCKET` value for commands such as `agnt monitor` (PowerShell: `$env:AGNT_DAEMON_SOCKET='C:\Users\…\AppData\Local\Temp\agnt\ssh-….sock'`; POSIX shells: `export AGNT_DAEMON_SOCKET=…`). Remote reverse-proxy ports are forwarded automatically to loopback. The same local port is preferred; if occupied, a replacement is selected and reported. `--status` prints active mappings. Forwards are rebuilt from remote state after reconnect. The overlay is a per-session Unix-domain socket on the remote host and is not forwarded.

On native Windows only the control endpoint is a named pipe. It uses an owner-only security descriptor and disappears when the owning process exits; a live same-host pipe collision fails loudly. Pipe names include a hash of the normalized SSH host identity so sanitization cannot alias distinct hosts. `agnt push` works with `--host`; enumeration without `--host` is intentionally unsupported on native Windows because named pipes cannot be safely listed and therefore fails with an actionable error. `go-winio` is used because Go's standard library and the existing socket abstraction do not provide named-pipe listeners with explicit ACLs.

## Push files

```bash
agnt push ./logo.png
agnt push ./a.js ./b.css assets
agnt push --host my-host ./artifact.zip
```

Files default to `<remote-project>/.agnt-inbox/<basename>`. A trailing non-file argument selects a project-relative destination directory. `--host` disambiguates active sessions. SFTP uploads reject absolute, traversing, or symlink-escaping destinations, verify uploaded bytes, and atomically activate the final file; interrupted uploads leave no partial final file.

## Troubleshooting

- Fix host, user, port, identities, and jump hosts in `~/.ssh/config`; `host:path` does not accept a port number.
- Update `known_hosts` through normal OpenSSH procedures; there is no insecure bypass flag.
- For automation that needs provisioning, pass `--bootstrap=yes` or preinstall the matching remote version.
- If reconnect reports a missing session, use `--create-if-missing` only when replacement is acceptable.
- Keep `agnt ssh` running for `agnt push`; use `--host` when several sessions exist.
- Forwarding failures are loud but may leave the PTY usable; inspect stderr and verify the remote daemon and local socket or port.

Remote SSH currently has no `.agnt.kdl` keys. Use the implemented flags above; SSH KDL examples in archived design specifications are aspirational, not shipped configuration.
