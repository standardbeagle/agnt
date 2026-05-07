package daemon

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/proxy"
)

type emitRecord struct {
	mu      sync.Mutex
	entries []proxy.LogEntry
	proxies []string
	counts  []int
}

func (r *emitRecord) record(entry proxy.LogEntry, proxyID string, count int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, entry)
	r.proxies = append(r.proxies, proxyID)
	r.counts = append(r.counts, count)
}

func (r *emitRecord) snapshot() ([]proxy.LogEntry, []string, []int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := make([]proxy.LogEntry, len(r.entries))
	copy(e, r.entries)
	p := make([]string, len(r.proxies))
	copy(p, r.proxies)
	c := make([]int, len(r.counts))
	copy(c, r.counts)
	return e, p, c
}

func makeTransportEntry(event, msg string) proxy.LogEntry {
	return proxy.LogEntry{
		Type: proxy.LogTypeDiagnostic,
		Diagnostic: &proxy.ProxyDiagnostic{
			Level:    proxy.DiagnosticError,
			Category: "transport",
			Event:    event,
			Message:  msg,
		},
	}
}

func makeBrowserError(message string) proxy.LogEntry {
	return proxy.LogEntry{
		Type: proxy.LogTypeError,
		Error: &proxy.FrontendError{
			Message: message,
		},
	}
}

func TestHoldBuffer_RecoveryCancelsTransportErr(t *testing.T) {
	rec := &emitRecord{}
	cfg := &config.OutageHoldConfig{WindowMs: 5000}
	b := NewHoldBuffer(cfg, rec.record)
	defer b.Stop()

	b.Hold(makeTransportEntry("refused", "ECONNREFUSED"), "p1", "diag:transport:refused", true)
	require.Eventually(t, func() bool { return b.pendingCount() == 1 }, time.Second, 5*time.Millisecond)

	b.OnRecovery("p1")
	// Recovery drops cascade entries — emit must remain empty.
	time.Sleep(50 * time.Millisecond)

	entries, _, _ := rec.snapshot()
	assert.Empty(t, entries, "cascade entry must be dropped on recovery, not emitted")
	assert.Equal(t, 0, b.pendingCount())
}

func TestHoldBuffer_TimerExpiryEmitsOnce(t *testing.T) {
	rec := &emitRecord{}
	cfg := &config.OutageHoldConfig{WindowMs: 50}
	b := NewHoldBuffer(cfg, rec.record)
	defer b.Stop()

	b.Hold(makeTransportEntry("refused", "ECONNREFUSED"), "p1", "fp1", true)

	require.Eventually(t, func() bool {
		entries, _, _ := rec.snapshot()
		return len(entries) == 1
	}, time.Second, 10*time.Millisecond, "timer expiry must emit held entry")

	_, proxies, counts := rec.snapshot()
	assert.Equal(t, []string{"p1"}, proxies)
	assert.Equal(t, []int{1}, counts)
}

func TestHoldBuffer_NonCascadeJSEmittedOnRecovery(t *testing.T) {
	rec := &emitRecord{}
	cfg := &config.OutageHoldConfig{WindowMs: 5000}
	b := NewHoldBuffer(cfg, rec.record)
	defer b.Stop()

	// Genuine app error that happened during outage — not cascade.
	b.Hold(makeBrowserError("TypeError: Cannot read property 'foo' of undefined"), "p1", "err:typeerror", false)
	require.Eventually(t, func() bool { return b.pendingCount() == 1 }, time.Second, 5*time.Millisecond)

	b.OnRecovery("p1")

	require.Eventually(t, func() bool {
		entries, _, _ := rec.snapshot()
		return len(entries) == 1
	}, time.Second, 5*time.Millisecond, "non-cascade entries must emit on recovery")

	entries, _, _ := rec.snapshot()
	require.Len(t, entries, 1)
	assert.Equal(t, proxy.LogTypeError, entries[0].Type)
}

func TestHoldBuffer_DuplicateFingerprintCoalesces(t *testing.T) {
	rec := &emitRecord{}
	cfg := &config.OutageHoldConfig{WindowMs: 50}
	b := NewHoldBuffer(cfg, rec.record)
	defer b.Stop()

	for i := 0; i < 5; i++ {
		b.Hold(makeTransportEntry("refused", "ECONNREFUSED"), "p1", "fp-shared", true)
	}

	require.Eventually(t, func() bool {
		entries, _, _ := rec.snapshot()
		return len(entries) == 1
	}, time.Second, 10*time.Millisecond, "5 same-fp pushes must coalesce to 1 emit")

	_, _, counts := rec.snapshot()
	assert.Equal(t, []int{5}, counts, "merged count must reflect 5 occurrences")
}

func TestHoldBuffer_MultipleProxiesIndependent(t *testing.T) {
	rec := &emitRecord{}
	cfg := &config.OutageHoldConfig{WindowMs: 5000}
	b := NewHoldBuffer(cfg, rec.record)
	defer b.Stop()

	b.Hold(makeTransportEntry("refused", "p1 err"), "p1", "fp1", true)
	b.Hold(makeTransportEntry("refused", "p2 err"), "p2", "fp1", true)
	require.Eventually(t, func() bool { return b.pendingCount() == 2 }, time.Second, 5*time.Millisecond)

	b.OnRecovery("p1")
	time.Sleep(20 * time.Millisecond)

	assert.Equal(t, 1, b.pendingCount(), "p2 entry must remain held after p1 recovery")
}

func TestHoldBuffer_MatchesJSCascade(t *testing.T) {
	cfg := &config.OutageHoldConfig{
		JSCascadePatterns: []string{"Failed to fetch", "WebSocket"},
	}
	b := NewHoldBuffer(cfg, func(proxy.LogEntry, string, int) {})
	defer b.Stop()

	cases := []struct {
		msg  string
		want bool
	}{
		{"TypeError: Failed to fetch at line 42", true},
		{"WebSocket connection closed", true},
		{"WEBSOCKET error", true}, // case-insensitive
		{"ReferenceError: foo is not defined", false},
		{"NetworkError: dns failed", false}, // not in this test's pattern set
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, b.MatchesJSCascade(tc.msg), "msg=%q", tc.msg)
	}
}

func TestHoldBuffer_DefaultCascadePatterns(t *testing.T) {
	b := NewHoldBuffer(nil, func(proxy.LogEntry, string, int) {})
	defer b.Stop()

	// Spot-check the default set
	assert.True(t, b.MatchesJSCascade("Failed to fetch"))
	assert.True(t, b.MatchesJSCascade("net::ERR_CONNECTION_REFUSED"))
	assert.True(t, b.MatchesJSCascade("WebSocket disconnected"))
	assert.False(t, b.MatchesJSCascade("Cannot read property of undefined"))

	// Vite HMR client reconnect noise — the canonical real-world spam case
	assert.True(t, b.MatchesJSCascade("Unhandled Promise Rejection: Error: send was called before connect"))
	assert.True(t, b.MatchesJSCascade("at send@http://localhost:31710/@vite/client:384:15"))
	assert.True(t, b.MatchesJSCascade("ViteHotContext lost connection"))
	assert.True(t, b.MatchesJSCascade("failed to connect to websocket"))

	// Webpack / generic HMR
	assert.True(t, b.MatchesJSCascade("[HMR] Waiting for update signal from WDS..."))
	assert.True(t, b.MatchesJSCascade("Disconnected. Attempting to reconnect..."))
}

func TestHoldBuffer_ForgetDropsAll(t *testing.T) {
	rec := &emitRecord{}
	cfg := &config.OutageHoldConfig{WindowMs: 5000}
	b := NewHoldBuffer(cfg, rec.record)
	defer b.Stop()

	b.Hold(makeTransportEntry("refused", "x"), "p1", "fp1", true)
	b.Hold(makeBrowserError("y"), "p1", "fp2", false)
	require.Eventually(t, func() bool { return b.pendingCount() == 2 }, time.Second, 5*time.Millisecond)

	b.Forget("p1")
	time.Sleep(20 * time.Millisecond)

	assert.Equal(t, 0, b.pendingCount())
	entries, _, _ := rec.snapshot()
	assert.Empty(t, entries, "Forget must not emit")
}

func TestHoldBuffer_StopIsIdempotent(t *testing.T) {
	b := NewHoldBuffer(nil, func(proxy.LogEntry, string, int) {})
	b.Stop()
	b.Stop() // must not panic
}

func TestFingerprintForEntry_Stability(t *testing.T) {
	a := makeTransportEntry("refused", "msg A")
	b := makeTransportEntry("refused", "msg B") // different msg, same event
	assert.Equal(t, FingerprintForEntry(a), FingerprintForEntry(b),
		"diagnostic fingerprint should depend on category+event, not message")

	c := makeBrowserError("TypeError: x")
	d := makeBrowserError("TypeError: x")
	assert.Equal(t, FingerprintForEntry(c), FingerprintForEntry(d))

	e := makeBrowserError("TypeError: x")
	f := makeBrowserError("ReferenceError: y")
	assert.NotEqual(t, FingerprintForEntry(e), FingerprintForEntry(f))
}

func TestHoldBuffer_ConcurrentHolds(t *testing.T) {
	rec := &emitRecord{}
	cfg := &config.OutageHoldConfig{WindowMs: 100}
	b := NewHoldBuffer(cfg, rec.record)
	defer b.Stop()

	var wg sync.WaitGroup
	const goroutines = 10
	const perG = 20
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				b.Hold(makeTransportEntry("refused", "x"), "p1", "fp-shared", true)
			}
			_ = gid
		}(g)
	}
	wg.Wait()

	require.Eventually(t, func() bool {
		entries, _, _ := rec.snapshot()
		return len(entries) == 1
	}, time.Second, 10*time.Millisecond, "concurrent same-fp holds must coalesce to one emit")

	_, _, counts := rec.snapshot()
	if assert.Len(t, counts, 1) {
		assert.Equal(t, goroutines*perG, counts[0])
	}
}

// Belt and braces: ensure unused-import guard does not pollute coverage.
var _ atomic.Int64
