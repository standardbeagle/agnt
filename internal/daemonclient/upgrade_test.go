package daemonclient

import (
	"os"
	"path/filepath"
	"testing"
)

// The upgrader must spawn the replacement daemon through the same provisioning
// step as autostart, which refreshes the `agnt-daemon` copy from the current
// binary. Picking up whatever `<exe>-daemon` already sits on disk restarted the
// stale pre-upgrade copy (and on Windows looked for `agnt.exe-daemon`, which
// never exists, so it self-exec'd instead).
func TestUpgraderSpawnsProvisionedDaemonCopy(t *testing.T) {
	fresh := filepath.Join(t.TempDir(), "agnt-daemon")
	orig := provisionDaemonBinary
	provisionDaemonBinary = func() string { return fresh }
	t.Cleanup(func() { provisionDaemonBinary = orig })

	u := NewDaemonUpgrader(UpgradeConfig{SocketPath: filepath.Join(t.TempDir(), "d.sock")})
	if got := u.autoStartConfig().toLibraryConfig().HubPath; got != fresh {
		t.Fatalf("upgrade spawns %q, want the provisioned copy %q", got, fresh)
	}
}

// An explicit NewBinaryPath still wins over provisioning.
func TestUpgraderSpawnsExplicitNewBinary(t *testing.T) {
	orig := provisionDaemonBinary
	provisionDaemonBinary = func() string { t.Fatal("provisioning must not run when NewBinaryPath is set"); return "" }
	t.Cleanup(func() { provisionDaemonBinary = orig })

	explicit := filepath.Join(t.TempDir(), "new-agnt")
	u := NewDaemonUpgrader(UpgradeConfig{SocketPath: filepath.Join(t.TempDir(), "d.sock"), NewBinaryPath: explicit})
	if got := u.autoStartConfig().toLibraryConfig().HubPath; got != explicit {
		t.Fatalf("upgrade spawns %q, want NewBinaryPath %q", got, explicit)
	}
}

// go-cli-server's socket.Manager writes its PID file at <socket>.pid on every
// platform. The upgrader's Windows branch derived <dir>/<name-without-.sock>.pid
// instead, so it removed a file that never exists and left the real one behind.
func TestUpgraderCleanupRemovesLibraryPIDFile(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "agnt.sock")
	pidFile := sock + ".pid"
	if err := os.WriteFile(pidFile, []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}

	NewDaemonUpgrader(UpgradeConfig{SocketPath: sock}).cleanupStaleFiles()

	if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
		t.Fatalf("PID file %s still present after cleanup (stat err: %v)", pidFile, err)
	}
}
