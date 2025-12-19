package proxy

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
)

var (
	// ErrProxyExists is returned when trying to create a proxy with an existing ID.
	ErrProxyExists = errors.New("proxy already exists")
	// ErrProxyNotFound is returned when a proxy ID is not found.
	ErrProxyNotFound = errors.New("proxy not found")
)

// ProxyManager manages multiple reverse proxy servers with lock-free access.
type ProxyManager struct {
	proxies      sync.Map // map[string]*ProxyServer
	activeCount  atomic.Int64
	totalStarted atomic.Int64

	shutdownOnce sync.Once
	shuttingDown atomic.Bool
}

// NewProxyManager creates a new proxy manager.
func NewProxyManager() *ProxyManager {
	return &ProxyManager{}
}

// Create creates and starts a new proxy server.
func (pm *ProxyManager) Create(ctx context.Context, config ProxyConfig) (*ProxyServer, error) {
	if pm.shuttingDown.Load() {
		return nil, errors.New("proxy manager is shutting down")
	}

	// Check if proxy already exists
	if _, exists := pm.proxies.Load(config.ID); exists {
		return nil, ErrProxyExists
	}

	// Create proxy server
	proxy, err := NewProxyServer(config)
	if err != nil {
		return nil, err
	}

	// Start proxy
	if err := proxy.Start(ctx); err != nil {
		return nil, err
	}

	// Store in registry
	pm.proxies.Store(config.ID, proxy)
	pm.activeCount.Add(1)
	pm.totalStarted.Add(1)

	return proxy, nil
}

// Get retrieves a proxy by ID (lock-free read).
func (pm *ProxyManager) Get(id string) (*ProxyServer, error) {
	val, ok := pm.proxies.Load(id)
	if !ok {
		return nil, ErrProxyNotFound
	}
	return val.(*ProxyServer), nil
}

// Stop stops a proxy server and removes it from the registry.
func (pm *ProxyManager) Stop(ctx context.Context, id string) error {
	proxy, err := pm.Get(id)
	if err != nil {
		return err
	}

	if err := proxy.Stop(ctx); err != nil {
		return err
	}

	pm.proxies.Delete(id)
	pm.activeCount.Add(-1)

	return nil
}

// List returns all managed proxy servers.
func (pm *ProxyManager) List() []*ProxyServer {
	var result []*ProxyServer
	pm.proxies.Range(func(key, value any) bool {
		result = append(result, value.(*ProxyServer))
		return true
	})
	return result
}

// ActiveCount returns the number of running proxies.
func (pm *ProxyManager) ActiveCount() int64 {
	return pm.activeCount.Load()
}

// TotalStarted returns the total number of proxies ever started.
func (pm *ProxyManager) TotalStarted() int64 {
	return pm.totalStarted.Load()
}

// StopAll stops all running proxies and removes them from the registry.
// Unlike Shutdown, this does NOT set shuttingDown flag, allowing new proxies
// to be started afterward. This is used for cleanup when the last client disconnects.
// Returns the list of stopped proxy IDs for state persistence cleanup.
func (pm *ProxyManager) StopAll(ctx context.Context) ([]string, error) {
	var stopWg sync.WaitGroup
	var errMu sync.Mutex
	var errs []error
	var stoppedIDs []string
	var stoppedMu sync.Mutex

	// Collect all proxy IDs to stop
	var toStop []string
	pm.proxies.Range(func(key, value any) bool {
		toStop = append(toStop, key.(string))
		return true
	})

	// Stop all proxies in parallel
	for _, id := range toStop {
		stopWg.Add(1)
		go func(proxyID string) {
			defer stopWg.Done()
			if err := pm.Stop(ctx, proxyID); err != nil {
				errMu.Lock()
				errs = append(errs, err)
				errMu.Unlock()
			} else {
				stoppedMu.Lock()
				stoppedIDs = append(stoppedIDs, proxyID)
				stoppedMu.Unlock()
			}
		}(id)
	}

	// Wait for all stops to complete with timeout
	done := make(chan struct{})
	go func() {
		stopWg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// All proxies stopped
	case <-ctx.Done():
		// Context cancelled, return what we have
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

// StopByProjectPath stops all running proxies for a specific project path and removes them.
// This is used for session-scoped cleanup when a client disconnects.
// Returns the list of stopped proxy IDs.
func (pm *ProxyManager) StopByProjectPath(ctx context.Context, projectPath string) ([]string, error) {
	var toStop []*ProxyServer
	pm.proxies.Range(func(key, value any) bool {
		proxy := value.(*ProxyServer)
		if proxy.Path == projectPath {
			toStop = append(toStop, proxy)
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

	for _, proxy := range toStop {
		stopWg.Add(1)
		go func(p *ProxyServer) {
			defer stopWg.Done()
			if err := pm.Stop(ctx, p.ID); err != nil {
				errMu.Lock()
				errs = append(errs, err)
				errMu.Unlock()
			} else {
				stoppedMu.Lock()
				stoppedIDs = append(stoppedIDs, p.ID)
				stoppedMu.Unlock()
			}
		}(proxy)
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

// Shutdown stops all managed proxies.
func (pm *ProxyManager) Shutdown(ctx context.Context) error {
	var shutdownErr error

	pm.shutdownOnce.Do(func() {
		pm.shuttingDown.Store(true)

		var stopWg sync.WaitGroup
		var errMu sync.Mutex
		var errs []error

		pm.proxies.Range(func(key, value any) bool {
			proxy := value.(*ProxyServer)
			if proxy.IsRunning() {
				stopWg.Add(1)
				go func(p *ProxyServer, id string) {
					defer stopWg.Done()
					if err := pm.Stop(ctx, id); err != nil {
						errMu.Lock()
						errs = append(errs, err)
						errMu.Unlock()
					}
				}(proxy, key.(string))
			}
			return true
		})

		done := make(chan struct{})
		go func() {
			stopWg.Wait()
			close(done)
		}()

		select {
		case <-done:
			// All proxies stopped
		case <-ctx.Done():
			shutdownErr = ctx.Err()
		}

		if len(errs) > 0 {
			shutdownErr = errors.Join(errs...)
		}
	})

	return shutdownErr
}
