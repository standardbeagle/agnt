package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeScopeFile writes raw bytes directly to the scope file on disk, bypassing
// the store's own marshaling so we can inject corrupt or pathological content.
func writeScopeFile(t *testing.T, basePath, scope, scopeKey string, raw []byte) string {
	t.Helper()
	path := getStorePath(basePath, scope, scopeKey)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, raw, 0o644))
	return path
}

// TestLoadStoreFile_CorruptJSON asserts that every read path surfaces a wrapped
// parse error rather than panicking or silently returning empty data when the
// scope file on disk is garbage.
func TestLoadStoreFile_CorruptJSON(t *testing.T) {
	corruptInputs := []struct {
		name string
		raw  string
	}{
		{"garbage", "this is not json at all {{{{"},
		{"truncated_object", `{"version":1,"entries":{"k":`},
		{"wrong_type", `[1,2,3]`},
		{"binary", "\x00\x01\x02\xff\xfe"},
	}

	for _, ci := range corruptInputs {
		t.Run(ci.name, func(t *testing.T) {
			tempDir := t.TempDir()
			mgr := NewStoreManager()
			path := writeScopeFile(t, tempDir, ScopeGlobal, "", []byte(ci.raw))

			// loadStoreFile directly: wrapped error, nil file, no panic.
			sf, err := loadStoreFile(path)
			require.Error(t, err)
			assert.Nil(t, sf, "corrupt file must not yield a partial StoreFile")
			assert.Contains(t, err.Error(), "failed to parse store file",
				"parse error must be wrapped with context")

			// Get propagates the parse error (not ErrNotFound, not nil).
			_, getErr := mgr.Get(tempDir, ScopeGlobal, "", "anykey")
			require.Error(t, getErr)
			assert.NotErrorIs(t, getErr, ErrNotFound,
				"corrupt file must NOT masquerade as a missing key")
			assert.Contains(t, getErr.Error(), "failed to parse store file")

			// List propagates the parse error and returns a nil slice on error.
			keys, listErr := mgr.List(tempDir, ScopeGlobal, "")
			require.Error(t, listErr)
			assert.Nil(t, keys, "List must return nil keys on parse error, not empty success")
			assert.Contains(t, listErr.Error(), "failed to parse store file")

			// GetAll propagates the parse error and returns a nil map on error.
			all, allErr := mgr.GetAll(tempDir, ScopeGlobal, "")
			require.Error(t, allErr)
			assert.Nil(t, all, "GetAll must return nil map on parse error")
			assert.Contains(t, allErr.Error(), "failed to parse store file")
		})
	}
}

// TestLoadStoreFile_EntriesNull asserts the defensive nil-map init: a valid JSON
// file with "entries": null (or entries omitted) must not cause a nil deref and
// must behave like an empty (but existing) scope.
func TestLoadStoreFile_EntriesNull(t *testing.T) {
	nullCases := []struct {
		name string
		raw  string
	}{
		{"explicit_null", `{"version":1,"scope":"global","entries":null}`},
		{"omitted_entries", `{"version":1,"scope":"global"}`},
		{"empty_object", `{}`},
	}

	for _, nc := range nullCases {
		t.Run(nc.name, func(t *testing.T) {
			tempDir := t.TempDir()
			mgr := NewStoreManager()
			path := writeScopeFile(t, tempDir, ScopeGlobal, "", []byte(nc.raw))

			// loadStoreFile must defensively initialize the map.
			sf, err := loadStoreFile(path)
			require.NoError(t, err)
			require.NotNil(t, sf)
			require.NotNil(t, sf.Entries, "Entries map must be defensively initialized, never nil")
			assert.Empty(t, sf.Entries)

			// Get returns ErrNotFound (file exists but key absent) — no panic.
			_, getErr := mgr.Get(tempDir, ScopeGlobal, "", "missing")
			assert.ErrorIs(t, getErr, ErrNotFound)

			// List returns an empty, non-nil slice — no nil deref.
			keys, listErr := mgr.List(tempDir, ScopeGlobal, "")
			require.NoError(t, listErr)
			assert.NotNil(t, keys)
			assert.Empty(t, keys)

			// A Set after loading a null-entries file must succeed (map writable).
			require.NoError(t, mgr.Set(tempDir, ScopeGlobal, "", "k", "v", nil))
			got, err := mgr.Get(tempDir, ScopeGlobal, "", "k")
			require.NoError(t, err)
			assert.Equal(t, "v", got.Value)
		})
	}
}

// TestSaveStoreFile_TempFileCleanup asserts the atomic temp-file pattern leaves
// no .tmp leftover after a successful rename, and that the final file is valid.
func TestSaveStoreFile_TempFileCleanup(t *testing.T) {
	tempDir := t.TempDir()
	mgr := NewStoreManager()

	require.NoError(t, mgr.Set(tempDir, ScopeGlobal, "", "k1", "v1", nil))
	require.NoError(t, mgr.Set(tempDir, ScopeGlobal, "", "k2", "v2", nil))

	path := getStorePath(tempDir, ScopeGlobal, "")
	tmpPath := path + ".tmp"

	// The destination file exists...
	_, err := os.Stat(path)
	require.NoError(t, err, "store file must exist after Set")

	// ...and no .tmp leftover remains.
	_, tmpErr := os.Stat(tmpPath)
	assert.True(t, os.IsNotExist(tmpErr), "temp file %q must be gone after rename", tmpPath)

	// No stray .tmp files anywhere in the scope dir.
	entries, err := os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)
	for _, e := range entries {
		assert.False(t, strings.HasSuffix(e.Name(), ".tmp"),
			"unexpected leftover temp file: %s", e.Name())
	}

	// Final content round-trips as valid JSON.
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var sf StoreFile
	require.NoError(t, json.Unmarshal(data, &sf))
	assert.Len(t, sf.Entries, 2)
}

// TestSaveStoreFile_UpdatedAtRFC3339 asserts the file-level UpdatedAt stamp is a
// parseable RFC3339 timestamp after a Set, and advances on subsequent writes.
func TestSaveStoreFile_UpdatedAtRFC3339(t *testing.T) {
	tempDir := t.TempDir()
	mgr := NewStoreManager()

	require.NoError(t, mgr.Set(tempDir, ScopeGlobal, "", "k", "v1", nil))

	path := getStorePath(tempDir, ScopeGlobal, "")
	sf1, err := loadStoreFile(path)
	require.NoError(t, err)
	require.NotEmpty(t, sf1.UpdatedAt, "UpdatedAt must be stamped on save")

	parsed, perr := time.Parse(time.RFC3339, sf1.UpdatedAt)
	require.NoError(t, perr, "UpdatedAt must be RFC3339-formatted, got %q", sf1.UpdatedAt)
	assert.WithinDuration(t, time.Now(), parsed, time.Minute,
		"UpdatedAt must reflect a recent write")

	// A later write keeps the timestamp parseable and non-regressing.
	require.NoError(t, mgr.Set(tempDir, ScopeGlobal, "", "k", "v2", nil))
	sf2, err := loadStoreFile(path)
	require.NoError(t, err)
	parsed2, perr2 := time.Parse(time.RFC3339, sf2.UpdatedAt)
	require.NoError(t, perr2)
	assert.False(t, parsed2.Before(parsed), "UpdatedAt must not regress across writes")
}

// TestSaveStoreFile_ConcurrentSet drives N goroutines Set-ing distinct keys into
// the same scope concurrently. Run under -race this proves the manager's lock
// serializes the read-modify-write cycle and the final state is consistent (no
// lost updates, no corruption). No sleeps — sync.WaitGroup gates completion.
func TestSaveStoreFile_ConcurrentSet(t *testing.T) {
	tempDir := t.TempDir()
	mgr := NewStoreManager()

	const n = 50
	var wg sync.WaitGroup
	wg.Add(n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			<-start // release all goroutines together to maximize contention
			key := "key-" + strings.Repeat("x", i%3) + itoa(i)
			require.NoError(t, mgr.Set(tempDir, ScopeGlobal, "", key, i, nil))
		}(i)
	}
	close(start)
	wg.Wait()

	// All N distinct keys must be present — no lost updates under contention.
	keys, err := mgr.List(tempDir, ScopeGlobal, "")
	require.NoError(t, err)
	assert.Len(t, keys, n, "every concurrent Set must persist")

	// File must still be valid JSON (no torn write left it corrupt).
	path := getStorePath(tempDir, ScopeGlobal, "")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var sf StoreFile
	require.NoError(t, json.Unmarshal(data, &sf), "concurrent writes must not corrupt the file")
	assert.Len(t, sf.Entries, n)

	// No leftover temp file from any of the concurrent writes.
	_, tmpErr := os.Stat(path + ".tmp")
	assert.True(t, os.IsNotExist(tmpErr))
}

// itoa is a tiny dependency-free int->string for unique key generation.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}

// TestSaveStoreFile_RenameFailureCleansTemp exercises the rename-failure branch:
// if os.Rename fails, the function must wrap the error AND remove the orphaned
// .tmp file so a failed write leaves no garbage behind. We force the failure by
// making the destination path a directory, which a file rename cannot overwrite.
func TestSaveStoreFile_RenameFailureCleansTemp(t *testing.T) {
	tempDir := t.TempDir()
	path := getStorePath(tempDir, ScopeGlobal, "")

	// Pre-create the destination as a directory so os.Rename(tmp, path) fails.
	require.NoError(t, os.MkdirAll(path, 0o755))

	sf := NewStoreFile(ScopeGlobal, "")
	sf.Entries["k"] = NewStoreEntry("v", nil)

	err := saveStoreFile(path, sf)
	require.Error(t, err, "rename onto a directory must fail")
	assert.Contains(t, err.Error(), "failed to rename temp file",
		"rename failure must be wrapped with context")

	// The orphaned temp file must have been cleaned up.
	_, tmpErr := os.Stat(path + ".tmp")
	assert.True(t, os.IsNotExist(tmpErr),
		"temp file must be removed when rename fails — no garbage left behind")

	// The destination is still the directory we created (untouched by the write).
	info, statErr := os.Stat(path)
	require.NoError(t, statErr)
	assert.True(t, info.IsDir(), "failed write must not have replaced the destination")
}

// TestDelete_LastEntryRemovesFile asserts the data-integrity branch in Delete:
// deleting the LAST key in a scope physically removes the file, while deleting
// one of several keys leaves the file present with the survivors intact.
func TestDelete_LastEntryRemovesFile(t *testing.T) {
	t.Run("last_entry_removes_file", func(t *testing.T) {
		tempDir := t.TempDir()
		mgr := NewStoreManager()
		path := getStorePath(tempDir, ScopeGlobal, "")

		require.NoError(t, mgr.Set(tempDir, ScopeGlobal, "", "only", "v", nil))
		_, statErr := os.Stat(path)
		require.NoError(t, statErr, "file must exist before delete")

		require.NoError(t, mgr.Delete(tempDir, ScopeGlobal, "", "only"))

		_, statErr = os.Stat(path)
		assert.True(t, os.IsNotExist(statErr),
			"deleting the last entry must physically remove the scope file")

		// And the scope now behaves as never-created.
		_, getErr := mgr.Get(tempDir, ScopeGlobal, "", "only")
		assert.ErrorIs(t, getErr, ErrNotFound)
		keys, listErr := mgr.List(tempDir, ScopeGlobal, "")
		require.NoError(t, listErr)
		assert.Empty(t, keys)
	})

	t.Run("partial_delete_keeps_file", func(t *testing.T) {
		tempDir := t.TempDir()
		mgr := NewStoreManager()
		path := getStorePath(tempDir, ScopeGlobal, "")

		require.NoError(t, mgr.Set(tempDir, ScopeGlobal, "", "a", "1", nil))
		require.NoError(t, mgr.Set(tempDir, ScopeGlobal, "", "b", "2", nil))
		require.NoError(t, mgr.Set(tempDir, ScopeGlobal, "", "c", "3", nil))

		require.NoError(t, mgr.Delete(tempDir, ScopeGlobal, "", "b"))

		// File survives because entries remain.
		_, statErr := os.Stat(path)
		require.NoError(t, statErr, "file must remain when other keys survive")

		// Deleted key is gone, survivors intact.
		_, getErr := mgr.Get(tempDir, ScopeGlobal, "", "b")
		assert.ErrorIs(t, getErr, ErrNotFound)
		keys, listErr := mgr.List(tempDir, ScopeGlobal, "")
		require.NoError(t, listErr)
		assert.ElementsMatch(t, []string{"a", "c"}, keys)

		// Deleting a non-existent key returns ErrNotFound (file still present).
		delErr := mgr.Delete(tempDir, ScopeGlobal, "", "missing")
		assert.ErrorIs(t, delErr, ErrNotFound)
		_, statErr = os.Stat(path)
		assert.NoError(t, statErr)
	})
}

// TestCleanURLString_FallbackBranches feeds NormalizeURL inputs that make
// url.Parse fail, forcing the cleanURLString fallback. It must still strip
// query, hash, and trailing slash without panicking.
func TestCleanURLString_FallbackBranches(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// "%zz" has no "://", so cleanURLString just strips ?/#/trailing-slash.
		{"invalid_percent_with_query", "%zz/foo/bar/baz?x=1#y", "%zz/foo/bar/baz"},
		{"control_char_trailing_slash", "\x00/a/b/c/", "\x00/a/b/c"},
		{"hash_only", "%zz/a#frag", "%zz/a"},
		{"reduces_to_root", "?q=1", "/"},
		{"trailing_slashes_collapse", "%zz/x///", "%zz/x"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Confirm we are actually exercising the fallback: url.Parse must fail.
			got := NormalizeURL(tc.in)
			assert.Equal(t, tc.want, got)
			assert.NotContains(t, got, "?", "query must be stripped")
			assert.NotContains(t, got, "#", "hash must be stripped")
			if got != "/" {
				assert.False(t, strings.HasSuffix(got, "/"),
					"non-root result must not end with a trailing slash")
			}
		})
	}
}

// TestExtractPathFolder_FallbackBranches feeds GetFolderKey inputs whose
// normalized form still fails url.Parse, forcing the extractPathFolder fallback.
// The fallback must yield a sane folder key (ends with "/", query/hash stripped,
// no panic).
func TestExtractPathFolder_FallbackBranches(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// No "://": split path segments, drop last, query/hash already stripped.
		{"no_scheme_invalid_percent", "%zz/foo/bar/baz?x=1#y", "/%zz/foo/bar/"},
		{"single_segment_to_root", "%zz?q=1", "/"},
		{"two_segments", "%zz/only", "/%zz/"},
		// With "://": extractPathFolder slices after first "/" past the scheme sep.
		{"scheme_sep_no_path", "%zz://hostonly", "/"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := GetFolderKey(tc.in)
			assert.Equal(t, tc.want, got)
			assert.True(t, strings.HasPrefix(got, "/"), "folder key must start with /")
			assert.True(t, strings.HasSuffix(got, "/"), "folder key must end with /")
			assert.NotContains(t, got, "?", "query must not survive into folder key")
			assert.NotContains(t, got, "#", "hash must not survive into folder key")
		})
	}
}

// TestNewStoreEntry_TypeDiscrimination asserts the value->Type mapping and that
// CreatedAt == UpdatedAt at creation time.
func TestNewStoreEntry_TypeDiscrimination(t *testing.T) {
	cases := []struct {
		name     string
		value    interface{}
		wantType string
	}{
		{"string", "hello", TypeString},
		{"empty_string", "", TypeString},
		{"file_ref_ptr", &FileRef{FileID: "f1", FilePath: "/tmp/x", Size: 10}, TypeFileRef},
		{"int", 42, TypeJSON},
		{"float", 3.14, TypeJSON},
		{"map", map[string]any{"a": 1}, TypeJSON},
		{"slice", []int{1, 2, 3}, TypeJSON},
		{"nil", nil, TypeJSON},
		{"bool", true, TypeJSON},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := time.Now()
			entry := NewStoreEntry(tc.value, nil)
			after := time.Now()

			require.NotNil(t, entry)
			assert.Equal(t, tc.wantType, entry.Type, "type discrimination mismatch")
			assert.Equal(t, tc.value, entry.Value)
			assert.True(t, entry.CreatedAt.Equal(entry.UpdatedAt),
				"CreatedAt must equal UpdatedAt at creation")
			assert.False(t, entry.CreatedAt.Before(before))
			assert.False(t, entry.CreatedAt.After(after))
		})
	}

	// A non-pointer FileRef is NOT TypeFileRef (only *FileRef is matched).
	t.Run("value_file_ref_is_json", func(t *testing.T) {
		entry := NewStoreEntry(FileRef{FileID: "f"}, nil)
		assert.Equal(t, TypeJSON, entry.Type,
			"only *FileRef triggers TypeFileRef; a value FileRef is JSON")
		assert.True(t, entry.CreatedAt.Equal(entry.UpdatedAt))
	})

	// Metadata is carried through unchanged.
	t.Run("metadata_carried", func(t *testing.T) {
		md := map[string]any{"k": "v"}
		entry := NewStoreEntry("x", md)
		assert.Equal(t, md, entry.Metadata)
		assert.Equal(t, TypeString, entry.Type)
	})
}

// TestNeverCreatedScope asserts the empty-but-not-error semantics for a scope
// whose backing file was never written: Get->ErrNotFound, List->empty non-nil
// slice, GetAll->empty non-nil map.
func TestNeverCreatedScope(t *testing.T) {
	scopes := []struct {
		name     string
		scope    string
		scopeKey string
	}{
		{"global", ScopeGlobal, ""},
		{"folder", ScopeFolder, "/products/"},
		{"page", ScopePage, "https://example.com/x/y"},
	}

	for _, sc := range scopes {
		t.Run(sc.name, func(t *testing.T) {
			tempDir := t.TempDir()
			mgr := NewStoreManager()

			// Confirm nothing exists on disk for this scope.
			path := getStorePath(tempDir, sc.scope, sc.scopeKey)
			_, statErr := os.Stat(path)
			require.True(t, os.IsNotExist(statErr), "precondition: scope file must not exist")

			// Get -> ErrNotFound.
			entry, getErr := mgr.Get(tempDir, sc.scope, sc.scopeKey, "anykey")
			assert.Nil(t, entry)
			assert.ErrorIs(t, getErr, ErrNotFound)

			// List -> empty, non-nil slice.
			keys, listErr := mgr.List(tempDir, sc.scope, sc.scopeKey)
			require.NoError(t, listErr)
			assert.NotNil(t, keys, "List must return a non-nil empty slice for a never-created scope")
			assert.Empty(t, keys)

			// GetAll -> empty, non-nil map.
			all, allErr := mgr.GetAll(tempDir, sc.scope, sc.scopeKey)
			require.NoError(t, allErr)
			assert.NotNil(t, all, "GetAll must return a non-nil empty map for a never-created scope")
			assert.Empty(t, all)
		})
	}
}
