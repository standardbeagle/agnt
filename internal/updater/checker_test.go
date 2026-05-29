package updater

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeFetcher is a releaseFetcher double for driving checkForUpdates without
// touching the network.
type fakeFetcher struct {
	mu    sync.Mutex
	rel   *GitHubRelease
	err   error
	calls int
}

func (f *fakeFetcher) CheckLatestRelease() (*GitHubRelease, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.rel, f.err
}

func (f *fakeFetcher) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func newTestChecker(current string, f releaseFetcher) *UpdateChecker {
	uc := NewUpdateChecker(Config{CurrentVersion: current, CheckInterval: time.Hour})
	uc.githubChecker = f
	return uc
}

func TestCheckForUpdates_Success(t *testing.T) {
	f := &fakeFetcher{rel: &GitHubRelease{
		TagName: "v9.9.9",
		HTMLURL: "https://github.com/standardbeagle/agnt/releases/tag/v9.9.9",
		Body:    "shiny new release",
	}}
	uc := newTestChecker("0.1.0", f)

	uc.checkForUpdates()
	info := uc.GetUpdateInfo()

	assert.True(t, info.Available, "9.9.9 is newer than 0.1.0")
	assert.Equal(t, "9.9.9", info.LatestVersion)
	assert.Equal(t, "0.1.0", info.CurrentVersion)
	assert.Equal(t, "https://github.com/standardbeagle/agnt/releases/tag/v9.9.9", info.ReleaseURL)
	assert.Equal(t, "shiny new release", info.ReleaseNotes)
	assert.False(t, info.LastChecked.IsZero(), "LastChecked stamped")
	assert.Empty(t, info.CheckError)
	assert.Equal(t, 1, f.callCount())
}

func TestCheckForUpdates_NotNewer(t *testing.T) {
	f := &fakeFetcher{rel: &GitHubRelease{TagName: "v0.0.1"}}
	uc := newTestChecker("1.0.0", f)

	uc.checkForUpdates()
	info := uc.GetUpdateInfo()

	assert.False(t, info.Available, "0.0.1 is older than 1.0.0")
	assert.Equal(t, "0.0.1", info.LatestVersion)
	assert.False(t, info.LastChecked.IsZero())
	assert.Empty(t, info.CheckError)
}

func TestCheckForUpdates_FetchErrorPreservesPriorInfo(t *testing.T) {
	f := &fakeFetcher{rel: &GitHubRelease{TagName: "v9.9.9", HTMLURL: "u", Body: "b"}}
	uc := newTestChecker("0.1.0", f)

	// First a successful check populates the info.
	uc.checkForUpdates()
	require.True(t, uc.GetUpdateInfo().Available)

	// Now the fetch fails: CheckError + LastChecked update, prior fields intact.
	f.mu.Lock()
	f.err = errors.New("network down")
	f.mu.Unlock()
	before := uc.GetUpdateInfo().LastChecked

	uc.checkForUpdates()
	info := uc.GetUpdateInfo()

	assert.Equal(t, "network down", info.CheckError, "error recorded")
	assert.True(t, info.Available, "prior Available not corrupted on error")
	assert.Equal(t, "9.9.9", info.LatestVersion, "prior LatestVersion not corrupted")
	assert.False(t, info.LastChecked.Before(before), "LastChecked advanced (or equal)")
}

func TestCheckForUpdates_IsNewerParseError(t *testing.T) {
	// A release tag that parseVersion cannot parse forces IsNewer to error.
	f := &fakeFetcher{rel: &GitHubRelease{TagName: "vnot-a-version"}}
	uc := newTestChecker("1.0.0", f)

	uc.checkForUpdates()
	info := uc.GetUpdateInfo()

	assert.NotEmpty(t, info.CheckError, "parse error recorded as CheckError")
	assert.False(t, info.LastChecked.IsZero(), "LastChecked stamped even on parse error")
	assert.False(t, info.Available)
}

func TestGetUpdateInfo_ConcurrentWithCheck(t *testing.T) {
	f := &fakeFetcher{rel: &GitHubRelease{TagName: "v2.0.0", HTMLURL: "u", Body: "b"}}
	uc := newTestChecker("1.0.0", f)

	var wg sync.WaitGroup
	wg.Add(20)
	for i := 0; i < 10; i++ {
		go func() { defer wg.Done(); uc.checkForUpdates() }()
		go func() { defer wg.Done(); _ = uc.GetUpdateInfo() }()
	}
	wg.Wait()

	info := uc.GetUpdateInfo()
	assert.True(t, info.Available)
	assert.Equal(t, "2.0.0", info.LatestVersion)
	assert.GreaterOrEqual(t, f.callCount(), 10)
}

func TestGetUpdateInfo_ZeroBeforeFirstCheck(t *testing.T) {
	uc := NewUpdateChecker(Config{CurrentVersion: "1.0.0"})
	info := uc.GetUpdateInfo()

	assert.True(t, info.LastChecked.IsZero(), "never checked -> zero LastChecked")
	assert.False(t, info.Available)
	assert.Equal(t, "1.0.0", info.CurrentVersion)
	assert.Empty(t, info.CheckError)
}

func TestNewUpdateChecker_DefaultsZeroInterval(t *testing.T) {
	uc := NewUpdateChecker(Config{CurrentVersion: "1.0.0", CheckInterval: 0})
	assert.Equal(t, 24*time.Hour, uc.checkInterval, "zero interval defaults to 24h")

	uc2 := NewUpdateChecker(Config{CurrentVersion: "1.0.0", CheckInterval: 5 * time.Minute})
	assert.Equal(t, 5*time.Minute, uc2.checkInterval, "explicit interval preserved")

	cfg := DefaultConfig()
	assert.Equal(t, 24*time.Hour, cfg.CheckInterval)
	assert.True(t, cfg.Enabled)
	assert.Equal(t, DefaultGitHubRepo, cfg.GitHubRepo)
}

func TestCheckNow_TriggersFetch(t *testing.T) {
	f := &fakeFetcher{rel: &GitHubRelease{TagName: "v3.0.0"}}
	uc := newTestChecker("1.0.0", f)

	uc.CheckNow()
	assert.Equal(t, 1, f.callCount())
	assert.True(t, uc.GetUpdateInfo().Available)
}

func TestStartTriggersImmediateCheck_StopIsClean(t *testing.T) {
	f := &fakeFetcher{rel: &GitHubRelease{TagName: "v4.0.0"}}
	uc := newTestChecker("1.0.0", f)

	uc.Start()
	// Start's checkLoop performs an immediate check; wait for it without a sleep.
	require.Eventually(t, func() bool {
		return !uc.GetUpdateInfo().LastChecked.IsZero()
	}, time.Second, 5*time.Millisecond, "Start should trigger an immediate check")
	assert.True(t, uc.GetUpdateInfo().Available)

	// Stop returns within the deadline (loop is idle on the ticker).
	done := make(chan struct{})
	go func() { uc.Stop(context.Background()); close(done) }()
	require.Eventually(t, func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}, time.Second, 5*time.Millisecond, "Stop should return promptly")
}

func TestStop_AlreadyCancelledContextReturns(t *testing.T) {
	uc := NewUpdateChecker(Config{CurrentVersion: "1.0.0"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	done := make(chan struct{})
	go func() { uc.Stop(ctx); close(done) }()
	require.Eventually(t, func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}, time.Second, 5*time.Millisecond, "Stop with cancelled ctx returns without hanging")
}
