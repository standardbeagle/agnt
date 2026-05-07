//go:build unix

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/daemon"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- helpers ----------------------------------------------------------------

// redirectDropLog points runHookInternal's drop-log path at a temp file
// inside t.TempDir() via XDG_CACHE_HOME. Returns the exact absolute path
// where the drop-log will end up so tests can stat it directly.
func redirectDropLog(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmp)
	return filepath.Join(tmp, "agnt", "hook-drop.log")
}

// readDropLogLines returns the non-empty lines of the drop-log at path, or
// empty slice if the file does not exist. Makes the "exactly one line"
// invariant a simple len() check in each test.
func readDropLogLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		t.Fatalf("read drop-log %s: %v", path, err)
	}
	var lines []string
	for _, l := range strings.Split(string(data), "\n") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

// startTestHookDaemon starts a real in-process daemon on a tempdir socket
// and wires a captureHookSink to its AlertHub. Tests that need the
// daemon-side round trip assert against the sink's recorded events.
// Returns the socket path, the sink, and a cleanup function.
func startTestHookDaemon(t *testing.T) (sockPath string, sink *captureHookSinkCLI) {
	t.Helper()
	return startTestHookDaemonTB(t)
}

// startTestHookDaemonTB is the testing.TB variant shared with the
// benchmark in hook_bench_test.go. Both *testing.T and *testing.B
// implement testing.TB so this factors out the one t.Cleanup call.
func startTestHookDaemonTB(tb testing.TB) (sockPath string, sink *captureHookSinkCLI) {
	tb.Helper()
	tmpDir := tb.TempDir()
	sockPath = filepath.Join(tmpDir, "agnt.sock")

	d := daemon.New(daemon.DaemonConfig{
		SocketPath:   sockPath,
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	})
	if err := d.Start(); err != nil {
		tb.Fatalf("start test daemon: %v", err)
	}
	tb.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = d.Stop(ctx)
	})

	sink = newCaptureHookSinkCLI()
	d.AlertHub().AddHookSink(sink)
	return sockPath, sink
}

// captureHookSinkCLI is a minimal HookEventSink that records every hook
// event it receives into a slice guarded by a mutex. Duplicate of the
// daemon-package test sink, kept local to this _test.go file because the
// daemon-package type is not exported.
type captureHookSinkCLI struct {
	mu     sync.Mutex
	events []daemon.HookEvent
	notify chan struct{}
}

func newCaptureHookSinkCLI() *captureHookSinkCLI {
	return &captureHookSinkCLI{notify: make(chan struct{}, 64)}
}

func (s *captureHookSinkCLI) EmitHookEvent(ev daemon.HookEvent) {
	s.mu.Lock()
	s.events = append(s.events, ev)
	s.mu.Unlock()
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

func (s *captureHookSinkCLI) waitFor(t *testing.T, n int, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		s.mu.Lock()
		got := len(s.events)
		s.mu.Unlock()
		if got >= n {
			return
		}
		select {
		case <-s.notify:
		case <-deadline:
			t.Fatalf("timed out waiting for %d hook events (got %d)", n, got)
		}
	}
}

func (s *captureHookSinkCLI) snapshot() []daemon.HookEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]daemon.HookEvent, len(s.events))
	copy(out, s.events)
	return out
}

// --- buildHookTags unit tests -----------------------------------------------

// TestBuildHookTags_MergeAndPrecedence asserts that typed flags
// (--session-id, --project-path, --agent) outrank repeatable --tag
// entries with the same key and that unknown keys pass through untouched.
func TestBuildHookTags_MergeAndPrecedence(t *testing.T) {
	tags, err := buildHookTags("sess-1", "/proj", "claude", []string{
		"extra=yes",
		"session_id=SHOULD-BE-OVERRIDDEN",
		"agent=also-overridden",
	})
	require.NoError(t, err)
	assert.Equal(t, "sess-1", tags["session_id"], "typed --session-id wins")
	assert.Equal(t, "/proj", tags["project_path"], "typed --project-path is set")
	assert.Equal(t, "claude", tags["agent"], "typed --agent wins")
	assert.Equal(t, "yes", tags["extra"], "unknown key passes through")
}

// TestBuildHookTags_MalformedTag rejects an entry with no = delimiter.
func TestBuildHookTags_MalformedTag(t *testing.T) {
	_, err := buildHookTags("", "", "", []string{"notkvpair"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "notkvpair")
}

// TestBuildHookTags_EmptyKey rejects "=value" because a tag key of ""
// would land in the map as a no-op that still silently swallows the value.
func TestBuildHookTags_EmptyKey(t *testing.T) {
	_, err := buildHookTags("", "", "", []string{"=orphan"})
	require.Error(t, err)
}

// TestBuildHookTags_NoFlags returns a nil map (not an empty allocation)
// when no tags are provided, so the hot path doesn't allocate for hook
// calls that carry no metadata.
func TestBuildHookTags_NoFlags(t *testing.T) {
	tags, err := buildHookTags("", "", "", nil)
	require.NoError(t, err)
	assert.Nil(t, tags)
}

// TestBuildHookTags_TagWithEqualsInValue allows = in the value half of
// a tag — strings.Cut splits on the first delimiter, so a value like
// "k=a=b" becomes k=a=b.
func TestBuildHookTags_TagWithEqualsInValue(t *testing.T) {
	tags, err := buildHookTags("", "", "", []string{"k=a=b"})
	require.NoError(t, err)
	assert.Equal(t, "a=b", tags["k"])
}

// --- runHookInternal exit-code tests ----------------------------------------

// TestHook_MissingEventArg asserts that empty event (after --event-override
// wipe) produces exit 2 and a stderr message. The cobra ExactArgs(1)
// guard catches the positional case; this test covers the override path.
func TestHook_MissingEventArg(t *testing.T) {
	_ = redirectDropLog(t) // isolate cache path

	stderr := &bytes.Buffer{}
	code := runHookInternal(hookInvocation{
		event:         "",
		eventOverride: "",
		stdin:         strings.NewReader(`{}`),
		stderr:        stderr,
		socketPath:    "/nonexistent/agnt.sock",
	})
	assert.Equal(t, 2, code)
	assert.Contains(t, stderr.String(), "event")
}

// TestHook_CobraArgs_AcceptsAnyArity is the regression for the silent
// "Failed with non-blocking status code: No stderr output" bug. Before
// the fix, cobra.ExactArgs(1) + SilenceErrors would reject a bare
// `agnt hook` before RunE ever ran, main() would os.Exit(1), and Claude
// Code would see exit 1 + empty stderr. The fix is to use ArbitraryArgs
// so the empty-event path reaches runHookInternal (which handles it with
// a visible exit 2 + stderr message) and extra positional args — e.g.
// from a shell expansion that produced two tokens instead of one — are
// silently accepted.
//
// We assert against the Args validator directly rather than driving
// hookCmd.Execute(): Execute() walks to rootCmd and parses os.Args, so
// SetArgs on a child command doesn't do what it looks like it does, and
// runHookInternal calls os.Exit(2) on the empty-event path which would
// kill the test binary. The Args validator is the exact contract this
// test guards, so checking it directly is both more precise and safer.
func TestHook_CobraArgs_AcceptsAnyArity(t *testing.T) {
	require.NotNil(t, hookCmd.Args, "hookCmd.Args must be set")

	cases := [][]string{
		nil,                             // zero args — the original failure mode
		{},                              // empty slice — same shape, different spelling
		{"pre-tool-use"},                // the normal case
		{"pre-tool-use", "extra-token"}, // shell expansion producing extras
	}
	for _, args := range cases {
		err := hookCmd.Args(hookCmd, args)
		assert.NoError(t, err, "Args validator must accept args=%v", args)
	}
}

// TestHook_MalformedTag asserts that a malformed --tag entry produces
// exit 2 before the CLI ever touches the daemon.
func TestHook_MalformedTag(t *testing.T) {
	_ = redirectDropLog(t)

	stderr := &bytes.Buffer{}
	code := runHookInternal(hookInvocation{
		event:      "pre-tool-use",
		tags:       []string{"notkvpair"},
		stdin:      strings.NewReader(`{}`),
		stderr:     stderr,
		socketPath: "/nonexistent/agnt.sock",
	})
	assert.Equal(t, 2, code)
	assert.Contains(t, stderr.String(), "notkvpair")
}

// TestHook_DaemonDown_ExitsZeroSilently asserts the daemon-not-running
// case: exit 0, empty stderr, and specifically NO drop-log entry. This
// is the "user explicitly has no daemon" case and a drop-log here would
// be noise on every single hook call.
func TestHook_DaemonDown_ExitsZeroSilently(t *testing.T) {
	dropLog := redirectDropLog(t)
	tmp := t.TempDir()
	sock := filepath.Join(tmp, "does-not-exist.sock")

	stderr := &bytes.Buffer{}
	start := time.Now()
	code := runHookInternal(hookInvocation{
		event:      "pre-tool-use",
		stdin:      strings.NewReader(`{"tool":"Bash"}`),
		stderr:     stderr,
		socketPath: sock,
	})
	elapsed := time.Since(start)

	assert.Equal(t, 0, code, "daemon-down must exit 0 silently")
	assert.Empty(t, stderr.String(), "daemon-down must produce no stderr")
	assert.Empty(t, readDropLogLines(t, dropLog), "daemon-down must NOT touch the drop-log")
	// Sanity check the latency budget. A connect to a missing unix
	// socket is near-instant; 1s is a generous bound that only fires
	// if something is very wrong (e.g. hung DNS on a misconfigured
	// transport).
	assert.Less(t, elapsed, time.Second, "daemon-down exit took too long: %s", elapsed)
}

// TestHook_WedgedDaemon_ExitsZeroWithDropLog starts a listener that
// accepts but never reads, runs the CLI against it, and asserts:
//   - exit 0
//   - the HookSend deadline trips and the CLI drops exactly one line
//     in the drop-log
//   - total wall clock stays inside a hook deadline budget (55ms
//     HookSend deadline + headroom)
func TestHook_WedgedDaemon_ExitsZeroWithDropLog(t *testing.T) {
	dropLog := redirectDropLog(t)

	// A tempdir sock path keeps the test hermetic.
	tmp := t.TempDir()
	sock := filepath.Join(tmp, "wedged.sock")
	ln, err := net.Listen("unix", sock)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	// Park every incoming connection forever so the client's
	// SetDeadline is the only thing that makes progress. We hold
	// references to the accepted conns in a slice so t.Cleanup
	// can close them after the test.
	var (
		acceptedMu sync.Mutex
		accepted   []net.Conn
		done       = make(chan struct{})
	)
	t.Cleanup(func() {
		close(done)
		acceptedMu.Lock()
		for _, c := range accepted {
			_ = c.Close()
		}
		acceptedMu.Unlock()
	})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			acceptedMu.Lock()
			accepted = append(accepted, c)
			acceptedMu.Unlock()
			// Intentionally do NOT read. The client's write+ack
			// will hit its deadline.
		}
	}()

	stderr := &bytes.Buffer{}
	start := time.Now()
	code := runHookInternal(hookInvocation{
		event:      "pre-tool-use",
		stdin:      strings.NewReader(`{"tool":"Bash"}`),
		stderr:     stderr,
		socketPath: sock,
	})
	elapsed := time.Since(start)

	assert.Equal(t, 0, code, "wedged daemon must still exit 0")
	assert.Empty(t, stderr.String(), "wedged daemon must not print to stderr")

	lines := readDropLogLines(t, dropLog)
	assert.Len(t, lines, 1, "wedged daemon must produce exactly one drop-log line (got %v)", lines)
	if len(lines) == 1 {
		assert.Contains(t, lines[0], "pre-tool-use", "drop-log line must include the event name")
	}
	// Budget: HookSend deadline is 50ms, runHookInternal's context
	// timeout is 50ms. 500ms gives comfortable headroom for CI jitter
	// without hiding a real regression.
	assert.Less(t, elapsed, 500*time.Millisecond, "wedged daemon exit took too long: %s", elapsed)
}

// TestHook_PayloadFidelity_RoundTrip runs a full CLI → daemon → sink
// round trip and asserts the enqueued event carries the exact payload
// bytes and merged tags. This is the load-bearing end-to-end test that
// phase 1's daemon unit tests cannot cover by themselves.
func TestHook_PayloadFidelity_RoundTrip(t *testing.T) {
	sock, sink := startTestHookDaemon(t)
	_ = redirectDropLog(t)

	payload := `{"tool":"Bash","command":"echo hi"}`
	stderr := &bytes.Buffer{}
	code := runHookInternal(hookInvocation{
		event:       "pre-tool-use",
		sessionID:   "sess-42",
		projectPath: "/proj/root",
		agent:       "claude",
		tags:        []string{"extra=ok"},
		stdin:       strings.NewReader(payload),
		stderr:      stderr,
		socketPath:  sock,
	})
	require.Equal(t, 0, code)
	assert.Empty(t, stderr.String())

	sink.waitFor(t, 1, 2*time.Second)
	events := sink.snapshot()
	require.Len(t, events, 1)
	ev := events[0]
	assert.Equal(t, "pre-tool-use", ev.Event)
	assert.Equal(t, "sess-42", ev.SessionID)
	assert.Equal(t, "/proj/root", ev.ProjectPath)
	assert.Equal(t, "claude", ev.Agent)
	assert.Equal(t, "ok", ev.Tags["extra"], "unknown tag should survive round trip")
	assert.JSONEq(t, payload, string(ev.Payload), "payload bytes must round-trip verbatim")
}

// TestHook_EmptyStdin_IsValid asserts that reading zero bytes from stdin
// still produces a successful enqueue (the hook event carries no
// payload but is otherwise a valid enqueue).
func TestHook_EmptyStdin_IsValid(t *testing.T) {
	sock, sink := startTestHookDaemon(t)
	_ = redirectDropLog(t)

	code := runHookInternal(hookInvocation{
		event:      "Stop",
		stdin:      strings.NewReader(""),
		stderr:     io.Discard,
		socketPath: sock,
	})
	require.Equal(t, 0, code)

	sink.waitFor(t, 1, 2*time.Second)
	events := sink.snapshot()
	require.Len(t, events, 1)
	assert.Equal(t, "Stop", events[0].Event)
	assert.Empty(t, events[0].Payload, "empty stdin produces empty payload")
}

// TestHook_EventOverride asserts --event-override replaces the positional
// event name in the enqueued HookEvent.
func TestHook_EventOverride(t *testing.T) {
	sock, sink := startTestHookDaemon(t)
	_ = redirectDropLog(t)

	code := runHookInternal(hookInvocation{
		event:         "pre-tool-use",
		eventOverride: "Stop",
		stdin:         strings.NewReader(`{}`),
		stderr:        io.Discard,
		socketPath:    sock,
	})
	require.Equal(t, 0, code)

	sink.waitFor(t, 1, 2*time.Second)
	events := sink.snapshot()
	require.Len(t, events, 1)
	assert.Equal(t, "Stop", events[0].Event)
}

// TestHook_MalformedJSONStdin asserts that non-JSON stdin is routed to
// the drop-log + exit 0 path rather than the exit-2 config-error path.
// The CLI's exit-2 branch is reserved for argument validation only;
// a garbage payload is a daemon-side rejection which hooks must swallow.
func TestHook_MalformedJSONStdin(t *testing.T) {
	sock, _ := startTestHookDaemon(t)
	dropLog := redirectDropLog(t)

	stderr := &bytes.Buffer{}
	code := runHookInternal(hookInvocation{
		event:      "pre-tool-use",
		stdin:      strings.NewReader("not json at all"),
		stderr:     stderr,
		socketPath: sock,
	})
	assert.Equal(t, 0, code, "malformed stdin must still exit 0")
	assert.Empty(t, stderr.String())

	// Daemon-side rejection → drop-log line. The daemon returns an
	// error response; the CLI maps that to the drop-log + exit 0
	// path because it's indistinguishable from the wedged-daemon case
	// from the hook caller's perspective.
	lines := readDropLogLines(t, dropLog)
	assert.Len(t, lines, 1, "malformed JSON must produce exactly one drop-log line")
}

// TestHook_DropLog_DeterministicNow uses a frozen clock and asserts the
// drop-log line begins with an RFC3339 UTC timestamp.
func TestHook_DropLog_DeterministicNow(t *testing.T) {
	dropLog := redirectDropLog(t)

	fixed := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	opts := hookInvocation{now: func() time.Time { return fixed }, dropLogPath: dropLog}
	writeHookDropLog(opts, "pre-tool-use", errors.New("simulated failure"))

	lines := readDropLogLines(t, dropLog)
	require.Len(t, lines, 1)
	assert.True(t, strings.HasPrefix(lines[0], "2030-01-02T03:04:05Z"),
		"drop-log line must begin with RFC3339 UTC timestamp, got %q", lines[0])
	assert.Contains(t, lines[0], "pre-tool-use")
	assert.Contains(t, lines[0], "simulated failure")
}

// TestHook_DropLog_NormalizesNewlines asserts that a multi-line error
// message gets collapsed into a single physical line so readers that
// line-count drops don't get fooled by embedded \n.
func TestHook_DropLog_NormalizesNewlines(t *testing.T) {
	dropLog := redirectDropLog(t)
	writeHookDropLog(
		hookInvocation{dropLogPath: dropLog},
		"Stop",
		errors.New("first\nsecond\nthird"),
	)

	lines := readDropLogLines(t, dropLog)
	require.Len(t, lines, 1, "multi-line error must collapse to exactly one log line")
	assert.Contains(t, lines[0], "first second third")
}

// TestHook_LatencyAgainstWarmDaemon measures the wall clock of the
// runHookInternal hot path (minus process fork) against a warm test
// daemon. The budget is loose (50ms p99) because we run hundreds of
// calls back-to-back and the final deadline comes from the warm-daemon
// contribution. Process fork cost is excluded.
//
// This is a smoke test rather than a strict benchmark — the bench lives
// in hook_bench_test.go so `go test -bench` has an opt-in knob.
func TestHook_LatencyAgainstWarmDaemon(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sock, sink := startTestHookDaemon(t)
	_ = redirectDropLog(t)

	const iterations = 50
	var maxDur time.Duration
	for i := 0; i < iterations; i++ {
		start := time.Now()
		code := runHookInternal(hookInvocation{
			event:      "pre-tool-use",
			stdin:      strings.NewReader(fmt.Sprintf(`{"i":%d}`, i)),
			stderr:     io.Discard,
			socketPath: sock,
		})
		elapsed := time.Since(start)
		if elapsed > maxDur {
			maxDur = elapsed
		}
		require.Equal(t, 0, code)
	}

	// 5s wait covers concurrent test load (pre-commit runs all package
	// tests in parallel; on a busy host the drain goroutine schedule
	// slips past 2s for the last 1-2 events). The actual latency budget
	// is asserted below via maxDur — this wait is just for sink
	// reconciliation, not a perf gate.
	sink.waitFor(t, iterations, 5*time.Second)
	require.Len(t, sink.snapshot(), iterations)

	// 100ms is well above the 5ms p99 target in the task spec. This test
	// catches order-of-magnitude regressions, not the final latency budget
	// (that's the benchmark's job). Budget raised from 50ms: pre-commit runs
	// packages concurrently so worst-case across 50 iterations can spike
	// under load without indicating a real regression.
	assert.Less(t, maxDur, 100*time.Millisecond,
		"warm-daemon hook roundtrip worst case %s exceeds 100ms smoke budget", maxDur)
}

// TestHook_MultipleInvocations_DropLogAppends asserts that the drop-log
// truly appends (O_APPEND) rather than overwriting, so repeated wedged-
// daemon calls accumulate one line each.
func TestHook_MultipleInvocations_DropLogAppends(t *testing.T) {
	dropLog := redirectDropLog(t)

	for i := 0; i < 3; i++ {
		writeHookDropLog(
			hookInvocation{dropLogPath: dropLog},
			"pre-tool-use",
			fmt.Errorf("call %d failed", i),
		)
	}
	lines := readDropLogLines(t, dropLog)
	assert.Len(t, lines, 3)
}

// TestHook_DropLog_MissingParentDir_IsCreated asserts the drop-log
// writer creates any missing intermediate directories (XDG_CACHE_HOME
// points at a tempdir, and /agnt/ is the missing child).
func TestHook_DropLog_MissingParentDir_IsCreated(t *testing.T) {
	dropLog := redirectDropLog(t)
	// The parent directory (tempdir/agnt) does not exist yet; the
	// writer must mkdir -p it.
	_, statErr := os.Stat(filepath.Dir(dropLog))
	require.True(t, errors.Is(statErr, os.ErrNotExist), "pre-condition: parent dir must not exist")

	writeHookDropLog(
		hookInvocation{dropLogPath: dropLog},
		"pre-tool-use",
		errors.New("boom"),
	)
	_, err := os.Stat(dropLog)
	require.NoError(t, err, "drop-log file must have been created")
}

// --- defense-in-depth: buildHookTags quoting edge cases ---------------------

// TestBuildHookTags_EmptyValueIsAllowed permits "k=" because a caller
// may legitimately want to set a tag to the empty string (e.g. to
// signal "I ran this hook but there is no session yet").
func TestBuildHookTags_EmptyValueIsAllowed(t *testing.T) {
	tags, err := buildHookTags("", "", "", []string{"k="})
	require.NoError(t, err)
	got, ok := tags["k"]
	assert.True(t, ok, "empty value must still populate the key")
	assert.Equal(t, "", got)
}

// TestHook_SilentFailureDoesNotPanic is a paranoia test: runHookInternal
// must never panic, regardless of how degenerate the inputs are.
func TestHook_SilentFailureDoesNotPanic(t *testing.T) {
	_ = redirectDropLog(t)
	assert.NotPanics(t, func() {
		_ = runHookInternal(hookInvocation{
			event:      "x",
			stdin:      errReader{err: errors.New("stdin broke")},
			stderr:     io.Discard,
			socketPath: "/nonexistent",
		})
	})
}

// errReader is an io.Reader that always returns an error. Used to
// exercise the "stdin read failed" branch of runHookInternal.
type errReader struct{ err error }

func (e errReader) Read(_ []byte) (int, error) { return 0, e.err }

// --- probe HookSend sentinels from this package ----------------------------

// TestHook_UsesDaemonSentinels double-checks that this package really
// imports daemon.ErrHookDaemonDown / daemon.ErrHookDeadline so a phase-3
// refactor cannot silently drop the wiring.
func TestHook_UsesDaemonSentinels(t *testing.T) {
	require.NotNil(t, daemon.ErrHookDaemonDown)
	require.NotNil(t, daemon.ErrHookDeadline)
	assert.True(t, errors.Is(
		fmt.Errorf("wrap: %w", daemon.ErrHookDaemonDown),
		daemon.ErrHookDaemonDown,
	))
}

// --- guard rails: json.RawMessage passthrough -------------------------------

// TestHook_PayloadIsJSONRawMessage_NotString asserts the payload survives
// the CLI → daemon round trip as raw JSON (object), not as an escaped
// string. A regression where the CLI double-marshals the bytes would
// turn {"tool":"Bash"} into "\"{\\\"tool\\\":\\\"Bash\\\"}\"".
func TestHook_PayloadIsJSONRawMessage_NotString(t *testing.T) {
	sock, sink := startTestHookDaemon(t)
	_ = redirectDropLog(t)

	code := runHookInternal(hookInvocation{
		event:      "pre-tool-use",
		stdin:      strings.NewReader(`{"tool":"Bash"}`),
		stderr:     io.Discard,
		socketPath: sock,
	})
	require.Equal(t, 0, code)

	sink.waitFor(t, 1, 2*time.Second)
	events := sink.snapshot()
	require.Len(t, events, 1)

	var parsed map[string]string
	require.NoError(t, json.Unmarshal(events[0].Payload, &parsed),
		"payload must unmarshal as a JSON object, not a quoted string")
	assert.Equal(t, "Bash", parsed["tool"])
}

// --- concurrency probe: drop-log is append-safe ----------------------------

// TestHook_DropLog_ConcurrentAppends fires N goroutines appending
// simultaneously and asserts all N lines land in the file. On Unix with
// O_APPEND and sub-PIPE_BUF writes this is guaranteed to be atomic.
func TestHook_DropLog_ConcurrentAppends(t *testing.T) {
	dropLog := redirectDropLog(t)
	const n = 20

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			writeHookDropLog(
				hookInvocation{dropLogPath: dropLog},
				"pre-tool-use",
				fmt.Errorf("concurrent %d", i),
			)
		}(i)
	}
	wg.Wait()

	lines := readDropLogLines(t, dropLog)
	assert.Len(t, lines, n, "every concurrent append must land as its own line")
}

// --- internal safety: ensure no stray goroutine from runHookInternal ------

// TestHook_NoLeakedGoroutines is a minimal smoke check: after a
// daemon-down invocation the goroutine count should return to baseline
// inside a short grace window. Catches a regression where a forgotten
// `go func() { ... }()` survives exit.
func TestHook_NoLeakedGoroutines(t *testing.T) {
	_ = redirectDropLog(t)
	baseline := countGoroutines()

	for i := 0; i < 5; i++ {
		_ = runHookInternal(hookInvocation{
			event:      "pre-tool-use",
			stdin:      strings.NewReader(`{}`),
			stderr:     io.Discard,
			socketPath: "/nonexistent.sock",
		})
	}

	// Allow a very short grace period for any one-shot goroutines to
	// finalize before we sample.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if countGoroutines() <= baseline+2 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	// A small drift is tolerable (test framework goroutines, etc.).
	// We only fail on a clear leak.
	after := countGoroutines()
	assert.Less(t, after-baseline, 10,
		"goroutine count drifted from %d to %d after daemon-down hooks", baseline, after)
}

// countGoroutines returns runtime.NumGoroutine. Wrapped so tests read
// cleanly without a runtime call sprinkled through the test body.
func countGoroutines() int { return runtime.NumGoroutine() }
