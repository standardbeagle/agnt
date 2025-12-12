package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
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
type StateManager struct {
	statePath string
	mu        sync.RWMutex
	state     PersistentState

	// Debounce saves to avoid excessive disk writes
	saveTimer    *time.Timer
	saveInterval time.Duration
	pendingSave  bool
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

// NewStateManager creates a new state manager.
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
	}

	if config.AutoLoad {
		// Best-effort load - ignore errors, will start with empty state
		_ = sm.Load()
	}

	return sm
}

// Load loads state from disk.
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

// Save saves state to disk.
func (sm *StateManager) Save() error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	return sm.saveLocked()
}

// saveLocked saves state without acquiring the lock.
func (sm *StateManager) saveLocked() error {
	sm.state.UpdatedAt = time.Now().Format(time.RFC3339)

	data, err := json.MarshalIndent(sm.state, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal state: %w", err)
	}

	// Ensure directory exists
	dir := filepath.Dir(sm.statePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create state directory: %w", err)
	}

	// Write atomically via temp file
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

// SaveDebounced saves state with debouncing to avoid excessive writes.
func (sm *StateManager) SaveDebounced() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.pendingSave = true

	// Reset timer if already running
	if sm.saveTimer != nil {
		sm.saveTimer.Stop()
	}

	sm.saveTimer = time.AfterFunc(sm.saveInterval, func() {
		sm.mu.Lock()
		if sm.pendingSave {
			sm.pendingSave = false
			_ = sm.saveLocked() // Best-effort save, errors are non-critical
		}
		sm.mu.Unlock()
	})
}

// SetOverlayEndpoint updates the overlay endpoint in state.
func (sm *StateManager) SetOverlayEndpoint(endpoint string) {
	sm.mu.Lock()
	sm.state.OverlayEndpoint = endpoint
	sm.mu.Unlock()

	sm.SaveDebounced()
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
	defer sm.mu.Unlock()

	// Check if proxy already exists (update it)
	for i, p := range sm.state.Proxies {
		if p.ID == config.ID {
			sm.state.Proxies[i] = config
			sm.mu.Unlock()
			sm.SaveDebounced()
			sm.mu.Lock()
			return
		}
	}

	// Add new proxy
	if config.CreatedAt == "" {
		config.CreatedAt = time.Now().Format(time.RFC3339)
	}
	sm.state.Proxies = append(sm.state.Proxies, config)

	sm.mu.Unlock()
	sm.SaveDebounced()
	sm.mu.Lock()
}

// RemoveProxy removes a proxy configuration from state.
func (sm *StateManager) RemoveProxy(id string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	for i, p := range sm.state.Proxies {
		if p.ID == id {
			sm.state.Proxies = append(sm.state.Proxies[:i], sm.state.Proxies[i+1:]...)
			sm.mu.Unlock()
			sm.SaveDebounced()
			sm.mu.Lock()
			return
		}
	}
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

// Clear removes all state.
func (sm *StateManager) Clear() error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.state = PersistentState{Version: 1}

	// Remove state file
	if err := os.Remove(sm.statePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove state file: %w", err)
	}

	return nil
}

// Flush ensures any pending saves are written immediately.
func (sm *StateManager) Flush() error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.saveTimer != nil {
		sm.saveTimer.Stop()
		sm.saveTimer = nil
	}

	if sm.pendingSave {
		sm.pendingSave = false
		return sm.saveLocked()
	}

	return nil
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
