package browser

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// fakeBrowserEnv makes this test binary stand in for a browser: re-executed
// with it set, the process ignores its arguments and stays alive until killed.
// That gives the lifecycle tests a real OS process to start and stop without
// needing Chrome. Tests here start real processes, so none use t.Parallel().
const fakeBrowserEnv = "AGNT_TEST_FAKE_BROWSER"

func TestMain(m *testing.M) {
	// Checked before the testing package parses flags: the fake is launched
	// with Chrome's command line, which is not a set of test flags.
	if os.Getenv(fakeBrowserEnv) == "1" {
		time.Sleep(10 * time.Minute) // far beyond any test; killed long before
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// liveness ceiling for "eventually happens" waits; generous on purpose.
const lifecycleCeiling = 60 * time.Second

func fakeBrowser(t *testing.T, cfg Config) *Browser {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	t.Setenv(fakeBrowserEnv, "1")
	cfg.BinaryPath = exe
	if cfg.ID == "" {
		cfg.ID = "fake"
	}
	return New(cfg)
}

func waitDone(t *testing.T, b *Browser) {
	t.Helper()
	select {
	case <-b.Done():
	case <-time.After(lifecycleCeiling):
		t.Fatal("browser Done() never closed")
	}
}

func stop(t *testing.T, b *Browser) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), lifecycleCeiling)
	defer cancel()
	return b.Stop(ctx)
}

func argValue(args []string, prefix string) (string, int) {
	val, n := "", 0
	for _, a := range args {
		if strings.HasPrefix(a, prefix) {
			val = strings.TrimPrefix(a, prefix)
			n++
		}
	}
	return val, n
}

// TestStart_UsesItsOwnTemporaryProfile: every launch gets a fresh profile
// directory, passed to the browser as --user-data-dir, and removes it again
// when the process exits.
func TestStart_UsesItsOwnTemporaryProfile(t *testing.T) {
	b := fakeBrowser(t, Config{URL: "http://localhost:3000"})
	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	dir, n := argValue(b.cmd.Args, "--user-data-dir=")
	if n != 1 || dir == "" {
		_ = stop(t, b)
		t.Fatalf("launch must carry exactly one --user-data-dir (got %d: %q) in %v", n, dir, b.cmd.Args)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		_ = stop(t, b)
		t.Fatalf("profile directory %s does not exist while the browser runs: %v", dir, err)
	}
	if last := b.cmd.Args[len(b.cmd.Args)-1]; last != "http://localhost:3000" {
		t.Errorf("URL must stay the last argument, got %q", last)
	}

	if err := stop(t, b); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("temporary profile %s still exists after stop (stat err: %v)", dir, err)
	}
	if err := b.Error(); err != nil {
		t.Errorf("clean stop left an error: %v", err)
	}
}

// TestStart_TwoLaunchesGetDistinctProfiles: the point of the profile is that
// no two browsers — and no user browser — ever share one.
func TestStart_TwoLaunchesGetDistinctProfiles(t *testing.T) {
	a := fakeBrowser(t, Config{ID: "a"})
	b := fakeBrowser(t, Config{ID: "b"})
	for _, br := range []*Browser{a, b} {
		if err := br.Start(context.Background()); err != nil {
			t.Fatalf("Start %s: %v", br.ID(), err)
		}
	}
	dirA, _ := argValue(a.cmd.Args, "--user-data-dir=")
	dirB, _ := argValue(b.cmd.Args, "--user-data-dir=")
	if dirA == "" || dirA == dirB {
		t.Errorf("profiles must be distinct and non-empty: %q vs %q", dirA, dirB)
	}
	for _, br := range []*Browser{a, b} {
		if err := stop(t, br); err != nil {
			t.Errorf("Stop %s: %v", br.ID(), err)
		}
	}
}

// TestStart_CallerSuppliedProfileIsUsedAndKept: a caller that names a profile
// owns it; it is passed through and never deleted.
func TestStart_CallerSuppliedProfileIsUsedAndKept(t *testing.T) {
	own := t.TempDir()
	b := fakeBrowser(t, Config{UserDataDir: own})
	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if dir, n := argValue(b.cmd.Args, "--user-data-dir="); n != 1 || dir != own {
		t.Errorf("--user-data-dir = %q (count %d), want %q", dir, n, own)
	}
	if err := stop(t, b); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if _, err := os.Stat(own); err != nil {
		t.Errorf("caller-supplied profile was removed: %v", err)
	}
}

// TestStart_FailedSpawnLeavesNoProfileBehind: a launch that never produced a
// process must not leak the directory it created for it.
func TestStart_FailedSpawnLeavesNoProfileBehind(t *testing.T) {
	b := New(Config{ID: "x", BinaryPath: "/nonexistent/chrome"})
	if err := b.Start(context.Background()); err == nil {
		t.Fatal("Start with a missing binary succeeded")
	}
	if b.profileDir == "" {
		t.Fatal("test precondition: Start should have created a profile before spawning")
	}
	if _, err := os.Stat(b.profileDir); !os.IsNotExist(err) {
		t.Errorf("profile %s leaked after a failed spawn (stat err: %v)", b.profileDir, err)
	}
}

// TestStop_DuringStartupIsHonoured lands a Stop in the window between the
// process being spawned and Running being published. Start used to publish
// Running with an unconditional store, overwriting Stop's claim: Stop then
// waited on a browser that nobody was going to stop, and returned with the
// process still alive in state "running".
func TestStop_DuringStartupIsHonoured(t *testing.T) {
	b := fakeBrowser(t, Config{})

	stopErr := make(chan error, 1)
	b.afterSpawn = func() {
		go func() { stopErr <- stop(t, b) }()
		// Hold Start in the window until Stop has made its claim.
		deadline := time.Now().Add(lifecycleCeiling)
		for b.State() != StateStopping {
			if time.Now().After(deadline) {
				t.Error("Stop never claimed Starting→Stopping")
				return
			}
			time.Sleep(time.Millisecond)
		}
	}

	err := b.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "stopped during startup") {
		t.Fatalf("Start = %v, want \"browser stopped during startup\"", err)
	}

	select {
	case err := <-stopErr:
		if err != nil {
			t.Errorf("Stop = %v, want nil", err)
		}
	case <-time.After(lifecycleCeiling):
		t.Fatal("Stop never returned")
	}

	waitDone(t, b)
	if got := b.State(); got != StateStopped {
		t.Errorf("state = %s, want stopped", got)
	}
	// The process is really gone: Start reaped it on the stopped path.
	if b.cmd.ProcessState == nil {
		t.Error("process was not reaped; it would still be running")
	}
	if _, err := os.Stat(b.profileDir); !os.IsNotExist(err) {
		t.Errorf("profile %s leaked on the stopped-during-startup path (stat err: %v)", b.profileDir, err)
	}
}

// TestStop_RunningBrowserReportsNoSpuriousKillError: stopping is cancelling.
// Stop used to follow the cancel with its own Kill, which raced the context's
// kill and the reaper and could report "failed to kill browser" for a browser
// that had stopped correctly.
func TestStop_RunningBrowserReportsNoSpuriousKillError(t *testing.T) {
	for i := 0; i < 5; i++ {
		b := fakeBrowser(t, Config{})
		if err := b.Start(context.Background()); err != nil {
			t.Fatalf("Start: %v", err)
		}
		if got := b.State(); got != StateRunning {
			t.Fatalf("state after Start = %s, want running", got)
		}
		if err := stop(t, b); err != nil {
			t.Fatalf("Stop #%d = %v, want nil", i, err)
		}
		if got := b.State(); got != StateStopped {
			t.Errorf("state after Stop = %s, want stopped", got)
		}
		if b.cmd.ProcessState == nil {
			t.Error("process not reaped after Stop")
		}
	}
}
