package daemon

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// NewForTest constructs a Daemon and brings it up via the minimum wiring
// shared with Start() (registerCommands, hub start, scheduler, URL tracker,
// proxy event loop, hook drain), then registers a t.Cleanup that calls
// Stop with a 5s timeout.
//
// The function takes *testing.T so it can ONLY be linked from _test.go
// files. That signature is the build-time fence — no production caller
// can construct a *testing.T, so a build tag is unnecessary. Callers
// outside test code that try to invoke NewForTest will fail compilation
// at the testing.T parameter.
//
// Test startup contract — what NewForTest SKIPS vs Start():
//
//	setupDebugLogging      — skipped: tests don't need the rotated log file
//	cleanupOrphans         — skipped: walks the host PID tracker, can kill
//	                         unrelated processes owned by the same uid
//	startupPortCleanup     — skipped: scans persisted proxy state and
//	                         issues kill(2) on whatever PID owns the port
//	startupOrphanPGIDScan  — skipped: walks /proc looking for pgids whose
//	                         leader is dead but members are alive (already
//	                         gated by OrphanScanEnabled which defaults to
//	                         false for tests; this skip is belt-and-braces)
//	restoreProxies         — skipped: replays persisted ProxyConfigs into
//	                         a fresh proxy manager, which a fresh test
//	                         daemon almost never wants
//	updateChecker.Start    — skipped: spawns a 24h ticker goroutine that
//	                         contacts GitHub
//
// Test startup contract — what NewForTest STILL RUNS:
//
//	registerCommands       — required: the hub needs every agnt verb
//	                         registered before it accepts connections
//	SetSessionCleanup      — required: tests that drop sessions need the
//	                         deferred-cleanup callback wired
//	hub.Start              — required: opens the unix socket / named pipe
//	                         and starts the accept loop
//	scheduler.Start        — required: tests that exercise the scheduler
//	                         need it running; cheap and idempotent
//	urlTracker.Start       — required: feeds the proxy event channel
//	handleProxyEvents      — required: drains d.proxyEvents
//	drainHooks             — required: drains hookRing for hook-event tests
//
// Production Start() runs the same bootstrap() helper plus the four
// skipped startup tasks plus updateChecker.Start, so production
// behavior is byte-identical with or without this split.
//
// See .claude/rules/daemon-architecture.md "Test startup contract" for
// the rationale and invariants this split must preserve.
func NewForTest(t *testing.T, cfg DaemonConfig) *Daemon {
	t.Helper()

	// monitorStartupFailure blocks the caller for the full StartupMonitorTimeout
	// (default 3s) on every SUCCESSFUL start — it only returns "no early failure"
	// once the deadline passes. Production wants that 3s crash-watch window; tests
	// assert process state directly and do not, so the default added ~3s to every
	// real-process test (dozens of them, serialized). A short window keeps
	// early-crash detection intact (the monitor polls every 100ms, so an
	// immediate `exit 1` is still caught on the first tick) while cutting the
	// healthy-start wait to a couple of ticks. Tests that specifically need a
	// longer crash-watch set StartupMonitorTimeout explicitly in their cfg.
	if cfg.StartupMonitorTimeout == 0 {
		cfg.StartupMonitorTimeout = 200 * time.Millisecond
	}
	// Keep the durable publish store hermetic: every test daemon gets its own
	// temp dir so publish state never touches the real ~/.local/state path and
	// tests can't cross-contaminate each other's shares.
	if cfg.PublishDir == "" {
		cfg.PublishDir = filepath.Join(t.TempDir(), "publish")
	}
	// Same hermetic discipline for the durable feedback store (P8/P9): each test
	// daemon gets its own temp dir so feedback state never touches the real
	// ~/.local/state path and tests can't cross-contaminate each other's rows.
	if cfg.FeedbackDir == "" {
		cfg.FeedbackDir = filepath.Join(t.TempDir(), "feedback")
	}
	// Same hermetic discipline for the control socket. An empty SocketPath falls
	// through hub.New to protocol.DefaultSocketPath() — the PRODUCTION socket —
	// so every NewForTest(t, DaemonConfig{}) caller bound the SAME fixed path.
	// Sequentially (the real `-p 1` gate, one process) each test's t.Cleanup
	// Stop frees the socket before the next binds it, so the collision stayed
	// latent. Two concurrent test binaries (the repro in this file's owning
	// task) race for that one path and the loser's bootstrap fails with
	// "daemon already running" — an instant failure, not a timeout. It also
	// means these tests would fight a real agnt daemon running on the dev box.
	// A per-test temp socket removes the shared global path, closing the class
	// for good; callers that need a specific path (e.g. the default-socket
	// pid-tracker behaviour) still set SocketPath explicitly and are untouched.
	if cfg.SocketPath == "" {
		cfg.SocketPath = filepath.Join(t.TempDir(), "d.sock")
	}

	d := New(cfg)

	if err := d.bootstrap(); err != nil {
		t.Fatalf("daemon.NewForTest: bootstrap failed: %v", err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = d.Stop(ctx)
	})

	return d
}
