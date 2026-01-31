package browser

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
)

var (
	// ErrBrowserExists is returned when trying to create a browser with an existing ID.
	ErrBrowserExists = errors.New("browser already exists")
	// ErrBrowserNotFound is returned when a browser ID is not found.
	ErrBrowserNotFound = errors.New("browser not found")
	// ErrBrowserAmbiguous is returned when a fuzzy lookup matches multiple browsers.
	ErrBrowserAmbiguous = errors.New("browser ID is ambiguous - multiple matches")
)

// Manager manages browser instances.
type Manager struct {
	browsers     sync.Map // map[string]*Browser
	active       atomic.Int32
	totalStarted atomic.Int64
	shuttingDown atomic.Bool
}

// NewManager creates a new browser manager.
func NewManager() *Manager {
	return &Manager{}
}

// Start starts a browser with the given configuration.
func (m *Manager) Start(ctx context.Context, id string, config Config) (*Browser, error) {
	if m.shuttingDown.Load() {
		return nil, fmt.Errorf("browser manager is shutting down")
	}

	// Check if browser already exists
	if _, loaded := m.browsers.Load(id); loaded {
		return nil, ErrBrowserExists
	}

	// Ensure ID is set in config
	config.ID = id

	browser := New(config)

	// Store before starting to prevent race
	if _, loaded := m.browsers.LoadOrStore(id, browser); loaded {
		return nil, ErrBrowserExists
	}

	if err := browser.Start(ctx); err != nil {
		m.browsers.Delete(id)
		return nil, err
	}

	m.active.Add(1)
	m.totalStarted.Add(1)

	// Clean up when browser exits
	go func() {
		<-browser.Done()
		m.browsers.Delete(id)
		m.active.Add(-1)
	}()

	return browser, nil
}

// Stop stops a browser by ID.
func (m *Manager) Stop(ctx context.Context, id string) error {
	value, ok := m.browsers.Load(id)
	if !ok {
		return ErrBrowserNotFound
	}

	browser := value.(*Browser)
	return browser.Stop(ctx)
}

// Get returns a browser by ID with fuzzy matching support.
// First tries exact match, then looks for browsers where the ID contains
// the search string as a component (for compound IDs).
func (m *Manager) Get(id string) (*Browser, error) {
	return m.GetWithPathFilter(id, "")
}

// GetWithPathFilter retrieves a browser by ID with fuzzy matching, filtered by path.
// If pathFilter is non-empty, only browsers with matching Path are considered for fuzzy lookup.
// Exact matches are always returned regardless of path filter.
func (m *Manager) GetWithPathFilter(id, pathFilter string) (*Browser, error) {
	// First try exact match (lock-free read) - always works regardless of path
	if val, ok := m.browsers.Load(id); ok {
		return val.(*Browser), nil
	}

	// Normalize path filter for comparison
	normalizedFilter := normalizePath(pathFilter)

	// Fuzzy match: look for browser where the ID contains the search string as a component
	// Compound ID format: {project-hash}:{browser-name}
	var matches []*Browser
	m.browsers.Range(func(key, value any) bool {
		browserID := key.(string)
		browser := value.(*Browser)

		// If path filter is specified, only consider browsers in that path
		if normalizedFilter != "" && normalizedFilter != "." {
			browserPath := normalizePath(browser.Path())
			if browserPath != normalizedFilter {
				return true // Skip this browser, continue iteration
			}
		}

		// Check if search string matches a component of the compound ID
		// Split by ":" and check each part
		parts := strings.Split(browserID, ":")
		for _, part := range parts {
			if part == id {
				matches = append(matches, browser)
				break
			}
		}
		return true
	})

	if len(matches) == 0 {
		return nil, ErrBrowserNotFound
	}
	if len(matches) > 1 {
		return nil, ErrBrowserAmbiguous
	}
	return matches[0], nil
}

// normalizePath normalizes a path for comparison.
func normalizePath(p string) string {
	if p == "" {
		return ""
	}
	// Remove trailing slashes and normalize
	for len(p) > 1 && p[len(p)-1] == '/' {
		p = p[:len(p)-1]
	}
	return p
}

// List returns information about all browsers.
func (m *Manager) List() []Info {
	var infos []Info
	m.browsers.Range(func(key, value interface{}) bool {
		browser := value.(*Browser)
		infos = append(infos, browser.Info())
		return true
	})
	return infos
}

// ListByPath returns browsers filtered by project path.
// If pathFilter is empty, returns all browsers.
func (m *Manager) ListByPath(pathFilter string) []Info {
	if pathFilter == "" {
		return m.List()
	}

	normalizedFilter := normalizePath(pathFilter)
	var infos []Info
	m.browsers.Range(func(key, value interface{}) bool {
		browser := value.(*Browser)
		if normalizePath(browser.Path()) == normalizedFilter {
			infos = append(infos, browser.Info())
		}
		return true
	})
	return infos
}

// StopByProjectPath stops all browsers for a specific project path.
// This is used for session-scoped cleanup when a client disconnects.
// Returns the list of stopped browser IDs.
func (m *Manager) StopByProjectPath(ctx context.Context, projectPath string) ([]string, error) {
	normalizedPath := normalizePath(projectPath)
	var toStop []*Browser
	m.browsers.Range(func(key, value any) bool {
		browser := value.(*Browser)
		if normalizePath(browser.Path()) == normalizedPath {
			toStop = append(toStop, browser)
		}
		return true
	})

	if len(toStop) == 0 {
		return nil, nil
	}

	var stopWg sync.WaitGroup
	var errMu sync.Mutex
	var errs []error
	var stoppedIDs []string
	var stoppedMu sync.Mutex

	for _, browser := range toStop {
		stopWg.Add(1)
		go func(b *Browser) {
			defer stopWg.Done()
			id := b.ID()
			if err := m.Stop(ctx, id); err != nil {
				errMu.Lock()
				errs = append(errs, err)
				errMu.Unlock()
			} else {
				stoppedMu.Lock()
				stoppedIDs = append(stoppedIDs, id)
				stoppedMu.Unlock()
			}
		}(browser)
	}

	done := make(chan struct{})
	go func() {
		stopWg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		if len(errs) > 0 {
			errs = append(errs, ctx.Err())
		} else {
			return stoppedIDs, ctx.Err()
		}
	}

	if len(errs) > 0 {
		return stoppedIDs, errors.Join(errs...)
	}
	return stoppedIDs, nil
}

// ActiveCount returns the number of active browsers.
func (m *Manager) ActiveCount() int {
	return int(m.active.Load())
}

// TotalStarted returns the total number of browsers started.
func (m *Manager) TotalStarted() int64 {
	return m.totalStarted.Load()
}

// StopAll stops all running browsers.
// Unlike Shutdown, this does NOT set shuttingDown flag, allowing new browsers
// to be started afterward. This is used for cleanup when the last client disconnects.
func (m *Manager) StopAll(ctx context.Context) ([]string, error) {
	var wg sync.WaitGroup
	var firstErr error
	var errMu sync.Mutex
	var stoppedIDs []string
	var stoppedMu sync.Mutex

	m.browsers.Range(func(key, value interface{}) bool {
		browser := value.(*Browser)
		wg.Add(1)
		go func(b *Browser) {
			defer wg.Done()
			id := b.ID()
			if err := b.Stop(ctx); err != nil {
				errMu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				errMu.Unlock()
			} else {
				// Wait for the browser's done channel to ensure cleanup goroutine
				// has decremented the active count before we return
				select {
				case <-b.Done():
				case <-ctx.Done():
				}
				stoppedMu.Lock()
				stoppedIDs = append(stoppedIDs, id)
				stoppedMu.Unlock()
			}
		}(browser)
		return true
	})

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return stoppedIDs, firstErr
	case <-ctx.Done():
		return stoppedIDs, ctx.Err()
	}
}

// Shutdown stops all browsers.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.shuttingDown.Store(true)

	var wg sync.WaitGroup
	var firstErr error
	var errMu sync.Mutex

	m.browsers.Range(func(key, value interface{}) bool {
		browser := value.(*Browser)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := browser.Stop(ctx); err != nil {
				errMu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				errMu.Unlock()
			}
		}()
		return true
	})

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return firstErr
	case <-ctx.Done():
		return ctx.Err()
	}
}
