package sshclient

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// Client wraps an established *ssh.Client, exposing it publicly (invariant
// 7 in the task brief) so later tasks (port forwarding, SFTP) can call
// SSH.OpenChannel / SSH.Dial directly without this package growing new
// wrapper methods for every future channel type.
type Client struct {
	// SSH is the underlying golang.org/x/crypto/ssh client. Exported
	// deliberately — do not hide behind an unexported field.
	SSH *ssh.Client

	// jumps are the ProxyJump clients this connection is tunnelled through,
	// outermost first. Close closes them after SSH; nil for a direct dial.
	jumps []*ssh.Client

	stopKeepalive chan struct{}
	dead          chan struct{}
	// closeOnce guards stopKeepalive so a second Close (reconnect teardown
	// closes clients in several places) is a no-op instead of a
	// close-of-closed-channel panic.
	closeOnce sync.Once
	// deadOnce guards dead: the keepalive goroutine and TransportLost can
	// both decide the transport is gone.
	deadOnce sync.Once
}

// Dial is DialContext with a background context.
func Dial(alias, configPath, knownHostsPath, defaultUser string, prompter Prompter) (*Client, error) {
	return DialContext(context.Background(), alias, configPath, knownHostsPath, defaultUser, prompter)
}

// DialContext resolves alias against the ssh_config at configPath
// (DefaultConfigPath if empty), verifies the host key against known_hosts
// (DefaultKnownHostsPath if empty) via HostKeyCallback, builds the auth method
// chain, and performs ProxyJump hops (if the resolved config has one) before
// establishing the final connection. defaultUser is used when neither the
// alias nor ssh_config supplies a User (e.g. the OS user invoking the command).
//
// Every step is bounded and cancellable. The TCP connect has its own timeout;
// each SSH handshake (banner, key exchange, authentication), including the
// ones tunnelled through jump hosts, must finish within the context's deadline
// or, when ctx has none, within DefaultHandshakeTimeout. Cancelling ctx closes
// the connection in progress, so a peer that accepts TCP and then goes silent
// cannot hold the caller forever.
func DialContext(ctx context.Context, alias, configPath, knownHostsPath, defaultUser string, prompter Prompter) (*Client, error) {
	if configPath == "" {
		configPath = DefaultConfigPath()
	}
	if knownHostsPath == "" {
		knownHostsPath = DefaultKnownHostsPath()
	}

	cfg, err := ResolveHost(configPath, alias)
	if err != nil {
		return nil, err
	}
	if cfg.User == "" {
		cfg.User = defaultUser
	}
	for _, w := range cfg.Warnings {
		// Once per process: a reconnect dials again on every attempt.
		if _, seen := printedConfigWarnings.LoadOrStore(w, struct{}{}); !seen {
			fmt.Fprintf(prompter.Out, "sshclient: %s\n", w)
		}
	}

	sshClient, jumps, err := dialChain(ctx, cfg, configPath, knownHostsPath, defaultUser, prompter)
	if err != nil {
		return nil, err
	}

	c := &Client{SSH: sshClient, jumps: jumps, stopKeepalive: make(chan struct{}), dead: make(chan struct{})}
	c.startKeepalive(15*time.Second, 5*time.Second, 3)
	return c, nil
}

// printedConfigWarnings records the ssh_config warnings already shown.
var printedConfigWarnings sync.Map

// DefaultHandshakeTimeout bounds one SSH handshake when the caller's context
// carries no deadline. It is long because the handshake includes whatever the
// user types: the host-key confirmation, a key passphrase, a password.
// Unattended callers (reconnect) pass a context with a shorter deadline.
const DefaultHandshakeTimeout = 2 * time.Minute

// tcpConnectTimeout bounds the TCP connect alone.
const tcpConnectTimeout = 15 * time.Second

// dialWithJumps dials cfg through its ProxyJump hops and returns the final
// client. The jump clients it opened stay open for as long as the returned
// client is used; callers that need to close them use dialChain.
func dialWithJumps(ctx context.Context, cfg HostConfig, configPath, knownHostsPath, defaultUser string, prompter Prompter) (*ssh.Client, error) {
	client, _, err := dialChain(ctx, cfg, configPath, knownHostsPath, defaultUser, prompter)
	return client, err
}

// dialChain recursively dials through cfg.ProxyJump hops (if any), resolving
// each hop independently from ssh_config, and finally reaches
// cfg.HostName:cfg.Port. It returns the final client and the jump clients
// carrying it, outermost first; on error every client it opened is closed.
func dialChain(ctx context.Context, cfg HostConfig, configPath, knownHostsPath, defaultUser string, prompter Prompter) (_ *ssh.Client, jumps []*ssh.Client, err error) {
	defer func() {
		if err != nil {
			closeClients(jumps)
			jumps = nil
		}
	}()

	target := net.JoinHostPort(cfg.HostName, strconv.Itoa(cfg.Port))

	clientCfg, err := clientConfigFor(cfg, prompter, knownHostsPath)
	if err != nil {
		return nil, nil, err
	}

	if cfg.ProxyJump == "" {
		client, err := dialDirect(ctx, target, clientCfg)
		return client, nil, err
	}

	hops := strings.Split(cfg.ProxyJump, ",")
	var jumpClient *ssh.Client
	for i, hop := range hops {
		hop = strings.TrimSpace(hop)
		hopUser, hopHostPort := splitUserHost(hop)
		hopHost, hopPort := splitHostPort(hopHostPort, 22)

		hopCfg, err := ResolveHost(configPath, hopHost)
		if err != nil {
			return nil, jumps, fmt.Errorf("sshclient: resolving jump host %s: %w", hopHost, err)
		}
		if hopUser != "" {
			hopCfg.User = hopUser
		}
		if hopCfg.User == "" {
			hopCfg.User = defaultUser
		}
		if hopPort != 22 {
			hopCfg.Port = hopPort
		}
		if hopCfg.HostName == "" || hopCfg.HostName == hopCfg.Host {
			hopCfg.HostName = hopHost
		}

		hopClientCfg, err := clientConfigFor(hopCfg, prompter, knownHostsPath)
		if err != nil {
			return nil, jumps, err
		}
		hopTarget := net.JoinHostPort(hopCfg.HostName, strconv.Itoa(hopCfg.Port))

		if jumpClient == nil {
			// First hop: direct TCP dial.
			jumpClient, err = dialDirect(ctx, hopTarget, hopClientCfg)
			if err != nil {
				return nil, jumps, fmt.Errorf("sshclient: dialing jump host %s: %w", hopTarget, err)
			}
			jumps = append(jumps, jumpClient)
			continue
		}

		// Subsequent hop: open a direct-tcpip channel through the
		// previously-established jump client and use it as the
		// transport for a fresh SSH handshake.
		conn, err := jumpClient.DialContext(ctx, "tcp", hopTarget)
		if err != nil {
			return nil, jumps, fmt.Errorf("sshclient: opening direct-tcpip to hop %d (%s): %w", i, hopTarget, err)
		}
		jumpClient, err = handshake(ctx, conn, hopTarget, hopClientCfg)
		if err != nil {
			return nil, jumps, fmt.Errorf("sshclient: handshake with hop %d (%s): %w", i, hopTarget, err)
		}
		jumps = append(jumps, jumpClient)
	}

	// Final hop: from the last jump client, open a direct-tcpip channel to
	// the real target and handshake over it.
	conn, err := jumpClient.DialContext(ctx, "tcp", target)
	if err != nil {
		return nil, jumps, fmt.Errorf("sshclient: opening direct-tcpip to target %s: %w", target, err)
	}
	client, err := handshake(ctx, conn, target, clientCfg)
	if err != nil {
		return nil, jumps, fmt.Errorf("sshclient: handshake with target %s: %w", target, err)
	}
	return client, jumps, nil
}

// dialDirect opens a TCP connection to addr and runs the SSH handshake on it.
func dialDirect(ctx context.Context, addr string, cfg *ssh.ClientConfig) (*ssh.Client, error) {
	dialer := net.Dialer{Timeout: tcpConnectTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	return handshake(ctx, conn, addr, cfg)
}

// handshake runs the SSH client handshake on conn, bounded by ctx.
//
// ssh.ClientConfig.Timeout covers only the TCP connect, and
// ssh.NewClientConn reads the banner, key exchange and auth replies with no
// deadline of its own: a peer that accepts the connection and then says
// nothing blocks it forever. A watcher therefore closes conn when ctx is
// cancelled or the handshake budget runs out, which is what unblocks
// NewClientConn. It is closed rather than given a read deadline because a
// direct-tcpip channel through a jump host does not support deadlines.
//
// conn is always consumed: owned by the returned client, or closed.
func handshake(ctx context.Context, conn net.Conn, addr string, cfg *ssh.ClientConfig) (*ssh.Client, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultHandshakeTimeout)
		defer cancel()
	}

	finished := make(chan struct{})
	watcherDone := make(chan struct{})
	aborted := false // written by the watcher, read only after watcherDone
	go func() {
		defer close(watcherDone)
		select {
		case <-finished:
		case <-ctx.Done():
			aborted = true
			conn.Close()
		}
	}()

	ncc, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	close(finished)
	<-watcherDone

	if aborted {
		// The watcher closed conn. Whatever NewClientConn made of that, the
		// connection is gone, so report why rather than a bare read error.
		if err == nil {
			ncc.Close()
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("ssh handshake with %s did not complete in time: %w", addr, ctx.Err())
		}
		return nil, ctx.Err()
	}
	if err != nil {
		conn.Close()
		return nil, err
	}
	return ssh.NewClient(ncc, chans, reqs), nil
}

func closeClients(clients []*ssh.Client) {
	// Innermost first: each later client is tunnelled through the earlier ones.
	for i := len(clients) - 1; i >= 0; i-- {
		clients[i].Close()
	}
}

func clientConfigFor(cfg HostConfig, prompter Prompter, knownHostsPath string) (*ssh.ClientConfig, error) {
	hkCallback, err := HostKeyCallback(knownHostsPath, prompter)
	if err != nil {
		return nil, err
	}
	return &ssh.ClientConfig{
		User:            cfg.User,
		Auth:            BuildAuthMethods(identityFilesOrDefaults(cfg.IdentityFile), prompter),
		HostKeyCallback: hkCallback,
		// Ask for the key types known_hosts already records for this host, so
		// the callback is offered a key it can actually compare (see
		// knownHostKeyAlgorithms). Nil keeps the library default.
		HostKeyAlgorithms: knownHostKeyAlgorithms(knownHostsPath, net.JoinHostPort(cfg.HostName, strconv.Itoa(cfg.Port))),
		Timeout:           tcpConnectTimeout,
	}, nil
}

// splitUserHost splits an OpenSSH "[user@]host[:port]" jump-host spec into
// user (possibly empty) and the remaining "host[:port]" string.
func splitUserHost(spec string) (user, hostPort string) {
	if idx := strings.IndexByte(spec, '@'); idx >= 0 {
		return spec[:idx], spec[idx+1:]
	}
	return "", spec
}

// splitHostPort splits "host[:port]" into host and port, defaulting port
// to defaultPort if absent. Does not attempt IPv6 bracket handling (out of
// scope, matching the host[:path] CLI parsing note in cmd/agnt/ssh.go).
func splitHostPort(hostPort string, defaultPort int) (host string, port int) {
	if idx := strings.LastIndexByte(hostPort, ':'); idx >= 0 {
		host = hostPort[:idx]
		if p, err := strconv.Atoi(hostPort[idx+1:]); err == nil {
			return host, p
		}
	}
	return hostPort, defaultPort
}

// Close closes the underlying ssh.Client and stops the keepalive goroutine.
// It is safe to call more than once: reconnect teardown closes the same
// Client from several places, and a bare close(c.stopKeepalive) would panic
// on the second call.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		close(c.stopKeepalive)
	})
	err := c.SSH.Close()
	closeClients(c.jumps)
	return err
}

// Dead returns a channel that is closed when the keepalive detector
// determines the transport is no longer responsive (3 consecutive
// unanswered keepalives, ~45s), or when TransportLost finds it gone. A hard
// drop ends session channels with a plain EOF before keepalive notices, so a
// caller whose relay ended on its own must ask TransportLost, not just
// check this channel.
func (c *Client) Dead() <-chan struct{} {
	return c.dead
}

// startKeepalive spawns the background goroutine described in spec
// invariant 19: send a global "keepalive@openssh.com" request every
// interval; after maxFailures consecutive failures/timeouts, close c.dead
// exactly once. sendTimeout bounds each individual SendRequest call — it
// must be shorter than interval, since a black-holed transport (packet
// loss, no RST) would otherwise leave SendRequest blocked indefinitely and
// the failure counter would never advance.
func (c *Client) startKeepalive(interval, sendTimeout time.Duration, maxFailures int) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		failures := 0
		for {
			select {
			case <-c.stopKeepalive:
				return
			case <-ticker.C:
				err := c.sendKeepaliveRequest(sendTimeout)
				// The server has no handler for this request name, so a
				// well-formed reply (ok == false, err == nil) is the
				// EXPECTED liveness signal, per OpenSSH's own keepalive
				// convention. A transport-level error or a timed-out
				// reply both count as a failure.
				if err != nil {
					failures++
					if failures >= maxFailures {
						closeDeadOnce(c)
						// Close the transport so any SendRequest call
						// still blocked on a black-holed reply (from
						// this or a prior tick) unblocks instead of
						// leaking for the lifetime of the process.
						c.SSH.Close()
						return
					}
					continue
				}
				failures = 0
			}
		}
	}()
}

// sendKeepaliveRequest issues the keepalive global request in a goroutine
// and waits for either its result or timeout to elapse. A timeout is
// reported as an error, same as a transport-level SendRequest failure, so
// callers don't need to distinguish "the server is gone" from "the server
// stopped answering."
func (c *Client) sendKeepaliveRequest(timeout time.Duration) error {
	resultCh := make(chan error, 1)
	go func() {
		_, _, err := c.SSH.SendRequest("keepalive@openssh.com", true, nil)
		resultCh <- err
	}()

	select {
	case err := <-resultCh:
		return err
	case <-time.After(timeout):
		return errKeepaliveTimeout
	}
}

var errKeepaliveTimeout = fmt.Errorf("sshclient: keepalive request timed out")

func closeDeadOnce(c *Client) {
	c.deadOnce.Do(func() { close(c.dead) })
}

// TransportLost reports whether the transport under c is gone. Callers ask
// it when a session channel ended on its own: a hard drop (RST, sshd
// restart) closes every channel at once, so a relay sees a plain EOF tens of
// seconds before keepalive would fire Dead(), and that EOF looks exactly like
// the remote session exiting. An already-fired Dead() answers true; otherwise
// one keepalive request bounded by timeout decides, and a closed transport
// fails it at once. A lost transport also fires Dead() and closes c.SSH.
func (c *Client) TransportLost(timeout time.Duration) bool {
	select {
	case <-c.dead:
		return true
	default:
	}
	if c.sendKeepaliveRequest(timeout) == nil {
		return false
	}
	closeDeadOnce(c)
	c.SSH.Close()
	return true
}
