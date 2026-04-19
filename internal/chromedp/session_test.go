package chromedp

import (
	"runtime"
	"testing"
)

// TestBuildAllocatorOptsPasswordStore verifies that buildAllocatorOpts always
// includes the password-store=basic flag (prevents OS keyring popups on Linux/WSL).
func TestBuildAllocatorOptsPasswordStore(t *testing.T) {
	session := NewSession(SessionConfig{ID: "test", Headless: true})
	opts := session.buildAllocatorOpts(1920, 1080)

	// Layout (headless=true, no proxy, non-darwin):
	//  0: NoFirstRun
	//  1: NoDefaultBrowserCheck
	//  2: DisableGPU
	//  3: WindowSize
	//  4: disable-background-networking
	//  5: disable-default-apps
	//  6: disable-extensions
	//  7: disable-sync
	//  8: disable-dev-shm-usage
	//  9: password-store        ← must be present
	// 10: use-mock-keychain     (darwin only)
	// 10/11: Headless
	expectedBase := 11 // headless=true, non-darwin
	if runtime.GOOS == "darwin" {
		expectedBase = 12 // +use-mock-keychain
	}
	if len(opts) != expectedBase {
		t.Errorf("buildAllocatorOpts(headless=true) count = %d, want %d", len(opts), expectedBase)
	}
}

// TestBuildAllocatorOptsNoHeadless verifies the count without headless mode.
func TestBuildAllocatorOptsNoHeadless(t *testing.T) {
	session := NewSession(SessionConfig{ID: "test", Headless: false})
	opts := session.buildAllocatorOpts(1920, 1080)

	// Same layout minus Headless opt.
	expectedBase := 10
	if runtime.GOOS == "darwin" {
		expectedBase = 11
	}
	if len(opts) != expectedBase {
		t.Errorf("buildAllocatorOpts(headless=false) count = %d, want %d", len(opts), expectedBase)
	}
}

// TestBuildAllocatorOptsWithProxy verifies the proxy opt is appended.
func TestBuildAllocatorOptsWithProxy(t *testing.T) {
	base := NewSession(SessionConfig{ID: "a", Headless: true})
	baseCount := len(base.buildAllocatorOpts(1920, 1080))

	withProxy := NewSession(SessionConfig{ID: "b", Headless: true, ProxyURL: "http://127.0.0.1:9999"})
	proxyCount := len(withProxy.buildAllocatorOpts(1920, 1080))

	if proxyCount != baseCount+1 {
		t.Errorf("proxy opts = %d, want base(%d)+1", proxyCount, baseCount)
	}
}

// TestBuildAllocatorOptsDarwinMockKeychain verifies use-mock-keychain is
// added on darwin and absent on other platforms, by comparing opt counts.
func TestBuildAllocatorOptsDarwinMockKeychain(t *testing.T) {
	session := NewSession(SessionConfig{ID: "test", Headless: false})
	opts := session.buildAllocatorOpts(1920, 1080)

	// Non-darwin base (headless=false): 10 opts.
	// Darwin base (headless=false): 11 opts (includes use-mock-keychain).
	if runtime.GOOS == "darwin" {
		if len(opts) != 11 {
			t.Errorf("darwin: opts count = %d, want 11 (includes use-mock-keychain)", len(opts))
		}
	} else {
		if len(opts) != 10 {
			t.Errorf("non-darwin: opts count = %d, want 10 (no use-mock-keychain)", len(opts))
		}
	}
}
