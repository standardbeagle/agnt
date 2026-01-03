// Package store provides persistent key-value storage with three scopes:
// global (project-wide), folder (URL path prefix), and page (specific URL).
package store

import (
	"time"
)

// Scope constants for the three storage levels.
const (
	ScopeGlobal = "global"
	ScopeFolder = "folder"
	ScopePage   = "page"
)

// Entry type constants.
const (
	TypeString  = "string"
	TypeJSON    = "json"
	TypeFileRef = "file_ref"
)

// StoreDir is the directory within each project for store data.
const StoreDir = ".agnt/store"

// StoreEntry represents a stored value with metadata.
type StoreEntry struct {
	Value     interface{}    `json:"value"`
	Type      string         `json:"type"` // string, json, file_ref
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// FileRef represents a reference to a large file stored separately.
type FileRef struct {
	FileID      string `json:"file_id"`
	FilePath    string `json:"file_path"`
	Size        int64  `json:"size"`
	ContentType string `json:"content_type,omitempty"`
}

// StoreFile represents the JSON structure of a scope file.
type StoreFile struct {
	Version   int                    `json:"version"`
	Scope     string                 `json:"scope"`
	ScopeKey  string                 `json:"scope_key"`
	Entries   map[string]*StoreEntry `json:"entries"`
	UpdatedAt string                 `json:"updated_at"`
}

// StoreRequest represents a store operation request.
type StoreRequest struct {
	Action   string         `json:"action"`    // get, set, delete, list, clear, get_all
	Scope    string         `json:"scope"`     // global, folder, page
	ScopeKey string         `json:"scope_key"` // URL or path for page/folder
	Key      string         `json:"key"`
	Value    interface{}    `json:"value,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// StoreResponse represents a store operation response.
type StoreResponse struct {
	Success bool                   `json:"success"`
	Entry   *StoreEntry            `json:"entry,omitempty"`
	Entries map[string]*StoreEntry `json:"entries,omitempty"`
	Keys    []string               `json:"keys,omitempty"`
	Error   string                 `json:"error,omitempty"`
}

// NewStoreFile creates a new empty store file for a scope.
func NewStoreFile(scope, scopeKey string) *StoreFile {
	return &StoreFile{
		Version:  1,
		Scope:    scope,
		ScopeKey: scopeKey,
		Entries:  make(map[string]*StoreEntry),
	}
}

// NewStoreEntry creates a new store entry with the current timestamp.
func NewStoreEntry(value interface{}, metadata map[string]any) *StoreEntry {
	now := time.Now()
	valueType := TypeJSON
	if _, ok := value.(string); ok {
		valueType = TypeString
	}
	if _, ok := value.(*FileRef); ok {
		valueType = TypeFileRef
	}

	return &StoreEntry{
		Value:     value,
		Type:      valueType,
		CreatedAt: now,
		UpdatedAt: now,
		Metadata:  metadata,
	}
}
