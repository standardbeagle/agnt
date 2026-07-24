//go:build !windows

package daemon

import (
	"os/exec"
	"syscall"
	"time"
)

// configureHookProcessGroup starts the hook in its own process group and
// kills the whole group on context cancellation (timeout). The default
// CommandContext cancel kills only the direct child (the shell), orphaning
// hook grandchildren — which violates the repo's process-group containment
// invariant. WaitDelay bounds how long Wait hangs on grandchildren that
// inherited the output pipes.
func configureHookProcessGroup(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error {
		return syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
	}
	c.WaitDelay = 2 * time.Second
}
