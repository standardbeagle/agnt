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

	"github.com/standardbeagle/agnt/internal/platform"
)

// Provider represents a tunnel service provider.
type Provider string

const (
	// ProviderCloudflare uses cloudflared for Cloudflare Quick Tunnels.
	ProviderCloudflare Provider = "cloudflare"
	// ProviderNgrok uses ngrok for tunneling.
	ProviderNgrok Provider = "ngrok"
	// ProviderTailscale uses `tailscale serve` for tailnet-private HTTPS at
	// the node's MagicDNS name (https://<machine>.<tailnet>.ts.net).
	// NOT public: use cloudflare/ngrok for that.
	ProviderTailscale Provider = "tailscale"
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
	ID         string // tunnel identifier
	Path       string // project path for session scoping
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
	ID        string   `json:"id"`
	Provider  Provider `json:"provider"`
	State     string   `json:"state"`
	PublicURL string   `json:"public_url,omitempty"`
	LocalAddr string   `json:"local_addr"`
	Path      string   `json:"path,omitempty"`
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
	case ProviderTailscale:
		return t.startTailscale(ctx)
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
		ID:        t.config.ID,
		Provider:  t.config.Provider,
		State:     t.State().String(),
		PublicURL: t.PublicURL(),
		LocalAddr: fmt.Sprintf("%s:%d", t.config.LocalHost, t.config.LocalPort),
		Path:      t.config.Path,
	}

	t.errMu.RLock()
	if t.err != nil {
		info.Error = t.err.Error()
	}
	t.errMu.RUnlock()

	return info
}

// Path returns the project path for this tunnel.
func (t *Tunnel) Path() string {
	return t.config.Path
}

// ID returns the tunnel ID.
func (t *Tunnel) ID() string {
	return t.config.ID
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

// tailscale serve output patterns
var (
	// Matches tailnet MagicDNS URLs like https://machine.tailnet.ts.net.
	// MagicDNS labels are DNS-safe (lowercase letters, digits, hyphens);
	// the trailing path is consumed up to first whitespace.
	tailscaleURLPattern = regexp.MustCompile(`https://[a-z0-9-]+\.[a-z0-9.-]+\.ts\.net\S*`)
)

// tailscaleDNSFallbackDelay is the grace period after process start before
// we synthesise the MagicDNS URL when serve hasn't printed one. Var (not
// const) so tests can shorten it.
var tailscaleDNSFallbackDelay = 750 * time.Millisecond

func (t *Tunnel) startTailscale(ctx context.Context) error {
	binary := t.config.BinaryPath
	if binary == "" {
		binary = "tailscale"
	}

	if _, err := exec.LookPath(binary); err != nil {
		t.setState(StateFailed)
		t.setError(fmt.Errorf("tailscale not found in PATH: %w", err))
		close(t.done)
		return t.err
	}

	// Foreground `tailscale serve <port>` — blocks until killed and
	// tears down the serve mapping on exit. Do NOT pass --bg; that
	// would detach from the lifecycle and leave the serve config
	// behind on Stop().
	t.cmd = exec.CommandContext(ctx, binary, "serve", fmt.Sprintf("%d", t.config.LocalPort))

	stdout, err := t.cmd.StdoutPipe()
	if err != nil {
		t.setState(StateFailed)
		t.setError(fmt.Errorf("failed to create stdout pipe: %w", err))
		close(t.done)
		return t.err
	}
	stderr, err := t.cmd.StderrPipe()
	if err != nil {
		t.setState(StateFailed)
		t.setError(fmt.Errorf("failed to create stderr pipe: %w", err))
		close(t.done)
		return t.err
	}

	if err := t.cmd.Start(); err != nil {
		t.setState(StateFailed)
		t.setError(fmt.Errorf("failed to start tailscale serve: %w", err))
		close(t.done)
		return t.err
	}

	// Parse both pipes — serve's URL output stream is not version-stable.
	go t.parseTailscaleOutput(stdout)
	go t.parseTailscaleOutput(stderr)

	// MagicDNS fallback: if serve hasn't printed a parseable URL within
	// the grace window, query `tailscale status --json` for Self.DNSName
	// and synthesise the URL. Belt-and-suspenders so WaitForURL resolves
	// even if a future tailscale release changes serve's stdout format.
	go func() {
		select {
		case <-time.After(tailscaleDNSFallbackDelay):
		case <-t.done:
			return
		}
		if t.PublicURL() != "" {
			return
		}
		if name := platform.TailscaleDNSName(ctx); name != "" {
			t.setPublicURL("https://" + name)
			t.setState(StateConnected)
		}
	}()

	go func() {
		defer close(t.done)
		if err := t.cmd.Wait(); err != nil {
			if ctx.Err() == nil { // Not cancelled
				t.setError(fmt.Errorf("tailscale serve exited: %w", err))
				t.setState(StateFailed)
			}
		}
	}()

	return nil
}

func (t *Tunnel) parseTailscaleOutput(r io.Reader) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		if match := tailscaleURLPattern.FindString(line); match != "" {
			t.setPublicURL(match)
			t.setState(StateConnected)
		}
	}
}
