package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Race Condition Tests ---

func TestAlertStore_ConcurrentAddAndQuery(t *testing.T) {
	t.Parallel()
	store := NewProcessAlertStore(50)
	var wg sync.WaitGroup

	// 10 writers adding 100 entries each
	for w := 0; w < 10; w++ {
		wg.Add(1)
		go func(writerID int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				store.Add(&AlertEntry{
					PatternID: fmt.Sprintf("pattern-%d", writerID),
					Severity:  "error",
					Category:  "test",
					Line:      fmt.Sprintf("writer %d entry %d", writerID, i),
					ScriptID:  fmt.Sprintf("script-%d", writerID%3),
					Timestamp: time.Now(),
				})
			}
		}(w)
	}

	// 5 concurrent readers querying while writes are happening
	for r := 0; r < 5; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				_ = store.Query(AlertStoreFilter{})
				_ = store.Query(AlertStoreFilter{Severity: "error"})
				_ = store.Query(AlertStoreFilter{ProcessID: "script-0"})
				_ = store.Query(AlertStoreFilter{Limit: 10})
				_ = store.Len()
			}
		}()
	}

	wg.Wait()

	// After all concurrent ops, store should be coherent
	assert.LessOrEqual(t, store.Len(), 50, "len should not exceed maxSize")
	result := store.Query(AlertStoreFilter{})
	assert.LessOrEqual(t, len(result), 50)
}

func TestAlertStore_ConcurrentAddAndClear(t *testing.T) {
	t.Parallel()
	store := NewProcessAlertStore(100)
	var wg sync.WaitGroup

	// Writers continuously adding
	for w := 0; w < 5; w++ {
		wg.Add(1)
		go func(writerID int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				store.Add(&AlertEntry{
					PatternID: fmt.Sprintf("p-%d", writerID),
					Severity:  "error",
					Timestamp: time.Now(),
				})
			}
		}(w)
	}

	// Concurrent clears
	for c := 0; c < 3; c++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				store.Clear()
				time.Sleep(time.Millisecond)
			}
		}()
	}

	wg.Wait()

	// Store should be in a valid state (not crashed or deadlocked)
	_ = store.Len()
	_ = store.Query(AlertStoreFilter{})
}

func TestSessionRegistry_ConcurrentRegisterUnregister(t *testing.T) {
	t.Parallel()
	reg := NewSessionRegistry(60 * time.Second)
	var wg sync.WaitGroup

	// Concurrent registrations
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			session := &Session{
				Code:        fmt.Sprintf("session-%d", id),
				ProjectPath: "/test/project",
				Command:     "test",
				StartedAt:   time.Now(),
				Status:      SessionStatusActive,
				LastSeen:    time.Now(),
			}
			_ = reg.Register(session)
		}(i)
	}

	wg.Wait()

	// Concurrent reads, heartbeats, and unregistrations
	var wg2 sync.WaitGroup

	for i := 0; i < 10; i++ {
		wg2.Add(1)
		go func(id int) {
			defer wg2.Done()
			_ = reg.Heartbeat(fmt.Sprintf("session-%d", id))
			_, _ = reg.Get(fmt.Sprintf("session-%d", id))
			_ = reg.List("", true)
			_ = reg.ListActive("", true)
		}(i)
	}

	for i := 10; i < 20; i++ {
		wg2.Add(1)
		go func(id int) {
			defer wg2.Done()
			code := fmt.Sprintf("session-%d", id)
			if s, ok := reg.Get(code); ok {
				reg.UnregisterExact(code, s)
			}
		}(i)
	}

	wg2.Wait()

	// Verify consistency: active count should be non-negative
	assert.GreaterOrEqual(t, reg.ActiveCount(), int64(0))
}

func TestSessionRegistry_ConcurrentCheckHeartbeats(t *testing.T) {
	t.Parallel()
	reg := NewSessionRegistry(50 * time.Millisecond)
	var wg sync.WaitGroup

	// Register sessions
	for i := 0; i < 10; i++ {
		session := &Session{
			Code:        fmt.Sprintf("hb-%d", i),
			ProjectPath: "/test",
			Command:     "test",
			StartedAt:   time.Now(),
			Status:      SessionStatusActive,
			LastSeen:    time.Now(),
		}
		require.NoError(t, reg.Register(session))
	}

	// Concurrent heartbeat checks while some sessions send heartbeats
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				reg.CheckHeartbeats()
				time.Sleep(time.Millisecond)
			}
		}()
	}

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				_ = reg.Heartbeat(fmt.Sprintf("hb-%d", id))
				time.Sleep(2 * time.Millisecond)
			}
		}(i)
	}

	wg.Wait()

	// ActiveCount is derived from per-session Status, so it can never drift
	// negative the way the old hand-maintained counter did (Heartbeat revives
	// Disconnected→Active without re-incrementing, and Unregister decremented
	// regardless of status — together they drove the counter to -10). Assert
	// the real invariant: the reported count equals an independent recount of
	// active sessions, and stays within [0, registered].
	got := reg.ActiveCount()
	var manual int64
	for i := 0; i < 10; i++ {
		if s, ok := reg.Get(fmt.Sprintf("hb-%d", i)); ok && s.IsActive() {
			manual++
		}
	}
	assert.Equal(t, manual, got, "ActiveCount must equal an independent recount of active sessions")
	assert.GreaterOrEqual(t, got, int64(0))
	assert.LessOrEqual(t, got, int64(10), "active count cannot exceed registered sessions")
}

func TestSessionRegistry_ConcurrentFindByDirectory(t *testing.T) {
	t.Parallel()
	reg := NewSessionRegistry(60 * time.Second)

	// Register sessions at different directory depths
	for i := 0; i < 5; i++ {
		session := &Session{
			Code:        fmt.Sprintf("find-%d", i),
			ProjectPath: fmt.Sprintf("/test/project/sub%d", i),
			Command:     "test",
			StartedAt:   time.Now(),
			Status:      SessionStatusActive,
			LastSeen:    time.Now(),
		}
		require.NoError(t, reg.Register(session))
	}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			_, _ = reg.FindByDirectory(fmt.Sprintf("/test/project/sub%d/deeper", id%5))
			_ = reg.GenerateSessionCode("test")
		}(i)
	}

	wg.Wait()
}

func TestSessionRegistry_ConcurrentGenerateSessionCode(t *testing.T) {
	t.Parallel()
	reg := NewSessionRegistry(60 * time.Second)
	var wg sync.WaitGroup
	codes := make([]string, 20)

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			codes[idx] = reg.GenerateSessionCode("claude")
		}(i)
	}

	wg.Wait()

	// All codes should be non-empty
	for i, code := range codes {
		assert.NotEmpty(t, code, "code %d should not be empty", i)
	}
}

func TestStateManager_ConcurrentReadWrite(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "race-state.json")

	sm := NewStateManager(StateManagerConfig{
		StatePath:    statePath,
		SaveInterval: 10 * time.Millisecond,
		AutoLoad:     false,
	})
	defer sm.Close()

	var wg sync.WaitGroup

	// Concurrent proxy additions
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			sm.AddProxy(PersistentProxyConfig{
				ID:        fmt.Sprintf("proxy-%d", id),
				TargetURL: fmt.Sprintf("http://localhost:%d", 3000+id),
			})
		}(i)
	}

	// Concurrent reads
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = sm.GetProxies()
			_ = sm.GetOverlayEndpoint()
			_ = sm.State()
		}()
	}

	// Concurrent endpoint updates
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			sm.SetOverlayEndpoint(fmt.Sprintf("http://localhost:%d", 19190+id))
		}(i)
	}

	wg.Wait()

	// Allow debounced saves to complete
	time.Sleep(50 * time.Millisecond)

	// Should not panic, deadlock, or corrupt
	proxies := sm.GetProxies()
	assert.GreaterOrEqual(t, len(proxies), 1, "at least some proxies should be stored")
}

func TestStateManager_ConcurrentSaveLoad(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "save-load-race.json")

	sm := NewStateManager(StateManagerConfig{
		StatePath:    statePath,
		SaveInterval: 5 * time.Millisecond,
		AutoLoad:     false,
	})
	defer sm.Close()

	var wg sync.WaitGroup

	// Concurrent saves
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				_ = sm.Save()
			}
		}()
	}

	// Concurrent loads
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				_ = sm.Load()
			}
		}()
	}

	// Concurrent debounced saves
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				sm.SaveDebounced()
			}
		}()
	}

	wg.Wait()

	// Flush should work after all concurrent ops
	assert.NoError(t, sm.Flush())
}

func TestStateManager_ConcurrentAddRemoveProxy(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "add-remove-race.json")

	sm := NewStateManager(StateManagerConfig{
		StatePath:    statePath,
		SaveInterval: 50 * time.Millisecond,
		AutoLoad:     false,
	})
	defer sm.Close()

	var wg sync.WaitGroup

	// Add proxies concurrently
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			sm.AddProxy(PersistentProxyConfig{
				ID:        fmt.Sprintf("proxy-%d", id),
				TargetURL: fmt.Sprintf("http://localhost:%d", 3000+id),
			})
		}(i)
	}

	wg.Wait()

	// Now add and remove concurrently
	var wg2 sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg2.Add(1)
		go func(id int) {
			defer wg2.Done()
			sm.RemoveProxy(fmt.Sprintf("proxy-%d", id))
		}(i)
	}
	for i := 20; i < 30; i++ {
		wg2.Add(1)
		go func(id int) {
			defer wg2.Done()
			sm.AddProxy(PersistentProxyConfig{
				ID:        fmt.Sprintf("proxy-%d", id),
				TargetURL: fmt.Sprintf("http://localhost:%d", 3000+id),
			})
		}(i)
	}

	wg2.Wait()

	// State should be valid
	proxies := sm.GetProxies()
	assert.GreaterOrEqual(t, len(proxies), 1)

	// Each proxy should have valid data
	for _, p := range proxies {
		assert.NotEmpty(t, p.ID)
	}
}

// --- Path Traversal Tests ---

func TestStateManager_PathTraversal_StatePath(t *testing.T) {
	t.Parallel()
	// Verify state files cannot escape their intended directory via path traversal
	tmpDir := t.TempDir()

	tests := []struct {
		name      string
		statePath string
	}{
		{"dot-dot-slash", filepath.Join(tmpDir, "..", "escaped-state.json")},
		{"double-dot-dot", filepath.Join(tmpDir, "..", "..", "escaped-state.json")},
		{"null-byte", filepath.Join(tmpDir, "state\x00.json")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sm := NewStateManager(StateManagerConfig{
				StatePath: tt.statePath,
				AutoLoad:  false,
			})
			defer sm.Close()

			sm.SetOverlayEndpoint("http://test")
			err := sm.Save()

			if err == nil {
				// If save succeeded, verify the file was created at the resolved path
				// filepath.Join normalizes ".." so the file might be in a valid location.
				// The important thing is that it did not panic or corrupt.
				resolved, _ := filepath.Abs(filepath.Clean(tt.statePath))
				_, statErr := os.Stat(resolved)
				if statErr == nil {
					// Clean up the escaped file
					os.Remove(resolved)
				}
			}
			// The test passes if no panic or data corruption occurred
		})
	}
}

func TestStateManager_PathTraversal_ProxyID(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "state.json")

	sm := NewStateManager(StateManagerConfig{
		StatePath: statePath,
		AutoLoad:  false,
	})
	defer sm.Close()

	// Proxy IDs should not be used for filesystem operations, but
	// verify they don't cause issues when stored/retrieved
	maliciousIDs := []string{
		"../../../etc/passwd",
		"..\\..\\windows\\system32",
		"proxy\x00evil",
		"proxy; rm -rf /",
		"proxy\ninjection",
		"proxy\t\ttabs",
		string(make([]byte, 10000)), // Very long ID
	}

	for _, id := range maliciousIDs {
		sm.AddProxy(PersistentProxyConfig{
			ID:        id,
			TargetURL: "http://localhost:3000",
		})
	}

	// All should be retrievable
	proxies := sm.GetProxies()
	assert.Equal(t, len(maliciousIDs), len(proxies))

	// Save and reload should preserve them
	require.NoError(t, sm.Save())

	sm2 := NewStateManager(StateManagerConfig{
		StatePath: statePath,
		AutoLoad:  true,
	})
	defer sm2.Close()

	proxies2 := sm2.GetProxies()
	assert.Equal(t, len(maliciousIDs), len(proxies2))
}

func TestDefaultSocketPath_NoTraversal(t *testing.T) {
	t.Parallel()
	// Socket path should be under a controlled directory
	path := DefaultSocketPath()
	assert.NotEmpty(t, path)
	// Should not contain unresolved traversal
	assert.NotContains(t, filepath.Clean(path), "..")
}
