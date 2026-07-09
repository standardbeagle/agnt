package script

import (
	"fmt"
	"hash/fnv"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// State represents the current state of a managed script.
type State uint32

const (
	StateIdle       State = iota // Not running, never started or cleanly stopped
	StateStarting                // Process is being created
	StateRunning                 // Process is running
	StateFailed                  // Process exited with error
	StateStopped                 // Process was explicitly stopped
	StateRestarting              // Process is restarting
)

// String returns the string representation of a State.
func (s State) String() string {
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
	State     State     `json:"state"`
	Timestamp time.Time `json:"timestamp"`
}

// Config holds the configuration for a script.
type Config struct {
	Run     string            // Shell command string
	Command string            // Executable name (used with Args)
	Args    []string          // Command arguments (used with Command)
	Env     map[string]string // Environment variables
}

const (
	maxOutputLines      = 2000
	maxStateTransitions = 100
	restartMarker       = "--- restart ---"
)

// Entry holds all persistent state for a managed script.
// Lock-free reads via atomics for state, process ID, and counters.
// sync.RWMutex protects output history and state history writes only.
type Entry struct {
	Name        string
	ProjectPath string
	ProcessID   string // MakeProcessID(projectPath, name)
	Config      *Config

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

// newEntry creates a new Entry with the given parameters.
func newEntry(name, projectPath string, cfg *Config) *Entry {
	entry := &Entry{
		Name:        name,
		ProjectPath: projectPath,
		ProcessID:   MakeProcessID(projectPath, name),
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
func (e *Entry) State() State {
	return State(e.state.Load())
}

// CompareAndSwapState atomically transitions from old to new state.
// Returns true if the transition succeeded.
func (e *Entry) CompareAndSwapState(old, new State) bool {
	if !e.state.CompareAndSwap(uint32(old), uint32(new)) {
		return false
	}
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
	return true
}

// SetState unconditionally sets the state and records the transition.
func (e *Entry) SetState(new State) {
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
func (e *Entry) StartCount() int64 {
	return e.startCount.Load()
}

// FailCount returns the total number of failures (lock-free).
func (e *Entry) FailCount() int64 {
	return e.failCount.Load()
}

// IncrementStartCount atomically increments the start counter.
func (e *Entry) IncrementStartCount() {
	e.startCount.Add(1)
}

// IncrementFailCount atomically increments the fail counter.
func (e *Entry) IncrementFailCount() {
	e.failCount.Add(1)
}

// AddRestartMarker inserts a restart separator into the output history.
func (e *Entry) AddRestartMarker() {
	e.appendOutput(restartMarker)
}

// AppendOutput adds a line to the output ring buffer.
func (e *Entry) AppendOutput(line string) {
	e.appendOutput(line)
}

func (e *Entry) appendOutput(line string) {
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
func (e *Entry) OutputLines() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	result := make([]string, e.outputLen)
	for i := 0; i < e.outputLen; i++ {
		result[i] = e.outputLines[(e.outputHead+i)%maxOutputLines]
	}
	return result
}

// OutputLen returns the current number of output lines.
func (e *Entry) OutputLen() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.outputLen
}

// StateHistory returns a copy of the state transition history.
func (e *Entry) StateHistory() []StateTransition {
	e.mu.RLock()
	defer e.mu.RUnlock()
	result := make([]StateTransition, len(e.stateHistory))
	copy(result, e.stateHistory)
	return result
}

// SetResolvedCommand stores the resolved command and arguments.
func (e *Entry) SetResolvedCommand(cmd string, args []string) {
	e.mu.Lock()
	e.resolvedCmd = cmd
	e.resolvedArgs = args
	e.mu.Unlock()
}

// ResolvedCommand returns the resolved command and arguments.
func (e *Entry) ResolvedCommand() (string, []string) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.resolvedCmd, e.resolvedArgs
}

// SetLastError records the last error message for the script.
func (e *Entry) SetLastError(msg string) {
	e.mu.Lock()
	e.lastError = msg
	e.mu.Unlock()
}

// LastError returns the last error message.
func (e *Entry) LastError() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.lastError
}

// AddSession registers a session as observing this script.
func (e *Entry) AddSession(sessionCode string) {
	e.sessions.Store(sessionCode, struct{}{})
}

// RemoveSession unregisters a session from observing this script.
func (e *Entry) RemoveSession(sessionCode string) {
	e.sessions.Delete(sessionCode)
}

// ListSessions returns the session codes currently observing this script.
func (e *Entry) ListSessions() []string {
	var codes []string
	e.sessions.Range(func(key, value interface{}) bool {
		codes = append(codes, key.(string))
		return true
	})
	return codes
}

// SetOwner sets the owning session for this script (the session that started it).
func (e *Entry) SetOwner(sessionCode string) {
	e.ownerSession.Store(&sessionCode)
}

// Owner returns the session code that owns this script, or empty string if unowned.
func (e *Entry) Owner() string {
	p := e.ownerSession.Load()
	if p == nil {
		return ""
	}
	return *p
}

// ObserverCount returns the number of sessions observing this script.
func (e *Entry) ObserverCount() int {
	count := 0
	e.sessions.Range(func(_, _ interface{}) bool {
		count++
		return true
	})
	return count
}

// TransferOwnership picks a remaining observer to become the new owner.
// Returns the new owner's session code, or empty string if no observers remain.
func (e *Entry) TransferOwnership() string {
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

// Registry manages Entry instances with lock-free operations.
// Keys are projectPath + "\x00" + scriptName for uniqueness.
type Registry struct {
	entries sync.Map // map[string]*Entry
}

// NewRegistry creates a new Registry.
func NewRegistry() *Registry {
	return &Registry{}
}

// entryKey builds the registry key from project path and script name.
func entryKey(projectPath, name string) string {
	return normalizeProjectPath(projectPath) + "\x00" + name
}

func normalizeProjectPath(projectPath string) string {
	if projectPath == "" {
		return ""
	}
	return filepath.Clean(projectPath)
}

// Register adds or returns an existing Entry for the given script.
// If the entry already exists, it is returned as-is (shared across sessions).
//
// A re-register under a different config is an error: two callers disagreeing
// about how a script launches is a bug, and silently keeping either config
// would make the winner depend on registration order. Callers whose config is
// expected to change over the registry's lifetime — because it is reloaded
// from a file the user edits — want [Registry.Upsert].
func (r *Registry) Register(name, projectPath string, cfg *Config) (*Entry, error) {
	if name == "" {
		return nil, fmt.Errorf("script name is required")
	}
	if projectPath == "" {
		return nil, fmt.Errorf("project path is required")
	}

	projectPath = normalizeProjectPath(projectPath)
	key := entryKey(projectPath, name)
	entry := newEntry(name, projectPath, cfg)

	if existing, loaded := r.entries.LoadOrStore(key, entry); loaded {
		existingEntry := existing.(*Entry)
		if !reflect.DeepEqual(existingEntry.Config, cfg) {
			return nil, fmt.Errorf("script %q already registered for project %q with different config", name, projectPath)
		}
		return existingEntry, nil
	}
	return entry, nil
}

// Upsert registers a script, replacing an existing entry whose config differs.
// It reports whether a replacement happened.
//
// This is the counterpart to [Registry.Register] for configs that are reloaded
// from disk. A caller that re-registers a script after the user edited its
// launch command gets an error from Register — correctly, since the registered
// entry describes a different script — but has no way to say "this edit is the
// point." Upsert is that statement.
//
// An equal config reuses the existing entry untouched, so runtime state
// (output history, counters, state machine) survives an idempotent reload.
// A differing config yields a fresh entry: its runtime state described a
// process launched from a config that no longer exists. Session observers and
// ownership do carry over, because they describe who is watching the script,
// not how it runs — dropping them would make the next session disconnect tear
// down a script another session still owns.
func (r *Registry) Upsert(name, projectPath string, cfg *Config) (*Entry, bool, error) {
	if name == "" {
		return nil, false, fmt.Errorf("script name is required")
	}
	if projectPath == "" {
		return nil, false, fmt.Errorf("project path is required")
	}

	projectPath = normalizeProjectPath(projectPath)
	key := entryKey(projectPath, name)

	for {
		entry := newEntry(name, projectPath, cfg)

		existing, loaded := r.entries.LoadOrStore(key, entry)
		if !loaded {
			return entry, false, nil
		}

		existingEntry := existing.(*Entry)
		if reflect.DeepEqual(existingEntry.Config, cfg) {
			return existingEntry, false, nil
		}

		for _, sessionCode := range existingEntry.ListSessions() {
			entry.AddSession(sessionCode)
		}
		if owner := existingEntry.Owner(); owner != "" {
			entry.SetOwner(owner)
		}

		// Swap only if nobody replaced the entry we just inspected; otherwise
		// retry against the winner so a concurrent Upsert cannot be lost.
		if r.entries.CompareAndSwap(key, existing, entry) {
			return entry, true, nil
		}
	}
}

// Get retrieves an Entry by name and project path.
func (r *Registry) Get(name, projectPath string) (*Entry, bool) {
	key := entryKey(projectPath, name)
	val, ok := r.entries.Load(key)
	if !ok {
		return nil, false
	}
	return val.(*Entry), true
}

// List returns all Entry instances for a given project path.
func (r *Registry) List(projectPath string) []*Entry {
	prefix := normalizeProjectPath(projectPath) + "\x00"
	var result []*Entry
	r.entries.Range(func(key, value interface{}) bool {
		k := key.(string)
		if strings.HasPrefix(k, prefix) {
			result = append(result, value.(*Entry))
		}
		return true
	})
	return result
}

// GetByProcessID retrieves an Entry by its process ID.
func (r *Registry) GetByProcessID(processID string) (*Entry, bool) {
	var found *Entry
	r.entries.Range(func(key, value interface{}) bool {
		entry := value.(*Entry)
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

// Remove deletes an Entry by name and project path.
// Returns true if the entry was found and removed.
func (r *Registry) Remove(name, projectPath string) bool {
	key := entryKey(projectPath, name)
	_, existed := r.entries.LoadAndDelete(key)
	return existed
}

// ListAll returns all Entry instances across all projects.
func (r *Registry) ListAll() []*Entry {
	var result []*Entry
	r.entries.Range(func(key, value interface{}) bool {
		result = append(result, value.(*Entry))
		return true
	})
	return result
}

// MakeProcessID creates a unique process ID scoped to a project path.
// Format: <basename>-<hash>:<name> (e.g., "my-project-a1b2:dev")
// The hash is derived from the full path to ensure uniqueness even when
// different directories have the same basename.
func MakeProcessID(projectPath, name string) string {
	if projectPath == "" {
		return name
	}
	projectPath = normalizeProjectPath(projectPath)
	basename := filepath.Base(projectPath)
	hash := shortPathHash(projectPath)
	return fmt.Sprintf("%s-%s:%s", basename, hash, name)
}

// shortPathHash returns an 8-char hex hash of a path for ID uniqueness.
func shortPathHash(path string) string {
	h := fnv.New32a()
	h.Write([]byte(path))
	return fmt.Sprintf("%08x", h.Sum32())
}
