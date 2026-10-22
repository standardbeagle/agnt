// Package store provides persistent key-value storage with three scopes:
// global (project-wide), folder (URL path prefix), and page (specific URL).
package store

import (
	"encoding/json"
	"fmt"
	"github.com/standardbeagle/agnt/internal/pathutil"
	"os"
	"path/filepath"
	"time"
)

// getStorePath returns the path to a store file for a given scope and scope key.
// basePath is the project directory, scope is global/folder/page, scopeKey is the URL/path.
func getStorePath(basePath, scope, scopeKey string) string {
	storeDir := filepath.Join(basePath, StoreDir, scope)

	var filename string
	if scope == ScopeGlobal {
		filename = "global.json"
	} else {
		// Hash the scope key to create a safe filename
		filename = HashScopeKey(scopeKey) + ".json"
	}

	return filepath.Join(storeDir, filename)
}

// ensureStoreDir creates the .agnt/store directory structure if it doesn't exist.
func ensureStoreDir(basePath string) error {
	storePath := filepath.Join(basePath, StoreDir)
	if err := os.MkdirAll(storePath, storeDirMode); err != nil {
		return fmt.Errorf("failed to create store directory: %w", err)
	}

	// Create subdirectories for each scope
	for _, scope := range []string{ScopeGlobal, ScopeFolder, ScopePage} {
		scopePath := filepath.Join(storePath, scope)
		if err := os.MkdirAll(scopePath, storeDirMode); err != nil {
			return fmt.Errorf("failed to create %s scope directory: %w", scope, err)
		}
	}

	return nil
}

// loadStoreFile loads a store file from disk.
// Returns nil with no error if the file doesn't exist.
func loadStoreFile(path string) (*StoreFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read store file: %w", err)
	}

	var sf StoreFile
	if err := json.Unmarshal(data, &sf); err != nil {
		return nil, fmt.Errorf("failed to parse store file: %w", err)
	}

	// Initialize entries map if nil (defensive)
	if sf.Entries == nil {
		sf.Entries = make(map[string]*StoreEntry)
	}

	return &sf, nil
}

// storeFileMode and storeDirMode keep the store private to the daemon's user.
const (
	storeFileMode = 0o600
	storeDirMode  = 0o700
)

// saveStoreFile saves a store file to disk atomically.
// Uses temp file + rename pattern to ensure atomic writes.
func saveStoreFile(path string, sf *StoreFile) error {
	// Update timestamp
	sf.UpdatedAt = time.Now().Format(time.RFC3339)

	// Marshal to JSON
	data, err := json.MarshalIndent(sf, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal store file: %w", err)
	}

	// Ensure parent directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, storeDirMode); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// Write atomically via temp file + rename. The file is private to its
	// owner: the global scope holds secret entries in plaintext (see
	// MetaSecret), so a world-readable mode would hand every secret to every
	// local user. A leftover temp file from an interrupted write is removed
	// first, since WriteFile keeps an existing file's mode.
	if err := pathutil.WriteFileAtomic(path, data, storeFileMode); err != nil {
		return fmt.Errorf("failed to write store file: %w", err)
	}

	return nil
}
