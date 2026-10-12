//go:build !windows

package browser

import (
	"context"
	"os/exec"
	"time"
)

// versionProbeTimeout bounds the version probe. `chrome --version` prints and
// exits at once; the deadline exists so a wrapper script or a wedged binary
// can never hang the caller.
const versionProbeTimeout = 10 * time.Second

// readVersion asks the binary for its version.
func readVersion(path string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), versionProbeTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--version").Output()
	return string(out), err
}
