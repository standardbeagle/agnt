//go:build unix

package main

import (
	"os"
	"os/exec"
	"syscall"
	"testing"

	"github.com/creack/pty"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/standardbeagle/agnt/internal/platform"
)

// TestPTYChildSessionPGIDIsExclusiveToTheChild pins the premise the daemon's
// whole session-containment model rests on: the pgid `agnt run` reports as
// SessionPGID identifies the PTY child's OWN process group and nothing else.
//
// If that ever stopped holding — if creack/pty stopped calling setsid, or a
// platform put the child in the caller's group — SessionPGID would name an
// ancestor group and the daemon's cleanup would reap the user's terminal. That
// exact failure was the leading hypothesis behind a run of `daemon reaped
// session cdsp-*` records; measuring it here is what ruled it out, so the
// measurement is kept rather than the conclusion.
//
// It asserts the same syscall runPTYChild uses (syscall.Getpgid on the child's
// pid), so a regression in the capture site is caught, not just in creack/pty.
//
// No t.Parallel(): this starts a real OS process.
func TestPTYChildSessionPGIDIsExclusiveToTheChild(t *testing.T) {
	c := exec.Command("sleep", "30")
	ptmx, err := pty.Start(c)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = ptmx.Close()
		_ = c.Process.Kill()
		_, _ = c.Process.Wait()
	})
	require.NotNil(t, c.Process)

	childPGID, err := syscall.Getpgid(c.Process.Pid)
	require.NoError(t, err)

	assert.Equal(t, c.Process.Pid, childPGID,
		"the PTY child must lead its own process group — a pgid that is not the child's pid is an inherited (ancestor) group")

	childSID, err := getsid(c.Process.Pid)
	require.NoError(t, err)
	assert.Equal(t, c.Process.Pid, childSID,
		"the PTY child must lead its own POSIX session (creack/pty sets Setsid)")

	selfPGID, err := syscall.Getpgid(os.Getpid())
	require.NoError(t, err)
	assert.NotEqual(t, selfPGID, childPGID,
		"the child's group must be disjoint from the launching process's group")

	assert.NotContains(t, platform.MembersOfPGID(childPGID), os.Getpid(),
		"the launching process must never be a member of the group reported as SessionPGID — the daemon refuses to reap a group it finds itself or the wrapper in")
}

// getsid wraps the getsid(2) syscall, which x/sys does not expose portably
// across this project's Unix targets.
func getsid(pid int) (int, error) {
	sid, _, errno := syscall.Syscall(syscall.SYS_GETSID, uintptr(pid), 0, 0)
	if errno != 0 {
		return 0, errno
	}
	return int(sid), nil
}
