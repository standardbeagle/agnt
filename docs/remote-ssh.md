# Remote SSH

`agnt ssh` opens a local terminal on a daemon-owned session on a remote host. It uses OpenSSH configuration and `known_hosts`, keeps the remote session alive across transport loss, forwards remote proxy ports, and exposes the remote daemon through a local Unix socket.

## Quick start

```bash
agnt ssh my-host:/path/to/project
```

The argument is `host[:remote-project-path]`; the first colon separates host from path, not a port. Configure `Port`, `User`, `IdentityFile`, and `ProxyJump` in `~/.ssh/config`. The default session name is the local working-directory basename; override it with `--attach NAME`. Native Windows is unsupported; use WSL. Linux and macOS are supported.

## Bootstrap and authentication

The client checks the remote `agnt` version before attaching. An interactive terminal prompts before an install or upgrade. In non-interactive use, pass `--bootstrap=yes`; `--no-bootstrap` skips provisioning. Host keys are verified against `~/.ssh/known_hosts`. Authentication tries ssh-agent, configured identity files, then interactive password or keyboard-interactive authentication.

## Sessions and reconnect

Initial connection creates a missing named session and attaches to it. The remote daemon owns the PTY, so SSH loss does not kill the shell. The client reconnects with bounded exponential backoff and reattaches to the same session. If that session disappeared, reconnect fails loudly by default; `--create-if-missing` or `--new` permits a replacement. `--reconnect-max N` limits attempts (`0` is unlimited). Ctrl-C stops reconnecting.

## Daemon and proxy forwarding

On connection, `agnt ssh` prints a local socket and an `AGNT_DAEMON_SOCKET` export for commands such as `agnt monitor`. Remote reverse-proxy ports are forwarded automatically to loopback. The same local port is preferred; if occupied, a replacement is selected and reported. `--status` prints active mappings. Forwards are rebuilt from remote state after reconnect. Overlay port `19191` is not forwarded.

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
