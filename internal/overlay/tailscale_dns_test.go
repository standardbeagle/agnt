package overlay

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resetTailscaleCache resets all tailscale DNS cache state for test isolation.
//
// It first DRAINS any refresh goroutine still in flight from a previous test.
// getTailscaleDNS spawns a background goroutine that does a delayed
// Store(tailscaleDNSPtr/Time); if a prior test returned before that goroutine
// finished (some tests only close their unblock channel without waiting), the
// late Store would repopulate the globals we clear here and pollute this test —
// the cause of TestGetTailscaleDNS_EmptyDetectResult flaking under load. Prior
// tests always unblock their detect func in a defer, so the busy flag clears
// promptly and this cannot hang; the bound is a belt-and-braces backstop.
func resetTailscaleCache() {
	for i := 0; i < 2000 && tailscaleDNSBusy.Load(); i++ {
		time.Sleep(time.Millisecond)
	}
	tailscaleDNSPtr.Store(nil)
	tailscaleDNSTime.Store(0)
	tailscaleDNSBusy.Store(false)
}

func TestGetTailscaleDNS_ReturnsEmptyOnFirstCall(t *testing.T) {
	resetTailscaleCache()

	// Detect function blocks until we release it
	blockCh := make(chan struct{})
	tailscaleDetectFunc = func() string {
		<-blockCh
		return "myhost.tail1234.ts.net"
	}
	defer func() { tailscaleDetectFunc = detectTailscaleDNS }()

	// First call should return empty (cache never populated, refresh started async)
	result := getTailscaleDNS()
	assert.Equal(t, "", result)

	// Unblock the detector and wait for refresh to complete
	close(blockCh)
	require.Eventually(t, func() bool {
		return tailscaleDNSPtr.Load() != nil
	}, time.Second, 10*time.Millisecond)

	// Now should return cached value
	result = getTailscaleDNS()
	assert.Equal(t, "myhost.tail1234.ts.net", result)
}

func TestGetTailscaleDNS_ReturnsCachedValueWhileFresh(t *testing.T) {
	resetTailscaleCache()

	var callCount atomic.Int32
	tailscaleDetectFunc = func() string {
		callCount.Add(1)
		return "cached.ts.net"
	}
	defer func() { tailscaleDetectFunc = detectTailscaleDNS }()

	// Trigger initial populate
	getTailscaleDNS()
	require.Eventually(t, func() bool {
		return tailscaleDNSPtr.Load() != nil
	}, time.Second, 10*time.Millisecond)
	initialCalls := callCount.Load()

	// Subsequent calls while cache is fresh should NOT trigger detect
	for i := 0; i < 100; i++ {
		result := getTailscaleDNS()
		assert.Equal(t, "cached.ts.net", result)
	}

	assert.Equal(t, initialCalls, callCount.Load(), "detect should not be called while cache is fresh")
}

func TestGetTailscaleDNS_ConcurrentCallersNeverBlock(t *testing.T) {
	resetTailscaleCache()

	// Detect function takes a long time
	slowCh := make(chan struct{})
	tailscaleDetectFunc = func() string {
		<-slowCh
		return "slow.ts.net"
	}
	defer func() {
		close(slowCh)
		// Wait for any background refresh to finish before resetting
		require.Eventually(t, func() bool {
			return !tailscaleDNSBusy.Load()
		}, time.Second, 10*time.Millisecond)
		tailscaleDetectFunc = detectTailscaleDNS
	}()

	// Launch many concurrent callers — they should all return immediately
	const numCallers = 50
	var wg sync.WaitGroup
	results := make([]string, numCallers)
	durations := make([]time.Duration, numCallers)

	wg.Add(numCallers)
	for i := 0; i < numCallers; i++ {
		go func(idx int) {
			defer wg.Done()
			start := time.Now()
			results[idx] = getTailscaleDNS()
			durations[idx] = time.Since(start)
		}(i)
	}
	wg.Wait()

	// All callers should have returned nearly instantly (not blocked on slow detect)
	for i, d := range durations {
		assert.Less(t, d, 50*time.Millisecond, "caller %d blocked for %v", i, d)
	}

	// All should have returned empty (first call, cache not yet populated)
	for i, r := range results {
		assert.Equal(t, "", r, "caller %d got non-empty result before refresh", i)
	}
}

func TestGetTailscaleDNS_StaleValueServedDuringRefresh(t *testing.T) {
	resetTailscaleCache()

	var callCount atomic.Int32
	refreshStarted := make(chan struct{}, 1)
	refreshBlock := make(chan struct{})

	tailscaleDetectFunc = func() string {
		n := callCount.Add(1)
		if n > 1 {
			// Signal that refresh started, then block
			select {
			case refreshStarted <- struct{}{}:
			default:
			}
			<-refreshBlock
		}
		return "v" + string(rune('0'+n)) + ".ts.net"
	}
	defer func() {
		tailscaleDetectFunc = detectTailscaleDNS
		close(refreshBlock)
	}()

	// Populate cache with first value
	getTailscaleDNS()
	require.Eventually(t, func() bool {
		return tailscaleDNSPtr.Load() != nil
	}, time.Second, 10*time.Millisecond)
	assert.Equal(t, "v1.ts.net", getTailscaleDNS())

	// Expire the cache by backdating the timestamp
	tailscaleDNSTime.Store(time.Now().Add(-10 * time.Minute).UnixNano())

	// This call should trigger a refresh and return the stale value
	result := getTailscaleDNS()

	// Wait for refresh goroutine to actually start
	select {
	case <-refreshStarted:
	case <-time.After(time.Second):
		t.Fatal("refresh goroutine did not start")
	}

	// While refresh is running, we should still get the stale value
	assert.Equal(t, "v1.ts.net", result)
	assert.Equal(t, "v1.ts.net", getTailscaleDNS()) // still stale
}

func TestGetTailscaleDNS_OnlySingleRefreshAtOnce(t *testing.T) {
	resetTailscaleCache()

	var concurrentCount atomic.Int32
	var maxConcurrent atomic.Int32

	refreshBlock := make(chan struct{})
	tailscaleDetectFunc = func() string {
		cur := concurrentCount.Add(1)
		// Track max concurrent
		for {
			old := maxConcurrent.Load()
			if cur <= old || maxConcurrent.CompareAndSwap(old, cur) {
				break
			}
		}
		<-refreshBlock
		concurrentCount.Add(-1)
		return "test.ts.net"
	}
	defer func() {
		close(refreshBlock)
		// Wait for the background goroutine to finish before restoring the
		// detect function and returning — otherwise its delayed write to
		// tailscaleDNSPtr can leak into subsequent tests.
		require.Eventually(t, func() bool {
			return !tailscaleDNSBusy.Load()
		}, time.Second, 10*time.Millisecond)
		tailscaleDetectFunc = detectTailscaleDNS
	}()

	// Call getTailscaleDNS many times — should only spawn one refresh
	for i := 0; i < 20; i++ {
		getTailscaleDNS()
	}

	// Give goroutines time to start
	time.Sleep(50 * time.Millisecond)

	assert.Equal(t, int32(1), maxConcurrent.Load(), "should only have one concurrent refresh")
}

func TestGetTailscaleDNS_EmptyDetectResult(t *testing.T) {
	resetTailscaleCache()

	tailscaleDetectFunc = func() string {
		return ""
	}
	defer func() { tailscaleDetectFunc = detectTailscaleDNS }()

	// Should return empty and cache the empty result
	getTailscaleDNS()
	require.Eventually(t, func() bool {
		return tailscaleDNSTime.Load() > 0
	}, time.Second, 10*time.Millisecond)

	result := getTailscaleDNS()
	assert.Equal(t, "", result)
}
