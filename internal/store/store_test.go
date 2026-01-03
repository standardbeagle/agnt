package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestURLNormalization(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"https://example.com/products/123?query=test#section", "https://example.com/products/123"},
		{"/products/123/", "/products/123"},
		{"/products/", "/products"},
		{"/", "/"},
		{"https://example.com", "https://example.com/"},
		{"https://example.com/", "https://example.com/"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := NormalizeURL(tt.input)
			if result != tt.expected {
				t.Errorf("NormalizeURL(%q) = %q; want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestGetFolderKey(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"/products/123", "/products/"},
		{"/products/", "/products/"},
		{"/products", "/"},
		{"/", "/"},
		{"https://example.com/api/users/42", "/api/users/"},
		{"/api/v1/items/123/details", "/api/v1/items/123/"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := GetFolderKey(tt.input)
			if result != tt.expected {
				t.Errorf("GetFolderKey(%q) = %q; want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestHashScopeKey(t *testing.T) {
	// Test that hash is consistent
	key1 := HashScopeKey("/products/")
	key2 := HashScopeKey("/products/")
	if key1 != key2 {
		t.Errorf("HashScopeKey not consistent: %q != %q", key1, key2)
	}

	// Test that different keys produce different hashes
	key3 := HashScopeKey("/users/")
	if key1 == key3 {
		t.Errorf("HashScopeKey collision: %q == %q", key1, key3)
	}

	// Test empty key
	keyEmpty := HashScopeKey("")
	if keyEmpty != "global" {
		t.Errorf("HashScopeKey(\"\") = %q; want \"global\"", keyEmpty)
	}

	// Test length is 16 characters
	if len(key1) != 16 {
		t.Errorf("HashScopeKey length = %d; want 16", len(key1))
	}
}

func TestStoreManager_SetAndGet(t *testing.T) {
	tempDir := t.TempDir()
	mgr := NewStoreManager()

	// Test global scope
	err := mgr.Set(tempDir, ScopeGlobal, "", "test-key", "test-value", nil)
	if err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	entry, err := mgr.Get(tempDir, ScopeGlobal, "", "test-key")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	if entry.Value != "test-value" {
		t.Errorf("Get value = %v; want %q", entry.Value, "test-value")
	}

	if entry.Type != TypeString {
		t.Errorf("Entry type = %q; want %q", entry.Type, TypeString)
	}
}

func TestStoreManager_Delete(t *testing.T) {
	tempDir := t.TempDir()
	mgr := NewStoreManager()

	// Set a value
	err := mgr.Set(tempDir, ScopeGlobal, "", "test-key", "test-value", nil)
	if err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	// Delete it
	err = mgr.Delete(tempDir, ScopeGlobal, "", "test-key")
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// Verify it's gone
	_, err = mgr.Get(tempDir, ScopeGlobal, "", "test-key")
	if err != ErrNotFound {
		t.Errorf("Get after delete: got error %v; want %v", err, ErrNotFound)
	}
}

func TestStoreManager_List(t *testing.T) {
	tempDir := t.TempDir()
	mgr := NewStoreManager()

	// Add multiple keys
	keys := []string{"key1", "key2", "key3"}
	for _, key := range keys {
		err := mgr.Set(tempDir, ScopeGlobal, "", key, "value", nil)
		if err != nil {
			t.Fatalf("Set failed: %v", err)
		}
	}

	// List keys
	result, err := mgr.List(tempDir, ScopeGlobal, "")
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}

	if len(result) != len(keys) {
		t.Errorf("List returned %d keys; want %d", len(result), len(keys))
	}

	// Verify all keys are present
	keyMap := make(map[string]bool)
	for _, k := range result {
		keyMap[k] = true
	}

	for _, k := range keys {
		if !keyMap[k] {
			t.Errorf("List missing key %q", k)
		}
	}
}

func TestStoreManager_Clear(t *testing.T) {
	tempDir := t.TempDir()
	mgr := NewStoreManager()

	// Add some keys
	mgr.Set(tempDir, ScopeGlobal, "", "key1", "value1", nil)
	mgr.Set(tempDir, ScopeGlobal, "", "key2", "value2", nil)

	// Clear
	err := mgr.Clear(tempDir, ScopeGlobal, "")
	if err != nil {
		t.Fatalf("Clear failed: %v", err)
	}

	// Verify file is gone
	storePath := getStorePath(tempDir, ScopeGlobal, "")
	if _, err := os.Stat(storePath); !os.IsNotExist(err) {
		t.Errorf("Store file still exists after Clear")
	}

	// List should return empty
	keys, err := mgr.List(tempDir, ScopeGlobal, "")
	if err != nil {
		t.Fatalf("List after clear failed: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("List after clear returned %d keys; want 0", len(keys))
	}
}

func TestStoreManager_GetAll(t *testing.T) {
	tempDir := t.TempDir()
	mgr := NewStoreManager()

	// Add multiple entries
	mgr.Set(tempDir, ScopeGlobal, "", "key1", "value1", nil)
	mgr.Set(tempDir, ScopeGlobal, "", "key2", "value2", nil)

	// Get all
	entries, err := mgr.GetAll(tempDir, ScopeGlobal, "")
	if err != nil {
		t.Fatalf("GetAll failed: %v", err)
	}

	if len(entries) != 2 {
		t.Errorf("GetAll returned %d entries; want 2", len(entries))
	}

	if entries["key1"].Value != "value1" {
		t.Errorf("Entry key1 value = %v; want %q", entries["key1"].Value, "value1")
	}
}

func TestStoreManager_InvalidScope(t *testing.T) {
	tempDir := t.TempDir()
	mgr := NewStoreManager()

	err := mgr.Set(tempDir, "invalid", "", "key", "value", nil)
	if err != ErrInvalidScope {
		t.Errorf("Set with invalid scope: got error %v; want %v", err, ErrInvalidScope)
	}
}

func TestStoreManager_PageScope(t *testing.T) {
	tempDir := t.TempDir()
	mgr := NewStoreManager()

	url := "https://example.com/products/123"

	// Set a page-scoped value
	err := mgr.Set(tempDir, ScopePage, url, "last-visit", "2025-01-01", nil)
	if err != nil {
		t.Fatalf("Set page scope failed: %v", err)
	}

	// Get it back
	entry, err := mgr.Get(tempDir, ScopePage, url, "last-visit")
	if err != nil {
		t.Fatalf("Get page scope failed: %v", err)
	}

	if entry.Value != "2025-01-01" {
		t.Errorf("Page scope value = %v; want %q", entry.Value, "2025-01-01")
	}

	// Verify file was created with hashed name
	hash := HashScopeKey(url)
	storePath := filepath.Join(tempDir, StoreDir, ScopePage, hash+".json")
	if _, err := os.Stat(storePath); os.IsNotExist(err) {
		t.Errorf("Page scope file not created at %q", storePath)
	}
}

func TestStoreManager_FolderScope(t *testing.T) {
	tempDir := t.TempDir()
	mgr := NewStoreManager()

	folderKey := "/products/"

	// Set a folder-scoped value
	err := mgr.Set(tempDir, ScopeFolder, folderKey, "item-count", 42, nil)
	if err != nil {
		t.Fatalf("Set folder scope failed: %v", err)
	}

	// Get it back
	entry, err := mgr.Get(tempDir, ScopeFolder, folderKey, "item-count")
	if err != nil {
		t.Fatalf("Get folder scope failed: %v", err)
	}

	// JSON numbers come back as float64
	if entry.Value != float64(42) {
		t.Errorf("Folder scope value = %v (type %T); want 42", entry.Value, entry.Value)
	}
}

func TestStoreManager_UpdatePreservesCreatedAt(t *testing.T) {
	tempDir := t.TempDir()
	mgr := NewStoreManager()

	// Create initial entry
	err := mgr.Set(tempDir, ScopeGlobal, "", "key", "value1", nil)
	if err != nil {
		t.Fatalf("Initial Set failed: %v", err)
	}

	entry1, _ := mgr.Get(tempDir, ScopeGlobal, "", "key")
	createdAt1 := entry1.CreatedAt

	// Update the entry
	err = mgr.Set(tempDir, ScopeGlobal, "", "key", "value2", nil)
	if err != nil {
		t.Fatalf("Update Set failed: %v", err)
	}

	entry2, _ := mgr.Get(tempDir, ScopeGlobal, "", "key")

	// CreatedAt should be the same
	if !entry2.CreatedAt.Equal(createdAt1) {
		t.Errorf("CreatedAt changed on update: %v != %v", entry2.CreatedAt, createdAt1)
	}

	// UpdatedAt should be different
	if entry2.UpdatedAt.Equal(entry1.UpdatedAt) {
		t.Errorf("UpdatedAt not changed on update")
	}

	// Value should be updated
	if entry2.Value != "value2" {
		t.Errorf("Value not updated: got %v; want %q", entry2.Value, "value2")
	}
}

func TestStoreManager_Metadata(t *testing.T) {
	tempDir := t.TempDir()
	mgr := NewStoreManager()

	metadata := map[string]any{
		"author": "test",
		"tags":   []string{"important", "test"},
	}

	err := mgr.Set(tempDir, ScopeGlobal, "", "key", "value", metadata)
	if err != nil {
		t.Fatalf("Set with metadata failed: %v", err)
	}

	entry, err := mgr.Get(tempDir, ScopeGlobal, "", "key")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	if entry.Metadata["author"] != "test" {
		t.Errorf("Metadata author = %v; want %q", entry.Metadata["author"], "test")
	}
}
