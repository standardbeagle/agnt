package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/daemon"
	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/agnt/internal/sessionhost"
)

// Platform-specific entry point, implemented in attach_unix.go (real raw-mode
// relay) and attach_windows.go (clear stub — see task Scope note on native
// Windows attach being deferred).
type attachTerminal func(client *daemon.Client, sessionID string, detachChord []byte) error

var runAttachTerminal attachTerminal

// attachRawModeEntered is a test observation boundary. Production leaves it
// nil; PTY integration tests install a non-blocking signal so input is sent
// only after the real terminal transition has completed.
var attachRawModeEntered func()

var attachCmd = &cobra.Command{
	Use:   "attach <name|id>",
	Short: "Attach to a daemon-owned detachable session (see: agnt session hosts)",
	Long: `Attach to a session-host session: a PTY child owned by the daemon,
not by this CLI process, so it survives this client disconnecting.

The terminal is put into raw mode and relays bytes to/from the remote
session. Detach (leaving the session running) with the configured detach
chord — default: Ctrl-\ Ctrl-\ (two consecutive presses). Configure via
.agnt.kdl:

  session {
      detach-key "ctrl-\\ ctrl-\\"
  }

Examples:
  agnt attach my-session
  agnt session hosts        # list detachable sessions
  agnt session kill my-session`,
	Args: cobra.ExactArgs(1),
	RunE: runAttach,
}

func init() {
	rootCmd.AddCommand(attachCmd)
}

// defaultDetachChord is two consecutive Ctrl-\ presses (0x1c, the FS
// control byte) — a two-byte chord chosen because it never collides with
// the remote-side overlay command palette (':'/'/' printable ASCII) or the
// forwarding-pause hotkey (Ctrl+Up/Down, ESC-prefixed CSI sequences): 0x1c
// is neither printable nor ESC-prefixed.
var defaultDetachChord = []byte{0x1c, 0x1c}

// resolveDetachChord loads the project's .agnt.kdl session.detach-key (if
// set) and parses it; any parse error or absence falls back to
// defaultDetachChord so a malformed config value never leaves the user
// without a way to detach.
func resolveDetachChord(cwd string) []byte {
	cfg, err := config.LoadAgntConfig(cwd)
	if err != nil || cfg.Session == nil || cfg.Session.DetachKey == "" {
		return defaultDetachChord
	}
	chord, err := parseDetachChord(cfg.Session.DetachKey)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agnt attach: invalid session.detach-key %q (%v); using default Ctrl-\\ Ctrl-\\\n", cfg.Session.DetachKey, err)
		return defaultDetachChord
	}
	return chord
}

// parseDetachChord parses a "ctrl-<char> ctrl-<char> ..." chord spec into
// the raw byte sequence to watch for.
func parseDetachChord(spec string) ([]byte, error) {
	tokens := strings.Fields(spec)
	if len(tokens) == 0 {
		return nil, fmt.Errorf("empty chord")
	}
	out := make([]byte, 0, len(tokens))
	for _, tok := range tokens {
		b, err := parseCtrlToken(tok)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

// parseCtrlToken parses a single "ctrl-<char>" token into its control byte
// (e.g. "ctrl-\\" -> 0x1c).
func parseCtrlToken(tok string) (byte, error) {
	const prefix = "ctrl-"
	lower := strings.ToLower(tok)
	if !strings.HasPrefix(lower, prefix) || len(tok) != len(prefix)+1 {
		return 0, fmt.Errorf("token %q: expected ctrl-<char>", tok)
	}
	c := tok[len(prefix)]
	return c & 0x1f, nil
}

func runAttach(cmd *cobra.Command, args []string) error {
	target := args[0]
	socketPath := getSocketPath(cmd)
	client := daemon.NewClient(daemon.WithSocketPath(socketPath))
	if err := client.Connect(); err != nil {
		return fmt.Errorf("daemon is not running: %v", err)
	}
	defer client.Close()

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get working directory: %v", err)
	}

	id, err := resolveSessionHostID(client, cwd, target)
	if err != nil {
		return err
	}

	chord := resolveDetachChord(cwd)
	fmt.Fprint(os.Stdout, attachTerminalTitle(target))
	return runAttachTerminal(client, id, chord)
}

func attachTerminalTitle(session string) string {
	return fmt.Sprintf("\x1b]0;agnt attach · %s\x07", session)
}

// resolveSessionHostID resolves a user-supplied name or id to a concrete
// session-host session id. An exact id match wins; otherwise a unique name
// match among project-scoped sessions, falling back to a global search
// (a detachable session outlives the attaching client's own directory, so
// it may have been created from elsewhere).
func resolveSessionHostID(client *daemon.Client, cwd, target string) (string, error) {
	result, err := client.SessionHostList(protocol.DirectoryFilter{Directory: cwd})
	if err != nil {
		return "", fmt.Errorf("failed to list session-host sessions: %v", err)
	}
	if id, ok := matchSessionHostID(result, target); ok {
		return id, nil
	}

	result, err = client.SessionHostList(protocol.DirectoryFilter{Global: true})
	if err != nil {
		return "", fmt.Errorf("failed to list session-host sessions: %v", err)
	}
	if id, ok := matchSessionHostID(result, target); ok {
		return id, nil
	}
	return "", fmt.Errorf("no session-host session named or id'd %q", target)
}

// matchSessionHostID scans a SESSION-HOST LIST result for target, matching
// on exact session_id first, then falling back to a unique name match.
func matchSessionHostID(result map[string]interface{}, target string) (string, bool) {
	sessions, _ := result["sessions"].([]interface{})
	var nameMatches []string
	for _, s := range sessions {
		sm, ok := s.(map[string]interface{})
		if !ok {
			continue
		}
		id := getString(sm, "session_id")
		if id == target {
			return id, true
		}
		if getString(sm, "name") == target {
			nameMatches = append(nameMatches, id)
		}
	}
	if len(nameMatches) == 1 {
		return nameMatches[0], true
	}
	return "", false
}

// renderAttachFrame handles one server->client SESSION-HOST ATTACH frame:
// "stdout" is written verbatim to os.Stdout (dumb byte relay — the overlay
// that produces the terminal chrome runs remote-side), "replay-marker"
// surfaces a truncation notice on stderr (never on stdout, to avoid
// corrupting the relayed terminal stream), "exit" prints the session's exit
// notice. Any other/unknown frame type is ignored rather than erroring, so a
// future additive frame type doesn't break older attach clients.
func renderAttachFrame(f sessionhost.Frame) error {
	switch f.Type {
	case "stdout":
		var b64 string
		if err := json.Unmarshal(f.Data, &b64); err != nil {
			return nil
		}
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil
		}
		_, err = os.Stdout.Write(raw)
		return err
	case "replay-marker":
		var marker sessionhost.ReplayMarkerData
		_ = json.Unmarshal(f.Data, &marker)
		if marker.Truncated {
			fmt.Fprintf(os.Stderr, "\r\n\x1b[2m[agnt attach] scrollback truncated (session ring wrapped)\x1b[0m\r\n")
		}
		return nil
	case "exit":
		var exitData sessionhost.ExitData
		_ = json.Unmarshal(f.Data, &exitData)
		fmt.Fprintf(os.Stderr, "\r\n\x1b[33m[agnt attach] session exited (code %d)\x1b[0m\r\n", exitData.Code)
		return nil
	default:
		return nil
	}
}

// chordCarryScanner implements cross-read-boundary detection of a short
// detach chord in a raw stdin byte stream, without ever forwarding the
// chord's own bytes to the remote. It keeps back up to len(chord)-1 trailing
// bytes ("carry") between calls since the chord may straddle two Read()
// calls.
type chordCarryScanner struct {
	chord []byte
	carry []byte
}

func newChordCarryScanner(chord []byte) *chordCarryScanner {
	return &chordCarryScanner{chord: chord}
}

// Feed processes a new chunk of stdin bytes. It returns (forward, detached):
// forward is the bytes safe to send to the remote right now (chord bytes and
// any not-yet-resolved carry are withheld); detached is true the moment the
// full chord has been observed (forward already excludes it).
func (s *chordCarryScanner) Feed(data []byte) (forward []byte, detached bool) {
	combined := append(append([]byte(nil), s.carry...), data...)
	if idx := bytes.Index(combined, s.chord); idx >= 0 {
		s.carry = nil
		return combined[:idx], true
	}
	keep := len(s.chord) - 1
	if keep < 0 {
		keep = 0
	}
	if len(combined) <= keep {
		s.carry = combined
		return nil, false
	}
	sendLen := len(combined) - keep
	s.carry = append([]byte(nil), combined[sendLen:]...)
	return combined[:sendLen], false
}

// Flush returns any withheld carry bytes (call once at EOF, after which the
// chord can no longer be completed).
func (s *chordCarryScanner) Flush() []byte {
	out := s.carry
	s.carry = nil
	return out
}

// panicSafeRestore guarantees console restoration before propagating a panic
// from either relay direction.
func panicSafeRestore(restore func(), fn func()) {
	defer func() {
		if recovered := recover(); recovered != nil {
			restore()
			panic(recovered)
		}
	}()
	fn()
}

// relayAttachInput is platform-neutral: console preparation is owned by the
// platform entry point, while byte/chord behavior is identical everywhere.
func relayAttachInput(in io.Reader, client *daemon.Client, sessionID, attachID string, isPrimary bool, chord []byte) error {
	scanner := newChordCarryScanner(chord)
	buf := make([]byte, 4096)
	for {
		n, readErr := in.Read(buf)
		if n > 0 {
			forward, detached := scanner.Feed(buf[:n])
			if len(forward) > 0 && isPrimary {
				_ = client.SessionHostStdin(sessionID, attachID, forward)
			}
			if detached {
				_ = client.SessionHostDetach(sessionID, attachID)
				return nil
			}
		}
		if readErr != nil {
			if leftover := scanner.Flush(); len(leftover) > 0 && isPrimary {
				_ = client.SessionHostStdin(sessionID, attachID, leftover)
			}
			return readErr
		}
	}
}

type attachInfo struct {
	id      string
	primary bool
}

// runAttachedSession owns the protocol relay after the platform has prepared
// its console. EOF/detach is a clean half-close: canceling the attach stream
// leaves the daemon-owned session running. A server/frame error wins whenever
// it is already observable, and context cancellation is normalized to clean
// exit after local EOF/detach.
func runAttachedSession(client *daemon.Client, sessionID string, detachChord []byte, restore func(), interruptInput func() error, watchResize func(context.Context, func(int, int)) func()) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	attachedCh := make(chan attachInfo, 1)
	var attachedOnce sync.Once
	frameErrCh := make(chan error, 1)
	go panicSafeRestore(restore, func() {
		frameErrCh <- client.SessionHostAttach(ctx, sessionID, func(id string, primary bool) { attachedOnce.Do(func() { attachedCh <- attachInfo{id, primary} }) }, renderAttachFrame)
	})
	var info attachInfo
	select {
	case info = <-attachedCh:
	case err := <-frameErrCh:
		cancel()
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
	stopResize := func() {}
	if info.primary {
		stopResize = watchResize(ctx, func(cols, rows int) { _ = client.SessionHostResize(sessionID, cols, rows) })
	}
	stdinDone := make(chan error, 1)
	go panicSafeRestore(restore, func() { stdinDone <- relayAttachInput(os.Stdin, client, sessionID, info.id, info.primary, detachChord) })
	return joinAttachWorkers(cancel, interruptInput, stopResize, frameErrCh, stdinDone)
}

// joinAttachWorkers is the lifecycle barrier between the relay and console
// restoration. It cancels the peer direction, actively interrupts a blocked
// input read when the frame pump finishes first, and joins frame, input, and
// resize workers before returning. A non-cancellation frame error always wins,
// including when local EOF/detach initiated shutdown.
func joinAttachWorkers(cancel func(), interruptInput func() error, stopResize func(), frameDone, inputDone <-chan error) error {
	var frameErr error
	var interruptErr error
	select {
	case frameErr = <-frameDone:
		cancel()
		interruptErr = interruptInput()
		<-inputDone
	case <-inputDone:
		cancel()
		frameErr = <-frameDone
	}
	stopResize()
	if frameErr != nil && !errors.Is(frameErr, context.Canceled) {
		return frameErr
	}
	if interruptErr != nil {
		return interruptErr
	}
	return nil
}

// pollAttachResize is the host-independent Windows resize lifecycle. It emits
// only changed, successfully observed sizes and closes joined when cancellation
// has stopped the ticker and worker.
func pollAttachResize(ctx context.Context, interval time.Duration, size func() (int, int, error), onResize func(int, int)) <-chan struct{} {
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		lastCols, lastRows := 0, 0
		if cols, rows, err := size(); err == nil {
			lastCols, lastRows = cols, rows
			onResize(cols, rows)
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cols, rows, err := size()
				if err == nil && (cols != lastCols || rows != lastRows) {
					lastCols, lastRows = cols, rows
					onResize(cols, rows)
				}
			}
		}
	}()
	return joined
}
