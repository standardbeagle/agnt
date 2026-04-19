package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/standardbeagle/agnt/internal/daemon"
)

// hookCmd is the Claude Code hook dispatcher subcommand. It is designed to
// be wired from `~/.claude/settings.json` hook entries (and the equivalent
// for other agents), so the hot path is:
//
//	main() → ReadAll(stdin) → daemon.Connect → HookSend → os.Exit(0)
//
// The CLI contract with the caller is:
//
//   - Argument errors (missing event, malformed --tag) exit 2 with stderr —
//     these are user configuration mistakes that must be loud during setup.
//   - Every other failure path (daemon not running, wedged daemon, daemon-
//     side rejection) exits 0 silently so a broken agnt install never breaks
//     the agent's tool call. Wedged-daemon failures additionally append one
//     rate-limited line to the drop-log so the developer can later correlate
//     missing events with buffer pressure.
var hookCmd = &cobra.Command{
	Use:   "hook <event>",
	Short: "Forward a Claude Code (or other agent) hook event to the agnt daemon",
	Long: `Read a Claude Code hook payload from stdin, enqueue it in the daemon, and exit.

The total wall clock from invocation to exit must stay inside a few milliseconds
against a warm daemon — this is the budget Claude Code sees on every tool call.

This subcommand is intended to be wired from ~/.claude/settings.json hook entries
(or the equivalent for other agent harnesses). Missing daemon, wedged daemon, and
daemon-side rejection all exit 0 silently so a broken agnt install never breaks
the agent's tool call. Only user configuration mistakes (missing event name,
malformed --tag flag) exit 2 with a visible stderr message.

Argument validation is intentionally loose: cobra's strict ExactArgs would
fail before RunE runs, and with SilenceErrors that produces a silent exit 1
which Claude Code reports as "Failed with non-blocking status code: No
stderr output" — exactly the failure mode the exit-0 contract exists to
prevent. Missing event name is handled inside runHookInternal with a loud
exit 2 + stderr message; extra positional args are ignored.`,
	Args: cobra.ArbitraryArgs,
	RunE: runHookCmd,
	// Disable the automatic usage-on-error dump. Hook callers are piping
	// JSON on stdin; a cobra usage splat in that context is noise.
	SilenceUsage:  true,
	SilenceErrors: true,
}

var (
	hookSessionID     string
	hookProjectPath   string
	hookAgent         string
	hookTags          []string
	hookEventOverride string
)

func init() {
	hookCmd.Flags().StringVar(&hookSessionID, "session-id", "", "Session id, merged into tags as session_id")
	hookCmd.Flags().StringVar(&hookProjectPath, "project-path", "", "Project root path, merged into tags as project_path")
	hookCmd.Flags().StringVar(&hookAgent, "agent", "", "Agent name, merged into tags as agent")
	hookCmd.Flags().StringArrayVar(&hookTags, "tag", nil, "Additional tag in key=value form (repeatable)")
	hookCmd.Flags().StringVar(&hookEventOverride, "event-override", "", "Override the positional event name")

	rootCmd.AddCommand(hookCmd)
}

// runHookCmd is the cobra RunE. All the real work is in runHookInternal so
// unit tests can drive the full code path with a custom stdin/stderr and
// inspect the exit code without spinning a subprocess.
func runHookCmd(cmd *cobra.Command, args []string) error {
	// ArbitraryArgs means args may be empty. runHookInternal handles the
	// empty-event case with a loud exit 2; a silent exit 1 from cobra's
	// strict validator would break the agent's tool call.
	var event string
	if len(args) > 0 {
		event = args[0]
	}
	opts := hookInvocation{
		event:         event,
		eventOverride: hookEventOverride,
		sessionID:     hookSessionID,
		projectPath:   hookProjectPath,
		agent:         hookAgent,
		tags:          hookTags,
		stdin:         cmd.InOrStdin(),
		stderr:        cmd.ErrOrStderr(),
		socketPath:    getSocketPath(cmd),
	}
	code := runHookInternal(opts)
	if code != 0 {
		// Cobra's Execute() returns whatever RunE returns; main.go exits 1
		// on non-nil error. For exit-2 paths we need our own os.Exit
		// before main() sees the error and clobbers the code.
		os.Exit(code)
	}
	return nil
}

// hookInvocation packages every parameter runHookInternal needs. Keeping
// this struct explicit (rather than reading globals) makes the test plan
// obvious: unit tests construct one and call runHookInternal directly.
type hookInvocation struct {
	event         string
	eventOverride string
	sessionID     string
	projectPath   string
	agent         string
	tags          []string
	stdin         io.Reader
	stderr        io.Writer
	socketPath    string

	// dropLogPath is the absolute path to append drop-log entries to on
	// the "wedged daemon" failure path. When empty, runHookInternal
	// defaults to `${XDG_CACHE_HOME:-$HOME/.cache}/agnt/hook-drop.log`.
	// Tests override this via t.Setenv("XDG_CACHE_HOME", ...) or by
	// passing an explicit path.
	dropLogPath string

	// now is an optional time source for deterministic drop-log
	// timestamps in tests. Nil falls back to time.Now.
	now func() time.Time
}

// runHookInternal is the testable core of `agnt hook`. It returns a process
// exit code and never calls os.Exit directly, so tests can assert on the
// return value without subprocess fork cost.
//
// Return code contract:
//
//	0  Success, or non-fatal failure the CLI must swallow (daemon down,
//	   wedged daemon, daemon rejection). These are Claude-hook failure
//	   modes where visible errors would break the agent's tool call.
//	2  User configuration error (missing event, malformed --tag). These
//	   must be loud during setup so the user notices the mistake.
func runHookInternal(opts hookInvocation) int {
	// Resolve event name. --event-override wins over the positional arg
	// so users can repoint a hook entry without editing the wiring.
	event := opts.event
	if opts.eventOverride != "" {
		event = opts.eventOverride
	}
	if event == "" {
		fmt.Fprintln(opts.stderr, "agnt hook: event name is required")
		return 2
	}

	// Assemble tags. Known flags are copied into the canonical tag keys
	// the daemon looks for when populating HookEvent.SessionID /
	// .ProjectPath / .Agent. Extra --tag entries layer on top so callers
	// can attach arbitrary context without a CLI change.
	tags, err := buildHookTags(opts.sessionID, opts.projectPath, opts.agent, opts.tags)
	if err != nil {
		fmt.Fprintf(opts.stderr, "agnt hook: %s\n", err.Error())
		return 2
	}

	// Read stdin to EOF. Empty stdin is a valid payload (some hook
	// events legitimately carry no body). Malformed JSON is passed
	// through verbatim — the daemon owns the decision of what to
	// reject, and the CLI's job is fire-and-forget enqueue.
	payload, err := io.ReadAll(opts.stdin)
	if err != nil {
		// Stdin read errors are treated as "daemon-side failure" —
		// there's nothing actionable for the hook caller and exit 2
		// here would poison the agent's tool call.
		return 0
	}

	// Pre-tool-use chain: when this is a PreToolUse event for the Bash
	// tool, pipe the payload through the check-bash interceptor inline
	// before enqueuing. This lets a single hook entry in settings.json
	// do both telemetry forwarding AND Bash redirection without a
	// separate shell pipeline. The chain respects the same 1s fail-open
	// budget — any check-bash error falls through to telemetry-only.
	if event == "pre-tool-use" && len(payload) > 0 {
		if code := maybeChainCheckBash(payload, tags, opts.stderr); code == 2 {
			// Block decision wins over telemetry: exit 2 immediately
			// so the agent's tool call is intercepted. Telemetry is
			// deliberately NOT forwarded in this case because the
			// tool call never happened from Claude's perspective, so
			// a PreToolUse telemetry event for a blocked call would
			// be misleading.
			return 2
		}
	}

	// Dial the daemon. Any connect error → silent exit 0. We
	// deliberately do NOT create a new client with a custom dialer
	// here: the daemon.Client's Connect() already handles both Unix
	// and Windows transports correctly, and HookSend opens its own
	// dedicated short-lived socket for the actual write.
	client := daemon.NewClient(daemon.WithSocketPath(opts.socketPath))
	if err := client.Connect(); err != nil {
		return 0
	}
	defer client.Close()

	// Single deadline for the whole hook enqueue. HookSend applies
	// hookSendDeadline (50ms) internally but will respect a tighter
	// caller context, which is what we want here in case the agent
	// has a shorter budget.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err = client.HookSend(ctx, event, json.RawMessage(payload), tags)
	if err == nil {
		return 0
	}

	// Daemon down → silent exit 0, no drop-log. This is the "agnt is
	// not running" case and the user is explicitly opting out of hook
	// routing; spamming a drop-log here would be noise.
	if errors.Is(err, daemon.ErrHookDaemonDown) {
		return 0
	}

	// Wedged daemon OR daemon-side rejection → silent exit 0 + one
	// drop-log line so the developer has something to correlate
	// missing events against when they go looking.
	writeHookDropLog(opts, event, err)
	return 0
}

// buildHookTags merges the typed flags (--session-id, --project-path,
// --agent) with repeatable --tag entries into a single map. Malformed
// --tag values (no "=") return an error so the caller can surface them
// as an exit-2 configuration error.
//
// Precedence: a typed flag wins over a --tag entry with the same key
// so the user-intent of --project-path=/foo is not silently overridden
// by a stray --tag project_path=/bar. This is consistent with the
// broader CLI convention that explicit flags outrank catch-all maps.
func buildHookTags(sessionID, projectPath, agent string, rawTags []string) (map[string]string, error) {
	var tags map[string]string
	ensure := func() {
		if tags == nil {
			tags = make(map[string]string, len(rawTags)+3)
		}
	}

	for _, raw := range rawTags {
		key, val, ok := strings.Cut(raw, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("malformed --tag %q: expected key=value", raw)
		}
		ensure()
		tags[key] = val
	}
	if sessionID != "" {
		ensure()
		tags["session_id"] = sessionID
	}
	if projectPath != "" {
		ensure()
		tags["project_path"] = projectPath
	}
	if agent != "" {
		ensure()
		tags["agent"] = agent
	}
	return tags, nil
}

// writeHookDropLog appends one line to the drop-log so developers can
// correlate missing hook events with buffer/daemon pressure. Errors are
// deliberately swallowed — we are already on the failure path, and a
// failing drop-log write must never turn into a visible hook error.
//
// The file is opened with O_APPEND so concurrent hook invocations
// cannot clobber each other: on Unix, a single write of <PIPE_BUF bytes
// is atomic against other appenders on the same inode. The drop line is
// short and the platform POSIX guarantee (PIPE_BUF ≥ 512) is the
// relevant invariant — we do not need an explicit lock.
func writeHookDropLog(opts hookInvocation, event string, cause error) {
	path := opts.dropLogPath
	if path == "" {
		var err error
		path, err = defaultHookDropLogPath()
		if err != nil {
			return
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()

	nowFn := opts.now
	if nowFn == nil {
		nowFn = time.Now
	}
	// Normalize newlines in the error message so a multi-line error
	// cannot inject extra log lines (and confuse line-counting tests).
	msg := strings.ReplaceAll(cause.Error(), "\n", " ")
	_, _ = fmt.Fprintf(f, "%s %s %s\n", nowFn().UTC().Format(time.RFC3339), event, msg)
}

// maybeChainCheckBash runs the inline Bash interceptor against a
// PreToolUse payload that has already been read from stdin. It reuses the
// check-bash decision logic via an in-memory strings.Reader so we do not
// fork a second process on the hot path — the whole chain must stay
// inside the same single-digit-ms budget as plain telemetry forwarding.
//
// Returns the exit code the outer hook dispatcher should use:
//   - 2 when a block decision fired (caller exits early, skipping
//     telemetry enqueue)
//   - 0 when the command was allowed or soft-warned (caller continues to
//     telemetry forwarding)
//
// The project path is pulled from tags["project_path"] which the standard
// Claude Code wiring populates via `--project-path $PWD`. When empty, the
// scope guard inside runCheckBashImpl still fires (fail-open for
// unattributed hooks).
func maybeChainCheckBash(payload []byte, tags map[string]string, stderr io.Writer) int {
	projectPath := ""
	if tags != nil {
		projectPath = tags["project_path"]
	}
	return runCheckBashImpl(bytesReader(payload), stderr, projectPath, os.Getenv)
}

// bytesReader wraps a byte slice in an io.Reader without depending on
// bytes.NewReader — keeps the chain allocation-free-ish and mirrors the
// shape of cmd.InOrStdin() which check-bash expects.
func bytesReader(b []byte) io.Reader {
	return &byteSliceReader{b: b}
}

type byteSliceReader struct {
	b   []byte
	pos int
}

func (r *byteSliceReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.pos:])
	r.pos += n
	return n, nil
}

// defaultHookDropLogPath resolves the drop-log location. XDG_CACHE_HOME
// wins over $HOME/.cache so tests can redirect the log with a single
// t.Setenv call without needing to inject a full path.
func defaultHookDropLogPath() (string, error) {
	if cacheDir, err := os.UserCacheDir(); err == nil && cacheDir != "" {
		return filepath.Join(cacheDir, "agnt", "hook-drop.log"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cache", "agnt", "hook-drop.log"), nil
}
