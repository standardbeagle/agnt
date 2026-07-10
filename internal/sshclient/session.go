package sshclient

import (
	"context"
	"fmt"
	"io"
	"strings"

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
// channel: `agnt attach <name> --create-if-missing --cwd <cwd>`. name and
// cwd are shell-escaped defensively (single-quoted, with embedded single
// quotes escaped) so shell metacharacters in either cannot inject
// additional commands.
func RemoteAttachCommand(name, cwd string) string {
	var b strings.Builder
	b.WriteString("agnt attach ")
	b.WriteString(shellQuote(name))
	b.WriteString(" --create-if-missing")
	if cwd != "" {
		b.WriteString(" --cwd ")
		b.WriteString(shellQuote(cwd))
	}
	return b.String()
}

// shellQuote wraps s in single quotes for POSIX sh, escaping any embedded
// single quote as '\” (close quote, escaped literal quote, reopen quote).
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// PTYSession is an open "session" channel with a PTY attached, running the
// remote agnt attach command as a dumb byte relay (spec invariant 16 — all
// session-host framing happens remote-side; this layer only carries raw
// PTY bytes, never sessionhost.Frame JSON).
type PTYSession struct {
	channel ssh.Channel
	reqs    <-chan *ssh.Request
}

// OpenPTYSession opens a "session" channel on client, requests a PTY sized
// per size, and execs the remote attach command for the given session
// name/cwd. The returned *PTYSession's channel is not yet relayed — call
// Relay to pump bytes.
func OpenPTYSession(client *ssh.Client, name, cwd string, size TermSize) (*PTYSession, error) {
	channel, reqs, err := client.OpenChannel("session", nil)
	if err != nil {
		return nil, fmt.Errorf("sshclient: opening session channel: %w", err)
	}

	if err := requestPTY(channel, size); err != nil {
		channel.Close()
		return nil, err
	}

	cmd := RemoteAttachCommand(name, cwd)
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
