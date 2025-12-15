package tunnel

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
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
		return nil, fmt.Errorf("tunnel %q already exists", id)
	}

	tunnel := New(config)

	// Store before starting to prevent race
	if _, loaded := m.tunnels.LoadOrStore(id, tunnel); loaded {
		return nil, fmt.Errorf("tunnel %q already exists", id)
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
		return fmt.Errorf("tunnel %q not found", id)
	}

	tunnel := value.(*Tunnel)
	return tunnel.Stop(ctx)
}

// Get returns a tunnel by ID.
func (m *Manager) Get(id string) (*Tunnel, bool) {
	value, ok := m.tunnels.Load(id)
	if !ok {
		return nil, false
	}
	return value.(*Tunnel), true
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

// ActiveCount returns the number of active tunnels.
func (m *Manager) ActiveCount() int {
	return int(m.active.Load())
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
