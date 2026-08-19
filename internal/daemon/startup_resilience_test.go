package daemon

import (
	"context"
	"runtime"
	"testing"
	"time"

	goprocess "github.com/standardbeagle/go-cli-server/process"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMonitorStartupFailure_EarlySuccessOnURLDetected proves the positive-
// readiness early exit: once the process's own output has yielded a serving
// URL (URLTracker), monitorStartupFailure returns success WITHOUT waiting out
// the crash-watch window.
//
// Determinism without a wall-clock assertion: the deadline is set to an
// effectively-never value (10 minutes) and the ready signal (a seeded URL) is
// injected before the call. If monitorStartupFailure returns nil, it can ONLY
// be via the affirmative-health early exit — the deadline path is unreachable
// within the test's own 10s guard. We assert the OUTCOME (nil) plus "returned
// before the deadline could ever fire", never an elapsed-time bound.
func TestMonitorStartupFailure_EarlySuccessOnURLDetected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /bin/sh")
	}

	d := NewForTest(t, DaemonConfig{
		SocketPath:   shortSockPath(t),
		MaxClients:   4,
		WriteTimeout: 2 * time.Second,
	})

	ctx := context.Background()
	const procID = "monitor-early-success"
	proc, err := d.hub.ProcessManager().StartCommand(ctx, goprocess.ProcessConfig{
		ID:          procID,
		ProjectPath: t.TempDir(),
		Command:     "/bin/sh",
		Args:        stayAliveArgs(), // healthy: stays alive, never crashes
	})
	require.NoError(t, err)
	defer func() { _ = d.hub.ProcessManager().StopProcess(context.Background(), proc) }()

	// Inject the affirmative-health signal: the process announced a serving URL
	// in its own output. (URLTracker normally populates this from a real scan;
	// seeding it directly makes the readiness event deterministic and decoupled
	// from the 500ms scan interval.)
	d.urlTracker.mu.Lock()
	d.urlTracker.urls[procID] = []string{"http://localhost:5173/"}
	d.urlTracker.mu.Unlock()

	// A deadline that cannot fire during the test. If monitorStartupFailure
	// returns nil, it is the URL early-exit, not the timeout.
	neverDeadline := 10 * time.Minute

	done := make(chan *StartupError, 1)
	go func() { done <- d.monitorStartupFailure(ctx, proc, 0, neverDeadline) }()

	select {
	case gotErr := <-done:
		assert.Nil(t, gotErr,
			"a process that has announced a serving URL must be reported healthy")
	case <-time.After(10 * time.Second):
		t.Fatal("monitorStartupFailure did not return early on URL detection — " +
			"it blocked toward the 10-minute deadline instead of taking the readiness exit")
	}
}

// TestMonitorStartupFailure_PreservesCrashDetection proves the early-success
// path did NOT weaken crash detection. Two cases:
//
//   - immediate `exit 1`: caught on the first poll tick.
//   - delayed `sleep 0.3; exit 1`: MUTATION guard. During its first few ticks
//     the process is "not crashed yet" and has produced no URL. If the
//     early-success predicate were relaxed to "still alive and no error =
//     ready" (the not-yet-crashed trap), this case would return nil at ~100ms.
//     Because readiness requires an AFFIRMATIVE signal (a detected URL), the
//     monitor keeps watching and catches the exit at ~300ms.
//
// We assert the failure's PROVENANCE (ErrorType == "startup_failed"), not
// merely that some error was returned.
func TestMonitorStartupFailure_PreservesCrashDetection(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /bin/sh")
	}

	d := NewForTest(t, DaemonConfig{
		SocketPath:   shortSockPath(t),
		MaxClients:   4,
		WriteTimeout: 2 * time.Second,
	})

	cases := []struct {
		name string
		id   string
		args string
	}{
		{"immediate_exit", "monitor-crash-immediate", "exit 1"},
		{"delayed_exit", "monitor-crash-delayed", "sleep 0.3; exit 1"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			proc, err := d.hub.ProcessManager().StartCommand(ctx, goprocess.ProcessConfig{
				ID:          tc.id,
				ProjectPath: t.TempDir(),
				Command:     "/bin/sh",
				Args:        []string{"-c", tc.args},
			})
			require.NoError(t, err)
			defer func() { _ = d.hub.ProcessManager().StopProcess(context.Background(), proc) }()

			// No URL is ever seeded, so the only way to return is via crash
			// detection. The 5s window is a generous ceiling: if crash detection
			// were broken the delayed case would fall through to the deadline and
			// return nil, failing the assertion below.
			gotErr := d.monitorStartupFailure(ctx, proc, 0, 5*time.Second)
			require.NotNil(t, gotErr, "an exiting process must be reported as a startup failure")
			assert.Equal(t, "startup_failed", gotErr.ErrorType,
				"crash must be attributed to the startup-failure path")
		})
	}
}

func TestExtractPortFromCommand(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		command  string
		args     []string
		expected int
	}{
		{"next dev with -p", "npm", []string{"run", "dev"}, 0}, // No port in args
		{"next dev with -p 3000", "next", []string{"dev", "-p", "3000"}, 3000},
		{"vite with --port", "vite", []string{"--port", "5173"}, 5173},
		{"vite with --port=", "vite", []string{"--port=5173"}, 5173},
		{"npm run with port in args", "npm", []string{"run", "dev", "--", "-p", "4000"}, 4000},
		{"go run", "go", []string{"run", "main.go"}, 0},
		{"localhost:port pattern", "node", []string{"server.js", "--host", "localhost:8080"}, 8080},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractPortFromCommand(tt.command, tt.args)
			if got != tt.expected {
				t.Errorf("extractPortFromCommand(%q, %v) = %d, want %d", tt.command, tt.args, got, tt.expected)
			}
		})
	}
}

func TestExtractPortFromPackageJsonScript(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		script   string
		expected int
	}{
		{"next dev -p 3465", "next dev -p 3465", 3465},
		{"next dev -p 3000", "next dev -p 3000", 3000},
		{"vite --port 5173", "vite --port 5173", 5173},
		{"vite --port=8080", "vite --port=8080", 8080},
		{"just next dev", "next dev", 0},
		{"PORT=3000 node", "PORT=3000 node server.js", 3000},
		{"webpack dev server", "webpack serve --port 9000", 9000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractPortFromCommand(tt.script, nil)
			if got != tt.expected {
				t.Errorf("extractPortFromCommand(%q, nil) = %d, want %d", tt.script, got, tt.expected)
			}
		})
	}
}

func TestDetectEADDRINUSE(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		output   string
		expected int
	}{
		{
			"Next.js EADDRINUSE",
			`Error: listen EADDRINUSE: address already in use :::3465`,
			3465,
		},
		{
			"Node.js EADDRINUSE",
			`Error: listen EADDRINUSE: address already in use 127.0.0.1:3000`,
			3000,
		},
		{
			"Generic address in use",
			`Failed to start server\nError: address already in use :8080`,
			8080,
		},
		{
			"Port already in use message",
			`Error: port 4000 is already in use`,
			4000,
		},
		{
			"No error",
			`Server started successfully on port 3000`,
			0,
		},
		{
			"Empty",
			"",
			0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectEADDRINUSE(tt.output)
			if got != tt.expected {
				t.Errorf("detectEADDRINUSE(%q) = %d, want %d", tt.output, got, tt.expected)
			}
		})
	}
}

func TestLastLines(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "", lastLines("", 5))
	assert.Equal(t, "", lastLines("   \n  \n  ", 5))
	assert.Equal(t, "hello", lastLines("hello", 5))
	assert.Equal(t, "line1\nline2\nline3", lastLines("line1\nline2\nline3", 5))
	assert.Equal(t, "line2\nline3", lastLines("line1\nline2\nline3", 2))
	assert.Equal(t, "a\nb\nc", lastLines("\n\na\n\nb\n\nc\n\n", 5), "filters empty lines")
	assert.Equal(t, "c", lastLines("a\nb\nc", 1))
}
