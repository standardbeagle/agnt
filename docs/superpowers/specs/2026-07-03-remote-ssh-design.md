# Design Spec: agnt over SSH — Remote Dev Experience + Reconnectable Sessions

Status: proposed (foundational — tasks 02-13 implement against this document)
Parent epic: `01KWMARXTVWKC33EPHZZJ43JT9`
This task: `01KWMAX978P4SM8GMDDSSCKEYW`
Style: numbered invariants + source-of-truth tables, matching `.claude/rules/daemon-architecture.md`.

## 0. Scope of this document

Two features, delivered together because the second is a prerequisite building
block for the first:

1. **Detachable sessions (session-host)** — the daemon, not the CLI client,
   owns the PTY child, so a session survives the client disconnecting
   (SSH drop, terminal close, laptop sleep) and can be reattached later with
   full scrollback replay. This works locally too (no SSH required) — it is
   the tmux-style primitive `agnt ssh` builds on.
2. **`agnt ssh <host>[:path]`** — one multiplexed `golang.org/x/crypto/ssh`
   connection giving a local client: a PTY attached to a remote session-host
   session, dynamic port forwards that track remote proxy lifecycle, a
   forwarded remote daemon socket, and an SFTP channel for file push/pull.

Everything below is organized as: **what it is** → **numbered invariants** →
**source-of-truth table** (where relevant) → **provenance** (why this shape,
which file/rule/tier informed it).

---

## 1. Session-host protocol

### 1.1 Model

Today (`internal/daemon/session.go`, `cmd/agnt/pty_common.go`): the **client**
(`agnt run`) spawns the PTY child via `creack/pty` with `Setsid`, captures its
PID as `SessionPGID`, and pushes `SESSION REGISTER` to the daemon. The client
owns the PTY file descriptor; the daemon only holds bookkeeping (`Session`
struct: `Code`, `ProjectPath`, `SessionPGID`, `LastSeen`, `Status`).

Session-host mode **inverts PTY ownership**: the daemon spawns and owns the
PTY child directly. A CLI client (`agnt run --detach-on-hangup` or the SSH
PTY channel) becomes a thin terminal relay that can disconnect and
reconnect without killing anything.

Two session flavors coexist, distinguished by who owns the PTY fd:

| Flavor | PTY owner | Registration verb | Survives client exit? |
|---|---|---|---|
| Classic (existing) | `agnt run` client process | `SESSION REGISTER` (unchanged) | No — cleanup runs on disconnect (grace period only) |
| Session-host (new) | daemon | `SESSION-HOST CREATE` | Yes — by design |

Classic sessions are **not** migrated or deprecated by this spec — they remain
the default for `agnt run` without `--detach-on-hangup`, keeping today's
behavior and its existing test suite untouched. Session-host is strictly
additive.

### 1.2 New verb: `SESSION-HOST`

Follows the existing verb/sub-verb pattern in `internal/protocol/commands.go`
(`VerbStreamEvents`, `VerbAlerts`, etc.) — a new top-level verb because
session-host has its own resource lifecycle (create/list/kill) distinct from
the existing `SESSION` verb (which manages classic-session bookkeeping:
register/heartbeat/list/tasks). Reusing `SESSION` and overloading its
sub-verbs would blur two different ownership models under one name.

```go
VerbSessionHost = "SESSION-HOST"

SubVerbCreate = "CREATE"   // spawn PTY child owned by daemon, return session id
SubVerbAttach = "ATTACH"   // (reuses existing SubVerbAttach) long-lived stream, see 1.3
SubVerbDetach = "DETACH"   // client-initiated clean detach (distinct from connection drop)
SubVerbList   = "LIST"     // (reuses existing pattern) list session-host sessions, project-scoped
SubVerbKill   = "KILL"     // explicit termination — see 1.5 for policy
SubVerbResize = "RESIZE"   // out-of-band window-size renegotiation, see 1.3
```

`SubVerbAttach` already exists (`internal/protocol/commands.go:50`, used by
`CURRENTPAGE ATTACH`-style flows) — reused here rather than adding a
duplicate constant, since sub-verb constants are shared across top-level verbs
by design (compare `SubVerbRestart` used by both `PROC` and `PROXY`).

**`SESSION-HOST CREATE` request/response:**

```go
// SessionHostCreateConfig is the payload for SESSION-HOST CREATE.
type SessionHostCreateConfig struct {
    Name        string   `json:"name,omitempty"`       // user-facing label; auto-generated if empty (mirrors GenerateSessionCode)
    ProjectPath string   `json:"project_path"`          // cwd for the PTY child
    Command     string   `json:"command"`               // e.g. "claude"
    Args        []string `json:"args,omitempty"`
    Cols        int      `json:"cols"`                   // initial terminal size
    Rows        int      `json:"rows"`
    Env         map[string]string `json:"env,omitempty"` // additive to daemon's own env
}

// SessionHostCreateResult is the response.
type SessionHostCreateResult struct {
    SessionID   string `json:"session_id"`    // stable id, used by ATTACH/KILL/LIST
    SessionPGID int    `json:"session_pgid"`  // PTY child pgid (daemon-owned; same containment invariant as classic)
}
```

### 1.3 Attach framing (long-lived stream, modeled on STREAM-EVENTS)

`SESSION-HOST ATTACH {session_id}` opens a long-lived connection exactly like
`hubHandleStreamEvents` (`internal/daemon/hub_stream.go`): server writes an
initial `WriteOK("streaming")`, then a loop of `conn.WriteChunk(json)` frames
until the client disconnects or the session ends, with a keepalive ticker
(reused constant: 30s, `streamKeepaliveInterval`) so idle attaches don't look
dead to intermediate proxies (relevant once this traverses an SSH channel).

Unlike STREAM-EVENTS (server→client only), attach is **bidirectional** — the
client must push stdin, resize events, and an explicit detach. Frame envelope
(JSON, one object per `WriteChunk`/inbound line, discriminated by `type`):

```go
type SessionHostFrame struct {
    Type string          `json:"type"` // "stdout" | "stdin" | "resize" | "detach" | "replay-marker" | "exit" | "keepalive"
    Data json.RawMessage `json:"data,omitempty"`
}

// type="stdout": Data = raw bytes (base64 in JSON, since PTY output is not
//   guaranteed valid UTF-8 mid-escape-sequence).
// type="stdin":  Data = base64 raw bytes, client → daemon.
// type="resize": Data = {"cols":N,"rows":N}, client → daemon.
// type="detach": Data = {} or {"reason":"..."}, client → daemon; daemon
//   acks with type="exit" is NOT sent (session keeps running) — see 1.5.
// type="replay-marker": Data = {"truncated":bool}, daemon → client, sent
//   once at the start of ATTACH before any live stdout frames (see 1.4).
// type="exit": Data = {"code":N}, daemon → client, sent once if/when the
//   PTY child itself exits (not on detach).
```

**Numbered invariants — attach framing:**

1. **One writer per direction.** The daemon-side attach handler owns the
   single goroutine writing `stdout`/`replay-marker`/`exit`/`keepalive`
   frames to the connection (mirrors `hubHandleStreamEvents`'s single
   `for { select }` loop — no second goroutine may call `conn.WriteChunk`
   concurrently, matching the existing STREAM-EVENTS contract).
2. **Multiple attaches, single writer each, fan-out via subscribe.** A
   session-host session may have more than one simultaneous attacher
   (tmux `attach -d`-equivalent is NOT required for v1 — see non-goals —
   but simple read-only co-attach is allowed: e.g. a monitoring client
   alongside the interactive one). Each attach gets its own goroutine and
   its own scrollback replay; live output fans out via the PTY's existing
   output-broadcast primitive (the daemon-side equivalent of
   `ActivityMonitor`'s `OnOutputLine` callback, generalized to N
   subscribers instead of 1).
3. **`stdin` frames from a non-primary attach are rejected.** Exactly one
   attach is "primary" (the first to attach, or explicitly promoted) and
   may send `stdin`/`resize`; others are read-only. Prevents two
   interactive clients fighting over keystrokes. Rejection is a `type:
   "stdin"` echo with an `error` field, not a silent drop (silent-failure
   prohibition, `daemon-architecture.md` § Silent Failure Prohibition).
4. **`detach` is not `kill`.** A `detach` frame or a bare connection drop
   both end that attach's stream (server sends nothing further, closes the
   chunk stream) but never touch the PTY child. This is the entire point
   of session-host — see 1.5 for what *does* end the PTY child.

### 1.4 Scrollback contract

Ring buffer per session-host session, populated by the same daemon-side
output-broadcast tap that feeds live `stdout` frames (single capture point —
no duplicate PTY reads).

**Config:**

```kdl
session {
    scrollback "1MB"   // default; parsed as a byte-size string, same
                        // convention as other size fields in .agnt.kdl
}
```

Default: **1 MiB** per session-host session. Chosen because it holds
roughly 10-20k lines of typical terminal output (assuming ~50-100 bytes/line
including ANSI codes) — enough to reconstruct "what just happened" after a
reconnect without the daemon accumulating unbounded memory across many
long-lived detached sessions. Same order of magnitude as `MaxOutputBuffer:
256KB` per stream already hardcoded for managed processes (`main.go:31-36`)
but sized up 4x because a session-host ring holds *interactive* multiplexed
output (agent + human commands + tool output) rather than one process's
stdout/stderr.

**Numbered invariants — scrollback:**

5. **Ring, not log.** The buffer is a fixed-capacity circular byte ring
   (reuse `internal/process/ringbuf.go`'s `RingBuffer` primitive — already
   battle-tested for the identical "bounded capture, oldest evicted"
   requirement in `ManagedProcess`). No disk persistence; daemon restart
   loses scrollback (consistent with "Bus in-flight events" and "Blob
   store" being best-effort/transient in the incident pipeline source-of-truth
   table).
6. **Replay-on-attach is always full-ring-then-live**, never partial. Every
   `SESSION-HOST ATTACH` call — first attach or Nth reconnect — receives:
   one `replay-marker` frame, then the entire current ring content as one or
   more `stdout` frames (chunked to avoid a single oversized `WriteChunk`),
   then live frames going forward. There is no "attach from cursor X"
   mode in v1 (see non-goals) — simpler contract, and 1MB replay is
   sub-100ms over a loopback or forwarded-socket connection.
7. **Truncation flag is explicit, not inferred.** `replay-marker.data.truncated`
   is `true` whenever the ring has ever wrapped (i.e., total bytes ever
   written > ring capacity) — even if the *current* attach is the first one.
   The client must render a "scrollback truncated" notice rather than silently
   showing a ring's worth of output as if it were the complete session
   history. This mirrors the `truncated` flag pattern already used by
   RingBuffer consumers (Common Gotcha #3 in `AGENTS.md`) and by
   `IncidentQueryResult.Truncated`.
8. **Resize does not replay.** A `resize` frame changes the PTY window
   size (`ioctl TIOCSWINSZ` equivalent via `creack/pty`'s `Setsize`) for
   all current attaches; it does not trigger a partial re-render. Client
   is responsible for whatever redraw its terminal emulator does locally
   (matches existing SIGWINCH handling for classic sessions).

### Provenance (§1)

- Attach-as-long-lived-stream, keepalive interval, single-writer-goroutine
  invariant: directly copied from `internal/daemon/hub_stream.go`
  (`hubHandleStreamEvents`), the only existing long-lived-stream precedent
  in this codebase. Chosen over inventing a new streaming primitive because
  the daemon-architecture rule already treats STREAM-EVENTS as the
  reference implementation for this shape (`daemon-architecture.md`'s
  "Reference impl" language for `findDuplicates`/`ScanWindows` sets the
  precedent of pointing new code at an existing correct analog rather than
  re-deriving).
- Ring-buffer reuse: `internal/process/ringbuf.go` already solves "bounded
  capture with a truncation flag" — reusing it satisfies
  `karpathy-principles.md` #1 (verification loop: this primitive is already
  tested) and the minimal-code-ladder discipline (reuse before extend before
  add).
- 1MB default size: derived from the existing `MaxOutputBuffer: 256KB`
  hardcoded daemon default (`main.go:31-36`), scaled up because scrollback
  serves a human/replay use case (denser, more varied content) vs. a single
  process stream.

---

## 2. Ownership inversion — daemon owns the PTY

### 2.1 What changes vs. classic sessions

| Concern | Classic (`agnt run`) | Session-host |
|---|---|---|
| PTY spawn | Client process (`cmd/agnt/pty_common.go`, `creack/pty` + `Setsid`) | Daemon process |
| `SessionPGID` capture | Client captures its own PTY child's PID, sends via `SESSION REGISTER` wire field | Daemon captures its own spawned child's PID directly — no wire hop needed |
| `SessionPGID` containment invariant | Unchanged: PTY child is session leader; all descendants (incl. backgrounded `npm run dev &`) inherit its pgid | **Unchanged** — same invariant, same primitive (`platform.KillSessionPGID`), just invoked without a network round-trip since the daemon already holds the PID in-process |
| `OverlayAlertSink` (writes synthetic input to PTY stdin) | Writes to the client's PTY fd over the existing Unix-domain overlay socket (`OverlayPath` in `SessionRegisterConfig`) | Writes **directly** to the daemon-owned PTY fd — no socket hop; `OverlayPath` becomes empty/unused for session-host sessions since the daemon *is* the process holding the fd |
| `CleanupSessionResources` trigger | Client disconnects from daemon (socket drop) → deferred cleanup after grace period | **Two independent lifecycles now**: (a) attach-stream disconnect (no cleanup — see 1.5.4), (b) explicit `SESSION-HOST KILL` or PTY child exiting on its own (both trigger cleanup) |
| Who calls `killSessionPGID` | Daemon, using PID reported over the wire | Daemon, using PID it captured itself — same function (`internal/daemon/daemon_session_cleanup.go`), same containment guarantee, now invoked from the session-host teardown path rather than `doCleanup`'s classic-session path |

### 2.2 Numbered invariants — ownership inversion

9. **`SessionPGID` containment is preserved exactly**, not reinvented. A
   session-host `Session` (a new struct, `SessionHostSession`, distinct from
   the classic `Session` in `internal/daemon/session.go` — see 2.3 for why
   separate) still carries a `SessionPGID` field and the daemon still calls
   `platform.KillSessionPGID` on teardown. The only structural change is
   *who observed the PID*: the daemon reads it from its own `exec.Cmd`/
   `pty.Start()` call instead of trusting a value reported over the wire by
   a remote client. This is strictly safer (no possibility of a malicious
   or buggy client reporting a wrong PID) and requires no change to
   `internal/platform/sessionpgid_unix.go`.
10. **`OverlayAlertSink` writes in-process for session-host sessions.**
    Today it writes synthetic stdin bytes across the overlay Unix socket
    (`cmd/agnt/pty_common.go` sets this up per classic session). For
    session-host sessions there is no overlay socket to cross — the daemon
    both owns the PTY fd and hosts the alert scanner, so the sink becomes a
    direct `io.Writer` call into the PTY child's stdin. This eliminates a
    hop, not a capability: alert-injected messages still arrive in the same
    place (the agent's terminal stdin), just without serializing across a
    socket first.
11. **Explicit kill vs. auto-cleanup — decided: explicit-only for v1.**
    Session-host sessions do **not** auto-terminate when the last attach
    disconnects (that is the entire feature — surviving disconnect). They
    terminate only when:
    (a) an operator/agent runs `agnt session kill <name>` (new CLI verb
        wrapping `SESSION-HOST KILL`), or
    (b) the PTY child process exits on its own (agent process exited,
        `exit` typed at the shell), or
    (c) daemon shutdown (session-host sessions are swept the same way
        classic sessions' pgids are swept — via the existing startup/shutdown
        orphan-pgid scan, `startupOrphanPGIDScan` — because a daemon
        restart has no way to re-attach to a PTY fd it no longer holds a
        handle to; the child becomes exactly the same "orphaned pgid"
        shape the scan already exists to catch).
    No idle-timeout auto-kill in v1 (see non-goals) — an unattended detached
    session consuming a dev-server port indefinitely is an accepted
    trade-off, explicitly surfaced via `agnt session list` /
    `SESSION-HOST LIST` output showing `last_attached` age, not silently
    hidden.
12. **`SESSION-HOST LIST` is project-scoped like everything else.** Routes
    through the same `resolveProjectScope` chokepoint
    (`daemon-architecture.md` § Tool session-scoping) as `SESSION LIST` —
    default scoped to caller's session/project, `global` override available.
    Classified here as **Gated**, joining the existing table in
    `daemon-architecture.md`.
13. **A session-host session's project-resource ownership mirrors a classic
    session's.** It is still "the" session for `CleanupSessionResources`'s
    purposes when it is the sole session for a project — i.e., when a
    session-host session backing an SSH-attached remote dev loop is
    killed and it was the last session for that project, scripts/proxies for
    that project are torn down exactly as today (`doCleanup`'s
    `hasOtherSessions` check does not need to distinguish session flavor —
    both flavors populate the same `sessionRegistry` for this purpose, see
    2.3).

### 2.3 Struct-level decision: shared registry, distinguished by a field

Rather than a parallel `SessionHostRegistry`, `SessionHostSession` embeds the
existing `Session` struct's project/status/pgid fields and registers into the
**same** `SessionRegistry` (`internal/daemon/session.go`), tagged with a new
`Kind SessionKind` field (`"classic"` | `"session-host"`). This keeps
`hasOtherSessions`, `FindByDirectory`, `List`/`ListActive`, and every existing
project-scoping consumer working unmodified — they already only care about
`ProjectPath` and `Status`. Only the cleanup dispatch (2.1's "who calls
`killSessionPGID`" row) and the attach-handling code need to branch on `Kind`.

**Provenance**: rejected a fully separate registry because
`daemon-architecture.md`'s Tool session-scoping section is explicit that
project scoping is a *structural* property with exactly one resolution point
(`resolveProjectScope`) — a second registry would need its own parallel
scoping wiring, duplicating that chokepoint rather than reusing it. Extending
the existing struct is the minimal-code-ladder "extend" rung, not "add."

---

## 3. `agnt ssh` channel plan

### 3.1 Connection lifecycle

```
agnt ssh user@host[:path]
  1. Resolve ~/.ssh/config (ProxyJump, IdentityFile, HostName, Port, User)      [3.4]
  2. Dial TCP → SSH handshake (golang.org/x/crypto/ssh)
  3. Host key verify against ~/.ssh/known_hosts                                 [5.1]
  4. Auth: ssh-agent first, then IdentityFile(s), matching OpenSSH client order
  5. Bootstrap check: does remote `agnt` exist + version-match?                 [3.5]
     - No / mismatched → SFTP-push matching-arch binary to a cache dir
  6. Open multiplexed channels over the ONE ssh.Client:                        [3.2]
     a. "session" channel → remote `agnt session-host attach` (or create+attach)
     b. control channel (a second "session" channel, exec'd to a lightweight
        `agnt ssh-control` subcommand) carrying the forwarded daemon socket
        and STREAM-EVENTS-driven port-forward directives
     c. "direct-tcpip" channels, opened/closed dynamically per (b)'s events
     d. "subsystem: sftp" channel for file push/pull
  7. Steady state: PTY relay + dynamic forwards + keepalive                     [3.3]
  8. On drop: reconnect state machine                                          [3.6]
```

### 3.2 Channel multiplexing — numbered invariants

14. **One `ssh.Client`, N logical channels.** `golang.org/x/crypto/ssh`
    natively multiplexes independent channels over one TCP connection (this
    is the whole point of the SSH connection protocol) — `agnt ssh` opens
    exactly one `ssh.Client` per invocation and never redials for a new
    channel type. Reconnect (3.6) tears down and rebuilds the entire
    `ssh.Client` and all its channels together, not per-channel.
15. **The control channel is the single source of forward directives.**
    It runs a small remote helper (`agnt ssh-control`, exec'd once per SSH
    session) that itself opens a `SESSION-HOST`-adjacent connection to the
    *remote* daemon's `STREAM-EVENTS` (filtered to proxy start/stop events
    for the attached project) and relays those as newline-delimited JSON
    directives down the control channel to the local `agnt ssh` process,
    which opens/closes local listeners and `direct-tcpip` channels in
    response. This reuses STREAM-EVENTS rather than inventing a second
    remote-daemon-side event feed (reference-impl provenance, same as §1).
16. **PTY channel is a dumb relay**, not a fresh client. It opens a
    "session" channel, requests a pty (`pty-req`), then runs
    `agnt session-host attach <name>` (or `create` if `<name>` doesn't
    exist) as the remote command — all session-host protocol logic (1.3's
    framing) happens *inside* that remote process talking to its local
    daemon over the existing Unix socket; the SSH layer just carries bytes.
    This keeps the SSH transport layer decoupled from the session-host
    wire format — `agnt ssh` never needs to speak `SESSION-HOST` frames
    directly, it speaks raw PTY bytes to a remote process that does.
17. **Forwarded daemon socket is read/write proxied, not re-implemented.**
    `agnt ssh --forward-daemon` (default on) opens a `direct-tcpip`-style
    channel(s) on demand, one per local connection to a local Unix socket
    the command creates (e.g. `~/.agnt/ssh/<host>.sock`), each forwarded
    1:1 to the remote daemon's Unix socket path. Local `agnt monitor`/
    `doctor`/MCP tools pointed at that local socket work completely
    unchanged — they don't know they're talking to a remote daemon.
18. **SFTP channel is opened lazily**, only when `agnt push`/`pull`/the
    drop-folder watcher actually has bytes to move — not held open for the
    session's lifetime. Reduces channel count on a lightly-used connection
    and matches the existing "no idle cost" principle applied elsewhere in
    the codebase (e.g. proxy auto-port-discovery only fires on demand).

### 3.3 Keepalive

19. **SSH-level keepalive**: `ssh.Client` sends a `keepalive@openssh.com`
    global request every **15s** (below most NAT/firewall idle-connection
    timeouts, which commonly sit at 60-300s) and treats 3 consecutive
    unanswered keepalives (45s) as a dead connection, triggering the
    reconnect state machine (3.6). This is independent of and layered
    below the session-host attach-stream's own 30s `streamKeepaliveInterval`
    (§1.3) — the SSH-level one detects transport death, the attach-level
    one is application-layer liveness for the PTY stream specifically.

### 3.4 `~/.ssh/config` respect

20. **Config resolution happens once, before dialing**, using
    `golang.org/x/crypto/ssh` + a config parser (e.g. reading
    `~/.ssh/config` via a small parser, not shelling out to `ssh`) to
    resolve `HostName`, `Port`, `User`, `IdentityFile`, and `ProxyJump` for
    the given host alias, exactly as the OpenSSH client would. `ProxyJump`
    is honored by recursively dialing through the jump host's own SSH
    connection and using its resulting `net.Conn` as the transport for the
    target dial (standard `golang.org/x/crypto/ssh` pattern — a jump host
    connection carries a `direct-tcpip` channel to the final target's
    `host:22`).

### 3.5 Bootstrap (remote binary provisioning)

21. **Version + arch check before any upload.** `agnt ssh` runs a cheap
    remote probe (`agnt --version` over a throwaway "exec" channel) first;
    only on missing-binary or version-mismatch does it fall through to
    SFTP-uploading a matching-`GOOS`/`GOARCH` binary (embedding release
    binaries or fetching from the same distribution channel `agnt`'s
    self-upgrade already uses, `cmd/agnt/upgrade.go`). This reuses the
    existing "binary copies instead of self-exec" precedent
    (`AGENTS.md` Core Architecture Decision #1) — the uploaded binary is a
    plain copy, never a self-exec of the local binary across architectures.
22. **Upload integrity is checksum-verified**, not trust-on-write (§5.4).

### 3.6 Reconnect state machine + backoff

```
        ┌──────────┐  connect ok   ┌──────────┐
   ────►│CONNECTING│──────────────►│ CONNECTED│
        └────┬─────┘               └────┬─────┘
             │ fail                      │ transport dead
             ▼                           ▼
        ┌──────────┐   attempts     ┌──────────┐
        │ BACKOFF  │◄───exhausted───│RECONNECT-│
        │ (wait)   │                │ING       │
        └────┬─────┘                └────┬─────┘
             │ timer fires               │ connect ok → CONNECTED
             └───────────────────────────┘  (re-attach same session-host name)
```

23. **Backoff is exponential with jitter, capped**: 1s, 2s, 4s, 8s, 16s,
    30s (cap), each ± up to 20% jitter to avoid thundering-herd reconnects
    against a recovering host. No maximum attempt count in interactive mode
    (the user is watching a terminal — indefinite retry with visible status
    is correct); a `--max-reconnect-attempts N` flag exists for scripted/CI
    use where indefinite hang is wrong.
24. **Reconnect re-attaches, never re-creates**, the same session-host
    session by name — this is the entire value proposition. If the named
    session no longer exists remotely (daemon restarted and the orphan-pgid
    scan reaped it, or someone ran `agnt session kill`), `agnt ssh` reports
    that explicitly (not a silent fresh session) and requires
    `--create-if-missing` or an explicit `agnt ssh host --new` to proceed,
    per the silent-failure prohibition.
25. **Port forwards and the daemon-socket forward are torn down and
    rebuilt on every reconnect**, not assumed stale-but-valid — local
    listeners are closed, then re-derived from a fresh `STREAM-EVENTS`
    snapshot query (current proxy list) rather than replaying a
    potentially-stale in-memory forward table. Same "cache is never
    authority" principle as `daemon-architecture.md`'s Data Ownership
    table applied to the SSH client's own forward-tracking state.

### Provenance (§3)

- Multiplexed-channel design and `ProxyJump` handling: standard
  `golang.org/x/crypto/ssh` capabilities, cross-checked against OpenSSH's own
  `ssh_config(5)` semantics for `ProxyJump`/`IdentityFile` resolution order
  (two independent sources: the Go SSH package's channel model docs and
  OpenSSH's config semantics — satisfies the load-bearing-claims rule for
  an architectural assertion).
- Keepalive intervals (15s SSH-level / 45s dead-declare): chosen conservatively
  below common NAT/LB idle timeouts (60s is a frequently-cited default for
  cloud LB idle timeouts); no single authoritative number exists industry-wide,
  so this is a tunable default, not a hard architectural claim, and is called
  out as such rather than asserted as definitively correct.
- Binary bootstrap reusing the self-upgrade distribution path: `AGENTS.md`
  Core Architecture Decision #1 (binary copies precedent) + `cmd/agnt/upgrade.go`
  existing implementation.

---

## 4. Port mapping policy

26. **Same-port preferred, always attempted first.** When the remote
    daemon's `STREAM-EVENTS` reports a proxy started on remote port `N`,
    `agnt ssh`'s control-channel handler attempts to bind local port `N`
    first. Rationale: proxies serve HTML with injected JS that posts to
    same-origin `/__devtool_metrics` and other absolute-feeling paths;
    keeping the local port identical to the remote one means any URL a
    developer copies from the remote-side proxy log (or that appears in
    browser devtools) is directly usable locally without translation.
27. **On local-port collision, fallback + mandatory visible notice.** If
    local port `N` is already bound (another local dev server, or another
    `agnt ssh` session), `agnt ssh` picks the nearest free port using the
    same hash-based auto-discovery algorithm the proxy subsystem already
    uses for its own port selection (`ProxyServer`, `10000-60000` range,
    per `AGENTS.md` Reverse Proxy Architecture), and prints a mapped-URL
    notice to the local `agnt ssh` terminal output:
    `"remote :5173 → local :5174 (port 5173 in use locally)"`.
    This is a hard requirement, not optional logging — silent
    same-origin-breaking remaps are exactly the kind of thing the
    silent-failure prohibition exists to prevent, since injected JS
    referencing the *old* absolute port would silently misbehave.
28. **Which side prints what:**
    - **Remote daemon** (via the control channel, ultimately surfacing in
      the remote session's own terminal/log if attached) prints "proxy
      started on :N" — exactly what it already prints for local dev, no
      SSH-awareness needed on the remote side.
    - **Local `agnt ssh` client** prints the forward line (same-port
      confirmation or the collision/remap notice from #27) — the local
      client is the only party that knows about local port availability,
      so it owns that half of the message.
29. **Forward teardown is event-driven, not polled.** A `proxy stopped`
    STREAM-EVENTS entry closes the corresponding local listener and any
    open `direct-tcpip` channels for that port immediately — no polling
    loop, consistent with STREAM-EVENTS being push-based by design.

### Provenance (§4)

- Hash-based port range reuse (`10000-60000`): `AGENTS.md` Reverse Proxy
  Architecture section, "Default port hash-based (stable, 10000-60000)" —
  reusing the exact existing range keeps forwarded ports visually consistent
  with local-only proxy ports rather than introducing a second numbering
  space.
- Silent-remap-forbidden: `daemon-architecture.md` § Silent Failure
  Prohibition, directly applicable.

---

## 5. Security

Written in normal prose register per task instructions (security sections are
one of the CLAUDE.md-listed exceptions to Wenyan-mode internal reasoning, and
more importantly this is user/operator-facing documentation where precision
matters more than compression).

### 5.1 Host key verification

`agnt ssh` verifies the remote host key against `~/.ssh/known_hosts` using
the standard `ssh.FixedHostKey` / `knownhosts.New` support in
`golang.org/x/crypto/ssh/knownhosts`. There is no blind-accept mode and no
`--insecure` flag that skips verification silently. On first connection to
an unknown host, `agnt ssh` prompts interactively (fingerprint shown, exactly
as OpenSSH's own TOFU prompt) and, on acceptance, appends the key to
`~/.ssh/known_hosts` — it does not maintain a separate trust store, so the
same file remains the single source of truth an operator already manages
with the standard `ssh` client. A host key mismatch (key changed since last
connection) is always a hard failure with a visible warning, never a silent
fallback to an unverified connection — this is the single most
security-critical invariant in this document, since it is the only thing
standing between "connecting to your server" and "connecting to a
man-in-the-middle."

### 5.2 Forwarded daemon socket exposure

The forwarded daemon socket (§3.2 invariant 17) exposes the **remote**
daemon's full control surface — process start/stop, proxy management,
incident inbox — to whatever can reach the **local** Unix socket it's
forwarded to. Because Unix sockets are filesystem-permission-gated by
default (created `0600`, owner-only, under `~/.agnt/ssh/`), the exposure is
bounded to the local user account running `agnt ssh` — no additional network
listener is opened locally or remotely for this purpose. This deliberately
mirrors the existing local daemon socket's own trust model (same
permissions, same "your user, your daemon" assumption) rather than
introducing a new, weaker one. The forward is torn down when `agnt ssh`
exits or reconnects (invariant 25), so there is no dangling forwarded socket
outliving the client process.

### 5.3 File-push path-traversal guards

`agnt push` and the drop-folder sync write into `<project>/.agnt-inbox/` on
the remote host. All destination paths are resolved via `filepath.Clean` and
then explicitly checked with `filepath.Rel` (or equivalent) to confirm the
resolved path stays within the `.agnt-inbox` root — any `../` component in a
requested destination, or a symlink inside `.agnt-inbox` pointing outside it,
is rejected before any write occurs, not sanitized-and-continued. This is the
same discipline the codebase already applies to any user-controlled path
input at a trust boundary (SSH-received filenames are exactly such a
boundary — the remote daemon must not trust a filename it received over the
wire any more than it would trust one from an untrusted HTTP request body).

### 5.4 Binary upload integrity

The bootstrap binary upload (§3.5) is checksum-verified end to end: `agnt
ssh` computes a SHA-256 of the local binary before upload, sends it alongside
the SFTP transfer, and the remote side (a small pre-existing `agnt`-less
verification, or the newly-uploaded binary's own self-check on first
invocation) confirms the checksum before the binary is marked executable and
invoked. A checksum mismatch is a hard failure with the partial file removed,
never a "close enough, run it anyway" fallback — an interrupted or corrupted
SFTP transfer producing a truncated binary must not silently execute
undefined behavior on the remote host.

---

## 6. Config surface

`.agnt.kdl` gets one new top-level block (KDL for app config, per project
convention — this is settings/preferences, not content data):

```kdl
session {
    scrollback "1MB"           // §1.4 invariant 5-7
}

ssh {
    host "myserver" {
        forward-daemon true     // default true; §3.2 invariant 17
        auto-reconnect true     // default true; §3.6
        max-reconnect-attempts 0 // 0 = unlimited (interactive default); §3.6 invariant 23
        bootstrap "auto"         // "auto" | "skip" | "force" — §3.5
    }
}
```

Per-host blocks (keyed by the same host alias used in `~/.ssh/config`) let a
project pin different behavior per remote target (e.g. a CI runner host with
`max-reconnect-attempts 3`, vs. an interactive dev box with unlimited
retries) without a separate config file. Absence of a `ssh { host "x" {} }`
block for a given host falls back to the documented defaults shown above —
config fields parsed but not acted on are bugs per `config-contracts.md`, so
every field listed here must have a resolved consumer before this spec's
child tasks are considered complete.

**CLI flags** (override `.agnt.kdl` per-invocation, standard flag-overrides-config
precedent already used elsewhere in the codebase):

```
agnt ssh <host>[:path]
  --new                      # force a fresh session-host session, don't attach existing
  --create-if-missing         # attach if exists, else create (opt-in; default is hard-fail per invariant 24)
  --no-forward-daemon         # disable daemon socket forward for this invocation
  --max-reconnect-attempts N  # override config
  --scrollback SIZE           # override config for this session only
```

---

## 7. Non-goals (explicit)

- **No multi-hop fan-out.** `agnt ssh` connects to exactly one remote host
  per invocation. Chaining through a `ProxyJump` to *reach* that host is
  supported (§3.4); treating the remote host as a further hop to additional
  hosts, or attaching to multiple remote daemons simultaneously from one
  `agnt ssh` invocation, is out of scope. Run multiple `agnt ssh` invocations
  for multiple hosts.
- **No Windows sshd server-side support in v1.** `agnt ssh <host>` (the
  client, connecting *out*) works from a Windows client to any POSIX or
  Windows remote running `agnt`'s own daemon+session-host. But being the
  **server** side of an inbound SSH connection on Windows (i.e., someone
  SSHing *into* a Windows box specifically to reach `agnt`) is out of scope
  — that's Windows OpenSSH Server's job, not `agnt`'s, and session-host's
  PTY-ownership model on Windows (ConPTY + Job Objects) has not been
  validated against an SSH-server-mediated PTY request in this design pass.
- **No idle-timeout auto-kill for session-host sessions in v1** (§2.2
  invariant 11) — explicit `agnt session kill` or process exit only.
- **No attach-from-cursor / partial-scrollback-replay mode** (§1.4 invariant
  6) — every attach replays the full ring.
- **No cross-session dedup or shared scrollback between two independently
  created session-host sessions** — each is an independent PTY child with
  its own ring, exactly like two independent tmux sessions.
- **No re-implementation of `ssh_config(5)` in full** — only the directives
  needed for dial+auth (`HostName`, `Port`, `User`, `IdentityFile`,
  `ProxyJump`, `IdentitiesOnly`) are honored; exotic directives (e.g.
  `Match` blocks with complex conditionals, `Ciphers`/`MACs` overrides) fall
  back to `golang.org/x/crypto/ssh` package defaults.
- **No automatic multi-attach write-arbitration UI** (e.g. tmux's
  `attach -r`/read-only vs. read-write negotiation prompts) — v1's
  first-attach-is-primary rule (§1.3 invariant 3) is static for the
  session's lifetime; promoting a different attach to primary requires
  detaching the current primary first.

---

## 8. Cross-references

| Topic | This spec section | Existing doc |
|---|---|---|
| Session pgid containment | §2 | `daemon-architecture.md` § Session Containment |
| STREAM-EVENTS reference impl | §1.3, §3.2 | `daemon-architecture.md` § StreamEvents Hub; `internal/daemon/hub_stream.go` |
| Tool session-scoping gate | §2.2 invariant 12 | `daemon-architecture.md` § Tool session-scoping |
| RingBuffer / truncation flag | §1.4 | `internal/process/ringbuf.go`; `AGENTS.md` Common Gotcha #3 |
| Binary-copies precedent | §3.5 | `AGENTS.md` Core Architecture Decision #1 |
| Proxy port hash range | §4 | `AGENTS.md` Reverse Proxy Architecture |
| Silent Failure Prohibition | §1.3 inv.3, §3.6 inv.24, §4 inv.27, §5.3 | `daemon-architecture.md` § Silent Failure Prohibition |
| Config field must-honor rule | §6 | `.claude/rules/config-contracts.md` |
| WSL shell/path awareness (relevant to remote-side bootstrap if remote is WSL) | §3.5 | `.claude/rules/wsl-audit.md` |
