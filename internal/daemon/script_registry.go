package daemon

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
)

// ScriptState represents the current state of a managed script.
type ScriptState uint32

const (
	StateIdle       ScriptState = iota // Not running, never started or cleanly stopped
	StateStarting                      // Process is being created
	StateRunning                       // Process is running
	StateFailed                        // Process exited with error
	StateStopped                       // Process was explicitly stopped
	StateRestarting                    // Process is restarting
)

// String returns the string representation of a ScriptState.
func (s ScriptState) String() string {
	switch s {
	case StateIdle:
		return "idle"
	case StateStarting:
		return "starting"
	case StateRunning:
		return "running"
	case StateFailed:
		return "failed"
	case StateStopped:
		return "stopped"
	case StateRestarting:
		return "restarting"
	default:
		return fmt.Sprintf("unknown(%d)", s)
	}
}

// StateTransition records a state change with its timestamp.
type StateTransition struct {
	State     ScriptState `json:"state"`
	Timestamp time.Time   `json:"timestamp"`
}

const (
	maxOutputLines      = 2000
	maxStateTransitions = 100
	restartMarker       = "--- restart ---"
)

// ScriptEntry holds all persistent state for a managed script.
// Lock-free reads via atomics for state, process ID, and counters.
// sync.RWMutex protects output history and state history writes only.
type ScriptEntry struct {
	Name        string
	ProjectPath string
	ProcessID   string // makeProcessID(projectPath, name)
	Config      *config.ScriptConfig

	// Lock-free state (atomic reads)
	state      atomic.Uint32
	startCount atomic.Int64
	failCount  atomic.Int64

	// Protected by mu: output history, state history, and last error
	mu           sync.RWMutex
	outputLines  []string
	outputHead   int
	outputLen    int
	stateHistory []StateTransition
	resolvedCmd  string
	resolvedArgs []string
	lastError    string

	// Session tracking (lock-free via sync.Map)
	sessions sync.Map // map[string]struct{}

	// Ownership tracking (lock-free via atomic pointer)
	ownerSession atomic.Pointer[string]
}

// newScriptEntry creates a new ScriptEntry with the given parameters.
func newScriptEntry(name, projectPath string, cfg *config.ScriptConfig) *ScriptEntry {
	entry := &ScriptEntry{
		Name:        name,
		ProjectPath: projectPath,
		ProcessID:   makeProcessID(projectPath, name),
		Config:      cfg,
		outputLines: make([]string, maxOutputLines),
	}
	entry.state.Store(uint32(StateIdle))
	entry.stateHistory = append(entry.stateHistory, StateTransition{
		State:     StateIdle,
		Timestamp: time.Now(),
	})
	return entry
}

// State returns the current script state (lock-free).
func (e *ScriptEntry) State() ScriptState {
	return ScriptState(e.state.Load())
}

// CompareAndSwapState atomically transitions from old to new state.
// Returns true if the transition succeeded.
func (e *ScriptEntry) CompareAndSwapState(old, new ScriptState) bool {
	if !e.state.CompareAndSwap(uint32(old), uint32(new)) {
		return false
	}
	e.mu.Lock()
	e.stateHistory = append(e.stateHistory, StateTransition{
		State:     new,
		Timestamp: time.Now(),
	})
	if len(e.stateHistory) > maxStateTransitions {
		// Trim oldest entries, keeping the most recent maxStateTransitions
		copy(e.stateHistory, e.stateHistory[len(e.stateHistory)-maxStateTransitions:])
		e.stateHistory = e.stateHistory[:maxStateTransitions]
	}
	e.mu.Unlock()
	return true
}

// SetState unconditionally sets the state and records the transition.
func (e *ScriptEntry) SetState(new ScriptState) {
	e.state.Store(uint32(new))
	e.mu.Lock()
	e.stateHistory = append(e.stateHistory, StateTransition{
		State:     new,
		Timestamp: time.Now(),
	})
	if len(e.stateHistory) > maxStateTransitions {
		copy(e.stateHistory, e.stateHistory[len(e.stateHistory)-maxStateTransitions:])
		e.stateHistory = e.stateHistory[:maxStateTransitions]
	}
	e.mu.Unlock()
}

// StartCount returns the total number of starts (lock-free).
func (e *ScriptEntry) StartCount() int64 {
	return e.startCount.Load()
}

// FailCount returns the total number of failures (lock-free).
func (e *ScriptEntry) FailCount() int64 {
	return e.failCount.Load()
}

// IncrementStartCount atomically increments the start counter.
func (e *ScriptEntry) IncrementStartCount() {
	e.startCount.Add(1)
}

// IncrementFailCount atomically increments the fail counter.
func (e *ScriptEntry) IncrementFailCount() {
	e.failCount.Add(1)
}

// AddRestartMarker inserts a restart separator into the output history.
func (e *ScriptEntry) AddRestartMarker() {
	e.appendOutput(restartMarker)
}

// AppendOutput adds a line to the output ring buffer.
func (e *ScriptEntry) AppendOutput(line string) {
	e.appendOutput(line)
}

func (e *ScriptEntry) appendOutput(line string) {
	e.mu.Lock()
	idx := (e.outputHead + e.outputLen) % maxOutputLines
	e.outputLines[idx] = line
	if e.outputLen < maxOutputLines {
		e.outputLen++
	} else {
		e.outputHead = (e.outputHead + 1) % maxOutputLines
	}
	e.mu.Unlock()
}

// OutputLines returns a copy of the output history, oldest to newest.
func (e *ScriptEntry) OutputLines() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	result := make([]string, e.outputLen)
	for i := 0; i < e.outputLen; i++ {
		result[i] = e.outputLines[(e.outputHead+i)%maxOutputLines]
	}
	return result
}

// OutputLen returns the current number of output lines.
func (e *ScriptEntry) OutputLen() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.outputLen
}

// StateHistory returns a copy of the state transition history.
func (e *ScriptEntry) StateHistory() []StateTransition {
	e.mu.RLock()
	defer e.mu.RUnlock()
	result := make([]StateTransition, len(e.stateHistory))
	copy(result, e.stateHistory)
	return result
}

// SetResolvedCommand stores the resolved command and arguments.
func (e *ScriptEntry) SetResolvedCommand(cmd string, args []string) {
	e.mu.Lock()
	e.resolvedCmd = cmd
	e.resolvedArgs = args
	e.mu.Unlock()
}

// ResolvedCommand returns the resolved command and arguments.
func (e *ScriptEntry) ResolvedCommand() (string, []string) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.resolvedCmd, e.resolvedArgs
}

// SetLastError records the last error message for the script.
func (e *ScriptEntry) SetLastError(msg string) {
	e.mu.Lock()
	e.lastError = msg
	e.mu.Unlock()
}

// LastError returns the last error message.
func (e *ScriptEntry) LastError() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.lastError
}

// AddSession registers a session as observing this script.
func (e *ScriptEntry) AddSession(sessionCode string) {
	e.sessions.Store(sessionCode, struct{}{})
}

// RemoveSession unregisters a session from observing this script.
func (e *ScriptEntry) RemoveSession(sessionCode string) {
	e.sessions.Delete(sessionCode)
}

// ListSessions returns the session codes currently observing this script.
func (e *ScriptEntry) ListSessions() []string {
	var codes []string
	e.sessions.Range(func(key, value interface{}) bool {
		codes = append(codes, key.(string))
		return true
	})
	return codes
}

// SetOwner sets the owning session for this script (the session that started it).
func (e *ScriptEntry) SetOwner(sessionCode string) {
	e.ownerSession.Store(&sessionCode)
}

// Owner returns the session code that owns this script, or empty string if unowned.
func (e *ScriptEntry) Owner() string {
	p := e.ownerSession.Load()
	if p == nil {
		return ""
	}
	return *p
}

// ObserverCount returns the number of sessions observing this script.
func (e *ScriptEntry) ObserverCount() int {
	count := 0
	e.sessions.Range(func(_, _ interface{}) bool {
		count++
		return true
	})
	return count
}

// TransferOwnership picks a remaining observer to become the new owner.
// Returns the new owner's session code, or empty string if no observers remain.
func (e *ScriptEntry) TransferOwnership() string {
	var newOwner string
	e.sessions.Range(func(key, _ interface{}) bool {
		newOwner = key.(string)
		return false // stop after first
	})
	if newOwner != "" {
		e.ownerSession.Store(&newOwner)
	} else {
		e.ownerSession.Store(nil)
	}
	return newOwner
}

// ScriptRegistry manages ScriptEntry instances with lock-free operations.
// Keys are projectPath + "\x00" + scriptName for uniqueness.
type ScriptRegistry struct {
	entries sync.Map // map[string]*ScriptEntry
}

// NewScriptRegistry creates a new ScriptRegistry.
func NewScriptRegistry() *ScriptRegistry {
	return &ScriptRegistry{}
}

// scriptKey builds the registry key from project path and script name.
func scriptKey(projectPath, name string) string {
	return projectPath + "\x00" + name
}

// Register adds or returns an existing ScriptEntry for the given script.
// If the entry already exists, it is returned as-is (shared across sessions).
func (r *ScriptRegistry) Register(name, projectPath string, cfg *config.ScriptConfig) (*ScriptEntry, error) {
	if name == "" {
		return nil, fmt.Errorf("script name is required")
	}
	if projectPath == "" {
		return nil, fmt.Errorf("project path is required")
	}

	key := scriptKey(projectPath, name)
	entry := newScriptEntry(name, projectPath, cfg)

	if existing, loaded := r.entries.LoadOrStore(key, entry); loaded {
		return existing.(*ScriptEntry), nil
	}
	return entry, nil
}

// Get retrieves a ScriptEntry by name and project path.
func (r *ScriptRegistry) Get(name, projectPath string) (*ScriptEntry, bool) {
	key := scriptKey(projectPath, name)
	val, ok := r.entries.Load(key)
	if !ok {
		return nil, false
	}
	return val.(*ScriptEntry), true
}

// List returns all ScriptEntry instances for a given project path.
func (r *ScriptRegistry) List(projectPath string) []*ScriptEntry {
	prefix := projectPath + "\x00"
	var result []*ScriptEntry
	r.entries.Range(func(key, value interface{}) bool {
		k := key.(string)
		if strings.HasPrefix(k, prefix) {
			result = append(result, value.(*ScriptEntry))
		}
		return true
	})
	return result
}

// GetByProcessID retrieves a ScriptEntry by its process ID.
// This is used by the auto-restarter which only knows the processID.
func (r *ScriptRegistry) GetByProcessID(processID string) (*ScriptEntry, bool) {
	var found *ScriptEntry
	r.entries.Range(func(key, value interface{}) bool {
		entry := value.(*ScriptEntry)
		if entry.ProcessID == processID {
			found = entry
			return false
		}
		return true
	})
	if found == nil {
		return nil, false
	}
	return found, true
}

// ListAll returns all ScriptEntry instances across all projects.
func (r *ScriptRegistry) ListAll() []*ScriptEntry {
	var result []*ScriptEntry
	r.entries.Range(func(key, value interface{}) bool {
		result = append(result, value.(*ScriptEntry))
		return true
	})
	return result
}
