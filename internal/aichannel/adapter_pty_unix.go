//go:build !windows

package aichannel

import (
	"context"
	"os"
	"os/exec"

	"github.com/creack/pty"
)

// unixPTYSession is a command attached to a creack/pty master.
type unixPTYSession struct {
	*os.File
	cmd *exec.Cmd
}

func (s *unixPTYSession) Wait() error { return s.cmd.Wait() }

// bypassBatchShim is a no-op off Windows: batch shims are a Windows launch
// mechanism (see adapter_pty_windows.go).
func bypassBatchShim(*exec.Cmd) {}

// startPTYCommand starts cmd attached to a new pseudo-terminal. cmd was built
// with exec.CommandContext, so ctx cancellation already kills it.
func startPTYCommand(_ context.Context, cmd *exec.Cmd) (ptySession, error) {
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, err
	}
	return &unixPTYSession{File: ptmx, cmd: cmd}, nil
}
