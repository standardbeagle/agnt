//go:build unix

package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

// startPollablePTY starts c on a new PTY and returns a master the Go runtime
// polls, so closing it wakes a Read blocked on it.
//
// creack/pty runs every ioctl through (*os.File).Fd, and Fd switches the file
// to blocking mode for good: pty.Start's own ptsname call does it before the
// master is handed back. A blocking Read parks in read(2), and Close cannot
// interrupt it. Once the agent exited, nothing then ended the output copy
// while a job it left behind (an MCP server, `npm run dev &`) still held the
// slave open — Linux does not hang up a PTY when its session leader exits —
// so `agnt run` stayed up after Ctrl+C until the user killed it.
//
// The master is duplicated onto a non-blocking descriptor and wrapped with
// os.NewFile, which registers it with the poller. O_NONBLOCK lives on the
// shared open file description, so the original *os.File is closed here:
// one more Fd call on it would make the master blocking again. Size changes
// go through setPTYSize/getPTYSize, never pty.Setsize.
func startPollablePTY(c *exec.Cmd) (*os.File, error) {
	blocking, err := pty.Start(c)
	if err != nil {
		return nil, err
	}
	defer func() { _ = blocking.Close() }()
	fd, err := syscall.Dup(int(blocking.Fd()))
	if err != nil {
		return nil, fmt.Errorf("dup pty master: %w", err)
	}
	if err := syscall.SetNonblock(fd, true); err != nil {
		_ = syscall.Close(fd)
		return nil, fmt.Errorf("pty master non-blocking: %w", err)
	}
	syscall.CloseOnExec(fd)
	return os.NewFile(uintptr(fd), blocking.Name()), nil
}

// setPTYSize sets the PTY window size without touching (*os.File).Fd (see
// startPollablePTY).
func setPTYSize(ptmx *os.File, ws *unix.Winsize) error {
	return controlPTY(ptmx, func(fd int) error { return unix.IoctlSetWinsize(fd, unix.TIOCSWINSZ, ws) })
}

// getPTYSize reads the PTY window size without touching (*os.File).Fd.
func getPTYSize(ptmx *os.File) (*unix.Winsize, error) {
	var ws *unix.Winsize
	err := controlPTY(ptmx, func(fd int) error {
		var err error
		ws, err = unix.IoctlGetWinsize(fd, unix.TIOCGWINSZ)
		return err
	})
	return ws, err
}

func controlPTY(ptmx *os.File, op func(fd int) error) error {
	sc, err := ptmx.SyscallConn()
	if err != nil {
		return err
	}
	var opErr error
	if err := sc.Control(func(fd uintptr) { opErr = op(int(fd)) }); err != nil {
		return err
	}
	return opErr
}
