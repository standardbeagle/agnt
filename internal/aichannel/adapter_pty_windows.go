//go:build windows

package aichannel

import (
	"context"
	"fmt"
	"os/exec"
	"sync"

	"github.com/aymanbagabas/go-pty"
)

// conPTYSession is a command attached to a Windows ConPTY.
type conPTYSession struct {
	pty.Pty
	cmd       *pty.Cmd
	closeOnce sync.Once
	closeErr  error
}

func (s *conPTYSession) Wait() error { return s.cmd.Wait() }

// Close closes the pseudo-console, which is also what ends the output stream:
// ConPTY keeps its output pipe open after the child exits, so a reader only
// sees end-of-stream once the console itself is closed.
func (s *conPTYSession) Close() error {
	s.closeOnce.Do(func() { s.closeErr = s.Pty.Close() })
	return s.closeErr
}

// bypassBatchShim rewrites a pipe-mode command whose program is an npm `.cmd`
// shim to start the shim's target directly, for the same reason
// startPTYCommand does: the prompt rides in argv and cmd.exe would mangle it.
// Unlike the PTY path this never refuses — a batch file that is not a
// recognizable shim is left for os/exec to run as it always has.
func bypassBatchShim(cmd *exec.Cmd) {
	if cmd.Err != nil || !isBatchShimPath(cmd.Path) {
		return
	}
	resolved, err := resolveNpmCmdShim(cmd.Path, exec.LookPath)
	if err != nil {
		return
	}
	var rest []string
	if len(cmd.Args) > 1 {
		rest = cmd.Args[1:]
	}
	cmd.Path = resolved.Path
	cmd.Args = append(append([]string{resolved.Path}, resolved.Args...), rest...)
}

// startPTYCommand starts cmd attached to a new ConPTY. creack/pty — the Unix
// backend — compiles on Windows but only ever returns ErrUnsupported, which
// made every PTY-mode agent call (the Claude CLI summarizer) fail here.
//
// A process attached to a pseudo-console cannot be started through os/exec
// (golang/go#62708), so the exec.Cmd the adapter built is used as a spec
// (resolved path, argv, env, dir) for a go-pty command, and is never started
// itself.
func startPTYCommand(ctx context.Context, cmd *exec.Cmd) (ptySession, error) {
	if cmd.Err != nil {
		return nil, cmd.Err
	}
	path := cmd.Path
	var args []string
	if len(cmd.Args) > 1 {
		args = cmd.Args[1:]
	}

	// npm installs agent CLIs as .cmd shims — on Windows that is the usual
	// way `claude` is on PATH. A batch file only runs through cmd.exe, and
	// cmd.exe cannot carry a multi-line argument, which the prompt is. So the
	// shim is resolved to what it launches (node + the package's entry
	// script, or the package's native binary) and that is started directly
	// with a real argv.
	//
	// A batch file that is not a recognizable shim is refused, not run: the
	// only other way to deliver the prompt would be stdin, and the one
	// PTY-mode agent (Claude Code `-p`) does not read its prompt from there.
	if isBatchShimPath(path) {
		resolved, err := resolveNpmCmdShim(path, exec.LookPath)
		if err != nil {
			return nil, fmt.Errorf("%w; a batch file cannot receive a multi-line prompt argument on Windows, so it was not run. Install the agent's native executable, or configure an API key so summaries use API mode", err)
		}
		path = resolved.Path
		args = append(append([]string(nil), resolved.Args...), args...)
	}

	ptmx, err := pty.New()
	if err != nil {
		return nil, err
	}
	c := ptmx.CommandContext(ctx, path, args...)
	c.Env = cmd.Env
	c.Dir = cmd.Dir
	if err := c.Start(); err != nil {
		_ = ptmx.Close()
		return nil, err
	}
	return &conPTYSession{Pty: ptmx, cmd: c}, nil
}
