package sshclient

import (
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

	stopKeepalive chan struct{}
	dead          chan struct{}
	// closeOnce guards stopKeepalive so a second Close (reconnect teardown
	// closes clients in several places) is a no-op instead of a
	// close-of-closed-channel panic.
	closeOnce sync.Once
}

// Dial resolves alias against the ssh_config at configPath (DefaultConfigPath
// if empty), verifies the host key against known_hosts (DefaultKnownHostsPath
// if empty) via HostKeyCallback, builds the auth method chain, and performs
// ProxyJump hops (if the resolved config has one) before establishing the
// final connection. defaultUser is used when neither the alias nor ssh_config
// supplies a User (e.g. the OS user invoking the command).
func Dial(alias, configPath, knownHostsPath, defaultUser string, prompter Prompter) (*Client, error) {
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

	sshClient, err := dialWithJumps(cfg, configPath, knownHostsPath, defaultUser, prompter)
	if err != nil {
		return nil, err
	}

	c := &Client{SSH: sshClient, stopKeepalive: make(chan struct{}), dead: make(chan struct{})}
	c.startKeepalive(15*time.Second, 5*time.Second, 3)
	return c, nil
}

// dialWithJumps recursively dials through cfg.ProxyJump hops (if any),
// resolving each hop independently from ssh_config, and finally reaches
// cfg.HostName:cfg.Port.
func dialWithJumps(cfg HostConfig, configPath, knownHostsPath, defaultUser string, prompter Prompter) (*ssh.Client, error) {
	target := net.JoinHostPort(cfg.HostName, strconv.Itoa(cfg.Port))

	clientCfg, err := clientConfigFor(cfg, prompter, knownHostsPath)
	if err != nil {
		return nil, err
	}

	if cfg.ProxyJump == "" {
		return ssh.Dial("tcp", target, clientCfg)
	}

	hops := strings.Split(cfg.ProxyJump, ",")
	var jumpClient *ssh.Client
	for i, hop := range hops {
		hop = strings.TrimSpace(hop)
		hopUser, hopHostPort := splitUserHost(hop)
		hopHost, hopPort := splitHostPort(hopHostPort, 22)

		hopCfg, err := ResolveHost(configPath, hopHost)
		if err != nil {
			return nil, fmt.Errorf("sshclient: resolving jump host %s: %w", hopHost, err)
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
			return nil, err
		}
		hopTarget := net.JoinHostPort(hopCfg.HostName, strconv.Itoa(hopCfg.Port))

		if jumpClient == nil {
			// First hop: direct TCP dial.
			jumpClient, err = ssh.Dial("tcp", hopTarget, hopClientCfg)
			if err != nil {
				return nil, fmt.Errorf("sshclient: dialing jump host %s: %w", hopTarget, err)
			}
			continue
		}

		// Subsequent hop: open a direct-tcpip channel through the
		// previously-established jump client and use it as the
		// transport for a fresh SSH handshake.
		conn, err := jumpClient.Dial("tcp", hopTarget)
		if err != nil {
			return nil, fmt.Errorf("sshclient: opening direct-tcpip to hop %d (%s): %w", i, hopTarget, err)
		}
		ncc, chans, reqs, err := ssh.NewClientConn(conn, hopTarget, hopClientCfg)
		if err != nil {
			return nil, fmt.Errorf("sshclient: handshake with hop %d (%s): %w", i, hopTarget, err)
		}
		jumpClient = ssh.NewClient(ncc, chans, reqs)
	}

	// Final hop: from the last jump client, open a direct-tcpip channel to
	// the real target and handshake over it.
	conn, err := jumpClient.Dial("tcp", target)
	if err != nil {
		return nil, fmt.Errorf("sshclient: opening direct-tcpip to target %s: %w", target, err)
	}
	ncc, chans, reqs, err := ssh.NewClientConn(conn, target, clientCfg)
	if err != nil {
		return nil, fmt.Errorf("sshclient: handshake with target %s: %w", target, err)
	}
	return ssh.NewClient(ncc, chans, reqs), nil
}

func clientConfigFor(cfg HostConfig, prompter Prompter, knownHostsPath string) (*ssh.ClientConfig, error) {
	hkCallback, err := HostKeyCallback(knownHostsPath, prompter)
	if err != nil {
		return nil, err
	}
	return &ssh.ClientConfig{
		User:            cfg.User,
		Auth:            BuildAuthMethods(cfg.IdentityFile, prompter),
		HostKeyCallback: hkCallback,
		Timeout:         15 * time.Second,
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
	return c.SSH.Close()
}

// Dead returns a channel that is closed when the keepalive detector
// determines the transport is no longer responsive (3 consecutive
// unanswered keepalives, ~45s). Reconnect logic itself belongs to a later
// task; this channel is the hook that task uses to learn the transport
// died.
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
	select {
	case <-c.dead:
		// already closed
	default:
		close(c.dead)
	}
}
