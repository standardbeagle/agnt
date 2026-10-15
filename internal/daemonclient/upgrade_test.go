package daemonclient

import (
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
