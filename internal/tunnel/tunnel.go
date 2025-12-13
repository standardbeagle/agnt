// Package tunnel provides management for tunnel services like Cloudflare and ngrok.
package tunnel

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"sync"
	"sync/atomic"
	"time"
)

// Provider represents a tunnel service provider.
type Provider string

const (
	// ProviderCloudflare uses cloudflared for Cloudflare Quick Tunnels.
	ProviderCloudflare Provider = "cloudflare"
	// ProviderNgrok uses ngrok for tunneling.
	ProviderNgrok Provider = "ngrok"
)

// State represents the tunnel state.
type State uint32

const (
	StateIdle State = iota
	StateStarting
	StateConnected
	StateFailed
	StateStopped
)

func (s State) String() string {
	switch s {
	case StateIdle:
		return "idle"
	case StateStarting:
		return "starting"
	case StateConnected:
		return "connected"
	case StateFailed:
		return "failed"
	case StateStopped:
		return "stopped"
	default:
		return "unknown"
	}
}

// Config holds tunnel configuration.
type Config struct {
	Provider   Provider
	LocalPort  int
	LocalHost  string // defaults to "localhost"
	BinaryPath string // optional: path to tunnel binary, otherwise uses PATH
}

// Tunnel represents a running tunnel instance.
type Tunnel struct {
	config    Config
	state     atomic.Uint32
	publicURL atomic.Pointer[string]
	cmd       *exec.Cmd
	cancel    context.CancelFunc
	done      chan struct{}
	err       error
	errMu     sync.RWMutex

	// Callbacks
	onURL func(url string)
}

// TunnelInfo contains information about a tunnel.
type TunnelInfo struct {
	Provider  Provider `json:"provider"`
	State     string   `json:"state"`
	PublicURL string   `json:"public_url,omitempty"`
	LocalAddr string   `json:"local_addr"`
	Error     string   `json:"error,omitempty"`
}

// New creates a new tunnel with the given configuration.
func New(config Config) *Tunnel {
	if config.LocalHost == "" {
		config.LocalHost = "localhost"
	}
	return &Tunnel{
		config: config,
		done:   make(chan struct{}),
	}
}

// OnURL sets a callback that's invoked when the public URL is discovered.
func (t *Tunnel) OnURL(fn func(url string)) {
	t.onURL = fn
}

// Start starts the tunnel and returns immediately.
// Use WaitForURL to wait for the public URL to be available.
func (t *Tunnel) Start(ctx context.Context) error {
	if !t.compareAndSwapState(StateIdle, StateStarting) {
		return fmt.Errorf("tunnel already started")
	}

	ctx, cancel := context.WithCancel(ctx)
	t.cancel = cancel

	switch t.config.Provider {
	case ProviderCloudflare:
		return t.startCloudflare(ctx)
	case ProviderNgrok:
		return t.startNgrok(ctx)
	default:
		t.setState(StateFailed)
		return fmt.Errorf("unsupported tunnel provider: %s", t.config.Provider)
	}
}

// Stop stops the tunnel.
func (t *Tunnel) Stop(ctx context.Context) error {
	if t.cancel != nil {
		t.cancel()
	}

	if t.cmd != nil && t.cmd.Process != nil {
		// Send interrupt first for graceful shutdown
		if err := t.cmd.Process.Kill(); err != nil {
			return fmt.Errorf("failed to kill tunnel process: %w", err)
		}
	}

	// Wait for done or context timeout
	select {
	case <-t.done:
	case <-ctx.Done():
		return ctx.Err()
	}

	t.setState(StateStopped)
	return nil
}

// State returns the current tunnel state.
func (t *Tunnel) State() State {
	return State(t.state.Load())
}

// PublicURL returns the public URL if available.
func (t *Tunnel) PublicURL() string {
	if ptr := t.publicURL.Load(); ptr != nil {
		return *ptr
	}
	return ""
}

// Info returns information about the tunnel.
func (t *Tunnel) Info() TunnelInfo {
	info := TunnelInfo{
		Provider:  t.config.Provider,
		State:     t.State().String(),
		PublicURL: t.PublicURL(),
		LocalAddr: fmt.Sprintf("%s:%d", t.config.LocalHost, t.config.LocalPort),
	}

	t.errMu.RLock()
	if t.err != nil {
		info.Error = t.err.Error()
	}
	t.errMu.RUnlock()

	return info
}

// WaitForURL waits for the public URL to be available or timeout.
func (t *Tunnel) WaitForURL(ctx context.Context) (string, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-t.done:
			if url := t.PublicURL(); url != "" {
				return url, nil
			}
			t.errMu.RLock()
			err := t.err
			t.errMu.RUnlock()
			if err != nil {
				return "", err
			}
			return "", fmt.Errorf("tunnel closed without providing URL")
		case <-ticker.C:
			if url := t.PublicURL(); url != "" {
				return url, nil
			}
		}
	}
}

// Done returns a channel that's closed when the tunnel exits.
func (t *Tunnel) Done() <-chan struct{} {
	return t.done
}

func (t *Tunnel) setState(s State) {
	t.state.Store(uint32(s))
}

func (t *Tunnel) compareAndSwapState(old, new State) bool {
	return t.state.CompareAndSwap(uint32(old), uint32(new))
}

func (t *Tunnel) setError(err error) {
	t.errMu.Lock()
	t.err = err
	t.errMu.Unlock()
}

func (t *Tunnel) setPublicURL(url string) {
	t.publicURL.Store(&url)
	if t.onURL != nil {
		t.onURL(url)
	}
}

// cloudflared output patterns
var (
	// Matches: https://something-something.trycloudflare.com
	cloudflareURLPattern = regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`)
)

func (t *Tunnel) startCloudflare(ctx context.Context) error {
	binary := t.config.BinaryPath
	if binary == "" {
		binary = "cloudflared"
	}

	// Check if binary exists
	if _, err := exec.LookPath(binary); err != nil {
		t.setState(StateFailed)
		t.setError(fmt.Errorf("cloudflared not found in PATH: %w", err))
		close(t.done)
		return t.err
	}

	localURL := fmt.Sprintf("http://%s:%d", t.config.LocalHost, t.config.LocalPort)
	t.cmd = exec.CommandContext(ctx, binary, "tunnel", "--url", localURL)

	// Capture stderr (cloudflared logs to stderr)
	stderr, err := t.cmd.StderrPipe()
	if err != nil {
		t.setState(StateFailed)
		t.setError(fmt.Errorf("failed to create stderr pipe: %w", err))
		close(t.done)
		return t.err
	}

	if err := t.cmd.Start(); err != nil {
		t.setState(StateFailed)
		t.setError(fmt.Errorf("failed to start cloudflared: %w", err))
		close(t.done)
		return t.err
	}

	// Parse output in goroutine
	go t.parseCloudflareOutput(stderr)

	// Wait for process in goroutine
	go func() {
		defer close(t.done)
		if err := t.cmd.Wait(); err != nil {
			if ctx.Err() == nil { // Not cancelled
				t.setError(fmt.Errorf("cloudflared exited: %w", err))
				t.setState(StateFailed)
			}
		}
	}()

	return nil
}

func (t *Tunnel) parseCloudflareOutput(r io.Reader) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		if match := cloudflareURLPattern.FindString(line); match != "" {
			t.setPublicURL(match)
			t.setState(StateConnected)
		}
	}
}

// ngrok output patterns
var (
	// Matches ngrok URLs like https://abc123.ngrok.io or https://abc123.ngrok-free.app
	ngrokURLPattern = regexp.MustCompile(`https://[a-z0-9-]+\.ngrok(?:-free)?\.(?:io|app)`)
)

func (t *Tunnel) startNgrok(ctx context.Context) error {
	binary := t.config.BinaryPath
	if binary == "" {
		binary = "ngrok"
	}

	// Check if binary exists
	if _, err := exec.LookPath(binary); err != nil {
		t.setState(StateFailed)
		t.setError(fmt.Errorf("ngrok not found in PATH: %w", err))
		close(t.done)
		return t.err
	}

	t.cmd = exec.CommandContext(ctx, binary, "http", fmt.Sprintf("%d", t.config.LocalPort))

	// ngrok outputs to stdout
	stdout, err := t.cmd.StdoutPipe()
	if err != nil {
		t.setState(StateFailed)
		t.setError(fmt.Errorf("failed to create stdout pipe: %w", err))
		close(t.done)
		return t.err
	}

	if err := t.cmd.Start(); err != nil {
		t.setState(StateFailed)
		t.setError(fmt.Errorf("failed to start ngrok: %w", err))
		close(t.done)
		return t.err
	}

	// Parse output in goroutine
	go t.parseNgrokOutput(stdout)

	// Wait for process in goroutine
	go func() {
		defer close(t.done)
		if err := t.cmd.Wait(); err != nil {
			if ctx.Err() == nil { // Not cancelled
				t.setError(fmt.Errorf("ngrok exited: %w", err))
				t.setState(StateFailed)
			}
		}
	}()

	return nil
}

func (t *Tunnel) parseNgrokOutput(r io.Reader) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		if match := ngrokURLPattern.FindString(line); match != "" {
			t.setPublicURL(match)
			t.setState(StateConnected)
		}
	}
}
