package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/standardbeagle/agnt/internal/debug"
)

// PersistentProxyConfig stores the configuration needed to recreate a proxy.
type PersistentProxyConfig struct {
	ID         string `json:"id"`
	TargetURL  string `json:"target_url"`
	Port       int    `json:"port"`
	MaxLogSize int    `json:"max_log_size"`
	Path       string `json:"path"`
	CreatedAt  string `json:"created_at"`
}

// PersistentState stores daemon state that should survive restarts.
type PersistentState struct {
	Version         int                     `json:"version"`
	OverlayEndpoint string                  `json:"overlay_endpoint,omitempty"`
	Proxies         []PersistentProxyConfig `json:"proxies,omitempty"`
	UpdatedAt       string                  `json:"updated_at"`
}

// StateManager handles persisting and restoring daemon state.
// Reads are served from in-memory state. Writes are enqueued to a
// background goroutine via a write-behind channel, so the mutex
// never touches disk I/O.
type StateManager struct {
	statePath string
	mu        sync.RWMutex
	state     PersistentState

	// Write-behind channel signals the background writer.
	// A non-blocking send coalesces multiple rapid mutations into one write.
	writeCh   chan struct{}
	flushCh   chan chan error
	stopCh    chan struct{}
	stopped   chan struct{}
	closeOnce sync.Once

	// Debounce interval: after a mutation, the writer waits this long
	// for more mutations before flushing to disk.
	saveInterval time.Duration
}

// StateManagerConfig configures the state manager.
type StateManagerConfig struct {
	// StatePath is the path to the state file.
	// If empty, uses default location.
	StatePath string

	// SaveInterval is the minimum time between saves (debouncing).
	SaveInterval time.Duration

	// AutoLoad loads state on creation if true.
	AutoLoad bool
}

// DefaultStateManagerConfig returns sensible defaults.
func DefaultStateManagerConfig() StateManagerConfig {
	return StateManagerConfig{
		StatePath:    DefaultStatePath(),
		SaveInterval: 1 * time.Second,
		AutoLoad:     true,
	}
}

// DefaultStatePath returns the default state file path.
func DefaultStatePath() string {
	// Use XDG_STATE_HOME if available
	if stateHome := os.Getenv("XDG_STATE_HOME"); stateHome != "" {
		return filepath.Join(stateHome, "devtool-mcp", "state.json")
	}

	// Fall back to ~/.local/state
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "state", "devtool-mcp", "state.json")
	}

	// Last resort: temp directory
	return filepath.Join(os.TempDir(), "devtool-mcp-state.json")
}

// NewStateManager creates a new state manager with a background writer.
func NewStateManager(config StateManagerConfig) *StateManager {
	if config.StatePath == "" {
		config.StatePath = DefaultStatePath()
	}
	if config.SaveInterval == 0 {
		config.SaveInterval = 1 * time.Second
	}

	sm := &StateManager{
		statePath:    config.StatePath,
		saveInterval: config.SaveInterval,
		state: PersistentState{
			Version: 1,
		},
		writeCh: make(chan struct{}, 1),
		flushCh: make(chan chan error),
		stopCh:  make(chan struct{}),
		stopped: make(chan struct{}),
	}

	if config.AutoLoad {
		// Best-effort cache warm: the persisted state is a cache, so a load
		// failure (missing/corrupt file) is safe to ignore — we start with an
		// empty state and rebuild. Log it so a persistently unreadable state
		// file is diagnosable rather than silently invisible.
		if err := sm.Load(); err != nil {
			debug.Log("state", "auto-load of persisted state failed, starting empty: %v", err)
		}
	}

	go sm.writeLoop()

	return sm
}

// Load reads state from disk into memory. Only called at startup.
func (sm *StateManager) Load() error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	data, err := os.ReadFile(sm.statePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No existing state file is normal
		}
		return fmt.Errorf("failed to read state file: %w", err)
	}

	var state PersistentState
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("failed to parse state file: %w", err)
	}

	sm.state = state
	return nil
}

// Save saves state to disk synchronously by flushing the write-behind channel.
func (sm *StateManager) Save() error {
	return sm.Flush()
}

// enqueueWrite signals the background writer that state has changed.
// Non-blocking: if a signal is already pending, this is a no-op (coalescing).
func (sm *StateManager) enqueueWrite() {
	select {
	case sm.writeCh <- struct{}{}:
	default:
		// Signal already pending — write will pick up latest state
	}
}

// snapshotState returns a serialized copy of the current in-memory state.
// Caller must NOT hold the lock.
func (sm *StateManager) snapshotState() ([]byte, error) {
	sm.mu.RLock()
	s := sm.state
	s.UpdatedAt = time.Now().Format(time.RFC3339)
	// Deep copy proxies for the snapshot
	proxies := make([]PersistentProxyConfig, len(sm.state.Proxies))
	copy(proxies, sm.state.Proxies)
	s.Proxies = proxies
	sm.mu.RUnlock()

	return json.MarshalIndent(s, "", "  ")
}

// writeToDisk writes the given data to the state file atomically.
// No lock is held during this operation.
func (sm *StateManager) writeToDisk(data []byte) error {
	dir := filepath.Dir(sm.statePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create state directory: %w", err)
	}

	tmpPath := sm.statePath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write state file: %w", err)
	}

	if err := os.Rename(tmpPath, sm.statePath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to rename state file: %w", err)
	}

	return nil
}

// writeLoop is the background goroutine that drains write signals and
// persists state to disk. It coalesces rapid mutations by waiting
// saveInterval after the first signal before writing.
func (sm *StateManager) writeLoop() {
	defer close(sm.stopped)

	for {
		select {
		case <-sm.writeCh:
			// Debounce: wait for more mutations to coalesce
			timer := time.NewTimer(sm.saveInterval)
			select {
			case <-timer.C:
				// Debounce expired, write now
			case done := <-sm.flushCh:
				timer.Stop()
				// Flush requested — write immediately and respond
				done <- sm.doWrite()
				continue
			case <-sm.stopCh:
				timer.Stop()
				// Shutting down — do final write
				sm.doWrite()
				return
			}
			// Drain any extra signals that arrived during debounce
			sm.drainWriteCh()
			sm.doWrite()

		case done := <-sm.flushCh:
			// Flush with no pending write — still write current state
			done <- sm.doWrite()

		case <-sm.stopCh:
			// Drain any pending write signal and flush
			sm.drainWriteCh()
			sm.doWrite()
			return
		}
	}
}

// drainWriteCh empties the write channel without blocking.
func (sm *StateManager) drainWriteCh() {
	select {
	case <-sm.writeCh:
	default:
	}
}

// doWrite snapshots and writes state to disk.
func (sm *StateManager) doWrite() error {
	data, err := sm.snapshotState()
	if err != nil {
		return fmt.Errorf("failed to marshal state: %w", err)
	}
	return sm.writeToDisk(data)
}

// SaveDebounced enqueues an async write. Multiple rapid calls coalesce.
func (sm *StateManager) SaveDebounced() {
	sm.enqueueWrite()
}

// SetOverlayEndpoint updates the overlay endpoint in state.
func (sm *StateManager) SetOverlayEndpoint(endpoint string) {
	sm.mu.Lock()
	sm.state.OverlayEndpoint = endpoint
	sm.mu.Unlock()

	sm.enqueueWrite()
}

// GetOverlayEndpoint returns the persisted overlay endpoint.
func (sm *StateManager) GetOverlayEndpoint() string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.state.OverlayEndpoint
}

// AddProxy adds a proxy configuration to state.
func (sm *StateManager) AddProxy(config PersistentProxyConfig) {
	sm.mu.Lock()
	// Check if proxy already exists (update it)
	for i, p := range sm.state.Proxies {
		if p.ID == config.ID {
			sm.state.Proxies[i] = config
			sm.mu.Unlock()
			sm.enqueueWrite()
			return
		}
	}

	// Add new proxy
	if config.CreatedAt == "" {
		config.CreatedAt = time.Now().Format(time.RFC3339)
	}
	sm.state.Proxies = append(sm.state.Proxies, config)
	sm.mu.Unlock()

	sm.enqueueWrite()
}

// RemoveProxy removes a proxy configuration from state.
func (sm *StateManager) RemoveProxy(id string) {
	sm.mu.Lock()
	for i, p := range sm.state.Proxies {
		if p.ID == id {
			sm.state.Proxies = append(sm.state.Proxies[:i], sm.state.Proxies[i+1:]...)
			sm.mu.Unlock()
			sm.enqueueWrite()
			return
		}
	}
	sm.mu.Unlock()
}

// GetProxies returns all persisted proxy configurations.
func (sm *StateManager) GetProxies() []PersistentProxyConfig {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	// Return a copy
	result := make([]PersistentProxyConfig, len(sm.state.Proxies))
	copy(result, sm.state.Proxies)
	return result
}

// GetProxy returns a specific proxy configuration.
func (sm *StateManager) GetProxy(id string) (PersistentProxyConfig, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	for _, p := range sm.state.Proxies {
		if p.ID == id {
			return p, true
		}
	}
	return PersistentProxyConfig{}, false
}

// Clear removes all state and deletes the state file.
func (sm *StateManager) Clear() error {
	sm.mu.Lock()
	sm.state = PersistentState{Version: 1}
	sm.mu.Unlock()

	// Remove state file directly (no need to go through write channel)
	if err := os.Remove(sm.statePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove state file: %w", err)
	}

	return nil
}

// Flush ensures any pending saves are written immediately.
// Blocks until the write completes.
func (sm *StateManager) Flush() error {
	done := make(chan error, 1)
	select {
	case sm.flushCh <- done:
		return <-done
	case <-sm.stopped:
		// Writer already stopped — do a direct write
		return sm.doWrite()
	}
}

// Close stops the background writer after flushing pending state.
// Safe to call multiple times.
func (sm *StateManager) Close() error {
	var err error
	sm.closeOnce.Do(func() {
		close(sm.stopCh)
		<-sm.stopped
	})
	return err
}

// State returns a copy of the current state.
func (sm *StateManager) State() PersistentState {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	// Deep copy proxies
	proxies := make([]PersistentProxyConfig, len(sm.state.Proxies))
	copy(proxies, sm.state.Proxies)

	return PersistentState{
		Version:         sm.state.Version,
		OverlayEndpoint: sm.state.OverlayEndpoint,
		Proxies:         proxies,
		UpdatedAt:       sm.state.UpdatedAt,
	}
}
