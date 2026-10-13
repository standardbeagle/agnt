package testenv_test

import (
	"runtime"
	"testing"

	"github.com/standardbeagle/agnt/internal/testutil/sockdir"
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
// a unix-domain socket. It is sockdir.Short, the one implementation shared by
// every package's tests; see that package for the sun_path limit.
func shortSocketDir(t *testing.T) string {
	t.Helper()
	return sockdir.Short(t)
}
