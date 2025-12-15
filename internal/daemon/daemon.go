package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/standardbeagle/agnt/internal/process"
	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/standardbeagle/agnt/internal/tunnel"
)

// Version is the daemon version.
const Version = "0.1.0"

// DaemonConfig holds configuration for the daemon.
type DaemonConfig struct {
	// Socket configuration
	SocketPath string

	// Process manager configuration
	ProcessConfig process.ManagerConfig

	// Max concurrent clients (0 = unlimited)
	MaxClients int

	// Connection read timeout (0 = no timeout)
	ReadTimeout time.Duration

	// Connection write timeout (0 = no timeout)
	WriteTimeout time.Duration

	// OverlayEndpoint is the URL of the agnt overlay server for forwarding events.
	// Example: "http://127.0.0.1:19191"
	// When set, proxies will forward panel messages, sketches, etc. to the overlay.
	OverlayEndpoint string

	// EnableStatePersistence enables persisting proxy configs for recovery.
	EnableStatePersistence bool

	// StatePath is the path to the state file.
	// If empty, uses default location.
	StatePath string
}

// DefaultDaemonConfig returns sensible defaults.
func DefaultDaemonConfig() DaemonConfig {
	return DaemonConfig{
		SocketPath:             DefaultSocketPath(),
		ProcessConfig:          process.DefaultManagerConfig(),
		MaxClients:             100,
		ReadTimeout:            0, // No timeout for long-running commands
		WriteTimeout:           30 * time.Second,
		EnableStatePersistence: true,
	}
}

// Daemon is the main daemon process that manages state across client connections.
type Daemon struct {
	config DaemonConfig

	// Core managers
	pm      *process.ProcessManager
	proxym  *proxy.ProxyManager
	tunnelm *tunnel.Manager

	// State persistence
	stateMgr *StateManager

	// Socket management
	sockMgr  *SocketManager
	listener net.Listener

	// Client tracking
	clients     sync.Map // clientID -> *Connection
	clientCount atomic.Int64
	nextID      atomic.Int64

	// Overlay endpoint (can be set dynamically)
	overlayEndpoint atomic.Pointer[string]

	// Lifecycle
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	started    time.Time
	shutdownMu sync.Mutex
	shutdown   bool
}

// New creates a new daemon instance.
func New(config DaemonConfig) *Daemon {
	ctx, cancel := context.WithCancel(context.Background())

	d := &Daemon{
		config:  config,
		pm:      process.NewProcessManager(config.ProcessConfig),
		proxym:  proxy.NewProxyManager(),
		tunnelm: tunnel.NewManager(),
		sockMgr: NewSocketManager(SocketConfig{Path: config.SocketPath}),
		ctx:     ctx,
		cancel:  cancel,
	}

	// Initialize state manager if persistence is enabled
	if config.EnableStatePersistence {
		d.stateMgr = NewStateManager(StateManagerConfig{
			StatePath: config.StatePath,
			AutoLoad:  true,
		})
	}

	// Set initial overlay endpoint from config or persisted state
	if config.OverlayEndpoint != "" {
		d.overlayEndpoint.Store(&config.OverlayEndpoint)
	} else if d.stateMgr != nil {
		if endpoint := d.stateMgr.GetOverlayEndpoint(); endpoint != "" {
			d.overlayEndpoint.Store(&endpoint)
		}
	}

	return d
}

// Start starts the daemon and begins accepting connections.
func (d *Daemon) Start() error {
	d.shutdownMu.Lock()
	if d.shutdown {
		d.shutdownMu.Unlock()
		return errors.New("daemon already shutdown")
	}
	d.shutdownMu.Unlock()

	// Create socket
	listener, err := d.sockMgr.Listen()
	if err != nil {
		return fmt.Errorf("failed to create socket: %w", err)
	}
	d.listener = listener
	d.started = time.Now()

	log.Printf("Daemon started, listening on %s", d.sockMgr.Path())

	// Restore proxies from persisted state
	d.restoreProxies()

	// Start accept loop
	d.wg.Add(1)
	go d.acceptLoop()

	return nil
}

// restoreProxies restores proxy servers from persisted state.
func (d *Daemon) restoreProxies() {
	if d.stateMgr == nil {
		return
	}

	proxies := d.stateMgr.GetProxies()
	if len(proxies) == 0 {
		return
	}

	log.Printf("[Daemon] restoring %d proxies from state", len(proxies))

	overlayEndpoint := d.OverlayEndpoint()

	for _, pc := range proxies {
		config := proxy.ProxyConfig{
			ID:          pc.ID,
			TargetURL:   pc.TargetURL,
			ListenPort:  pc.Port,
			MaxLogSize:  pc.MaxLogSize,
			AutoRestart: true,
			Path:        pc.Path,
		}

		proxyServer, err := d.proxym.Create(d.ctx, config)
		if err != nil {
			log.Printf("[Daemon] failed to restore proxy %s: %v", pc.ID, err)
			// Remove from state if it can't be restored
			d.stateMgr.RemoveProxy(pc.ID)
			continue
		}

		// Configure overlay endpoint
		if overlayEndpoint != "" {
			proxyServer.SetOverlayEndpoint(overlayEndpoint)
		}

		log.Printf("[Daemon] restored proxy %s -> %s on port %d",
			pc.ID, pc.TargetURL, pc.Port)
	}
}

// Stop gracefully shuts down the daemon.
func (d *Daemon) Stop(ctx context.Context) error {
	d.shutdownMu.Lock()
	if d.shutdown {
		d.shutdownMu.Unlock()
		return nil
	}
	d.shutdown = true
	d.shutdownMu.Unlock()

	log.Println("Daemon stopping...")

	// Signal all goroutines to stop
	d.cancel()

	// Close listener to unblock accept (sockMgr.Close() will handle cleanup)
	// We close it here first to unblock the accept loop before waiting for it
	if d.listener != nil {
		d.listener.Close()
		d.listener = nil // Mark as closed so sockMgr.Close() won't try again
	}

	// Close all client connections
	d.clients.Range(func(key, value any) bool {
		conn := value.(*Connection)
		conn.Close()
		return true
	})

	// Shutdown managers
	var errs []error

	if err := d.tunnelm.Shutdown(ctx); err != nil {
		errs = append(errs, fmt.Errorf("tunnel manager: %w", err))
	}

	if err := d.proxym.Shutdown(ctx); err != nil {
		errs = append(errs, fmt.Errorf("proxy manager: %w", err))
	}

	if err := d.pm.Shutdown(ctx); err != nil {
		errs = append(errs, fmt.Errorf("process manager: %w", err))
	}

	// Wait for goroutines with timeout
	done := make(chan struct{})
	go func() {
		d.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Clean exit
	case <-ctx.Done():
		errs = append(errs, ctx.Err())
	}

	// Cleanup socket
	if err := d.sockMgr.Close(); err != nil {
		errs = append(errs, fmt.Errorf("socket cleanup: %w", err))
	}

	log.Println("Daemon stopped")

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// Wait blocks until the daemon stops.
func (d *Daemon) Wait() {
	<-d.ctx.Done()
	d.wg.Wait()
}

// Info returns daemon information.
func (d *Daemon) Info() DaemonInfo {
	return DaemonInfo{
		Version:     Version,
		SocketPath:  d.sockMgr.Path(),
		Uptime:      time.Since(d.started),
		ClientCount: d.clientCount.Load(),
		ProcessInfo: ProcessInfo{
			Active:       d.pm.ActiveCount(),
			TotalStarted: d.pm.TotalStarted(),
			TotalFailed:  d.pm.TotalFailed(),
		},
		ProxyInfo: ProxyInfo{
			Active:       d.proxym.ActiveCount(),
			TotalStarted: d.proxym.TotalStarted(),
		},
		TunnelInfo: TunnelInfo{
			Active: int64(d.tunnelm.ActiveCount()),
		},
	}
}

// ProcessManager returns the process manager.
func (d *Daemon) ProcessManager() *process.ProcessManager {
	return d.pm
}

// ProxyManager returns the proxy manager.
func (d *Daemon) ProxyManager() *proxy.ProxyManager {
	return d.proxym
}

// TunnelManager returns the tunnel manager.
func (d *Daemon) TunnelManager() *tunnel.Manager {
	return d.tunnelm
}

// SetOverlayEndpoint sets the overlay endpoint URL and updates all existing proxies.
// The endpoint should be the full URL, e.g., "http://127.0.0.1:19191".
// Pass an empty string to disable overlay forwarding.
func (d *Daemon) SetOverlayEndpoint(endpoint string) {
	if endpoint == "" {
		d.overlayEndpoint.Store(nil)
	} else {
		d.overlayEndpoint.Store(&endpoint)
	}

	// Persist to state
	if d.stateMgr != nil {
		d.stateMgr.SetOverlayEndpoint(endpoint)
	}

	// Update all existing proxies
	for _, p := range d.proxym.List() {
		p.SetOverlayEndpoint(endpoint)
	}
}

// StateManager returns the state manager (may be nil if persistence is disabled).
func (d *Daemon) StateManager() *StateManager {
	return d.stateMgr
}

// OverlayEndpoint returns the current overlay endpoint URL, or empty string if not set.
func (d *Daemon) OverlayEndpoint() string {
	ptr := d.overlayEndpoint.Load()
	if ptr == nil {
		return ""
	}
	return *ptr
}

// acceptLoop accepts new client connections.
func (d *Daemon) acceptLoop() {
	defer d.wg.Done()

	for {
		conn, err := d.listener.Accept()
		if err != nil {
			select {
			case <-d.ctx.Done():
				return // Shutting down
			default:
				log.Printf("Accept error: %v", err)
				continue
			}
		}

		// Check max clients
		if d.config.MaxClients > 0 && d.clientCount.Load() >= int64(d.config.MaxClients) {
			log.Printf("Max clients reached, rejecting connection")
			conn.Close()
			continue
		}

		// Create connection handler
		clientID := d.nextID.Add(1)
		clientConn := newConnection(clientID, conn, d)

		// Register client
		d.clients.Store(clientID, clientConn)
		d.clientCount.Add(1)

		// Handle in goroutine
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			defer func() {
				d.clients.Delete(clientID)
				d.clientCount.Add(-1)
			}()

			clientConn.Handle(d.ctx)
		}()
	}
}

// DaemonInfo holds daemon status information.
type DaemonInfo struct {
	Version     string        `json:"version"`
	SocketPath  string        `json:"socket_path"`
	Uptime      time.Duration `json:"uptime"`
	ClientCount int64         `json:"client_count"`
	ProcessInfo ProcessInfo   `json:"process_info"`
	ProxyInfo   ProxyInfo     `json:"proxy_info"`
	TunnelInfo  TunnelInfo    `json:"tunnel_info"`
}

// ProcessInfo holds process manager statistics.
type ProcessInfo struct {
	Active       int64 `json:"active"`
	TotalStarted int64 `json:"total_started"`
	TotalFailed  int64 `json:"total_failed"`
}

// ProxyInfo holds proxy manager statistics.
type ProxyInfo struct {
	Active       int64 `json:"active"`
	TotalStarted int64 `json:"total_started"`
}

// TunnelInfo holds tunnel manager statistics.
type TunnelInfo struct {
	Active int64 `json:"active"`
}
