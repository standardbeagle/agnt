package daemon

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestEnsureDaemonBinary_ReturnsEmptyUnderTest(t *testing.T) {
	// Provisioning must never copy the test binary. testing.Testing() is true
	// here, so both the exported and unexported entry points short-circuit.
	if got := ensureDaemonBinary(); got != "" {
		t.Fatalf("ensureDaemonBinary under test should return \"\", got %q", got)
	}
	if got := ResolveDaemonBinary(); got != "" {
		t.Fatalf("ResolveDaemonBinary under test should return \"\", got %q", got)
	}
}

func TestDaemonCopyPath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/usr/local/bin/agnt", "/usr/local/bin/agnt-daemon"},
		{"/home/u/.local/bin/agnt", "/home/u/.local/bin/agnt-daemon"},
		{`C:\tools\agnt.exe`, `C:\tools\agnt-daemon.exe`},
		{"agnt.exe", "agnt-daemon.exe"},
	}
	for _, c := range cases {
		if got := daemonCopyPath(c.in); got != c.want {
			t.Errorf("daemonCopyPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDaemonBinaryFresh(t *testing.T) {
	dir := t.TempDir()
	self := filepath.Join(dir, "agnt")
	dst := filepath.Join(dir, "agnt-daemon")

	writeBinFile(t, self, []byte("SELFSELF")) // 8 bytes

	// Missing copy → not fresh.
	if daemonBinaryFresh(self, dst) {
		t.Fatal("missing copy should not be fresh")
	}

	// Identical size, copy no older than self → fresh.
	writeBinFile(t, dst, []byte("COPYCOPY")) // 8 bytes
	touchAfter(t, self, dst)                 // dst mtime >= self mtime
	if !daemonBinaryFresh(self, dst) {
		t.Fatal("same-size, not-older copy should be fresh")
	}

	// Different size → not fresh (version changed the binary).
	writeBinFile(t, dst, []byte("SHORT"))
	if daemonBinaryFresh(self, dst) {
		t.Fatal("different-size copy should not be fresh")
	}

	// Same size but copy older than self (stale after a CLI upgrade) → not fresh.
	writeBinFile(t, dst, []byte("COPYCOPY"))
	touchAfter(t, dst, self) // self mtime > dst mtime
	if daemonBinaryFresh(self, dst) {
		t.Fatal("older copy should not be fresh")
	}
}

func TestCopyExecutable(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "agnt")
	dst := filepath.Join(dir, "agnt-daemon")
	content := []byte("BINARYPAYLOAD-0123456789")
	writeBinFile(t, src, content)

	if err := copyExecutable(src, dst); err != nil {
		t.Fatalf("copyExecutable: %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("copied content mismatch: got %q want %q", got, content)
	}

	// Copy must be executable (owner exec bit) on unix.
	if runtime.GOOS != "windows" {
		info, err := os.Stat(dst)
		if err != nil {
			t.Fatalf("stat dst: %v", err)
		}
		if info.Mode().Perm()&0o100 == 0 {
			t.Fatalf("copy not executable: mode %v", info.Mode())
		}
	}

	// No leftover temp files in the destination directory.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if name := e.Name(); name != "agnt" && name != "agnt-daemon" {
			t.Fatalf("unexpected leftover file after copy: %q", name)
		}
	}

	// Overwriting an existing copy succeeds (atomic rename over dst).
	writeBinFile(t, src, []byte("NEWERPAYLOAD"))
	if err := copyExecutable(src, dst); err != nil {
		t.Fatalf("copyExecutable overwrite: %v", err)
	}
	got, _ = os.ReadFile(dst)
	if string(got) != "NEWERPAYLOAD" {
		t.Fatalf("overwrite content mismatch: got %q", got)
	}
}

func TestRemoveStaleTemps(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "agnt")
	dst := filepath.Join(dir, "agnt-daemon")
	writeBinFile(t, src, []byte("PAYLOAD"))

	// A temp file orphaned by a SIGKILLed provisioner (old mtime) must be
	// reaped; a fresh temp (a concurrent provisioner's in-flight copy) and
	// unrelated files must survive.
	stale := filepath.Join(dir, ".agnt-daemon-1111")
	fresh := filepath.Join(dir, ".agnt-daemon-2222")
	other := filepath.Join(dir, "unrelated.txt")
	writeBinFile(t, stale, []byte("partial"))
	writeBinFile(t, fresh, []byte("partial"))
	writeBinFile(t, other, []byte("keep"))
	old := time.Now().Add(-staleTempAge - time.Minute)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}

	if err := copyExecutable(src, dst); err != nil {
		t.Fatalf("copyExecutable: %v", err)
	}

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale temp should be reaped, stat err = %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("fresh temp must survive (in-flight concurrent copy): %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("unrelated file must survive: %v", err)
	}
	if got, _ := os.ReadFile(dst); string(got) != "PAYLOAD" {
		t.Fatalf("dst content mismatch: %q", got)
	}
}

func writeBinFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// touchAfter sets later's mtime strictly after earlier's so freshness
// comparisons are deterministic regardless of filesystem timestamp resolution.
func touchAfter(t *testing.T, earlier, later string) {
	t.Helper()
	base := time.Now()
	if err := os.Chtimes(earlier, base, base); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(later, base.Add(time.Second), base.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
}
