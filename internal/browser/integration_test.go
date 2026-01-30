//go:build integration

package browser

import (
	"context"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/testutil"
)

// TestBrowser_Integration_Lifecycle tests the full browser lifecycle.
func TestBrowser_Integration_Lifecycle(t *testing.T) {
	chromePath := testutil.RequireChrome(t)

	// Start test web app
	webapp := testutil.NewTestWebApp(t)
	t.Logf("Test webapp running at %s", webapp.URL)

	// Create browser config
	config := Config{
		ID:         "test-browser",
		URL:        webapp.URL,
		Headless:   true,
		BinaryPath: chromePath,
	}

	browser := New(config)

	// Test initial state
	if browser.State() != StateIdle {
		t.Errorf("Initial state = %v, want StateIdle", browser.State())
	}

	// Start browser
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := browser.Start(ctx); err != nil {
		t.Fatalf("Failed to start browser: %v", err)
	}
	defer browser.Stop(ctx)

	// Verify running state
	if browser.State() != StateRunning {
		t.Errorf("State after start = %v, want StateRunning", browser.State())
	}

	// Verify PID is set
	if browser.PID() == 0 {
		t.Error("Expected non-zero PID after start")
	}

	// Wait for page load
	time.Sleep(2 * time.Second)

	// Verify request was made to webapp
	requests := webapp.GetRequests()
	if len(requests) == 0 {
		t.Error("Expected at least one request to webapp")
	} else {
		t.Logf("Webapp received %d requests", len(requests))
		for i, req := range requests {
			t.Logf("  Request %d: %s %s", i+1, req.Method, req.Path)
		}
	}

	// Stop browser
	if err := browser.Stop(ctx); err != nil {
		t.Errorf("Failed to stop browser: %v", err)
	}

	// Verify stopped state
	if browser.State() != StateStopped {
		t.Errorf("State after stop = %v, want StateStopped", browser.State())
	}
}

// TestBrowser_Integration_WithProxy tests browser with proxy routing.
func TestBrowser_Integration_WithProxy(t *testing.T) {
	chromePath := testutil.RequireChrome(t)

	// Start test web app
	webapp := testutil.NewTestWebApp(t)
	t.Logf("Test webapp running at %s", webapp.URL)

	// TODO: Start agnt proxy pointing to webapp
	// This requires wiring up the proxy manager

	// For now, just test direct connection
	config := Config{
		ID:         "test-browser-proxy",
		URL:        webapp.URL,
		Headless:   true,
		BinaryPath: chromePath,
		// ProxyURL will be set when proxy is available
	}

	browser := New(config)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := browser.Start(ctx); err != nil {
		t.Fatalf("Failed to start browser: %v", err)
	}
	defer browser.Stop(ctx)

	// Wait for page load
	time.Sleep(2 * time.Second)

	// Verify browser info
	info := browser.Info()
	if info.State != "running" {
		t.Errorf("Info.State = %q, want 'running'", info.State)
	}
	if info.Headless != true {
		t.Errorf("Info.Headless = %v, want true", info.Headless)
	}

	t.Logf("Browser info: %+v", info)
}

// TestBrowserManager_Integration tests the browser manager.
func TestBrowserManager_Integration(t *testing.T) {
	chromePath := testutil.RequireChrome(t)

	webapp := testutil.NewTestWebApp(t)

	manager := NewManager()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Start first browser
	config1 := Config{
		ID:         "browser-1",
		URL:        webapp.URL + "/index.html",
		Headless:   true,
		BinaryPath: chromePath,
		Path:       "/test/project1",
	}

	b1, err := manager.Start(ctx, config1.ID, config1)
	if err != nil {
		t.Fatalf("Failed to start browser-1: %v", err)
	}
	defer manager.Stop(ctx, b1.ID())

	// Verify count
	if manager.ActiveCount() != 1 {
		t.Errorf("ActiveCount = %d, want 1", manager.ActiveCount())
	}

	// Start second browser
	config2 := Config{
		ID:         "browser-2",
		URL:        webapp.URL + "/forms.html",
		Headless:   true,
		BinaryPath: chromePath,
		Path:       "/test/project2",
	}

	b2, err := manager.Start(ctx, config2.ID, config2)
	if err != nil {
		t.Fatalf("Failed to start browser-2: %v", err)
	}
	defer manager.Stop(ctx, b2.ID())

	// Verify count
	if manager.ActiveCount() != 2 {
		t.Errorf("ActiveCount = %d, want 2", manager.ActiveCount())
	}

	// List all browsers
	browsers := manager.List()
	if len(browsers) != 2 {
		t.Errorf("List() returned %d browsers, want 2", len(browsers))
	}

	// List by path
	proj1Browsers := manager.ListByPath("/test/project1")
	if len(proj1Browsers) != 1 {
		t.Errorf("ListByPath('/test/project1') returned %d browsers, want 1", len(proj1Browsers))
	}

	// Get by ID
	found, err := manager.Get("browser-1")
	if err != nil {
		t.Errorf("Get('browser-1') failed: %v", err)
	}
	if found.ID() != "browser-1" {
		t.Errorf("Get('browser-1') returned browser with ID %q", found.ID())
	}

	// Fuzzy lookup
	found, err = manager.Get("browser")
	if err == nil {
		// Should be ambiguous with two browsers
		t.Log("Fuzzy lookup 'browser' matched (may be ambiguous)")
	}

	// Stop all
	stopped, err := manager.StopAll(ctx)
	if err != nil {
		t.Errorf("StopAll failed: %v", err)
	}
	if len(stopped) != 2 {
		t.Errorf("StopAll stopped %d browsers, want 2", len(stopped))
	}

	// Verify count after stop
	if manager.ActiveCount() != 0 {
		t.Errorf("ActiveCount after StopAll = %d, want 0", manager.ActiveCount())
	}
}
