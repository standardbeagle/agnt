package tunnel

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
)

var (
	// ErrTunnelExists is returned when trying to create a tunnel with an existing ID.
	ErrTunnelExists = errors.New("tunnel already exists")
	// ErrTunnelNotFound is returned when a tunnel ID is not found.
	ErrTunnelNotFound = errors.New("tunnel not found")
	// ErrTunnelAmbiguous is returned when a fuzzy lookup matches multiple tunnels.
	ErrTunnelAmbiguous = errors.New("tunnel ID is ambiguous - multiple matches")
)

// Manager manages tunnel instances.
type Manager struct {
	tunnels      sync.Map // map[string]*Tunnel
	active       atomic.Int32
	shuttingDown atomic.Bool
}

// NewManager creates a new tunnel manager.
func NewManager() *Manager {
	return &Manager{}
}

// Start starts a tunnel for the given proxy.
func (m *Manager) Start(ctx context.Context, id string, config Config) (*Tunnel, error) {
	if m.shuttingDown.Load() {
		return nil, fmt.Errorf("tunnel manager is shutting down")
	}

	// Check if tunnel already exists
	if _, loaded := m.tunnels.Load(id); loaded {
		return nil, ErrTunnelExists
	}

	// Ensure ID is set in config
	config.ID = id

	tunnel := New(config)

	// Store before starting to prevent race
	if _, loaded := m.tunnels.LoadOrStore(id, tunnel); loaded {
		return nil, ErrTunnelExists
	}

	if err := tunnel.Start(ctx); err != nil {
		m.tunnels.Delete(id)
		return nil, err
	}

	m.active.Add(1)

	// Clean up when tunnel exits
	go func() {
		<-tunnel.Done()
		m.tunnels.Delete(id)
		m.active.Add(-1)
	}()

	return tunnel, nil
}

// Stop stops a tunnel by ID.
func (m *Manager) Stop(ctx context.Context, id string) error {
	value, ok := m.tunnels.Load(id)
	if !ok {
		return ErrTunnelNotFound
	}

	tunnel := value.(*Tunnel)
	return tunnel.Stop(ctx)
}

// Get returns a tunnel by ID with fuzzy matching support.
// First tries exact match, then looks for tunnels where the ID contains
// the search string as a component (for compound IDs).
func (m *Manager) Get(id string) (*Tunnel, error) {
	return m.GetWithPathFilter(id, "")
}

// GetWithPathFilter retrieves a tunnel by ID with fuzzy matching, filtered by path.
// If pathFilter is non-empty, only tunnels with matching Path are considered for fuzzy lookup.
// Exact matches are always returned regardless of path filter.
func (m *Manager) GetWithPathFilter(id, pathFilter string) (*Tunnel, error) {
	// First try exact match (lock-free read) - always works regardless of path
	if val, ok := m.tunnels.Load(id); ok {
		return val.(*Tunnel), nil
	}

	// Normalize path filter for comparison
	normalizedFilter := normalizePath(pathFilter)

	// Fuzzy match: look for tunnel where the ID contains the search string as a component
	// Compound ID format: {project-hash}:{tunnel-name}:{host-port}
	var matches []*Tunnel
	m.tunnels.Range(func(key, value any) bool {
		tunnelID := key.(string)
		tunnel := value.(*Tunnel)

		// If path filter is specified, only consider tunnels in that path
		if normalizedFilter != "" && normalizedFilter != "." {
			tunnelPath := normalizePath(tunnel.Path())
			if tunnelPath != normalizedFilter {
				return true // Skip this tunnel, continue iteration
			}
		}

		// Check if search string matches a component of the compound ID
		// Split by ":" and check each part
		parts := strings.Split(tunnelID, ":")
		for _, part := range parts {
			if part == id {
				matches = append(matches, tunnel)
				break
			}
		}
		return true
	})

	if len(matches) == 0 {
		return nil, ErrTunnelNotFound
	}
	if len(matches) > 1 {
		return nil, ErrTunnelAmbiguous
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

// List returns information about all tunnels.
func (m *Manager) List() []TunnelInfo {
	var infos []TunnelInfo
	m.tunnels.Range(func(key, value interface{}) bool {
		tunnel := value.(*Tunnel)
		info := tunnel.Info()
		infos = append(infos, info)
		return true
	})
	return infos
}

// ListByPath returns tunnels filtered by project path.
// If pathFilter is empty, returns all tunnels.
func (m *Manager) ListByPath(pathFilter string) []TunnelInfo {
	if pathFilter == "" {
		return m.List()
	}

	normalizedFilter := normalizePath(pathFilter)
	var infos []TunnelInfo
	m.tunnels.Range(func(key, value interface{}) bool {
		tunnel := value.(*Tunnel)
		if normalizePath(tunnel.Path()) == normalizedFilter {
			infos = append(infos, tunnel.Info())
		}
		return true
	})
	return infos
}

// StopByProjectPath stops all tunnels for a specific project path.
// This is used for session-scoped cleanup when a client disconnects.
// Returns the list of stopped tunnel IDs.
func (m *Manager) StopByProjectPath(ctx context.Context, projectPath string) ([]string, error) {
	normalizedPath := normalizePath(projectPath)
	var toStop []*Tunnel
	m.tunnels.Range(func(key, value any) bool {
		tunnel := value.(*Tunnel)
		if normalizePath(tunnel.Path()) == normalizedPath {
			toStop = append(toStop, tunnel)
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

	for _, tunnel := range toStop {
		stopWg.Add(1)
		go func(t *Tunnel) {
			defer stopWg.Done()
			id := t.ID()
			if err := m.Stop(ctx, id); err != nil {
				errMu.Lock()
				errs = append(errs, err)
				errMu.Unlock()
			} else {
				stoppedMu.Lock()
				stoppedIDs = append(stoppedIDs, id)
				stoppedMu.Unlock()
			}
		}(tunnel)
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

// ActiveCount returns the number of active tunnels.
func (m *Manager) ActiveCount() int {
	return int(m.active.Load())
}

// StopAll stops all running tunnels.
// Unlike Shutdown, this does NOT set shuttingDown flag, allowing new tunnels
// to be started afterward. This is used for cleanup when the last client disconnects.
func (m *Manager) StopAll(ctx context.Context) error {
	var wg sync.WaitGroup
	var firstErr error
	var errMu sync.Mutex

	m.tunnels.Range(func(key, value interface{}) bool {
		tunnel := value.(*Tunnel)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := tunnel.Stop(ctx); err != nil {
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

// Shutdown stops all tunnels.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.shuttingDown.Store(true)

	var wg sync.WaitGroup
	var firstErr error
	var errMu sync.Mutex

	m.tunnels.Range(func(key, value interface{}) bool {
		tunnel := value.(*Tunnel)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := tunnel.Stop(ctx); err != nil {
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
