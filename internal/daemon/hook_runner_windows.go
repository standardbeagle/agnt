//go:build windows

package daemon

import "os/exec"

// configureHookProcessGroup is a no-op on Windows: CommandContext's default
// kill targets the process, and Windows job-object containment for hooks is
// not wired here.
func configureHookProcessGroup(c *exec.Cmd) {}
