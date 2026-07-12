package sshclient

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/standardbeagle/go-cli-server/socket"
	"golang.org/x/crypto/ssh"
)

// RemoteDaemonSocketPath execs "agnt daemon socket-path" as a one-shot
// command on a new session channel over client and returns its trimmed
// stdout: the remote daemon's default unix socket path. That subcommand's
// contract (cmd/agnt/daemon.go's runDaemonSocketPath) is exactly one line,
// no decoration, so trimming trailing whitespace/newline is sufficient.
func RemoteDaemonSocketPath(client *ssh.Client) (string, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("sshclient: opening session for socket-path discovery: %w", err)
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	if err := session.Run("agnt daemon socket-path"); err != nil {
		return "", fmt.Errorf("sshclient: remote 'agnt daemon socket-path' failed: %w (stderr: %s)", err, stderr.String())
	}

	path := strings.TrimSpace(stdout.String())
	if path == "" {
		return "", fmt.Errorf("sshclient: remote 'agnt daemon socket-path' returned empty output")
	}
	return path, nil
}

// LocalForwardSocketPath returns the local unix socket path 'agnt ssh'
// listens on to forward the given host's remote daemon socket:
// $XDG_RUNTIME_DIR/agnt/ssh-<host>.sock, falling back to
// os.TempDir()/agnt/ssh-<host>.sock when XDG_RUNTIME_DIR is unset. The
// parent directory's uid-owned/0700 security is enforced by
// socket.Manager.Listen (same primitive internal/daemon/socket_compat.go
// relies on), not duplicated here.
func LocalForwardSocketPath(host string) string {
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "agnt", fmt.Sprintf("ssh-%s.sock", host))
}

// Forwarder listens on a local unix socket and, for every accepted
// connection, opens a NEW direct-streamlocal@openssh.com channel to
// remoteSocketPath on client and proxies bytes bidirectionally. Each
// accepted local connection gets its own remote channel — there is no
// shared/multiplexed channel — so concurrent local clients (e.g. `agnt
// monitor` and `agnt doctor` running at once) get independent byte streams.
type Forwarder struct {
	mu               sync.RWMutex
	client           *Client
	remoteSocketPath string
	sockMgr          *socket.Manager
	listener         net.Listener

	done              chan struct{}
	paused            chan struct{}
	conns             map[net.Conn]struct{}
	beforeRemoteTrack func()
}

// NewForwarder binds localSocketPath (via socket.Manager, which detects and
// removes a stale socket file left by a crashed prior run, and refuses to
// clobber a live listener there) and returns a Forwarder ready for Serve.
func NewForwarder(client *Client, remoteSocketPath, localSocketPath string) (*Forwarder, error) {
	mgr := socket.NewManager(socket.Config{
		Path: localSocketPath,
		Mode: 0600,
		Name: "agnt-ssh-forward",
		// This local socket is owned by this specific *Forwarder instance
		// (i.e. this "agnt ssh" process), not by any daemon process — the
		// default cmdline-based isDaemonProcess predicate would never match
		// us, so checkExisting would spuriously delete a live sibling's PID
		// file. Matching our own PID keeps a concurrent "agnt ssh" to the
		// same host correctly detected as ErrDaemonRunning (ID-scoped
		// coexist-loud policy: see doc comment on Serve) rather than
		// silently clobbered.
		ProcessMatcher: func(pid int) bool { return pid == os.Getpid() },
	})

	listener, err := mgr.Listen()
	if err != nil {
		return nil, fmt.Errorf("sshclient: binding local forward socket %s: %w", localSocketPath, err)
	}

	return &Forwarder{
		client:           client,
		remoteSocketPath: remoteSocketPath,
		sockMgr:          mgr,
		listener:         listener,
		done:             make(chan struct{}),
		paused:           make(chan struct{}),
		conns:            make(map[net.Conn]struct{}),
	}, nil
}

// Serve accepts local connections until the listener is closed (via Close),
// opening one new streamlocal channel per connection and proxying bytes
// both ways with io.Copy; either side erroring or hitting EOF closes both.
// Returns nil on a clean shutdown (Close called), otherwise the Accept error.
//
// Coexistence policy: a second "agnt ssh" to the same host that hits an
// already-live local socket gets a loud ErrDaemonRunning from NewForwarder
// rather than silently sharing or clobbering the first instance's listener —
// simplest to reason about, and matches the existing daemon socket
// contract's "never silently clobber a live listener" rule.
func (f *Forwarder) Serve() error {
	for {
		conn, err := f.listener.Accept()
		if err != nil {
			select {
			case <-f.done:
				return nil
			default:
				return err
			}
		}
		go f.handleConn(conn)
	}
}

func (f *Forwarder) handleConn(conn net.Conn) {
	defer conn.Close()

	f.mu.RLock()
	client := f.client
	remoteSocketPath := f.remoteSocketPath
	paused := f.paused
	f.mu.RUnlock()
	select {
	case <-paused:
		return
	default:
	}

	remote, err := client.SSH.Dial("unix", remoteSocketPath)
	if err != nil {
		return
	}
	defer remote.Close()
	if !f.track(conn) {
		return
	}
	defer f.untrack(conn)
	if f.beforeRemoteTrack != nil {
		f.beforeRemoteTrack()
	}
	if !f.track(remote) {
		return
	}
	defer f.untrack(remote)

	relayDone := make(chan struct{}, 2)
	go func() {
		io.Copy(remote, conn)
		relayDone <- struct{}{}
	}()
	go func() {
		io.Copy(conn, remote)
		relayDone <- struct{}{}
	}()
	<-relayDone
}

func (f *Forwarder) track(conn net.Conn) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	select {
	case <-f.paused:
		conn.Close()
		return false
	default:
		f.conns[conn] = struct{}{}
		return true
	}
}

func (f *Forwarder) untrack(conn net.Conn) {
	f.mu.Lock()
	delete(f.conns, conn)
	f.mu.Unlock()
}

// Pause keeps the local socket bound, rejects newly accepted connections,
// and closes every transport-dependent relay.
func (f *Forwarder) Pause() {
	f.mu.Lock()
	select {
	case <-f.paused:
	default:
		close(f.paused)
	}
	for conn := range f.conns {
		conn.Close()
	}
	f.mu.Unlock()
}

// Resume swaps in the freshly connected transport without rebinding the
// local socket. remoteSocketPath is rediscovered after reconnect.
func (f *Forwarder) Resume(client *Client, remoteSocketPath string) {
	f.mu.Lock()
	f.client = client
	f.remoteSocketPath = remoteSocketPath
	f.paused = make(chan struct{})
	f.mu.Unlock()
}

// Paused returns a signal closed exactly when Pause has taken effect. Tests
// and callers can observe this transition without sleep-based races.
func (f *Forwarder) Paused() <-chan struct{} {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.paused
}

// Close stops accepting new connections and removes the local socket file
// (and its PID file), so a subsequent connect to the same host does not see
// a stale "address already in use" error.
func (f *Forwarder) Close() error {
	select {
	case <-f.done:
	default:
		close(f.done)
	}
	f.Pause()
	return f.sockMgr.Close()
}
