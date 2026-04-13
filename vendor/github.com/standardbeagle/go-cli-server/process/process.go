package process

import (
	"context"
	"io"
	"os/exec"
	"sync/atomic"
	"time"
)

// ProcessState represents the lifecycle state of a managed process.
type ProcessState uint32

const (
	// StatePending is the initial state before the process is started.
	StatePending ProcessState = iota
	// StateStarting indicates the process is being started.
	StateStarting
	// StateRunning indicates the process is actively running.
	StateRunning
	// StateStopping indicates the process is being stopped.
	StateStopping
	// StateStopped indicates the process has stopped normally.
	StateStopped
	// StateFailed indicates the process exited with an error.
	StateFailed
)

// String returns a human-readable state name.
func (s ProcessState) String() string {
	switch s {
	case StatePending:
		return "pending"
	case StateStarting:
		return "starting"
	case StateRunning:
		return "running"
	case StateStopping:
		return "stopping"
	case StateStopped:
		return "stopped"
	case StateFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// ManagedProcess represents a subprocess managed by the hub.
type ManagedProcess struct {
	// ID is the unique identifier for this process.
	ID string

	// ProjectPath is the working directory for the process.
	ProjectPath string

	// WorkingDir is the working directory for the process.
	// If empty, ProjectPath is used.
	WorkingDir string

	// Command is the executable to run.
	Command string

	// Args are the command arguments.
	Args []string

	// Env holds environment variables for the process (KEY=VALUE format).
	Env []string

	// Labels provide metadata for filtering/grouping.
	Labels map[string]string

	// state holds the current lifecycle state (atomic for lock-free reads).
	state atomic.Uint32

	// pid holds the OS process ID (-1 if not started).
	pid atomic.Int32

	// cmd is the underlying exec.Cmd.
	cmd *exec.Cmd

	// stdout captures standard output.
	stdout *RingBuffer

	// stderr captures standard error.
	stderr *RingBuffer

	// stdin is the write end of the stdin pipe (nil if not enabled).
	stdin io.WriteCloser

	// stdinEnabled indicates if stdin pipe is available.
	stdinEnabled bool

	// outputCallback is called for each line of stdout/stderr output.
	outputCallback OutputCallback

	// lineWriters wrap stdout/stderr when outputCallback is set (nil otherwise).
	stdoutLineWriter *lineWriter
	stderrLineWriter *lineWriter

	// startTime records when the process was started.
	startTime atomic.Pointer[time.Time]

	// endTime records when the process stopped.
	endTime atomic.Pointer[time.Time]

	// exitCode holds the process exit code.
	exitCode atomic.Int32

	// ctx is the context for this process.
	ctx context.Context

	// cancel cancels the process context.
	cancel context.CancelFunc

	// done is closed when the process completes.
	done chan struct{}

	// descendants holds the last-known descendant PIDs, snapshotted while
	// the process tree was still intact. Written before stop/force-kill so
	// setsid-escaped grandchildren survive the parent's death for cleanup.
	descendants atomic.Pointer[[]int]
}

// OutputCallback is called for each line of process output.
type OutputCallback func(processID string, line string)

// ProcessConfig holds configuration for creating a new process.
type ProcessConfig struct {
	ID             string
	ProjectPath    string // Root project path (for session association)
	WorkingDir     string // Working directory for the process (if empty, uses ProjectPath)
	Command        string
	Args           []string
	Env            []string
	Labels         map[string]string
	BufferSize     int
	Timeout        time.Duration
	EnableStdin    bool           // Enable stdin pipe for writing to process
	OutputCallback OutputCallback // Called for each line of stdout/stderr (optional)
}

// NewManagedProcess creates a new ManagedProcess from config.
func NewManagedProcess(cfg ProcessConfig) *ManagedProcess {
	bufSize := cfg.BufferSize
	if bufSize <= 0 {
		bufSize = DefaultBufferSize
	}

	ctx, cancel := context.WithCancel(context.Background())

	if cfg.Timeout > 0 {
		ctx, cancel = context.WithTimeout(context.Background(), cfg.Timeout)
	}

	// Default WorkingDir to ProjectPath if not set
	workingDir := cfg.WorkingDir
	if workingDir == "" {
		workingDir = cfg.ProjectPath
	}

	p := &ManagedProcess{
		ID:             cfg.ID,
		ProjectPath:    cfg.ProjectPath,
		WorkingDir:     workingDir,
		Command:        cfg.Command,
		Args:           cfg.Args,
		Env:            cfg.Env,
		Labels:         cfg.Labels,
		stdout:         NewRingBuffer(bufSize),
		stderr:         NewRingBuffer(bufSize),
		stdinEnabled:   cfg.EnableStdin,
		outputCallback: cfg.OutputCallback,
		ctx:            ctx,
		cancel:         cancel,
		done:           make(chan struct{}),
	}

	p.state.Store(uint32(StatePending))
	p.pid.Store(-1)
	p.exitCode.Store(-1)

	return p
}

// State returns the current process state (lock-free atomic read).
func (p *ManagedProcess) State() ProcessState {
	return ProcessState(p.state.Load())
}

// SetState sets the process state.
func (p *ManagedProcess) SetState(s ProcessState) {
	p.state.Store(uint32(s))
}

// CompareAndSwapState atomically updates state if it matches expected.
func (p *ManagedProcess) CompareAndSwapState(expected, new ProcessState) bool {
	return p.state.CompareAndSwap(uint32(expected), uint32(new))
}

// PID returns the OS process ID, or -1 if not started.
func (p *ManagedProcess) PID() int {
	return int(p.pid.Load())
}

// ExitCode returns the exit code, or -1 if not finished.
func (p *ManagedProcess) ExitCode() int {
	return int(p.exitCode.Load())
}

// StartTime returns when the process was started, or nil if not started.
func (p *ManagedProcess) StartTime() *time.Time {
	return p.startTime.Load()
}

// EndTime returns when the process stopped, or nil if still running.
func (p *ManagedProcess) EndTime() *time.Time {
	return p.endTime.Load()
}

// Runtime returns how long the process has been running (or ran).
func (p *ManagedProcess) Runtime() time.Duration {
	start := p.startTime.Load()
	if start == nil {
		return 0
	}

	end := p.endTime.Load()
	if end == nil {
		return time.Since(*start)
	}
	return end.Sub(*start)
}

// IsRunning returns true if the process is currently running.
func (p *ManagedProcess) IsRunning() bool {
	return p.State() == StateRunning
}

// IsDone returns true if the process has finished (stopped or failed).
func (p *ManagedProcess) IsDone() bool {
	state := p.State()
	return state == StateStopped || state == StateFailed
}

// Done returns a channel that is closed when the process completes.
func (p *ManagedProcess) Done() <-chan struct{} {
	return p.done
}

// SetDescendants atomically replaces the cached descendant PID list. Used by
// the stop path to snapshot the tree while it is still walkable.
func (p *ManagedProcess) SetDescendants(pids []int) {
	// Copy to avoid sharing caller's backing array.
	cp := append([]int(nil), pids...)
	p.descendants.Store(&cp)
}

// Descendants returns the last-snapshotted descendant PIDs, or nil if none.
func (p *ManagedProcess) Descendants() []int {
	if d := p.descendants.Load(); d != nil {
		return *d
	}
	return nil
}

// Stdout returns a snapshot of the stdout buffer.
func (p *ManagedProcess) Stdout() ([]byte, bool) {
	return p.stdout.Snapshot()
}

// Stderr returns a snapshot of the stderr buffer.
func (p *ManagedProcess) Stderr() ([]byte, bool) {
	return p.stderr.Snapshot()
}

// CombinedOutput returns both stdout and stderr concatenated.
func (p *ManagedProcess) CombinedOutput() ([]byte, bool) {
	stdout, t1 := p.stdout.Snapshot()
	stderr, t2 := p.stderr.Snapshot()

	combined := make([]byte, 0, len(stdout)+len(stderr))
	combined = append(combined, stdout...)
	combined = append(combined, stderr...)

	return combined, t1 || t2
}

// WriteStdin writes data to the process stdin.
// Returns error if stdin is not enabled or process is not running.
func (p *ManagedProcess) WriteStdin(data []byte) (int, error) {
	if !p.stdinEnabled || p.stdin == nil {
		return 0, ErrStdinNotEnabled
	}
	if !p.IsRunning() {
		return 0, ErrInvalidState
	}
	return p.stdin.Write(data)
}

// CloseStdin closes the stdin pipe.
func (p *ManagedProcess) CloseStdin() error {
	if p.stdin != nil {
		return p.stdin.Close()
	}
	return nil
}

// StdinEnabled returns true if stdin pipe is available.
func (p *ManagedProcess) StdinEnabled() bool {
	return p.stdinEnabled
}

// Summary returns a brief status summary.
func (p *ManagedProcess) Summary() string {
	state := p.State()
	switch state {
	case StatePending:
		return "pending"
	case StateStarting:
		return "starting"
	case StateRunning:
		return "running"
	case StateStopping:
		return "stopping"
	case StateStopped:
		code := p.ExitCode()
		if code == 0 {
			return "passed"
		}
		return "stopped (exit " + itoa(code) + ")"
	case StateFailed:
		return "failed (exit " + itoa(p.ExitCode()) + ")"
	default:
		return "unknown"
	}
}

// Cancel signals the process to stop via context cancellation.
func (p *ManagedProcess) Cancel() {
	if p.cancel != nil {
		p.cancel()
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
