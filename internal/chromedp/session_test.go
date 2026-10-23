package chromedp

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"
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

// TestBuildAllocatorOptsWithProxy verifies a reverse-proxy URL is never turned
// into a browser-wide forward proxy.
func TestBuildAllocatorOptsWithProxy(t *testing.T) {
	base := NewSession(SessionConfig{ID: "a", Headless: true})
	baseCount := len(base.buildAllocatorOpts(1920, 1080))

	withProxy := NewSession(SessionConfig{ID: "b", Headless: true, ProxyURL: "http://127.0.0.1:9999"})
	proxyCount := len(withProxy.buildAllocatorOpts(1920, 1080))

	if proxyCount != baseCount {
		t.Errorf("proxy opts = %d, want base(%d): ProxyURL must not add a Chrome proxy flag", proxyCount, baseCount)
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

// startWithFakes builds a session whose two browser-touching startup steps are
// replaced, so Start's own control flow runs without spawning Chrome. The
// chromedp contexts Start creates are real but inert: nothing is allocated
// until a Run reaches the browser, and neither fake does.
func startWithFakes(t *testing.T, cfg SessionConfig, navigate func(ctx context.Context, url string) error) *AutomationSession {
	t.Helper()
	s := NewSession(cfg)
	s.launch = func(context.Context) error { return nil }
	s.navigate = navigate
	return s
}

// TestStart_NavigateBoundedByTimeout: a page that never finishes loading must
// fail Start after the session's action timeout instead of blocking it (and
// holding a session slot in "starting") for ever.
func TestStart_NavigateBoundedByTimeout(t *testing.T) {
	s := startWithFakes(t, SessionConfig{ID: "t", URL: "http://never.invalid/", Timeout: 50 * time.Millisecond},
		func(ctx context.Context, _ string) error {
			<-ctx.Done() // the hung page: returns only when cancelled
			return ctx.Err()
		})

	errCh := make(chan error, 1)
	go func() { errCh <- s.Start(context.Background()) }()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("Start succeeded although navigation never completed")
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Start error = %v, want it to wrap context.DeadlineExceeded", err)
		}
		if !strings.Contains(err.Error(), "did not finish loading within 50ms") {
			t.Errorf("Start error = %q, want it to name the timeout", err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("Start still blocked long after its navigation timeout")
	}

	if got := s.State(); got != StateFailed {
		t.Errorf("state = %s, want failed", got)
	}
	select {
	case <-s.Done():
	default:
		t.Error("Done() not closed after a failed start: the manager would never release the slot")
	}
}

// TestStop_DuringStartupNavigationCancelsIt: Stop on a session still in
// Starting must end the startup navigation rather than wait for a page that
// may never load. The navigation timeout is far longer than the test, so only
// Stop's cancel can release it.
func TestStop_DuringStartupNavigationCancelsIt(t *testing.T) {
	navigating := make(chan struct{})
	s := startWithFakes(t, SessionConfig{ID: "t", URL: "http://never.invalid/", Timeout: time.Hour},
		func(ctx context.Context, _ string) error {
			close(navigating)
			<-ctx.Done()
			return ctx.Err()
		})

	errCh := make(chan error, 1)
	go func() { errCh <- s.Start(context.Background()) }()
	<-navigating

	if got := s.State(); got != StateStarting {
		t.Fatalf("state during navigation = %s, want starting", got)
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := s.Stop(stopCtx); err != nil {
		t.Fatalf("Stop during startup = %v, want nil (it must not wait out the navigation)", err)
	}

	select {
	case err := <-errCh:
		if err == nil || !strings.Contains(err.Error(), "stopped during startup") {
			t.Errorf("Start error = %v, want \"session stopped during startup\"", err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("Start did not return after Stop")
	}
	if got := s.State(); got != StateStopped {
		t.Errorf("state = %s, want stopped", got)
	}
}

// TestStart_NavigatesWithinTimeout is the control: an ordinary navigation
// reaches Running and leaves no startup cancel behind.
func TestStart_NavigatesWithinTimeout(t *testing.T) {
	var gotURL string
	s := startWithFakes(t, SessionConfig{ID: "t", URL: "http://ok.invalid/", Timeout: time.Hour},
		func(_ context.Context, url string) error { gotURL = url; return nil })

	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start = %v", err)
	}
	if gotURL != "http://ok.invalid/" {
		t.Errorf("navigated to %q", gotURL)
	}
	if got := s.State(); got != StateRunning {
		t.Errorf("state = %s, want running", got)
	}
	if s.startCancel.Load() != nil {
		t.Error("startCancel still published after startup finished")
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := s.Stop(stopCtx); err != nil {
		t.Fatalf("Stop = %v", err)
	}
}
