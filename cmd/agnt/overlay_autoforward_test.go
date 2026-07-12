package main

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/overlay"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// nullPtmx implements PtyWriter and counts writes. Auto-forward should
// route through the AlertScanner queue, not directly to the PTY.
type nullPtmx struct {
	writes atomic.Int64
}

func (p *nullPtmx) Write(b []byte) (int, error) {
	p.writes.Add(int64(len(b)))
	return len(b), nil
}

func newAutoForwardOverlay(t *testing.T, scanner *overlay.AlertScanner, cascade []string) (*Overlay, *nullPtmx) {
	t.Helper()
	ptmx := &nullPtmx{}
	o := newOverlay("", ptmx)
	o.SetAutoForwardEnabled(true)
	o.SetAlertScanner(scanner, cascade)
	return o, ptmx
}

func newSpyScanner(t *testing.T, batchWindow time.Duration) (*overlay.AlertScanner, *spyAlertSink) {
	t.Helper()
	sink := &spyAlertSink{}
	scanner := overlay.NewAlertScanner(overlay.AlertScannerConfig{
		BatchWindow:  batchWindow,
		DedupeWindow: 5 * time.Second,
		OnAlert:      sink.record,
	})
	t.Cleanup(scanner.Stop)
	return scanner, sink
}

type spyAlertSink struct {
	mu      sync.Mutex
	batches []*overlay.AlertBatch
}

func (s *spyAlertSink) record(b *overlay.AlertBatch) {
	s.mu.Lock()
	s.batches = append(s.batches, b)
	s.mu.Unlock()
}

func (s *spyAlertSink) totalMatches() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, b := range s.batches {
		n += len(b.Matches)
	}
	return n
}

// TestAutoForward_BrowserError_RoutesThroughScanner verifies that browser
// errors flow through Inject (single canonical queue) instead of bypassing
// to the PTY directly — the canonical-queue invariant.
func TestAutoForward_BrowserError_RoutesThroughScanner(t *testing.T) {
	scanner, sink := newSpyScanner(t, 30*time.Millisecond)
	o, ptmx := newAutoForwardOverlay(t, scanner, nil)

	event := makeEvent("browser_error", "dev", map[string]interface{}{
		"message": "TypeError: Cannot read property 'map' of undefined",
		"source":  "App.tsx",
		"lineno":  42,
	})
	o.processAutoForwardEvent(event)

	time.Sleep(120 * time.Millisecond)
	assert.Equal(t, 1, sink.totalMatches(), "browser error must reach scanner")
	assert.Equal(t, int64(0), ptmx.writes.Load(), "must not write directly to PTY (scanner owns delivery)")
}

// TestAutoForward_CascadePatternDropsBeforeScanner verifies that vite/HMR
// cascade noise never reaches the queue — drop happens at the source
// gate, not after dedup.
func TestAutoForward_CascadePatternDropsBeforeScanner(t *testing.T) {
	scanner, sink := newSpyScanner(t, 30*time.Millisecond)
	o, _ := newAutoForwardOverlay(t, scanner, nil) // nil → DefaultJSCascadePatterns

	cascadeMessages := []string{
		"Unhandled Promise Rejection: Error: send was called before connect",
		"WebSocket connection lost",
		"Failed to fetch dynamically imported module",
		"net::ERR_CONNECTION_REFUSED",
		"[HMR] Waiting for update signal from WDS...",
	}
	for _, msg := range cascadeMessages {
		o.processAutoForwardEvent(makeEvent("browser_error", "dev", map[string]interface{}{
			"message": msg,
		}))
	}

	time.Sleep(80 * time.Millisecond)
	assert.Equal(t, 0, sink.totalMatches(), "cascade messages must drop, not enqueue")
}

// TestAutoForward_PerFingerprintDedup verifies that identical messages
// dedup independently (not the old global single-timestamp debounce that
// let any event mask any other for 10s).
func TestAutoForward_PerFingerprintDedup(t *testing.T) {
	scanner, sink := newSpyScanner(t, 30*time.Millisecond)
	o, _ := newAutoForwardOverlay(t, scanner, nil)

	// Three identical errors → dedup to one.
	for i := 0; i < 3; i++ {
		o.processAutoForwardEvent(makeEvent("browser_error", "dev", map[string]interface{}{
			"message": "TypeError: foo is undefined",
		}))
	}
	// Different error fingerprint → must NOT be masked by the dedup of the first.
	o.processAutoForwardEvent(makeEvent("browser_error", "dev", map[string]interface{}{
		"message": "ReferenceError: bar is not defined",
	}))

	time.Sleep(120 * time.Millisecond)

	require.GreaterOrEqual(t, sink.totalMatches(), 2, "different-fingerprint events must not mask each other")
}

// TestAutoForward_AutoForwardDisabledShortCircuits verifies the runtime
// toggle still works and short-circuits before any queue work.
func TestAutoForward_AutoForwardDisabledShortCircuits(t *testing.T) {
	scanner, sink := newSpyScanner(t, 30*time.Millisecond)
	o, _ := newAutoForwardOverlay(t, scanner, nil)
	o.SetAutoForwardEnabled(false)

	o.processAutoForwardEvent(makeEvent("browser_error", "dev", map[string]interface{}{
		"message": "TypeError: anything",
	}))

	time.Sleep(80 * time.Millisecond)
	assert.Equal(t, 0, sink.totalMatches())
}

func TestCanonicalAutoForwardMessage_HTTP(t *testing.T) {
	canon := canonicalAutoForwardMessage(makeEvent("http_error", "dev", map[string]interface{}{
		"method":      "GET",
		"url":         "/api/users",
		"status_code": 500,
	}))
	assert.Equal(t, "GET /api/users -> 500", canon)
}

// TestSetAlertScanner_ConcurrentWithEventProcessing pins the atomic alert
// wiring: SetAlertScanner runs on the pipeline goroutine after Start() has
// the HTTP event server already reading the scanner and cascade patterns.
// Fails under -race if Overlay.alertQueue reverts to plain fields.
func TestSetAlertScanner_ConcurrentWithEventProcessing(t *testing.T) {
	scanner, _ := newSpyScanner(t, 30*time.Millisecond)
	o, _ := newAutoForwardOverlay(t, scanner, nil)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			o.processAutoForwardEvent(makeEvent("browser_error", "dev", map[string]interface{}{
				"message": fmt.Sprintf("TypeError: boom %d", i),
			}))
		}
	}()
	for i := 0; i < 50; i++ {
		o.SetAlertScanner(scanner, []string{"[vite] server connection lost"})
	}
	<-done
}
