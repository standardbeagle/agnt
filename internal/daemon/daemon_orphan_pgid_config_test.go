//go:build unix

package daemon

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestOrphanScan_ConfigDisabledByDefault verifies the test-safe default:
// a DaemonConfig with OrphanScanEnabled unset (zero value = false) makes
// the startup orphan-pgid scan bail out before walking /proc. This is the
// replacement for the AGNT_DISABLE_ORPHAN_SCAN env var fence and is the
// path used by every test in the suite that constructs a daemon via a
// literal DaemonConfig{} without explicitly opting in.
//
// The scan returns 0 killed without touching /proc, so the test is safe
// to run natively (no procisolation build tag needed). We build the
// Daemon via New() but deliberately skip Start() — the gate is evaluated
// before any of the fields that Start() populates are touched.
func TestOrphanScan_ConfigDisabledByDefault(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	d := New(DaemonConfig{
		SocketPath:         sockPath,
		MaxClients:         10,
		WriteTimeout:       5 * time.Second,
		CleanupGracePeriod: 10 * time.Millisecond,
		// OrphanScanEnabled intentionally unset — zero value (false)
		// means the scan is disabled. This is the test-safe default.
	})
	t.Cleanup(func() {
		// Cancel the context so any background goroutines New() started
		// wind down. No Start() was called, so Stop() is unnecessary.
		d.cancel()
	})

	// Direct call. With the gate disabled the scan must return 0 and must
	// log a skip decision (no silent failures rule from
	// daemon-architecture.md).
	killed := d.startupOrphanPGIDScan("")
	require.Equal(t, 0, killed, "disabled scan must not kill anything")

	entries := d.startupErrorStore.Query(StartupLogFilter{
		Level: "info",
		Limit: 50,
	})
	var foundSkip bool
	for _, e := range entries {
		if e.EventType == "startup_orphan_pgid_skipped_by_config" {
			foundSkip = true
			break
		}
	}
	require.True(t, foundSkip,
		"expected startup_orphan_pgid_skipped_by_config log entry when scan is disabled via config")
}

// TestOrphanScan_ConfigEnabledOptIn verifies the production opt-in:
// setting OrphanScanEnabled=true matches the production call site in
// cmd/agnt/daemon.go where the scan is wanted.
//
// We deliberately do NOT invoke startupOrphanPGIDScan here — that would
// walk host /proc and issue real kill(2) syscalls, which is the whole
// reason the gate exists. What we verify instead is the config-level
// invariant: explicit Enabled == scan enabled == production path. The
// behavioral test for the scan itself lives under the procisolation
// build tag (daemon_orphan_pgid_test.go).
func TestOrphanScan_ConfigEnabledOptIn(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	d := New(DaemonConfig{
		SocketPath:         sockPath,
		MaxClients:         10,
		WriteTimeout:       5 * time.Second,
		CleanupGracePeriod: 10 * time.Millisecond,
		OrphanScanEnabled:  true,
	})
	t.Cleanup(func() { d.cancel() })

	require.True(t, d.config.OrphanScanEnabled,
		"explicit OrphanScanEnabled=true must propagate to d.config (production opt-in path)")
}
