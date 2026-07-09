package daemon

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/debug"
)

// ensureDaemonBinary returns the path to a fresh `agnt-daemon` copy sitting
// next to the current executable, provisioning or refreshing it as needed.
//
// Why this exists: sandboxes such as Claude Code forbid a binary from
// forking/exec'ing itself, so the daemon must be spawned from a separate
// binary (core architecture decision #1). The go-cli-server auto-starter
// already *prefers* an `<exe>-daemon` copy when one exists next to the
// executable, but nothing ever created that copy outside `make` — a user who
// runs `go install .../cmd/agnt@latest` gets only `agnt`, so autostart falls
// back to self-exec and silently fails inside the sandbox. Worse, a plain
// `go install` upgrade refreshes `agnt` but leaves a stale `agnt-daemon`
// behind, so autostart spawns an old daemon (observed: agnt v0.12.50 spawning
// an agnt-daemon v0.10.0). Provisioning here makes `go install` sufficient and
// eliminates the version skew: the copy is always byte-identical to the CLI
// that spawns it.
//
// Returns "" to signal "let the caller fall back to its default" (running
// under `go test`, an unresolvable executable, or a copy failure). Callers pass
// the result as the library's explicit HubPath; an empty HubPath keeps the
// library's own lookup/self-exec behaviour unchanged.
func ensureDaemonBinary() string {
	// Never provision from a test binary: copying a `*.test` executable to
	// `<exe>-daemon` and spawning it with `daemon start` args would re-run the
	// whole suite in a detached process. testing.Testing() is true only inside
	// `go test`; the testing package is already linked into the daemon build
	// (test_helpers.go), so this costs nothing in the production binary.
	if testing.Testing() {
		return ""
	}

	self, err := os.Executable()
	if err != nil {
		debug.Log("daemon", "ensureDaemonBinary: os.Executable failed: %v", err)
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		self = resolved
	}

	// Belt-and-braces guard for oddly-named test/build binaries that slip past
	// testing.Testing() (e.g. a manually-run compiled test binary).
	if base := filepath.Base(self); strings.HasSuffix(base, ".test") || strings.Contains(self, "/go-build") {
		return ""
	}

	daemonPath := daemonCopyPath(self)
	if daemonBinaryFresh(self, daemonPath) {
		return daemonPath
	}
	if err := copyExecutable(self, daemonPath); err != nil {
		// Provisioning is best-effort: on any failure (read-only install dir,
		// permissions) fall back to the library default rather than blocking
		// autostart entirely.
		debug.Log("daemon", "ensureDaemonBinary: copy %s -> %s failed: %v", self, daemonPath, err)
		return ""
	}
	debug.Log("daemon", "ensureDaemonBinary: provisioned daemon copy %s", daemonPath)
	return daemonPath
}

// ResolveDaemonBinary provisions (or refreshes) the `agnt-daemon` copy and
// returns its path, or "" when the caller should fall back to spawning the
// current executable itself. Exported for command-layer spawners (`agnt up`)
// that build their own detached daemon command rather than going through
// AutoStartClient.
func ResolveDaemonBinary() string {
	return ensureDaemonBinary()
}

// daemonCopyPath derives the `-daemon` sibling path, preserving any executable
// extension so the copy is runnable on Windows (agnt.exe -> agnt-daemon.exe).
func daemonCopyPath(self string) string {
	ext := filepath.Ext(self) // ".exe" on Windows, "" elsewhere
	return strings.TrimSuffix(self, ext) + "-daemon" + ext
}

// daemonBinaryFresh reports whether the existing daemon copy already matches the
// current executable. A copy is fresh when it exists, has the same size, and is
// no older than the CLI. Size differs across versions (the embedded version
// string alone changes the binary), and a `go install` upgrade always leaves
// the new CLI with a newer mtime than the stale copy, so this catches both the
// missing and the stale cases without reading 60MB of binary on every spawn.
func daemonBinaryFresh(self, daemonPath string) bool {
	ds, err := os.Stat(daemonPath)
	if err != nil {
		return false // missing (or unstat-able) → refresh
	}
	ss, err := os.Stat(self)
	if err != nil {
		return false
	}
	return ds.Size() == ss.Size() && !ds.ModTime().Before(ss.ModTime())
}

// copyExecutable copies src to dst atomically: it writes to a temp file in the
// destination directory, chmods it executable, then renames over dst. The
// rename is atomic, so a concurrent spawner never observes a half-written
// binary, and two processes racing to provision simply write byte-identical
// copies where the last rename wins.
func copyExecutable(src, dst string) error {
	removeStaleTemps(filepath.Dir(dst))
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	tmp, err := os.CreateTemp(filepath.Dir(dst), ".agnt-daemon-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	// Remove the temp file if we bail before (or the rename supersedes it
	// after) a successful rename; Remove of a renamed path is a harmless ENOENT.
	defer os.Remove(tmpName)

	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, dst)
}

// removeStaleTemps reaps `.agnt-daemon-*` temp files left behind when a
// provisioning process was killed between CreateTemp and Rename (the deferred
// Remove never runs on SIGKILL). Only files older than staleTempAge are
// removed so a concurrent provisioner's in-flight temp file is never yanked
// out from under its rename.
const staleTempAge = 15 * time.Minute

func removeStaleTemps(dir string) {
	matches, err := filepath.Glob(filepath.Join(dir, ".agnt-daemon-*"))
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-staleTempAge)
	for _, m := range matches {
		if fi, err := os.Stat(m); err == nil && fi.Mode().IsRegular() && fi.ModTime().Before(cutoff) {
			os.Remove(m)
		}
	}
}
