package chromedp

import (
	"context"
	"os"
	"testing"
	"time"

	cdp "github.com/chromedp/chromedp"
)

// TestSessionManagerIntegration tests the automation session manager with a real browser.
// This test requires Chrome to be installed and runs an actual browser instance.
func TestSessionManagerIntegration(t *testing.T) {
	if os.Getenv("SKIP_BROWSER_TESTS") != "" {
		t.Skip("SKIP_BROWSER_TESTS is set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	manager := NewSessionManager()

	// Test 1: Start a session
	config := SessionConfig{
		ID:       "test-session-1",
		URL:      "about:blank",
		Headless: true,
	}

	session, err := manager.Start(ctx, "test-session-1", config)
	if err != nil {
		t.Fatalf("Failed to start session: %v", err)
	}

	// Verify session is running
	if session.State() != StateRunning {
		t.Errorf("Expected state Running, got %s", session.State())
	}

	// Test 2: Verify session appears in list
	sessions := manager.List()
	if len(sessions) != 1 {
		t.Errorf("Expected 1 session in list, got %d", len(sessions))
	}

	// Test 3: Get session by ID
	retrieved, err := manager.Get("test-session-1")
	if err != nil {
		t.Errorf("Failed to get session: %v", err)
	}
	if retrieved.ID() != "test-session-1" {
		t.Errorf("Expected ID test-session-1, got %s", retrieved.ID())
	}

	// Test 4: Verify active count
	if manager.ActiveCount() != 1 {
		t.Errorf("Expected active count 1, got %d", manager.ActiveCount())
	}

	// Test 5: Stop the session
	if err := manager.Stop(ctx, "test-session-1"); err != nil {
		t.Fatalf("Failed to stop session: %v", err)
	}

	// Wait for session to be removed
	time.Sleep(200 * time.Millisecond)

	// Verify session is gone
	if manager.ActiveCount() != 0 {
		t.Errorf("Expected active count 0 after stop, got %d", manager.ActiveCount())
	}
}

// TestSessionNavigationIntegration tests navigating to a URL.
func TestSessionNavigationIntegration(t *testing.T) {
	if os.Getenv("SKIP_BROWSER_TESTS") != "" {
		t.Skip("SKIP_BROWSER_TESTS is set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	manager := NewSessionManager()

	config := SessionConfig{
		ID:       "nav-test",
		URL:      "data:text/html,<h1>Test Page</h1>",
		Headless: true,
	}

	session, err := manager.Start(ctx, "nav-test", config)
	if err != nil {
		t.Fatalf("Failed to start session: %v", err)
	}
	defer manager.Stop(ctx, "nav-test")

	// Navigate to a different page
	if err := session.Run(cdp.Navigate("data:text/html,<h1>New Page</h1>")); err != nil {
		t.Fatalf("Failed to navigate: %v", err)
	}

	// Verify session is running
	if session.State() != StateRunning {
		t.Errorf("Expected state Running, got %s", session.State())
	}
}

// TestScreenshotIntegration tests taking a screenshot.
func TestScreenshotIntegration(t *testing.T) {
	if os.Getenv("SKIP_BROWSER_TESTS") != "" {
		t.Skip("SKIP_BROWSER_TESTS is set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	manager := NewSessionManager()

	config := SessionConfig{
		ID:       "screenshot-test",
		URL:      "data:text/html,<h1 style='color:red'>Screenshot Test</h1>",
		Headless: true,
	}

	session, err := manager.Start(ctx, "screenshot-test", config)
	if err != nil {
		t.Fatalf("Failed to start session: %v", err)
	}
	defer manager.Stop(ctx, "screenshot-test")

	// Take a viewport screenshot
	result, err := CaptureViewport(session, ScreenshotOptions{
		Label: "test",
	})
	if err != nil {
		t.Fatalf("Failed to capture screenshot: %v", err)
	}

	t.Logf("Screenshot saved to: %s", result.Path)

	// Verify the file exists
	if _, err := os.Stat(result.Path); os.IsNotExist(err) {
		t.Errorf("Screenshot file does not exist: %s", result.Path)
	}

	// Cleanup
	os.Remove(result.Path)
}
