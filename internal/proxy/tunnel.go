package proxy

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"devtool-mcp/internal/protocol"
)

// TunnelManager manages a tunnel process alongside a proxy.
type TunnelManager struct {
	config    *protocol.TunnelConfig
	proxyPort int
	publicURL atomic.Value // stores string
	running   atomic.Bool
	cmd       *exec.Cmd
	cancel    context.CancelFunc
	mu        sync.Mutex
	output    []string // recent output lines for debugging
	outputMu  sync.RWMutex
}

// Common patterns to extract public URLs from tunnel output
var tunnelURLPatterns = []*regexp.Regexp{
	// ngrok patterns
	regexp.MustCompile(`Forwarding\s+(https?://[^\s]+)\s+->`),
	regexp.MustCompile(`url=(https?://[^\s]+)`),
	regexp.MustCompile(`https?://[a-zA-Z0-9-]+\.ngrok(?:-free)?\.(?:app|io)[^\s]*`),
	// cloudflared patterns
	regexp.MustCompile(`https?://[a-zA-Z0-9-]+\.trycloudflare\.com[^\s]*`),
	regexp.MustCompile(`\|\s+(https?://[^\s|]+)`),
	// tailscale funnel patterns (https://machine.tailnet.ts.net)
	regexp.MustCompile(`(https://[a-zA-Z0-9-]+\.[a-zA-Z0-9-]+\.ts\.net[^\s]*)`),
	// localtunnel patterns
	regexp.MustCompile(`your url is:\s*(https?://[^\s]+)`),
	// Generic https URL pattern (fallback)
	regexp.MustCompile(`(https://[a-zA-Z0-9][a-zA-Z0-9-]*\.[a-zA-Z0-9.-]+[^\s]*)`),
}

// NewTunnelManager creates a new tunnel manager.
func NewTunnelManager(config *protocol.TunnelConfig, proxyPort int) *TunnelManager {
	tm := &TunnelManager{
		config:    config,
		proxyPort: proxyPort,
		output:    make([]string, 0, 100),
	}
	tm.publicURL.Store("")
	return tm
}

// Start starts the tunnel process.
func (tm *TunnelManager) Start(ctx context.Context) error {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	if tm.running.Load() {
		return fmt.Errorf("tunnel already running")
	}

	// Build the command based on provider
	cmdName, args, err := tm.buildCommand()
	if err != nil {
		return err
	}

	// Create cancellable context
	tunnelCtx, cancel := context.WithCancel(ctx)
	tm.cancel = cancel

	// Create command
	tm.cmd = exec.CommandContext(tunnelCtx, cmdName, args...)

	// Capture stdout and stderr
	stdout, err := tm.cmd.StdoutPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	stderr, err := tm.cmd.StderrPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("failed to create stderr pipe: %w", err)
	}

	// Start the process
	if err := tm.cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("failed to start tunnel: %w", err)
	}

	tm.running.Store(true)

	// Monitor output for URL extraction
	go tm.monitorOutput(bufio.NewScanner(stdout))
	go tm.monitorOutput(bufio.NewScanner(stderr))

	// Wait for process to exit
	go func() {
		tm.cmd.Wait()
		tm.running.Store(false)
	}()

	return nil
}

// buildCommand builds the tunnel command based on provider.
func (tm *TunnelManager) buildCommand() (string, []string, error) {
	switch strings.ToLower(tm.config.Provider) {
	case "ngrok":
		return tm.buildNgrokCommand()
	case "cloudflared", "cloudflare":
		return tm.buildCloudflaredCommand()
	case "tailscale":
		return tm.buildTailscaleCommand()
	case "custom":
		return tm.buildCustomCommand()
	default:
		return "", nil, fmt.Errorf("unknown tunnel provider: %s", tm.config.Provider)
	}
}

func (tm *TunnelManager) buildNgrokCommand() (string, []string, error) {
	args := []string{"http", fmt.Sprintf("%d", tm.proxyPort)}

	if tm.config.AuthToken != "" {
		args = append(args, "--authtoken", tm.config.AuthToken)
	}

	if tm.config.Region != "" {
		args = append(args, "--region", tm.config.Region)
	}

	// Add any extra args
	args = append(args, tm.config.Args...)

	return "ngrok", args, nil
}

func (tm *TunnelManager) buildCloudflaredCommand() (string, []string, error) {
	args := []string{"tunnel", "--url", fmt.Sprintf("http://localhost:%d", tm.proxyPort)}

	// Add any extra args
	args = append(args, tm.config.Args...)

	return "cloudflared", args, nil
}

func (tm *TunnelManager) buildTailscaleCommand() (string, []string, error) {
	// tailscale funnel exposes a local port via Tailscale Funnel
	// Usage: tailscale funnel <port>
	// Or: tailscale funnel --bg <port> (background mode, but we manage the process)
	//
	// The funnel URL is typically: https://<machine>.<tailnet>.ts.net
	// Tailscale will output the URL when funnel starts

	args := []string{"funnel", fmt.Sprintf("%d", tm.proxyPort)}

	// Add any extra args (e.g., --bg for background, --https for custom port)
	args = append(args, tm.config.Args...)

	return "tailscale", args, nil
}

func (tm *TunnelManager) buildCustomCommand() (string, []string, error) {
	if tm.config.Command == "" {
		return "", nil, fmt.Errorf("custom tunnel requires command to be set")
	}

	// Replace {{PORT}} placeholder in command and args
	cmd := strings.ReplaceAll(tm.config.Command, "{{PORT}}", fmt.Sprintf("%d", tm.proxyPort))

	args := make([]string, len(tm.config.Args))
	for i, arg := range tm.config.Args {
		args[i] = strings.ReplaceAll(arg, "{{PORT}}", fmt.Sprintf("%d", tm.proxyPort))
	}

	return cmd, args, nil
}

// monitorOutput monitors tunnel output for URL extraction.
func (tm *TunnelManager) monitorOutput(scanner *bufio.Scanner) {
	for scanner.Scan() {
		line := scanner.Text()

		// Store recent output for debugging
		tm.outputMu.Lock()
		tm.output = append(tm.output, line)
		if len(tm.output) > 100 {
			tm.output = tm.output[1:]
		}
		tm.outputMu.Unlock()

		// Try to extract URL if we don't have one yet
		if tm.publicURL.Load().(string) == "" {
			if url := tm.extractURL(line); url != "" {
				tm.publicURL.Store(url)
			}
		}
	}
}

// extractURL tries to extract a public URL from a line of output.
func (tm *TunnelManager) extractURL(line string) string {
	for _, pattern := range tunnelURLPatterns {
		if matches := pattern.FindStringSubmatch(line); len(matches) > 0 {
			// Return the captured group if present, otherwise the full match
			if len(matches) > 1 {
				return matches[1]
			}
			return matches[0]
		}
	}
	return ""
}

// Stop stops the tunnel process.
func (tm *TunnelManager) Stop() error {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	if !tm.running.Load() {
		return nil
	}

	if tm.cancel != nil {
		tm.cancel()
	}

	// Give it a moment to exit gracefully
	done := make(chan struct{})
	go func() {
		if tm.cmd != nil && tm.cmd.Process != nil {
			tm.cmd.Wait()
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		// Force kill if still running
		if tm.cmd != nil && tm.cmd.Process != nil {
			tm.cmd.Process.Kill()
		}
	}

	tm.running.Store(false)
	return nil
}

// PublicURL returns the detected public URL.
func (tm *TunnelManager) PublicURL() string {
	return tm.publicURL.Load().(string)
}

// IsRunning returns whether the tunnel is running.
func (tm *TunnelManager) IsRunning() bool {
	return tm.running.Load()
}

// RecentOutput returns recent output lines for debugging.
func (tm *TunnelManager) RecentOutput() []string {
	tm.outputMu.RLock()
	defer tm.outputMu.RUnlock()
	result := make([]string, len(tm.output))
	copy(result, tm.output)
	return result
}

// WaitForURL waits for the public URL to be detected with a timeout.
func (tm *TunnelManager) WaitForURL(timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if url := tm.PublicURL(); url != "" {
			return url, nil
		}
		if !tm.running.Load() {
			return "", fmt.Errorf("tunnel process exited before URL was detected")
		}
		time.Sleep(100 * time.Millisecond)
	}
	return "", fmt.Errorf("timeout waiting for tunnel URL")
}
