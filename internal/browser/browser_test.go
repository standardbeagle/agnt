package browser

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestBrowserNew(t *testing.T) {
	config := Config{
		ID:       "test",
		URL:      "http://localhost:3000",
		Headless: true,
	}

	b := New(config)
	if b == nil {
		t.Fatal("New returned nil")
	}
	if b.ID() != "test" {
		t.Errorf("ID() = %q, want %q", b.ID(), "test")
	}
	if b.State() != StateIdle {
		t.Errorf("State() = %v, want StateIdle", b.State())
	}
	if b.PID() != 0 {
		t.Errorf("PID() = %d, want 0", b.PID())
	}
}

func TestBrowserDefaultWindowSize(t *testing.T) {
	config := Config{
		ID:       "test",
		URL:      "http://localhost:3000",
		Headless: true,
	}

	b := New(config)
	if b.config.WindowSize != "1920,1080" {
		t.Errorf("WindowSize = %q, want %q", b.config.WindowSize, "1920,1080")
	}
}

func TestBrowserCustomWindowSize(t *testing.T) {
	config := Config{
		ID:         "test",
		URL:        "http://localhost:3000",
		Headless:   true,
		WindowSize: "1280,720",
	}

	b := New(config)
	if b.config.WindowSize != "1280,720" {
		t.Errorf("WindowSize = %q, want %q", b.config.WindowSize, "1280,720")
	}
}

func TestBrowserBuildArgsPasswordStore(t *testing.T) {
	b := New(Config{ID: "test", Headless: true})
	args := b.buildArgs()

	found := false
	for _, arg := range args {
		if arg == "--password-store=basic" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("buildArgs() missing --password-store=basic; got: %v", args)
	}
}

func TestBrowserBuildArgsMockKeychainDarwin(t *testing.T) {
	b := New(Config{ID: "test", Headless: true})
	args := b.buildArgs()

	hasMockKeychain := false
	for _, arg := range args {
		if strings.Contains(arg, "use-mock-keychain") {
			hasMockKeychain = true
			break
		}
	}

	if runtime.GOOS == "darwin" {
		if !hasMockKeychain {
			t.Error("darwin: buildArgs() missing --use-mock-keychain")
		}
	} else {
		if hasMockKeychain {
			t.Error("non-darwin: buildArgs() should not include --use-mock-keychain")
		}
	}
}

func TestBrowserBuildArgsHeadless(t *testing.T) {
	config := Config{
		ID:       "test",
		URL:      "http://localhost:3000",
		Headless: true,
	}

	b := New(config)
	args := b.buildArgs()

	// Check for headless flags
	hasHeadless := false
	hasDisableGpu := false
	for _, arg := range args {
		if arg == "--headless=new" {
			hasHeadless = true
		}
		if arg == "--disable-gpu" {
			hasDisableGpu = true
		}
	}

	if !hasHeadless {
		t.Error("headless mode should include --headless=new")
	}
	if !hasDisableGpu {
		t.Error("headless mode should include --disable-gpu")
	}
}

func TestBrowserBuildArgsNonHeadless(t *testing.T) {
	config := Config{
		ID:       "test",
		URL:      "http://localhost:3000",
		Headless: false,
	}

	b := New(config)
	args := b.buildArgs()

	// Check that headless flags are NOT present
	for _, arg := range args {
		if arg == "--headless=new" {
			t.Error("non-headless mode should not include --headless=new")
		}
	}
}

func TestBrowserBuildArgsProxy(t *testing.T) {
	config := Config{
		ID:       "test",
		URL:      "http://localhost:3000",
		Headless: true,
		ProxyURL: "http://127.0.0.1:12345",
	}

	b := New(config)
	args := b.buildArgs()

	// Check for proxy flag
	hasProxy := false
	for _, arg := range args {
		if arg == "--proxy-server=http://127.0.0.1:12345" {
			hasProxy = true
			break
		}
	}

	if !hasProxy {
		t.Error("args should include proxy-server flag")
	}
}

func TestBrowserBuildArgsURL(t *testing.T) {
	config := Config{
		ID:       "test",
		URL:      "http://localhost:3000",
		Headless: true,
	}

	b := New(config)
	args := b.buildArgs()

	// URL should be the last argument
	if args[len(args)-1] != "http://localhost:3000" {
		t.Errorf("last arg = %q, want URL", args[len(args)-1])
	}
}

func TestBrowserStartInvalidBinary(t *testing.T) {
	config := Config{
		ID:         "test",
		URL:        "http://localhost:3000",
		BinaryPath: "/nonexistent/chrome",
		Headless:   true,
	}

	b := New(config)
	ctx := context.Background()

	err := b.Start(ctx)
	if err == nil {
		t.Fatal("expected error for nonexistent binary")
	}

	if b.State() != StateFailed {
		t.Errorf("State() = %v, want StateFailed", b.State())
	}

	// Done channel should be closed
	select {
	case <-b.Done():
		// Good
	default:
		t.Error("Done channel should be closed after failed start")
	}
}

func TestBrowserStartNoBinary(t *testing.T) {
	// Skip if Chrome is installed (test only makes sense when Chrome isn't found)
	discoverer := DefaultDiscoverer()
	if discoverer.Find() != "" {
		t.Skip("Chrome is installed, skipping no-binary test")
	}

	config := Config{
		ID:       "test",
		URL:      "http://localhost:3000",
		Headless: true,
	}

	b := New(config)
	ctx := context.Background()

	err := b.Start(ctx)
	if err == nil {
		t.Fatal("expected error when Chrome not found")
	}

	if b.State() != StateFailed {
		t.Errorf("State() = %v, want StateFailed", b.State())
	}
}

func TestBrowserStartDoubleStart(t *testing.T) {
	config := Config{
		ID:         "test",
		URL:        "http://localhost:3000",
		BinaryPath: "/nonexistent/chrome", // Will fail, but that's OK for this test
		Headless:   true,
	}

	b := New(config)
	ctx := context.Background()

	// First start (will fail but changes state)
	_ = b.Start(ctx)

	// Second start should fail with "already started"
	err := b.Start(ctx)
	if err == nil || err.Error() != "browser already started" {
		t.Errorf("second Start() error = %v, want 'browser already started'", err)
	}
}

func TestBrowserStopNotStarted(t *testing.T) {
	config := Config{
		ID:       "test",
		URL:      "http://localhost:3000",
		Headless: true,
	}

	b := New(config)
	ctx := context.Background()

	// Stop without start should be a no-op
	err := b.Stop(ctx)
	if err != nil {
		t.Errorf("Stop() on idle browser error: %v", err)
	}
}

func TestBrowserInfo(t *testing.T) {
	config := Config{
		ID:         "test-browser",
		URL:        "http://localhost:3000",
		Headless:   true,
		BinaryPath: "/usr/bin/chrome",
		ProxyURL:   "http://proxy:8080",
		Path:       "/home/project",
	}

	b := New(config)
	info := b.Info()

	if info.ID != "test-browser" {
		t.Errorf("Info.ID = %q, want %q", info.ID, "test-browser")
	}
	if info.State != "idle" {
		t.Errorf("Info.State = %q, want %q", info.State, "idle")
	}
	if info.URL != "http://localhost:3000" {
		t.Errorf("Info.URL = %q, want %q", info.URL, "http://localhost:3000")
	}
	if !info.Headless {
		t.Error("Info.Headless should be true")
	}
	if info.BinaryPath != "/usr/bin/chrome" {
		t.Errorf("Info.BinaryPath = %q, want %q", info.BinaryPath, "/usr/bin/chrome")
	}
	if info.ProxyURL != "http://proxy:8080" {
		t.Errorf("Info.ProxyURL = %q, want %q", info.ProxyURL, "http://proxy:8080")
	}
	if info.Path != "/home/project" {
		t.Errorf("Info.Path = %q, want %q", info.Path, "/home/project")
	}
}

func TestBrowserError(t *testing.T) {
	config := Config{
		ID:         "test",
		URL:        "http://localhost:3000",
		BinaryPath: "/nonexistent/chrome",
		Headless:   true,
	}

	b := New(config)
	ctx := context.Background()

	_ = b.Start(ctx)

	err := b.Error()
	if err == nil {
		t.Error("Error() should return error after failed start")
	}

	// Error should be reflected in Info
	info := b.Info()
	if info.Error == "" {
		t.Error("Info.Error should be set after failed start")
	}
}

func TestBrowserStateString(t *testing.T) {
	tests := []struct {
		state    State
		expected string
	}{
		{StateIdle, "idle"},
		{StateStarting, "starting"},
		{StateRunning, "running"},
		{StateStopping, "stopping"},
		{StateStopped, "stopped"},
		{StateFailed, "failed"},
		{State(99), "unknown"},
	}

	for _, tc := range tests {
		result := tc.state.String()
		if result != tc.expected {
			t.Errorf("State(%d).String() = %q, want %q", tc.state, result, tc.expected)
		}
	}
}

func TestBrowserCompareAndSwapState(t *testing.T) {
	b := New(Config{ID: "test"})

	// Should succeed
	if !b.compareAndSwapState(StateIdle, StateStarting) {
		t.Error("compareAndSwapState(Idle, Starting) should succeed")
	}

	// Should fail (wrong old state)
	if b.compareAndSwapState(StateIdle, StateRunning) {
		t.Error("compareAndSwapState(Idle, Running) should fail")
	}
}

func TestBrowserPath(t *testing.T) {
	config := Config{
		ID:   "test",
		Path: "/home/project",
	}

	b := New(config)
	if b.Path() != "/home/project" {
		t.Errorf("Path() = %q, want %q", b.Path(), "/home/project")
	}
}

func TestBrowserDoneChannel(t *testing.T) {
	config := Config{
		ID:       "test",
		URL:      "http://localhost:3000",
		Headless: true,
	}

	b := New(config)

	// Done channel should not be closed initially
	select {
	case <-b.Done():
		t.Error("Done() should not be closed for idle browser")
	case <-time.After(10 * time.Millisecond):
		// Good - not closed
	}
}
