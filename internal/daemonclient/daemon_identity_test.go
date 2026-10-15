package daemonclient

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestIsAgntDaemonArgv pins the identity rule the stale-PID kill path relies
// on. Every "refuse" row is a process the old substring matcher ("agnt"
// anywhere in the command line / image path) claimed, and would have killed
// after PID reuse.
func TestIsAgntDaemonArgv(t *testing.T) {
	const sock = "/tmp/agnt-1000/agnt.sock"
	cases := []struct {
		name string
		exe  string
		argv []string
		want bool
	}{
		{"autostart spawn", "/home/u/go/bin/agnt-daemon",
			[]string{"/home/u/go/bin/agnt-daemon", "daemon", "start", "--socket", sock}, true},
		{"manual start, default socket", "/usr/local/bin/agnt",
			[]string{"agnt", "daemon", "start"}, true},
		{"root flags first", "/usr/local/bin/agnt",
			[]string{"agnt", "--socket=" + sock, "-d", "daemon", "start"}, true},
		{"windows copy", `C:\Users\u\go\bin\agnt-daemon.exe`,
			[]string{`C:\Users\u\go\bin\agnt-daemon.exe`, "daemon", "start", "--socket", sock}, true},
		{"legacy alias", "/usr/bin/devtool-mcp",
			[]string{"devtool-mcp", "daemon", "start"}, true},

		{"editor with agnt in path", "/usr/bin/vim",
			[]string{"vim", "/home/u/work/agnt/main.go"}, false},
		{"node under an agnt checkout", `C:\work\agnt\node_modules\.bin\node.exe`,
			[]string{"node", "vite"}, false},
		{"agnt mcp server", "/usr/local/bin/agnt",
			[]string{"agnt", "mcp"}, false},
		{"agnt run wrapper", "/usr/local/bin/agnt",
			[]string{"agnt", "run", "claude", "daemon", "start"}, false},
		{"daemon stop", "/usr/local/bin/agnt",
			[]string{"agnt", "daemon", "stop"}, false},
		{"other socket's daemon", "/usr/local/bin/agnt",
			[]string{"agnt", "daemon", "start", "--socket", "/tmp/TestX/agnt.sock"}, false},
		{"binary merely named like agnt", "/opt/agnt/bin/agnt-helper",
			[]string{"/opt/agnt/bin/agnt-helper", "daemon", "start"}, false},
		{"empty argv", "/usr/local/bin/agnt", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isAgntDaemonArgv(tc.exe, tc.argv, sock); got != tc.want {
				t.Errorf("isAgntDaemonArgv(%q, %q) = %v, want %v", tc.exe, tc.argv, got, tc.want)
			}
		})
	}
}

// TestAgntDaemonMatcher_NeverClaimsSelf: a stale PID file can name the PID
// this process now holds. The matcher must refuse it even when the process
// looks exactly like a daemon, or checkExisting terminates the caller.
// Not parallel: swaps the package-level processIdentity seam.
func TestAgntDaemonMatcher_NeverClaimsSelf(t *testing.T) {
	const sock = "/tmp/agnt-1000/agnt.sock"
	orig := processIdentity
	t.Cleanup(func() { processIdentity = orig })
	processIdentity = func(pid int) (string, []string, bool) {
		return "/usr/local/bin/agnt", []string{"/usr/local/bin/agnt", "daemon", "start", "--socket", sock}, true
	}

	m := agntDaemonMatcher(sock)
	if !m(os.Getpid() + 1) {
		t.Fatalf("premise broken: matcher refused a daemon-shaped foreign PID")
	}
	if m(os.Getpid()) {
		t.Fatalf("matcher claimed the current process (pid %d) as a stale daemon", os.Getpid())
	}
	if m(0) || m(-1) {
		t.Fatalf("matcher claimed a non-positive PID")
	}
}

// TestReadProcessIdentity_Self exercises the real platform reader on this
// test process (which is not an agnt daemon, so the matcher must refuse it).
// No process is signalled.
func TestReadProcessIdentity_Self(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		t.Skipf("no process identity reader on %s (matcher always declines)", runtime.GOOS)
	}
	exe, argv, ok := readProcessIdentity(os.Getpid())
	if !ok {
		t.Fatalf("readProcessIdentity(self) failed")
	}
	if len(argv) != len(os.Args) {
		t.Fatalf("argv = %q, want %q", argv, os.Args)
	}
	want := filepath.Base(os.Args[0])
	if runtime.GOOS == "windows" {
		self, _ := os.Executable()
		want = filepath.Base(self)
	}
	if filepath.Base(exe) != want {
		t.Fatalf("exe = %q, want basename %q", exe, want)
	}
	if isAgntDaemonArgv(exe, argv, "") {
		t.Fatalf("test binary identified as an agnt daemon: %q %q", exe, argv)
	}
}
