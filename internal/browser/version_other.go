//go:build !windows

package browser

import (
	"context"
	"errors"
	"os/exec"
	"time"
)

// versionProbeTimeout bounds the version probe. `chrome --version` prints and
// exits at once; the deadline exists so a wrapper script or a wedged binary
// can never hang the caller.
const versionProbeTimeout = 10 * time.Second

// versionProbeWaitDelay bounds how long the probe waits for its stdout pipe to
// close once the binary has exited or the deadline has passed. Without it the
// timeout does not bound the call: Output reads stdout to EOF, and a wrapper
// script (snap, flatpak, a distro's chromium launcher) whose child keeps the
// inherited stdout open holds that pipe — and the caller — for as long as the
// child lives. The context only kills the process it started, which may
// already be gone.
const versionProbeWaitDelay = 2 * time.Second

// readVersion asks the binary for its version.
func readVersion(path string) (string, error) {
	return readVersionWithin(path, versionProbeTimeout, versionProbeWaitDelay)
}

func readVersionWithin(path string, timeout, waitDelay time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	cmd.WaitDelay = waitDelay
	out, err := cmd.Output()
	// ErrWaitDelay means the binary exited successfully and only a descendant
	// kept the pipe open; what it printed before exiting is the answer.
	if errors.Is(err, exec.ErrWaitDelay) && len(out) > 0 {
		err = nil
	}
	return string(out), err
}
