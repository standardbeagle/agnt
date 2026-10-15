package daemonclient

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
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

// A stale lock that cannot be removed made acquireLock call itself forever
// until the stack overflowed. It must now give up with an error naming the
// lock. The lock is made unremovable the way each OS allows: a non-empty
// directory in its place on Unix, an open handle on Windows.
func TestAcquireLock_UnremovableStaleLockFailsInsteadOfRecursing(t *testing.T) {
	u := NewDaemonUpgrader(UpgradeConfig{SocketPath: filepath.Join(t.TempDir(), "d.sock")})
	if runtime.GOOS == "windows" {
		f, err := os.Create(u.lockPath)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { f.Close() })
	} else if err := os.MkdirAll(filepath.Join(u.lockPath, "pin"), 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(u.lockPath, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(u.lockPath); err == nil {
		t.Skip("this filesystem removed the pinned lock; nothing to test")
	}
	err := u.acquireLock()
	if err == nil {
		u.releaseLock()
		t.Fatal("acquireLock succeeded over an unremovable lock")
	}
	if !strings.Contains(err.Error(), "lock") {
		t.Errorf("error should name the lock: %v", err)
	}
}
