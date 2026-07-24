package browser

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestManagerNew(t *testing.T) {
	m := NewManager()
	if m == nil {
		t.Fatal("NewManager returned nil")
	}
	if m.ActiveCount() != 0 {
		t.Errorf("ActiveCount() = %d, want 0", m.ActiveCount())
	}
	if m.TotalStarted() != 0 {
		t.Errorf("TotalStarted() = %d, want 0", m.TotalStarted())
	}
}

func TestManagerStartInvalidBinary(t *testing.T) {
	m := NewManager()
	ctx := context.Background()

	config := Config{
		ID:         "test-browser",
		URL:        "http://localhost:3000",
		BinaryPath: "/nonexistent/chrome",
		Headless:   true,
	}

	_, err := m.Start(ctx, "test-browser", config)
	if err == nil {
		t.Fatal("expected error for nonexistent binary")
	}

	if m.ActiveCount() != 0 {
		t.Errorf("ActiveCount() = %d, want 0 after failed start", m.ActiveCount())
	}
}

func TestManagerDuplicateID(t *testing.T) {
	m := NewManager()
	ctx := context.Background()

	// Try to create two browsers with the same ID
	// Since we don't have Chrome installed, both should fail at binary lookup
	// But we can test the LoadOrStore logic by mocking

	// Store a placeholder
	m.browsers.Store("dup-id", &Browser{config: Config{ID: "dup-id"}})

	config := Config{
		ID:       "dup-id",
		URL:      "http://localhost:3000",
		Headless: true,
	}

	_, err := m.Start(ctx, "dup-id", config)
	if err != ErrBrowserExists {
		t.Errorf("expected ErrBrowserExists, got %v", err)
	}
}

func TestManagerGetNotFound(t *testing.T) {
	m := NewManager()

	_, err := m.Get("nonexistent")
	if err != ErrBrowserNotFound {
		t.Errorf("expected ErrBrowserNotFound, got %v", err)
	}
}

func TestManagerGetExact(t *testing.T) {
	m := NewManager()

	// Store directly for testing
	b := &Browser{config: Config{ID: "test-id"}}
	m.browsers.Store("test-id", b)

	found, err := m.Get("test-id")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if found != b {
		t.Error("Get returned different browser")
	}
}

func TestManagerGetFuzzy(t *testing.T) {
	m := NewManager()

	// Store browser with compound ID
	b := &Browser{config: Config{ID: "project-abc:test-browser", Path: "/home/test"}}
	m.browsers.Store("project-abc:test-browser", b)

	// Fuzzy lookup by component
	found, err := m.Get("test-browser")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if found != b {
		t.Error("Get fuzzy match returned wrong browser")
	}
}

func TestManagerGetFuzzyAmbiguous(t *testing.T) {
	m := NewManager()

	// Store two browsers with same component name
	b1 := &Browser{config: Config{ID: "proj1:browser", Path: "/home/proj1"}}
	b2 := &Browser{config: Config{ID: "proj2:browser", Path: "/home/proj2"}}
	m.browsers.Store("proj1:browser", b1)
	m.browsers.Store("proj2:browser", b2)

	_, err := m.Get("browser")
	if err != ErrBrowserAmbiguous {
		t.Errorf("expected ErrBrowserAmbiguous, got %v", err)
	}
}

func TestManagerGetWithPathFilter(t *testing.T) {
	m := NewManager()

	// Store two browsers with same component name but different paths
	b1 := &Browser{config: Config{ID: "proj1:browser", Path: "/home/proj1"}}
	b2 := &Browser{config: Config{ID: "proj2:browser", Path: "/home/proj2"}}
	m.browsers.Store("proj1:browser", b1)
	m.browsers.Store("proj2:browser", b2)

	// Filter by path should find only one
	found, err := m.GetWithPathFilter("browser", "/home/proj1")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if found != b1 {
		t.Error("GetWithPathFilter returned wrong browser")
	}
}

func TestManagerList(t *testing.T) {
	m := NewManager()

	// Store some browsers
	b1 := &Browser{config: Config{ID: "b1", Path: "/home/proj1"}}
	b2 := &Browser{config: Config{ID: "b2", Path: "/home/proj2"}}
	m.browsers.Store("b1", b1)
	m.browsers.Store("b2", b2)

	infos := m.List()
	if len(infos) != 2 {
		t.Errorf("List() returned %d items, want 2", len(infos))
	}
}

func TestManagerListByPath(t *testing.T) {
	m := NewManager()

	// Store browsers with different paths
	b1 := &Browser{config: Config{ID: "b1", Path: "/home/proj1"}}
	b2 := &Browser{config: Config{ID: "b2", Path: "/home/proj2"}}
	b3 := &Browser{config: Config{ID: "b3", Path: "/home/proj1"}}
	m.browsers.Store("b1", b1)
	m.browsers.Store("b2", b2)
	m.browsers.Store("b3", b3)

	infos := m.ListByPath("/home/proj1")
	if len(infos) != 2 {
		t.Errorf("ListByPath() returned %d items, want 2", len(infos))
	}
}

func TestManagerStopNotFound(t *testing.T) {
	m := NewManager()
	ctx := context.Background()

	err := m.Stop(ctx, "nonexistent")
	if err != ErrBrowserNotFound {
		t.Errorf("expected ErrBrowserNotFound, got %v", err)
	}
}

func TestManagerShutdown(t *testing.T) {
	m := NewManager()
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	err := m.Shutdown(ctx)
	if err != nil {
		t.Errorf("Shutdown() error: %v", err)
	}

	// After shutdown, new starts should fail
	if !m.shuttingDown.Load() {
		t.Error("shuttingDown should be true after Shutdown")
	}
}

func TestManagerConcurrentAccess(t *testing.T) {
	m := NewManager()

	// Test concurrent reads/writes to the manager
	var wg sync.WaitGroup

	// Concurrent list operations
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = m.List()
			_ = m.ActiveCount()
		}()
	}

	// Concurrent store/load operations
	for i := 0; i < 10; i++ {
		id := string(rune('a' + i))
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			b := &Browser{config: Config{ID: id}}
			m.browsers.Store(id, b)
			m.browsers.Load(id)
			m.browsers.Delete(id)
		}(id)
	}

	wg.Wait()
}
