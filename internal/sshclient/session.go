package sshclient

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/standardbeagle/agnt/internal/daemonclient"
	"github.com/standardbeagle/agnt/internal/platform"
	"github.com/standardbeagle/agnt/internal/protocol"
	"golang.org/x/crypto/ssh"
)

// TermSize is the terminal dimensions used for the initial pty-req and
// subsequent window-change requests.
type TermSize struct {
	Cols   uint32
	Rows   uint32
	Width  uint32 // pixel width, may be 0
	Height uint32 // pixel height, may be 0
}

// RemoteAttachCommand builds the shell command run on the remote session
// channel: bare `agnt attach <name>`, identical in shape to
// RemoteReattachCommand. It used to bake `--create-if-missing --cwd <cwd>`
// into the command line, but cmd/agnt/attach.go's attachCmd defines neither
// flag — cobra rejects them on a real round trip, breaking every initial
// connect to a not-yet-existing session (see .claude/rules/lessons-ssh-transport.md
// #10: a remote-exec command line and the remote subcommand's actual flag
// set can drift independently). Session creation is now driven through the
// daemon protocol directly (SESSION-HOST LIST/CREATE, see
// ensureRemoteSessionCreated) by OpenPTYSession before this command is ever
// sent, mirroring the reconnect state machine's ensureRemoteSessionAttachable
// (cmd/agnt/ssh.go). name is shell-escaped defensively (single-quoted, with
// embedded single quotes escaped) so shell metacharacters cannot inject
// additional commands.
func RemoteAttachCommand(name, cwd string) string {
	return RemoteReattachCommand(name)
}

// RemoteReattachCommand builds the bare `agnt attach <name>` command used by
// the reconnect state machine (task 09c): NO --create-if-missing and no
// --cwd. This is deliberate, not an oversight — spec invariant 24 requires
// reconnect to never re-create the named session; the caller (reconnect.go's
// AttachFunc, wired in cmd/agnt/ssh.go) is responsible for confirming the
// session still exists (and creating one only on explicit --create-if-missing
// / --new opt-in, via the daemon protocol directly) before ever reaching
// this exec, so this command can safely assume attach-only semantics.
func RemoteReattachCommand(name string) string {
	return "agnt attach " + platform.ShellQuote(name)
}

// PTYSession is an open "session" channel with a PTY attached, running the
// remote agnt attach command as a dumb byte relay (spec invariant 16 — all
// session-host framing happens remote-side; this layer only carries raw
// PTY bytes, never sessionhost.Frame JSON).
type PTYSession struct {
	channel ssh.Channel
	reqs    <-chan *ssh.Request
}

// OpenPTYSession ensures the named session-host session exists on the
// remote daemon — creating it if this is the first connect (unconditional,
// matching this function's historical always-create-if-missing behavior;
// reconnect's opt-in --create-if-missing/--new is a separate, stricter
// policy handled by the caller via OpenPTYSessionWithCommand +
// RemoteReattachCommand, see cmd/agnt/ssh.go's reattachRemoteSession) — via
// the daemon protocol (SESSION-HOST LIST/CREATE), then opens a "session"
// channel on client, requests a PTY sized per size, and execs the remote
// attach command for the given session name. The returned *PTYSession's
// channel is not yet relayed — call Relay to pump bytes.
func OpenPTYSession(client *ssh.Client, name, cwd string, size TermSize) (*PTYSession, error) {
	if err := ensureRemoteSessionCreated(client, name, cwd, size); err != nil {
		return nil, err
	}
	return OpenPTYSessionWithCommand(client, RemoteAttachCommand(name, cwd), size)
}

// ensureRemoteSessionCreated confirms the named session-host session exists
// on the remote daemon, creating one if it does not. This is the
// initial-connect counterpart of cmd/agnt/ssh.go's
// ensureRemoteSessionAttachable — same shape (discover remote daemon socket,
// forward it locally, drive SESSION-HOST LIST/CREATE over that forward), but
// unconditionally creates rather than gating on an --create-if-missing flag,
// matching the behavior this package always had for a first connect (see
// RemoteAttachCommand's doc comment for why that behavior moved here instead
// of being expressed as remote CLI flags).
func ensureRemoteSessionCreated(client *ssh.Client, name, cwd string, size TermSize) error {
	remoteSocketPath, err := RemoteDaemonSocketPath(client)
	if err != nil {
		return fmt.Errorf("sshclient: discovering remote daemon socket for session create: %w", err)
	}

	localPath := fmt.Sprintf("%s.initial-probe-%d", LocalForwardSocketPath("initial-"+name), time.Now().UnixNano())
	fw, err := NewForwarder(&Client{SSH: client}, remoteSocketPath, localPath)
	if err != nil {
		return fmt.Errorf("sshclient: opening initial-connect probe forward: %w", err)
	}
	defer fw.Close()
	go fw.Serve()

	dclient := daemonclient.NewClientWithPath(localPath)
	var connectErr error
	for i := 0; i < 20; i++ {
		if connectErr = dclient.Connect(); connectErr == nil {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if connectErr != nil {
		return fmt.Errorf("sshclient: connecting to initial-connect probe forward: %w", connectErr)
	}
	defer dclient.Close()

	result, err := dclient.SessionHostList(protocol.DirectoryFilter{Global: true})
	if err != nil {
		return fmt.Errorf("sshclient: listing remote session-host sessions: %w", err)
	}
	exists, err := sessionHostNameOrIDExists(result, name)
	if err != nil {
		return fmt.Errorf("sshclient: resolving remote session %q before create: %w", name, err)
	}
	if exists {
		return nil
	}

	if _, err := dclient.SessionHostCreate(protocol.SessionHostCreateConfig{
		Name:        name,
		ProjectPath: cwd,
		Command:     "sh",
		Cols:        int(size.Cols),
		Rows:        int(size.Rows),
	}); err != nil {
		return fmt.Errorf("sshclient: creating remote session-host session %q: %w", name, err)
	}
	return nil
}

// sessionHostNameOrIDExists scans a SESSION-HOST LIST result for target,
// matching on exact session_id first, then a unique name match — mirrors
// cmd/agnt/attach.go's matchSessionHostID (duplicated rather than shared
// across the package boundary between cmd/agnt and internal/sshclient).
//
// An ambiguous name (matching more than one remote session) returns a loud
// error rather than false: returning false would make the caller create yet
// ANOTHER same-name session, and each retry leaks another daemon PTY child.
func sessionHostNameOrIDExists(result map[string]interface{}, target string) (bool, error) {
	sessions, _ := result["sessions"].([]interface{})
	nameMatches := 0
	for _, s := range sessions {
		sm, ok := s.(map[string]interface{})
		if !ok {
			continue
		}
		if id, _ := sm["session_id"].(string); id == target {
			return true, nil
		}
		if n, _ := sm["name"].(string); n == target {
			nameMatches++
		}
	}
	if nameMatches > 1 {
		return false, fmt.Errorf("ambiguous session name %q matches %d remote sessions; address it by session id instead", target, nameMatches)
	}
	return nameMatches == 1, nil
}

// OpenPTYSessionWithCommand is the lower-level form of OpenPTYSession that
// takes an already-built remote command line rather than assembling one via
// RemoteAttachCommand. The reconnect state machine (task 09c) uses this
// directly with RemoteReattachCommand so a reconnect attempt never sends
// --create-if-missing over the wire, regardless of what the initial-connect
// path does.
func OpenPTYSessionWithCommand(client *ssh.Client, cmd string, size TermSize) (*PTYSession, error) {
	channel, reqs, err := client.OpenChannel("session", nil)
	if err != nil {
		return nil, fmt.Errorf("sshclient: opening session channel: %w", err)
	}

	if err := requestPTY(channel, size); err != nil {
		channel.Close()
		return nil, err
	}

	payload := ssh.Marshal(struct{ Command string }{cmd})
	ok, err := channel.SendRequest("exec", true, payload)
	if err != nil {
		channel.Close()
		return nil, fmt.Errorf("sshclient: sending exec request: %w", err)
	}
	if !ok {
		channel.Close()
		return nil, fmt.Errorf("sshclient: remote refused exec request for %q", cmd)
	}

	return &PTYSession{channel: channel, reqs: reqs}, nil
}

// ptyRequestPayload mirrors the wire format of an SSH "pty-req" channel
// request (RFC 4254 §6.2).
type ptyRequestPayload struct {
	Term     string
	Columns  uint32
	Rows     uint32
	Width    uint32
	Height   uint32
	Modelist string
}

func requestPTY(channel ssh.Channel, size TermSize) error {
	term := "xterm-256color"
	payload := ssh.Marshal(ptyRequestPayload{
		Term:    term,
		Columns: size.Cols,
		Rows:    size.Rows,
		Width:   size.Width,
		Height:  size.Height,
	})
	ok, err := channel.SendRequest("pty-req", true, payload)
	if err != nil {
		return fmt.Errorf("sshclient: sending pty-req: %w", err)
	}
	if !ok {
		return fmt.Errorf("sshclient: remote refused pty-req")
	}
	return nil
}

// windowChangePayload mirrors the wire format of an SSH "window-change"
// channel request (RFC 4254 §6.7). It carries no reply, matching OpenSSH's
// own fire-and-forget resize notifications.
type windowChangePayload struct {
	Columns uint32
	Rows    uint32
	Width   uint32
	Height  uint32
}

// Resize sends a "window-change" request for the given new size. Actual
// OS SIGWINCH handling is the caller's responsibility (cmd/agnt/ssh.go),
// keeping this package signal-agnostic and testable.
func (s *PTYSession) Resize(size TermSize) error {
	payload := ssh.Marshal(windowChangePayload{
		Columns: size.Cols,
		Rows:    size.Rows,
		Width:   size.Width,
		Height:  size.Height,
	})
	_, err := s.channel.SendRequest("window-change", false, payload)
	return err
}

// Relay pumps stdin -> remote and remote -> stdout/stderr until the
// channel closes or ctx is cancelled. It discards incidental channel
// requests (e.g. exit-status) on s.reqs by draining them via
// ssh.DiscardRequests in a background goroutine.
//
// Completion is driven by the remote -> stdout direction only: an EOF on
// local stdin (a closed pipe, a batch script's input running out) does not
// by itself end an interactive session, so it is copied in a fire-and-forget
// goroutine. The channel closing (remote -> stdout hits EOF) or ctx
// cancellation are the two ways Relay returns. Returns the first non-EOF
// error from the remote -> stdout direction, or nil on clean channel close.
func (s *PTYSession) Relay(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer) error {
	go ssh.DiscardRequests(s.reqs)

	go io.Copy(s.channel, stdin)
	if stderr != nil {
		go io.Copy(stderr, s.channel.Stderr())
	}

	done := make(chan error, 1)
	go func() {
		_, err := io.Copy(stdout, s.channel)
		done <- err
	}()

	select {
	case <-ctx.Done():
		s.channel.Close()
		<-done // ensure the stdout copy goroutine has stopped writing before returning
		return ctx.Err()
	case err := <-done:
		s.channel.Close()
		if err == io.EOF {
			return nil
		}
		return err
	}
}

// Close closes the underlying channel.
func (s *PTYSession) Close() error {
	return s.channel.Close()
}
