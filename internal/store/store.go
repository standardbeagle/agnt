// Package store provides persistent key-value storage with three scopes:
// global (project-wide), folder (URL path prefix), and page (specific URL).
package store

import (
	"fmt"
	"os"
	"sync"
)

var (
	// ErrNotFound is returned when a key doesn't exist.
	ErrNotFound = fmt.Errorf("key not found")

	// ErrInvalidScope is returned when an invalid scope is provided.
	ErrInvalidScope = fmt.Errorf("invalid scope: must be global, folder, or page")
)

// StoreManager manages persistent key-value storage with file-based scopes.
type StoreManager struct {
	mu sync.RWMutex
}

// NewStoreManager creates a new store manager.
func NewStoreManager() *StoreManager {
	return &StoreManager{}
}

// validateScope checks if the scope is valid.
func validateScope(scope string) error {
	if scope != ScopeGlobal && scope != ScopeFolder && scope != ScopePage {
		return ErrInvalidScope
	}
	return nil
}

// Get retrieves a value from the store.
func (m *StoreManager) Get(basePath, scope, scopeKey, key string) (*StoreEntry, error) {
	if err := validateScope(scope); err != nil {
		return nil, err
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	storePath := getStorePath(basePath, scope, scopeKey)
	sf, err := loadStoreFile(storePath)
	if err != nil {
		return nil, err
	}

	if sf == nil || sf.Entries == nil {
		return nil, ErrNotFound
	}

	entry, ok := sf.Entries[key]
	if !ok {
		return nil, ErrNotFound
	}

	return entry, nil
}

// Set stores a value in the store.
func (m *StoreManager) Set(basePath, scope, scopeKey, key string, value interface{}, metadata map[string]any) error {
	if err := validateScope(scope); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Ensure store directory exists
	if err := ensureStoreDir(basePath); err != nil {
		return err
	}

	storePath := getStorePath(basePath, scope, scopeKey)

	// Load existing file or create new
	sf, err := loadStoreFile(storePath)
	if err != nil {
		return err
	}

	if sf == nil {
		sf = NewStoreFile(scope, scopeKey)
	}

	// Create or update entry
	if existing, ok := sf.Entries[key]; ok {
		// Update existing entry, preserve creation time
		entry := NewStoreEntry(value, metadata)
		entry.CreatedAt = existing.CreatedAt
		sf.Entries[key] = entry
	} else {
		// Create new entry
		sf.Entries[key] = NewStoreEntry(value, metadata)
	}

	// Save atomically
	return saveStoreFile(storePath, sf)
}

// Delete removes a key from the store.
func (m *StoreManager) Delete(basePath, scope, scopeKey, key string) error {
	if err := validateScope(scope); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	storePath := getStorePath(basePath, scope, scopeKey)

	// Load existing file
	sf, err := loadStoreFile(storePath)
	if err != nil {
		return err
	}

	if sf == nil || sf.Entries == nil {
		return ErrNotFound
	}

	// Check if key exists
	if _, ok := sf.Entries[key]; !ok {
		return ErrNotFound
	}

	// Delete the key
	delete(sf.Entries, key)

	// If no entries left, remove the file
	if len(sf.Entries) == 0 {
		if err := os.Remove(storePath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to remove empty store file: %w", err)
		}
		return nil
	}

	// Save updated file
	return saveStoreFile(storePath, sf)
}

// List returns all keys in a scope.
func (m *StoreManager) List(basePath, scope, scopeKey string) ([]string, error) {
	if err := validateScope(scope); err != nil {
		return nil, err
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	storePath := getStorePath(basePath, scope, scopeKey)
	sf, err := loadStoreFile(storePath)
	if err != nil {
		return nil, err
	}

	if sf == nil || sf.Entries == nil {
		return []string{}, nil
	}

	keys := make([]string, 0, len(sf.Entries))
	for k := range sf.Entries {
		keys = append(keys, k)
	}

	return keys, nil
}

// Clear removes all entries from a scope.
func (m *StoreManager) Clear(basePath, scope, scopeKey string) error {
	if err := validateScope(scope); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	storePath := getStorePath(basePath, scope, scopeKey)

	// Simply remove the file
	if err := os.Remove(storePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove store file: %w", err)
	}

	return nil
}

// GetAll returns all entries in a scope.
func (m *StoreManager) GetAll(basePath, scope, scopeKey string) (map[string]*StoreEntry, error) {
	if err := validateScope(scope); err != nil {
		return nil, err
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	storePath := getStorePath(basePath, scope, scopeKey)
	sf, err := loadStoreFile(storePath)
	if err != nil {
		return nil, err
	}

	if sf == nil || sf.Entries == nil {
		return make(map[string]*StoreEntry), nil
	}

	return sf.Entries, nil
}
