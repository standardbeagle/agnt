// Package sockdir gives tests a directory short enough to hold a Unix-domain
// socket.
//
// A Unix socket's path lives in sockaddr_un.sun_path, a fixed array: 104 bytes
// on macOS and the BSDs, 108 on Linux and for AF_UNIX on Windows, including
// the terminating NUL. bind and connect fail with "invalid argument" on a
// longer path. t.TempDir() is the wrong parent for a socket because it embeds
// the test's name under the system temp directory, and on macOS that directory
// alone is about 50 bytes (/var/folders/xx/<32 chars>/T/): a socket named
// after a long test name does not fit.
//
// The package has no dependencies beyond the standard library and is a plain
// (non-_test) file so that every package's tests, and the cross-package
// harnesses that are themselves plain files (internal/daemon/test_helpers.go),
// can import it. The testing.TB parameter is the fence that keeps production
// code from calling it (.claude/rules/testing-conventions.md §2). It is kept
// out of internal/testutil proper so that importing it does not pull that
// package's net/http/httptest dependency into a non-test build.
package sockdir

import (
	"os"
	"runtime"
	"testing"
)

// MaxPath is the longest socket path that works on every supported OS: the
// smallest sun_path (104 bytes, macOS) less the terminating NUL.
const MaxPath = 103

// NameBudget is how many bytes of path a test may add under Short's directory
// and still be inside MaxPath: the separator plus a file name, or a nested
// path such as ".agnt/ssh/<host>.ctl". TestShortLeavesRoomForASocketName
// holds Short to it.
const NameBudget = 64

// Short returns a new, empty directory whose path is short enough that a
// socket created under it fits in sun_path (see NameBudget), and removes it
// when the test ends. Use it wherever a test binds or dials a Unix socket;
// keep t.TempDir() for everything else.
//
// The directory is created under Base().
func Short(t testing.TB) string {
	t.Helper()
	dir, err := os.MkdirTemp(Base(), "s")
	if err != nil {
		t.Fatalf("sockdir: creating a short socket directory under %s: %v", Base(), err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// Base is the directory Short creates its directories in.
//
//   - Unix: /tmp when it is a directory this process can create entries in
//     (it is on Linux and macOS), deliberately ignoring TMPDIR, which is the
//     long path on macOS. Otherwise the system temp directory, and the guard
//     test says so if that is too long.
//   - Windows: C:\tmp, created on demand. %TEMP% is under the user profile
//     (C:\Users\<name>\AppData\Local\Temp) and long user names push socket
//     paths over the limit.
func Base() string {
	if runtime.GOOS == "windows" {
		const base = `C:\tmp`
		if err := os.MkdirAll(base, 0o755); err == nil {
			return base
		}
		return os.TempDir()
	}
	if usableDir("/tmp") {
		return "/tmp"
	}
	return os.TempDir()
}

// usableDir reports whether dir exists and this process can create an entry in
// it, checked by doing so: permission bits alone do not answer for ACLs or a
// read-only mount.
func usableDir(dir string) bool {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return false
	}
	probe, err := os.MkdirTemp(dir, "s")
	if err != nil {
		return false
	}
	_ = os.Remove(probe)
	return true
}
