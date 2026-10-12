package testenv_test

import (
	"os"
	"runtime"
	"testing"
)

// requirePOSIXHost skips a test whose fixture runs "remote" commands through
// the host's /bin/sh (testenv.Server and the stdin-exec server both do) or
// installs `#!/bin/sh` scripts on PATH. The harness emulates a Linux/macOS
// remote, the only kind agnt ssh supports; native Windows has no /bin/sh, so
// the exec would only ever report 127.
func requirePOSIXHost(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("SKIPPED: this fixture runs remote commands through the host's /bin/sh, which native Windows does not provide")
	}
}

// shortSocketDir returns a fresh directory whose path is short enough to hold
// a unix-domain socket. t.TempDir() embeds the test name, which on Windows
// pushes the socket path past the ~108-byte sun_path limit, so bind fails with
// "invalid argument".
func shortSocketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ssh")
	if err != nil {
		t.Fatalf("creating short socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
