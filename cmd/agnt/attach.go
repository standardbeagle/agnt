package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"

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
	return runAttachTerminal(client, id, chord)
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
