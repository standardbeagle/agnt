//go:build unix

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"unicode/utf8"

	acp "github.com/coder/acp-go-sdk"

	"github.com/standardbeagle/agnt/internal/daemonclient"
	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/agnt/internal/shims"
)

// defaultTerminalByteLimit caps a terminal's retained output when the agent
// does not specify outputByteLimit. Older output is truncated from the front.
const defaultTerminalByteLimit = 1 << 20 // 1 MiB

// acpTerminal is one agent-requested command running under the client. It
// captures combined stdout+stderr into a byte-bounded buffer and tracks the
// exit status once the process completes. It is its own io.Writer (cmd wires
// stdout/stderr to it).
type acpTerminal struct {
	cmd   *exec.Cmd
	limit int
	done  chan struct{} // closed when the process exits

	mu        sync.Mutex
	buf       []byte
	truncated bool
	exited    bool
	exitCode  *int
	signal    *string
}

// Write appends to the bounded buffer, truncating from the front at a UTF-8
// rune boundary when the limit is exceeded.
func (t *acpTerminal) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if t.limit > 0 && len(t.buf) > t.limit {
		over := len(t.buf) - t.limit
		for over < len(t.buf) && !utf8.RuneStart(t.buf[over]) {
			over++
		}
		t.buf = t.buf[over:]
		t.truncated = true
	}
	return len(p), nil
}

// snapshot returns the captured output and, if the process has exited, its
// status.
func (t *acpTerminal) snapshot() (output string, truncated bool, exit *acp.TerminalExitStatus) {
	t.mu.Lock()
	defer t.mu.Unlock()
	output = string(t.buf)
	truncated = t.truncated
	if t.exited {
		exit = &acp.TerminalExitStatus{ExitCode: t.exitCode, Signal: t.signal}
	}
	return output, truncated, exit
}

// kill SIGKILLs the process group (the leader's pgid == its pid because the
// command was started with Setpgid). No-op if not yet started.
func (t *acpTerminal) kill() {
	if t.cmd == nil || t.cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-t.cmd.Process.Pid, syscall.SIGKILL)
}

// terminalExitInfo derives an ACP exit code / signal from a finished process.
func terminalExitInfo(ps *os.ProcessState) (*int, *string) {
	if ps == nil {
		return nil, nil
	}
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		s := ws.Signal().String()
		return nil, &s
	}
	code := ps.ExitCode()
	return &code, nil
}

// getTerminal resolves a terminal id, returning a JSON-RPC-visible error when
// unknown.
func (c *acpClient) getTerminal(id string) (*acpTerminal, error) {
	c.termMu.Lock()
	defer c.termMu.Unlock()
	t, ok := c.terminals[id]
	if !ok {
		return nil, fmt.Errorf("unknown terminal: %s", id)
	}
	return t, nil
}

// CreateTerminal spawns the requested command in its own process group and
// returns immediately; the process runs asynchronously with its output
// captured. The agent later polls TerminalOutput / WaitForTerminalExit.
func (c *acpClient) CreateTerminal(_ context.Context, params acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	limit := defaultTerminalByteLimit
	if params.OutputByteLimit != nil && *params.OutputByteLimit > 0 {
		limit = *params.OutputByteLimit
	}

	t := &acpTerminal{limit: limit, done: make(chan struct{})}
	cmd := exec.Command(params.Command, params.Args...)
	if params.Cwd != nil {
		cmd.Dir = *params.Cwd
	}
	env := os.Environ()
	if len(params.Env) > 0 {
		for _, e := range params.Env {
			env = append(env, e.Name+"="+e.Value)
		}
	}
	// Route shell commands inside agent-requested terminals through the
	// daemon: stamp the project path (terminals don't inherit the
	// AGNT_PROJECT_PATH that `agnt run` sets) and shadow dev/build/kill
	// commands with the project's shim bin dir.
	env = injectShimEnv(env, terminalWorkDir(cmd))
	cmd.Env = env
	cmd.Stdout = t
	cmd.Stderr = t
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	t.cmd = cmd

	if err := cmd.Start(); err != nil {
		return acp.CreateTerminalResponse{}, fmt.Errorf("start terminal %q: %w", params.Command, err)
	}

	go func() {
		_ = cmd.Wait()
		ec, sig := terminalExitInfo(cmd.ProcessState)
		t.mu.Lock()
		t.exited = true
		t.exitCode, t.signal = ec, sig
		t.mu.Unlock()
		close(t.done)
	}()

	c.termMu.Lock()
	c.termSeq++
	id := fmt.Sprintf("term-%d", c.termSeq)
	c.terminals[id] = t
	c.termMu.Unlock()

	return acp.CreateTerminalResponse{TerminalId: id}, nil
}

// terminalWorkDir resolves the directory the terminal will run in.
func terminalWorkDir(cmd *exec.Cmd) string {
	if cmd.Dir != "" {
		return cmd.Dir
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return ""
}

// injectShimEnv stamps AGNT_PROJECT_PATH (when unset) and prepends the
// project's shim bin dir to PATH. No-op for projects without shims.
// Also records the install with the daemon once per project per process
// so shutdown/watcher cleanup can find the bin dir.
func injectShimEnv(env []string, workDir string) []string {
	if workDir == "" {
		return env
	}
	projectPath := workDir
	for _, kv := range env {
		if strings.HasPrefix(kv, "AGNT_PROJECT_PATH=") {
			projectPath = strings.TrimPrefix(kv, "AGNT_PROJECT_PATH=")
		}
	}
	if !hasEnvKey(env, "AGNT_PROJECT_PATH") {
		env = append(env, "AGNT_PROJECT_PATH="+projectPath)
	}
	binDir, err := shims.Ensure(projectPath)
	if err != nil || binDir == "" {
		return env
	}
	registerShimsOnce(projectPath, binDir)
	return shims.PrependPATH(env, binDir)
}

// shimRegistrations tracks projects already registered with the daemon
// from this process so CreateTerminal stays cheap after the first call.
var shimRegistrations sync.Map // projectPath -> struct{}

func registerShimsOnce(projectPath, binDir string) {
	if _, loaded := shimRegistrations.LoadOrStore(projectPath, struct{}{}); loaded {
		return
	}
	socketPath := daemonclient.DefaultSocketPath()
	if !daemonclient.IsRunning(socketPath) {
		return
	}
	client := daemonclient.NewClient(daemonclient.WithSocketPath(socketPath))
	if err := client.Connect(); err != nil {
		return
	}
	defer client.Close()
	_ = client.ShimRegister(protocol.ShimRegisterRequest{ProjectPath: projectPath, BinDir: binDir})
}

func hasEnvKey(env []string, key string) bool {
	prefix := key + "="
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return true
		}
	}
	return false
}

// TerminalOutput returns the captured output and exit status (if any) without
// blocking.
func (c *acpClient) TerminalOutput(_ context.Context, params acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	t, err := c.getTerminal(params.TerminalId)
	if err != nil {
		return acp.TerminalOutputResponse{}, err
	}
	out, trunc, exit := t.snapshot()
	return acp.TerminalOutputResponse{Output: out, Truncated: trunc, ExitStatus: exit}, nil
}

// WaitForTerminalExit blocks until the process exits (or the request is
// cancelled) and returns its exit code / signal.
func (c *acpClient) WaitForTerminalExit(ctx context.Context, params acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	t, err := c.getTerminal(params.TerminalId)
	if err != nil {
		return acp.WaitForTerminalExitResponse{}, err
	}
	select {
	case <-t.done:
	case <-ctx.Done():
		return acp.WaitForTerminalExitResponse{}, ctx.Err()
	}
	t.mu.Lock()
	ec, sig := t.exitCode, t.signal
	t.mu.Unlock()
	return acp.WaitForTerminalExitResponse{ExitCode: ec, Signal: sig}, nil
}

// KillTerminal force-kills the process group but keeps the terminal so its
// output remains queryable.
func (c *acpClient) KillTerminal(_ context.Context, params acp.KillTerminalRequest) (acp.KillTerminalResponse, error) {
	t, err := c.getTerminal(params.TerminalId)
	if err != nil {
		return acp.KillTerminalResponse{}, err
	}
	t.kill()
	return acp.KillTerminalResponse{}, nil
}

// ReleaseTerminal kills the process (if still running) and frees the terminal.
func (c *acpClient) ReleaseTerminal(_ context.Context, params acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	c.termMu.Lock()
	t, ok := c.terminals[params.TerminalId]
	if ok {
		delete(c.terminals, params.TerminalId)
	}
	c.termMu.Unlock()
	if ok {
		t.kill()
	}
	return acp.ReleaseTerminalResponse{}, nil
}

// releaseAllTerminals kills and clears every live terminal. Called on session
// teardown so no agent-spawned command is orphaned.
func (c *acpClient) releaseAllTerminals() {
	c.termMu.Lock()
	terms := make([]*acpTerminal, 0, len(c.terminals))
	for id, t := range c.terminals {
		terms = append(terms, t)
		delete(c.terminals, id)
	}
	c.termMu.Unlock()
	for _, t := range terms {
		t.kill()
	}
}
