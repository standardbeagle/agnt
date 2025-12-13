package agnt

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"devtool-mcp/internal/daemon"
	"devtool-mcp/internal/protocol"
)

// AutoStarter manages auto-starting scripts and proxies from config.
type AutoStarter struct {
	config *Config
	client *daemon.ResilientClient

	// Track what we started so we can clean up
	startedScripts []string
	startedProxies []string
	mu             sync.Mutex
}

// NewAutoStarter creates a new AutoStarter.
func NewAutoStarter(config *Config, client *daemon.ResilientClient) *AutoStarter {
	return &AutoStarter{
		config: config,
		client: client,
	}
}

// Start starts all configured auto-start scripts and proxies.
func (a *AutoStarter) Start(ctx context.Context, projectPath string) error {
	var errs []string

	// Start proxies first (scripts might depend on them)
	for name, proxy := range a.config.GetAutostartProxies() {
		if err := a.startProxy(ctx, name, proxy, projectPath); err != nil {
			errs = append(errs, fmt.Sprintf("proxy %s: %v", name, err))
		}
	}

	// Start scripts
	for name, script := range a.config.GetAutostartScripts() {
		if err := a.startScript(ctx, name, script, projectPath); err != nil {
			errs = append(errs, fmt.Sprintf("script %s: %v", name, err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("autostart errors: %s", strings.Join(errs, "; "))
	}

	return nil
}

func (a *AutoStarter) startProxy(ctx context.Context, name string, proxy *ProxyConfig, projectPath string) error {
	if a.client == nil {
		return fmt.Errorf("no daemon connection")
	}

	// Use -1 to signal "use default" (hash-based port), matching daemon_tools.go behavior
	port := proxy.Port
	if port == 0 {
		port = -1 // Trigger hash-based default in daemon
	}

	maxLogSize := proxy.MaxLogSize
	if maxLogSize == 0 {
		maxLogSize = 1000
	}

	_, err := a.client.ProxyStart(name, proxy.Target, port, maxLogSize, projectPath)
	if err != nil {
		// Ignore "already exists" errors - proxy might already be running
		if strings.Contains(err.Error(), "already exists") {
			return nil
		}
		return err
	}

	a.mu.Lock()
	a.startedProxies = append(a.startedProxies, name)
	a.mu.Unlock()

	return nil
}

func (a *AutoStarter) startScript(ctx context.Context, name string, script *ScriptConfig, projectPath string) error {
	if a.client == nil {
		return fmt.Errorf("no daemon connection")
	}

	if script.Command == "" {
		return fmt.Errorf("no command specified")
	}

	// Determine working directory
	cwd := projectPath
	if script.Cwd != "" {
		cwd = script.Cwd
	}

	// Build run config
	config := protocol.RunConfig{
		ID:      name,
		Path:    cwd,
		Mode:    "background",
		Raw:     true,
		Command: script.Command,
		Args:    script.Args,
	}

	// Use daemon client to run the script
	err := a.client.WithClient(func(c *daemon.Client) error {
		_, err := c.Run(config)
		return err
	})

	if err != nil {
		// Ignore "already exists" errors
		if strings.Contains(err.Error(), "already exists") {
			return nil
		}
		return err
	}

	a.mu.Lock()
	a.startedScripts = append(a.startedScripts, name)
	a.mu.Unlock()

	return nil
}

// Stop stops all auto-started scripts and proxies.
func (a *AutoStarter) Stop(ctx context.Context) error {
	var errs []string

	a.mu.Lock()
	scripts := a.startedScripts
	proxies := a.startedProxies
	a.startedScripts = nil
	a.startedProxies = nil
	a.mu.Unlock()

	// Stop scripts first
	for _, name := range scripts {
		if err := a.stopScript(ctx, name); err != nil {
			errs = append(errs, fmt.Sprintf("script %s: %v", name, err))
		}
	}

	// Stop proxies
	for _, name := range proxies {
		if err := a.stopProxy(ctx, name); err != nil {
			errs = append(errs, fmt.Sprintf("proxy %s: %v", name, err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("stop errors: %s", strings.Join(errs, "; "))
	}

	return nil
}

func (a *AutoStarter) stopScript(ctx context.Context, name string) error {
	if a.client == nil {
		return nil
	}

	return a.client.WithClient(func(c *daemon.Client) error {
		_, err := c.ProcStop(name, false)
		return err
	})
}

func (a *AutoStarter) stopProxy(ctx context.Context, name string) error {
	if a.client == nil {
		return nil
	}

	return a.client.ProxyStop(name)
}

// StartedScripts returns the names of scripts that were auto-started.
func (a *AutoStarter) StartedScripts() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	result := make([]string, len(a.startedScripts))
	copy(result, a.startedScripts)
	return result
}

// StartedProxies returns the names of proxies that were auto-started.
func (a *AutoStarter) StartedProxies() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	result := make([]string, len(a.startedProxies))
	copy(result, a.startedProxies)
	return result
}
