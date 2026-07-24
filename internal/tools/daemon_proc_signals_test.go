package tools

import (
	"strings"
	"testing"

	"github.com/standardbeagle/agnt/internal/daemonclient"
)

// newTestDaemonTools constructs a DaemonTools with a fixed socket path so
// buildWatchCommand tests can assert against the embedded path.
func newTestDaemonTools() *DaemonTools {
	return NewDaemonTools(daemonclient.AutoStartConfig{
		SocketPath: "/run/user/1000/agnt.sock",
	}, "0.13.4")
}

// TestExtractSignals_URL verifies the url signal pulls localhost and http(s)://
// URLs out of typical dev-server output.
func TestExtractSignals_URL(t *testing.T) {
	lines := []string{
		"Server listening at http://localhost:3000",
		"  ➜  Network:  use --host to expose",
		"App running at https://example.dev/",
		"  Local:   http://127.0.0.1:5173/",
	}
	got := extractSignals(lines, []string{"url"})
	if len(got.URLs) < 3 {
		t.Fatalf("want >=3 urls, got %d: %v", len(got.URLs), got.URLs)
	}
	want := []string{
		"http://localhost:3000",
		"https://example.dev/",
		"http://127.0.0.1:5173/",
	}
	for _, w := range want {
		if !containsURL(got.URLs, w) {
			t.Errorf("missing url %q in %v", w, got.URLs)
		}
	}
}

// TestExtractSignals_Ready covers the canonical ready phrases: "ready",
// "listening on", "started", "compiled successfully".
func TestExtractSignals_Ready(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
		want  bool
	}{
		{"vite-ready", []string{"  VITE v5.0.0  ready in 432 ms"}, true},
		{"node-listening", []string{"Server listening on port 3000"}, true},
		{"webpack-compiled", []string{"webpack compiled successfully"}, true},
		{"started", []string{"Application started in 1.2s"}, true},
		{"none", []string{"compiling client...", "running migrations"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractSignals(tc.lines, []string{"ready"})
			if got.Ready != tc.want {
				t.Errorf("ready=%v, want %v (lines=%v, matched=%q)", got.Ready, tc.want, tc.lines, got.ReadyLine)
			}
			if tc.want && got.ReadyLine == "" {
				t.Error("ready=true but ReadyLine empty")
			}
		})
	}
}

// TestExtractSignals_Error covers compile errors and ERROR lines. Reuses the
// AlertScanner pattern bank intent (we don't import it, but we cover the same
// rough categories: ERROR, panic, traceback).
func TestExtractSignals_Error(t *testing.T) {
	lines := []string{
		"src/foo.ts:12:3 - error TS2322: Type 'string' not assignable",
		"  some normal output",
		"ERROR in ./src/bar.js",
		"panic: runtime error: index out of range",
	}
	got := extractSignals(lines, []string{"error"})
	if len(got.Errors) < 3 {
		t.Fatalf("want >=3 errors, got %d: %v", len(got.Errors), got.Errors)
	}
}

// TestExtractSignals_Warning matches WARN/warning: lines.
func TestExtractSignals_Warning(t *testing.T) {
	lines := []string{
		"WARN: deprecated API used",
		"warning: unused variable 'x'",
		"  some normal output",
	}
	got := extractSignals(lines, []string{"warning"})
	if len(got.Warnings) < 2 {
		t.Fatalf("want >=2 warnings, got %d: %v", len(got.Warnings), got.Warnings)
	}
}

// TestExtractSignals_Port matches "port 3000" and ":3000" patterns.
func TestExtractSignals_Port(t *testing.T) {
	lines := []string{
		"Server listening on port 3000",
		"binding ::8080",
		"unrelated line",
		"now serving at http://localhost:5173/",
	}
	got := extractSignals(lines, []string{"port"})
	if len(got.Ports) < 2 {
		t.Fatalf("want >=2 ports, got %d: %v", len(got.Ports), got.Ports)
	}
	wantPorts := map[int]bool{3000: false, 8080: false, 5173: false}
	for _, p := range got.Ports {
		if _, ok := wantPorts[p]; ok {
			wantPorts[p] = true
		}
	}
	for port, found := range wantPorts {
		if !found {
			t.Errorf("missing port %d in %v", port, got.Ports)
		}
	}
}

// TestExtractSignals_All combines several signals in one call to verify the
// signals struct holds them simultaneously.
func TestExtractSignals_All(t *testing.T) {
	lines := []string{
		"Server listening at http://localhost:3000",
		"WARN: deprecated API",
		"ERROR in build",
	}
	got := extractSignals(lines, []string{"url", "warning", "error", "ready", "port"})
	if len(got.URLs) == 0 {
		t.Error("urls empty")
	}
	if len(got.Warnings) == 0 {
		t.Error("warnings empty")
	}
	if len(got.Errors) == 0 {
		t.Error("errors empty")
	}
	if !got.Ready {
		t.Error("ready=false, want true (listening)")
	}
	if len(got.Ports) == 0 {
		t.Error("ports empty")
	}
}

// TestExtractSignals_NoneRequested is a no-op when extract list is empty.
func TestExtractSignals_NoneRequested(t *testing.T) {
	lines := []string{"Server listening at http://localhost:3000"}
	got := extractSignals(lines, nil)
	if len(got.URLs) != 0 || len(got.Errors) != 0 || got.Ready {
		t.Errorf("expected empty signals, got %+v", got)
	}
}

// TestExtractSignals_UnknownSignal is silent (returns empty for that signal,
// no error). Validates lenient behavior.
func TestExtractSignals_UnknownSignal(t *testing.T) {
	lines := []string{"hello"}
	got := extractSignals(lines, []string{"bogus", "url"})
	// urls should still be empty (no URL in input), but no panic and no error.
	if len(got.URLs) != 0 {
		t.Errorf("urls should be empty: %v", got.URLs)
	}
}

// TestSignalMatchesAny verifies the wait-action matcher logic: given a
// signals payload, return the first matching signal name from the wanted list.
func TestSignalMatchesAny(t *testing.T) {
	signals := SignalSet{
		Ready:     true,
		ReadyLine: "vite ready in 432 ms",
		Errors:    []string{"ERROR in build"},
	}
	cases := []struct {
		name      string
		wanted    []string
		wantMatch string
		wantLine  string
	}{
		{"ready-first", []string{"ready", "error"}, "ready", "vite ready in 432 ms"},
		{"error-first", []string{"error", "ready"}, "error", "ERROR in build"},
		{"only-ready", []string{"ready"}, "ready", "vite ready in 432 ms"},
		{"no-match", []string{"warning"}, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			match, line := signalMatchesAny(signals, tc.wanted)
			if match != tc.wantMatch || line != tc.wantLine {
				t.Errorf("got (%q, %q), want (%q, %q)", match, line, tc.wantMatch, tc.wantLine)
			}
		})
	}
}

// TestFormatMultiStreamCompact verifies the multi-process compact output uses
// [process_id] line prefixes.
func TestFormatMultiStreamCompact(t *testing.T) {
	streams := []processStream{
		{ProcessID: "build", Lines: []string{"compiling...", "build OK"}},
		{ProcessID: "server", Lines: []string{"listening on :3000"}},
	}
	got := formatMultiStreamCompact(streams)
	for _, want := range []string{
		"[build] compiling...",
		"[build] build OK",
		"[server] listening on :3000",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// TestFormatMultiStreamNDJSON verifies raw mode emits one JSON object per line.
func TestFormatMultiStreamNDJSON(t *testing.T) {
	streams := []processStream{
		{ProcessID: "build", Lines: []string{"line1"}},
		{ProcessID: "server", Lines: []string{"line2", "line3"}},
	}
	got := formatMultiStreamNDJSON(streams)
	gotLines := strings.Split(strings.TrimSpace(got), "\n")
	if len(gotLines) != 3 {
		t.Fatalf("want 3 NDJSON lines, got %d:\n%s", len(gotLines), got)
	}
	for _, line := range gotLines {
		if !strings.HasPrefix(line, "{") || !strings.HasSuffix(line, "}") {
			t.Errorf("not a JSON object: %s", line)
		}
		if !strings.Contains(line, `"process_id"`) || !strings.Contains(line, `"line"`) {
			t.Errorf("missing process_id/line key: %s", line)
		}
	}
}

// TestBuildWatchCommand_MultiProcessIDs verifies the watch tool builds a
// single monitor command with comma-joined process_ids.
func TestBuildWatchCommand_MultiProcessIDs(t *testing.T) {
	dt := newTestDaemonTools()
	input := WatchInput{Target: "process", ProcessIDs: []string{"build", "server"}}
	cmd, _, err := buildWatchCommand(dt, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(cmd, "--process build,server") {
		t.Errorf("want '--process build,server', got: %s", cmd)
	}
}

// TestBuildWatchCommand_MultiProxyIDs verifies the watch tool builds a single
// monitor command with comma-joined proxy_ids.
func TestBuildWatchCommand_MultiProxyIDs(t *testing.T) {
	dt := newTestDaemonTools()
	input := WatchInput{Target: "errors", ProxyIDs: []string{"frontend", "api"}}
	cmd, _, err := buildWatchCommand(dt, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(cmd, "--proxy frontend,api") {
		t.Errorf("want '--proxy frontend,api', got: %s", cmd)
	}
}

// TestAssembleMultiStreamOutput_Compact verifies the assembled output
// for the multi-stream proc-output action: compact prefix lines, signal
// extraction per process, no NDJSON.
func TestAssembleMultiStreamOutput_Compact(t *testing.T) {
	streams := []processStream{
		{ProcessID: "build", Lines: []string{"compiling...", "build OK"}},
		{ProcessID: "server", Lines: []string{"Server listening at http://localhost:3000"}},
	}
	out := assembleMultiStreamOutput(streams, []string{"url", "ready"}, false)

	// Compact output present.
	if !strings.Contains(out.Output, "[build] compiling...") {
		t.Errorf("missing compact build line: %q", out.Output)
	}
	if !strings.Contains(out.Output, "[server] Server listening") {
		t.Errorf("missing compact server line: %q", out.Output)
	}
	// MultiStream populated with per-process signals.
	if len(out.MultiStream) != 2 {
		t.Fatalf("want 2 multi-stream entries, got %d", len(out.MultiStream))
	}
	server := findStream(out.MultiStream, "server")
	if server == nil {
		t.Fatal("server stream missing")
	}
	if server.Signals == nil || len(server.Signals.URLs) == 0 {
		t.Errorf("server should have url signal, got %+v", server.Signals)
	}
	if server.Signals == nil || !server.Signals.Ready {
		t.Errorf("server should have ready=true, got %+v", server.Signals)
	}
	build := findStream(out.MultiStream, "build")
	if build == nil {
		t.Fatal("build stream missing")
	}
	// build has no URL/ready content — signals struct should exist
	// (extract was requested) but URLs slice is empty and Ready=false.
	if build.Signals == nil {
		t.Error("build should have non-nil signals (extract was requested)")
	} else if len(build.Signals.URLs) != 0 || build.Signals.Ready {
		t.Errorf("build should have empty signals, got %+v", build.Signals)
	}
}

// TestAssembleMultiStreamOutput_Raw verifies raw=true emits NDJSON instead
// of the compact prefix format.
func TestAssembleMultiStreamOutput_Raw(t *testing.T) {
	streams := []processStream{
		{ProcessID: "build", Lines: []string{"line1"}},
	}
	out := assembleMultiStreamOutput(streams, nil, true)
	if !strings.HasPrefix(strings.TrimSpace(out.Output), "{") {
		t.Errorf("raw output should be NDJSON, got: %s", out.Output)
	}
	if !strings.Contains(out.Output, `"process_id":"build"`) {
		t.Errorf("missing process_id key: %s", out.Output)
	}
}

// TestAssembleMultiStreamOutput_Errors keeps successful streams and
// surfaces per-process errors. One failing process must not block the rest.
func TestAssembleMultiStreamOutput_Errors(t *testing.T) {
	streams := []processStream{
		{ProcessID: "good", Lines: []string{"ok"}},
		{ProcessID: "bad", Err: "process not found"},
	}
	out := assembleMultiStreamOutput(streams, nil, false)
	if !strings.Contains(out.Output, "[good] ok") {
		t.Errorf("good stream missing: %s", out.Output)
	}
	if !strings.Contains(out.Output, "[bad] (error: process not found)") {
		t.Errorf("bad stream error not surfaced: %s", out.Output)
	}
}

// TestWaitForSignal_HitImmediately verifies the wait helper returns
// immediately when the first poll already shows the signal.
func TestWaitForSignal_HitImmediately(t *testing.T) {
	calls := 0
	fetch := func() ([]string, error) {
		calls++
		return []string{"Server listening on port 3000"}, nil
	}
	res := waitForSignal(fetch, []string{"ready"}, 1000, 50)
	if res.TimedOut {
		t.Error("should not have timed out")
	}
	if res.Signal != "ready" {
		t.Errorf("signal=%q, want 'ready'", res.Signal)
	}
	if res.MatchedLine == "" {
		t.Error("matched_line empty")
	}
	if calls != 1 {
		t.Errorf("expected 1 fetch call (immediate hit), got %d", calls)
	}
}

// TestWaitForSignal_HitAfterPolls verifies the helper polls until the
// signal appears.
func TestWaitForSignal_HitAfterPolls(t *testing.T) {
	calls := 0
	fetch := func() ([]string, error) {
		calls++
		if calls < 3 {
			return []string{"compiling..."}, nil
		}
		return []string{"compiling...", "ready in 432 ms"}, nil
	}
	res := waitForSignal(fetch, []string{"ready"}, 2000, 20)
	if res.TimedOut {
		t.Errorf("should have matched after %d polls, timed out instead", calls)
	}
	if res.Signal != "ready" {
		t.Errorf("signal=%q, want 'ready'", res.Signal)
	}
	if calls < 3 {
		t.Errorf("expected >=3 polls, got %d", calls)
	}
}

// TestWaitForSignal_Timeout verifies the helper returns timed_out=true
// when the signal never appears within the budget.
func TestWaitForSignal_Timeout(t *testing.T) {
	fetch := func() ([]string, error) {
		return []string{"still compiling..."}, nil
	}
	res := waitForSignal(fetch, []string{"ready"}, 100, 30)
	if !res.TimedOut {
		t.Error("expected timed_out=true")
	}
	if res.Signal != "" {
		t.Errorf("signal should be empty on timeout, got %q", res.Signal)
	}
	if res.ElapsedMs <= 0 {
		t.Errorf("elapsed_ms should be positive, got %d", res.ElapsedMs)
	}
}

// TestWaitForSignal_FirstWins verifies "wait for whichever comes first"
// when multiple signals are requested.
func TestWaitForSignal_FirstWins(t *testing.T) {
	calls := 0
	fetch := func() ([]string, error) {
		calls++
		return []string{"ERROR in build"}, nil
	}
	res := waitForSignal(fetch, []string{"ready", "error"}, 500, 30)
	if res.TimedOut {
		t.Error("should have matched 'error', timed out instead")
	}
	if res.Signal != "error" {
		t.Errorf("signal=%q, want 'error'", res.Signal)
	}
}

// TestBuildWatchCommand_ProcessIDsTakesPrecedenceOverProcessID asserts that
// when both are set, process_ids wins (or the singular is appended). We pick
// "process_ids wins" — the singular is for back-compat only.
func TestBuildWatchCommand_ProcessIDsAndSingular(t *testing.T) {
	dt := newTestDaemonTools()
	input := WatchInput{Target: "process", ProcessID: "ignored", ProcessIDs: []string{"a", "b"}}
	cmd, _, err := buildWatchCommand(dt, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(cmd, "--process a,b") {
		t.Errorf("want '--process a,b', got: %s", cmd)
	}
	if strings.Contains(cmd, "ignored") {
		t.Errorf("singular process_id should not appear: %s", cmd)
	}
}

// helpers ---------------------------------------------------------

func containsURL(urls []string, target string) bool {
	for _, u := range urls {
		if u == target {
			return true
		}
	}
	return false
}

func findStream(streams []processStream, id string) *processStream {
	for i := range streams {
		if streams[i].ProcessID == id {
			return &streams[i]
		}
	}
	return nil
}
