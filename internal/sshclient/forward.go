package sshclient

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"

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

// LocalForwardSocketPath is platform-defined: Unix/WSL use a protected Unix
// socket and native Windows uses an owner-only named pipe.
//
// Forwarder listens on that local endpoint and, for every accepted
// connection, opens a NEW direct-streamlocal@openssh.com channel to
// remoteSocketPath on client and proxies bytes bidirectionally. Each
// accepted local connection gets its own remote channel — there is no
// shared/multiplexed channel — so concurrent local clients (e.g. `agnt
// monitor` and `agnt doctor` running at once) get independent byte streams.
type Forwarder struct {
	mu               sync.RWMutex
	client           *Client
	remoteSocketPath string
	listener         net.Listener
	cleanupListener  func() error

	done              chan struct{}
	paused            chan struct{}
	conns             map[net.Conn]struct{}
	beforeRemoteTrack func()
}

// NewForwarder binds localSocketPath using the platform listener and returns a
// Forwarder ready for Serve. Unix reclaims stale socket files; Windows pipe
// instances disappear with their owner and a live name collision fails loud.
func NewForwarder(client *Client, remoteSocketPath, localSocketPath string) (*Forwarder, error) {
	listener, cleanup, err := listenLocalForward(localSocketPath)
	if err != nil {
		return nil, fmt.Errorf("sshclient: binding local forward socket %s: %w", localSocketPath, err)
	}

	return &Forwarder{
		client:           client,
		remoteSocketPath: remoteSocketPath,
		listener:         listener,
		cleanupListener:  cleanup,
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
// Coexistence policy: a second "agnt ssh" to the same host fails to bind the
// already-live endpoint rather than silently sharing or clobbering it.
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

// Close stops accepting new connections and performs platform cleanup.
func (f *Forwarder) Close() error {
	select {
	case <-f.done:
	default:
		close(f.done)
	}
	f.Pause()
	if f.cleanupListener != nil {
		return f.cleanupListener()
	}
	return f.listener.Close()
}
