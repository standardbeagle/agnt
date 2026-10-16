//go:build windows

package daemon

import (
	"os/exec"
	"time"

	"golang.org/x/sys/windows"

	"github.com/standardbeagle/agnt/internal/platform"
)

// configureHookProcessGroup makes a hook timeout kill the hook's whole process
// tree, not just the shell. CommandContext's default cancel terminates only the
// direct child, so a hook's children (a dev tool it launched, a pinger) outlived
// the timeout — the Windows side of the Unix process-group kill. It returns the
// function that runs the hook.
//
// The tree is killed with platform.KillProcessTree, which follows parent links
// by creation time and kills each member through an identity-checked handle,
// rather than with a job object: a job with KILL_ON_JOB_CLOSE would also reap
// children a hook backgrounds on purpose when the hook finishes normally,
// which the Unix path does not do. WaitDelay bounds how long Wait hangs on a
// descendant that still holds an inherited handle.
//
// The root's identity is taken right after Start, while the exec package's
// own handle is certain to hold the PID, and a handle of ours keeps holding
// it until the hook has been waited for. The exec package can run Cancel just
// as Process.Wait returns and releases its handle (a timeout racing the
// shell's exit); the pin keeps the shell's PID, and with it the parent links
// of its children, from being reused under the kill. The kill used to look the
// root up by PID at cancel time and then killed every PID of the tree it had
// collected without checking any of them again.
func configureHookProcessGroup(c *exec.Cmd) func() error {
	root := &hookRoot{ready: make(chan struct{})}
	c.Cancel = func() error {
		<-root.ready
		if !root.ok {
			return c.Process.Kill() // no identity: the shell alone, through exec's handle
		}
		return platform.KillProcessTree(root.id, 0)
	}
	c.WaitDelay = 2 * time.Second
	return func() error {
		if err := c.Start(); err != nil {
			close(root.ready)
			return err
		}
		root.pin(c.Process.Pid)
		close(root.ready)
		defer root.unpin()
		return c.Wait()
	}
}

// hookRoot is the identity of a hook's shell and the handle that keeps its PID
// from being reused while the hook runs. Written before ready is closed, read
// only after.
type hookRoot struct {
	ready  chan struct{}
	handle windows.Handle
	id     platform.ProcessIdentity
	ok     bool
}

// pin opens the shell and records its identity. It runs between Start and
// Wait, so the PID can only be the shell's.
func (r *hookRoot) pin(pid int) {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return
	}
	id, ok := platform.IdentifyProcess(pid)
	if !ok {
		windows.CloseHandle(h)
		return
	}
	r.handle, r.id, r.ok = h, id, true
}

func (r *hookRoot) unpin() {
	if r.ok {
		windows.CloseHandle(r.handle)
	}
}
