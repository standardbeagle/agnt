// Package tunnel provides management for tunnel services like Cloudflare and ngrok.
package tunnel

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
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

// supportedProviders is the closed set of providers this build can start; it
// must stay in step with Start's dispatch switch (which independently rejects an
// unknown provider, so a drift here is a bad error message, never a silent
// start).
var supportedProviders = map[Provider]bool{
	ProviderCloudflare: true,
	ProviderNgrok:      true,
	ProviderTailscale:  true,
}

// ParseProvider validates a user-supplied provider name. Callers that take a
// provider from a CLI flag or config value use this so a typo fails loudly at
// the boundary, naming the legal set, instead of being cast straight to Provider
// and only surfacing later as "unsupported tunnel provider" from Start.
func ParseProvider(s string) (Provider, error) {
	p := Provider(strings.ToLower(strings.TrimSpace(s)))
	if !supportedProviders[p] {
		return "", fmt.Errorf("unknown tunnel provider %q (cloudflare|ngrok|tailscale)", s)
	}
	return p, nil
}

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
	done      chan struct{}
	doneOnce  sync.Once
	err       error
	errMu     sync.RWMutex

	// procMu guards cmd and cancel: Start publishes them while Stop reads
	// them concurrently.
	procMu sync.Mutex
	cmd    *exec.Cmd
	cancel context.CancelFunc

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
	// Publish cancel before any provider goroutine starts so a concurrent
	// Stop always has happens-before visibility of it.
	t.procMu.Lock()
	t.cancel = cancel
	t.procMu.Unlock()

	switch t.config.Provider {
	case ProviderCloudflare:
		return t.startCloudflare(ctx)
	case ProviderNgrok:
		return t.startNgrok(ctx)
	case ProviderTailscale:
		return t.startTailscale(ctx)
	default:
		err := fmt.Errorf("unsupported tunnel provider: %s", t.config.Provider)
		t.setState(StateFailed)
		t.setError(err)
		t.closeDone()
		return err
	}
}

// Stop stops the tunnel.
func (t *Tunnel) Stop(ctx context.Context) error {
	if t.State() == StateIdle {
		return nil
	}

	t.procMu.Lock()
	cancel := t.cancel
	cmd := t.cmd
	t.procMu.Unlock()

	if cancel != nil {
		cancel()
	}

	if cmd != nil && cmd.Process != nil {
		// Send interrupt first for graceful shutdown
		if err := cmd.Process.Kill(); err != nil {
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

// markConnected transitions Starting → Connected. Output-parse goroutines
// use this instead of an unconditional store so a late URL line can never
// resurrect a tunnel that already reached a terminal state (Failed/Stopped).
func (t *Tunnel) markConnected() {
	t.compareAndSwapState(StateStarting, StateConnected)
}

// closeDone closes t.done exactly once; every terminal path (start error,
// process exit, unsupported provider) must funnel through it so Stop and
// WaitForURL never block until context expiry.
func (t *Tunnel) closeDone() {
	t.doneOnce.Do(func() { close(t.done) })
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
		err = fmt.Errorf("cloudflared not found in PATH: %w", err)
		t.setState(StateFailed)
		t.setError(err)
		t.closeDone()
		return err
	}

	localURL := fmt.Sprintf("http://%s:%d", t.config.LocalHost, t.config.LocalPort)
	cmd := exec.CommandContext(ctx, binary, "tunnel", "--url", localURL)

	// Capture stderr (cloudflared logs to stderr)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		err = fmt.Errorf("failed to create stderr pipe: %w", err)
		t.setState(StateFailed)
		t.setError(err)
		t.closeDone()
		return err
	}

	if err := cmd.Start(); err != nil {
		err = fmt.Errorf("failed to start cloudflared: %w", err)
		t.setState(StateFailed)
		t.setError(err)
		t.closeDone()
		return err
	}

	t.procMu.Lock()
	t.cmd = cmd
	t.procMu.Unlock()

	// Parse output in goroutine
	go t.parseCloudflareOutput(stderr)

	// Wait for process in goroutine
	go func() {
		defer t.closeDone()
		if err := cmd.Wait(); err != nil {
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
			t.markConnected()
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
		err = fmt.Errorf("ngrok not found in PATH: %w", err)
		t.setState(StateFailed)
		t.setError(err)
		t.closeDone()
		return err
	}

	cmd := exec.CommandContext(ctx, binary, "http", fmt.Sprintf("%d", t.config.LocalPort))

	// ngrok outputs to stdout
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		err = fmt.Errorf("failed to create stdout pipe: %w", err)
		t.setState(StateFailed)
		t.setError(err)
		t.closeDone()
		return err
	}

	if err := cmd.Start(); err != nil {
		err = fmt.Errorf("failed to start ngrok: %w", err)
		t.setState(StateFailed)
		t.setError(err)
		t.closeDone()
		return err
	}

	t.procMu.Lock()
	t.cmd = cmd
	t.procMu.Unlock()

	// Parse output in goroutine
	go t.parseNgrokOutput(stdout)

	// Wait for process in goroutine
	go func() {
		defer t.closeDone()
		if err := cmd.Wait(); err != nil {
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
			t.markConnected()
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
		err = fmt.Errorf("tailscale not found in PATH: %w", err)
		t.setState(StateFailed)
		t.setError(err)
		t.closeDone()
		return err
	}

	// Foreground `tailscale serve <port>` — blocks until killed and
	// tears down the serve mapping on exit. Do NOT pass --bg; that
	// would detach from the lifecycle and leave the serve config
	// behind on Stop().
	cmd := exec.CommandContext(ctx, binary, "serve", fmt.Sprintf("%d", t.config.LocalPort))

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		err = fmt.Errorf("failed to create stdout pipe: %w", err)
		t.setState(StateFailed)
		t.setError(err)
		t.closeDone()
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		err = fmt.Errorf("failed to create stderr pipe: %w", err)
		t.setState(StateFailed)
		t.setError(err)
		t.closeDone()
		return err
	}

	if err := cmd.Start(); err != nil {
		err = fmt.Errorf("failed to start tailscale serve: %w", err)
		t.setState(StateFailed)
		t.setError(err)
		t.closeDone()
		return err
	}

	t.procMu.Lock()
	t.cmd = cmd
	t.procMu.Unlock()

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
			t.markConnected()
		}
	}()

	go func() {
		defer t.closeDone()
		if err := cmd.Wait(); err != nil {
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
			t.markConnected()
		}
	}
}
