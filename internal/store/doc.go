// Package store implements a scoped key-value store for agnt.
// Data is organized by scope (global, folder, page) and persisted to
// JSON files on disk under the project path.
//
// Key types:
//   - StoreManager: manages multiple scopes with file-backed persistence
//   - StoreEntry: a single stored value with metadata and timestamps
package store
