// Package testutil provides utilities for integration testing agnt components.
//
// It includes helpers for:
// - Setting up test web servers
// - Creating mock agents for PTY testing
// - Managing test daemon instances
// - Common test fixtures and assertions
package testutil

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestWebApp represents a test web application server.
type TestWebApp struct {
	Server   *httptest.Server
	URL      string
	Requests []RequestLog
	mu       sync.Mutex
}

// RequestLog stores information about a request.
type RequestLog struct {
	Time    time.Time
	Method  string
	Path    string
	Headers http.Header
}

// NewTestWebApp creates a test web server that serves the static test pages.
func NewTestWebApp(t *testing.T) *TestWebApp {
	t.Helper()

	app := &TestWebApp{}

	// Find static directory
	staticDir := findStaticDir()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Log the request
		app.mu.Lock()
		app.Requests = append(app.Requests, RequestLog{
			Time:    time.Now(),
			Method:  r.Method,
			Path:    r.URL.Path,
			Headers: r.Header.Clone(),
		})
		app.mu.Unlock()

		// Serve static files
		if staticDir != "" {
			filePath := filepath.Join(staticDir, r.URL.Path)
			if r.URL.Path == "/" {
				filePath = filepath.Join(staticDir, "index.html")
			}
			if _, err := os.Stat(filePath); err == nil {
				http.ServeFile(w, r, filePath)
				return
			}
		}

		// Default response for paths without static files
		switch r.URL.Path {
		case "/", "/index.html":
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(`<!DOCTYPE html><html><head></head><body><h1>Test Page</h1></body></html>`))
		case "/api/health":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"status":"ok"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	app.Server = httptest.NewServer(handler)
	app.URL = app.Server.URL

	t.Cleanup(func() {
		app.Server.Close()
	})

	return app
}

// findStaticDir finds the testdata/webapps/static directory.
func findStaticDir() string {
	// Try relative paths from common locations
	candidates := []string{
		"testdata/webapps/static",
		"../testdata/webapps/static",
		"../../testdata/webapps/static",
		"../../../testdata/webapps/static",
	}

	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			abs, _ := filepath.Abs(candidate)
			return abs
		}
	}

	return ""
}

// GetRequestCount returns the number of requests received.
func (app *TestWebApp) GetRequestCount() int {
	app.mu.Lock()
	defer app.mu.Unlock()
	return len(app.Requests)
}

// GetRequests returns a copy of all logged requests.
func (app *TestWebApp) GetRequests() []RequestLog {
	app.mu.Lock()
	defer app.mu.Unlock()
	result := make([]RequestLog, len(app.Requests))
	copy(result, app.Requests)
	return result
}

// ClearRequests clears the request log.
func (app *TestWebApp) ClearRequests() {
	app.mu.Lock()
	defer app.mu.Unlock()
	app.Requests = nil
}

// MockAgent represents a mock AI agent process for PTY testing.
type MockAgent struct {
	Cmd       *exec.Cmd
	Stdin     io.WriteCloser
	Stdout    io.ReadCloser
	Stderr    io.ReadCloser
	cancel    context.CancelFunc
	outputBuf []byte
	outputMu  sync.Mutex
}

// StartMockAgent starts the mock agent process.
func StartMockAgent(t *testing.T, opts ...MockAgentOption) *MockAgent {
	t.Helper()

	config := &mockAgentConfig{
		delay:       100,
		echoMode:    false,
		outputLines: 3,
	}
	for _, opt := range opts {
		opt(config)
	}

	// Find mock agent binary or use go run
	mockAgentPath := findMockAgentPath()

	ctx, cancel := context.WithCancel(context.Background())

	var cmd *exec.Cmd
	if mockAgentPath != "" {
		cmd = exec.CommandContext(ctx, mockAgentPath)
	} else {
		// Fall back to go run
		cmd = exec.CommandContext(ctx, "go", "run", "./testdata/mockagent")
	}

	// Set environment variables
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("MOCK_DELAY=%d", config.delay),
		fmt.Sprintf("MOCK_OUTPUT_LINES=%d", config.outputLines),
	)
	if config.echoMode {
		cmd.Env = append(cmd.Env, "MOCK_ECHO=true")
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		t.Fatalf("Failed to create stdin pipe: %v", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatalf("Failed to create stdout pipe: %v", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		t.Fatalf("Failed to create stderr pipe: %v", err)
	}

	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("Failed to start mock agent: %v", err)
	}

	agent := &MockAgent{
		Cmd:    cmd,
		Stdin:  stdin,
		Stdout: stdout,
		Stderr: stderr,
		cancel: cancel,
	}

	// Start output collector
	go agent.collectOutput()

	t.Cleanup(func() {
		agent.Stop()
	})

	// Wait for agent to be ready (read initial output)
	time.Sleep(200 * time.Millisecond)

	return agent
}

func (a *MockAgent) collectOutput() {
	buf := make([]byte, 4096)
	for {
		n, err := a.Stdout.Read(buf)
		if err != nil {
			return
		}
		if n > 0 {
			a.outputMu.Lock()
			a.outputBuf = append(a.outputBuf, buf[:n]...)
			a.outputMu.Unlock()
		}
	}
}

// SendInput sends input to the mock agent.
func (a *MockAgent) SendInput(input string) error {
	_, err := a.Stdin.Write([]byte(input + "\n"))
	return err
}

// GetOutput returns all collected output.
func (a *MockAgent) GetOutput() string {
	a.outputMu.Lock()
	defer a.outputMu.Unlock()
	return string(a.outputBuf)
}

// ClearOutput clears the output buffer.
func (a *MockAgent) ClearOutput() {
	a.outputMu.Lock()
	defer a.outputMu.Unlock()
	a.outputBuf = nil
}

// WaitForOutput waits for output containing the given substring.
func (a *MockAgent) WaitForOutput(substr string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if contains := func() bool {
			a.outputMu.Lock()
			defer a.outputMu.Unlock()
			return len(a.outputBuf) > 0 && contains(string(a.outputBuf), substr)
		}(); contains {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsAt(s, substr, 0))
}

func containsAt(s, substr string, start int) bool {
	for i := start; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// Stop stops the mock agent.
func (a *MockAgent) Stop() {
	if a.cancel != nil {
		a.cancel()
	}
	if a.Cmd != nil && a.Cmd.Process != nil {
		a.Cmd.Process.Kill()
		a.Cmd.Wait()
	}
}

type mockAgentConfig struct {
	delay       int
	echoMode    bool
	outputLines int
}

// MockAgentOption configures the mock agent.
type MockAgentOption func(*mockAgentConfig)

// WithDelay sets the response delay in milliseconds.
func WithDelay(ms int) MockAgentOption {
	return func(c *mockAgentConfig) {
		c.delay = ms
	}
}

// WithEchoMode enables echo mode.
func WithEchoMode() MockAgentOption {
	return func(c *mockAgentConfig) {
		c.echoMode = true
	}
}

// WithOutputLines sets the number of output lines per response.
func WithOutputLines(n int) MockAgentOption {
	return func(c *mockAgentConfig) {
		c.outputLines = n
	}
}

func findMockAgentPath() string {
	// Check if pre-built binary exists
	candidates := []string{
		"testdata/mockagent/mockagent",
		"../testdata/mockagent/mockagent",
		"../../testdata/mockagent/mockagent",
	}

	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			abs, _ := filepath.Abs(candidate)
			return abs
		}
	}

	return ""
}

// GetFreePort returns an available TCP port.
func GetFreePort(t *testing.T) int {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to get free port: %v", err)
	}
	defer listener.Close()

	return listener.Addr().(*net.TCPAddr).Port
}

// WaitForPort waits for a TCP port to become available.
func WaitForPort(t *testing.T, port int, timeout time.Duration) bool {
	t.Helper()

	deadline := time.Now().Add(timeout)
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}

	return false
}

// TempDir creates a temporary directory for the test.
func TempDir(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

// TempFile creates a temporary file with the given content.
func TempFile(t *testing.T, content string) string {
	t.Helper()

	f, err := os.CreateTemp(t.TempDir(), "testfile-*")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer f.Close()

	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("Failed to write temp file: %v", err)
	}

	return f.Name()
}

// RequireChrome skips the test if Chrome is not available.
func RequireChrome(t *testing.T) string {
	t.Helper()

	// Check environment variable first
	if path := os.Getenv("AGNT_TEST_CHROME_PATH"); path != "" {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}

	// Common Chrome paths
	paths := []string{
		"/usr/bin/google-chrome",
		"/usr/bin/google-chrome-stable",
		"/usr/bin/chromium",
		"/usr/bin/chromium-browser",
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
	}

	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}

	t.Skip("Chrome not found - skipping browser test")
	return ""
}
