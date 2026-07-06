package incident

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// testPingConfig returns a config suitable for tests: very short delays,
// all channels enabled, summary included.
func testPingConfig() PingConfig {
	return PingConfig{
		MCPNotifications: true,
		ChannelEnabled:   true,
		PTYInjection:     true,
		MaxTopFPs:        5,
		IncludeSummary:   true,
		Delays: PingDelays{
			Initial:    10 * time.Millisecond,
			Max:        50 * time.Millisecond,
			ResetAfter: 100 * time.Millisecond,
		},
	}
}

// testPingSetup builds a PingEmitter with capture functions. Returns the
// emitter, the inbox, and channels/slices to observe emitted pings.
type capturedPing struct {
	level   string
	payload PingPayload
}

func newTestEmitter(t *testing.T) (*PingEmitter, *Inbox, *[]capturedPing, *sync.Mutex) {
	t.Helper()
	inbox := NewInbox("test-sess")
	flow := NewFlowController(DefaultBucketConfigs)
	activity := NewActivityDetector(20*time.Millisecond, 50*time.Millisecond, nil)

	var mu sync.Mutex
	pings := []capturedPing{}

	cfg := testPingConfig()
	pe := NewPingEmitter(inbox, cfg, flow, activity,
		func(level string, p PingPayload) error {
			mu.Lock()
			pings = append(pings, capturedPing{level, p})
			mu.Unlock()
			return nil
		},
		nil, nil,
	)
	t.Cleanup(pe.Stop)
	return pe, inbox, &pings, &mu
}

// ── payload size ──────────────────────────────────────────────────────────────

func TestPing_PayloadUnder2KB(t *testing.T) {
	t.Parallel()
	inbox := NewInbox("sess")
	cfg := PingConfig{IncludeSummary: true, MaxTopFPs: 5}
	flow := NewFlowController(DefaultBucketConfigs)

	// Insert 20 distinct fingerprints with long summaries.
	for i := 0; i < 20; i++ {
		e := makeEntry("fp-size-"+string(rune('a'+i)), SeverityError)
		ev := NewIncidentEvent(SourceBrowserJS, SeverityError, "TypeError",
			"a very long error message that fills up the summary field "+strings.Repeat("x", 100),
			Context{}, nil)
		e.Sample = &ev
		inbox.Ingest(e)
	}

	entries, stats := inbox.Query(QueryFilter{})
	// Use a dummy emitter just for buildPayload.
	pe := &PingEmitter{inbox: inbox, config: cfg, flow: flow}
	payload := pe.buildPayload(stats, entries)

	size := PingPayloadSize(payload)
	if size > 2048 {
		t.Errorf("payload size %d bytes exceeds 2KB limit", size)
	}
}

// ── MCP notification ──────────────────────────────────────────────────────────

func TestPing_MCP_UsesLogNotification(t *testing.T) {
	t.Parallel()
	_, inbox, pings, mu := newTestEmitter(t)

	inbox.Ingest(makeEntry("fp-mcp", SeverityError))
	deadline := time.After(250 * time.Millisecond)
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()

	for {
		mu.Lock()
		n := len(*pings)
		mu.Unlock()
		if n > 0 {
			break
		}

		select {
		case <-deadline:
			t.Fatal("MCP notify not called")
		case <-tick.C:
		}
	}

	mu.Lock()
	first := (*pings)[0]
	mu.Unlock()
	if first.level != "error" {
		t.Errorf("MCP level: got %q, want error", first.level)
	}
	if first.payload.Type != "agnt.incident_ping" {
		t.Errorf("payload.Type: got %q", first.payload.Type)
	}
	if first.payload.Version != 1 {
		t.Errorf("payload.Version: got %d, want 1", first.payload.Version)
	}
	if first.payload.Session != "test-sess" {
		t.Errorf("payload.Session: got %q", first.payload.Session)
	}
}

// ── PTY line format ───────────────────────────────────────────────────────────

func TestPing_PTY_SingleLineFormat(t *testing.T) {
	t.Parallel()
	p := PingPayload{
		Session: "s",
		Summary: PingStats{Critical: 1, Error: 3, Warning: 12, New: 4},
	}
	line := ptyLine(p)
	if line == "" {
		t.Fatal("ptyLine empty")
	}
	if line[len(line)-1] != '\n' {
		t.Error("ptyLine must end with newline")
	}
	if len(line) > 200 {
		t.Errorf("ptyLine too long: %d chars", len(line))
	}
}

// ── config gates per channel ─────────────────────────────────────────────────

func TestPing_ConfigGates_PerChannel(t *testing.T) {
	t.Parallel()
	inbox := NewInbox("sess")
	flow := NewFlowController(DefaultBucketConfigs)

	var mcpCalled, channelCalled, ptyCalled atomic.Bool

	cfg := testPingConfig()
	cfg.MCPNotifications = false
	cfg.ChannelEnabled = false
	cfg.PTYInjection = false

	pe := NewPingEmitter(inbox, cfg, flow, nil,
		func(string, PingPayload) error { mcpCalled.Store(true); return nil },
		func(string, PingPayload) error { channelCalled.Store(true); return nil },
		func(string) error { ptyCalled.Store(true); return nil },
	)
	defer pe.Stop()

	inbox.Ingest(makeEntry("fp-gate", SeverityError))
	time.Sleep(50 * time.Millisecond)

	if mcpCalled.Load() {
		t.Error("MCPNotifications=false: MCP should not be called")
	}
	if channelCalled.Load() {
		t.Error("ChannelEnabled=false: channel should not be called")
	}
	if ptyCalled.Load() {
		t.Error("PTYInjection=false: PTY should not be called")
	}
}

// ── critical bypasses activity deferral ──────────────────────────────────────

func TestPing_CriticalSkipsActivityDeferral(t *testing.T) {
	t.Parallel()
	inbox := NewInbox("sess")
	flow := NewFlowController(DefaultBucketConfigs)
	activity := NewActivityDetector(500*time.Millisecond, 2*time.Second, nil) // long active window

	var count atomic.Int32
	cfg := testPingConfig()
	pe := NewPingEmitter(inbox, cfg, flow, activity,
		func(string, PingPayload) error { count.Add(1); return nil },
		nil, nil,
	)
	defer pe.Stop()

	// Simulate agent being active.
	activity.RecordHook()
	if !activity.IsActive() {
		t.Fatal("activity should be active")
	}

	// Ingest a critical event — must bypass deferral and emit immediately.
	inbox.Ingest(makeEntry("fp-crit", SeverityCritical))
	time.Sleep(30 * time.Millisecond) // much less than active window

	if count.Load() == 0 {
		t.Error("critical event should emit ping even when agent is active")
	}
}

// ── top fingerprints ordering ─────────────────────────────────────────────────

func TestPing_TopFingerprintsOrderedBySeverityThenCount(t *testing.T) {
	t.Parallel()
	inbox := NewInbox("sess")

	// Insert: 1 warning(count=100), 1 error(count=5), 1 critical(count=1)
	warn := makeEntry("fp-w", SeverityWarning)
	warn.Count = 100
	inbox.Ingest(warn)

	err := makeEntry("fp-e", SeverityError)
	err.Count = 5
	inbox.Ingest(err)

	crit := makeEntry("fp-c", SeverityCritical)
	crit.Count = 1
	inbox.Ingest(crit)

	flow := NewFlowController(DefaultBucketConfigs)
	pe := &PingEmitter{inbox: inbox, config: PingConfig{MaxTopFPs: 3}, flow: flow}
	entries, stats := inbox.Query(QueryFilter{})
	payload := pe.buildPayload(stats, entries)

	if len(payload.Top) != 3 {
		t.Fatalf("Top len: got %d, want 3", len(payload.Top))
	}
	// Must be sorted: critical first, then error, then warning.
	if payload.Top[0].Severity != SeverityCritical {
		t.Errorf("Top[0]: got %q, want critical", payload.Top[0].Severity)
	}
	if payload.Top[1].Severity != SeverityError {
		t.Errorf("Top[1]: got %q, want error", payload.Top[1].Severity)
	}
	if payload.Top[2].Severity != SeverityWarning {
		t.Errorf("Top[2]: got %q, want warning", payload.Top[2].Severity)
	}
}

// ── end-to-end: burst → 1 ping ────────────────────────────────────────────────

func TestPing_EndToEnd_BurstIngestOnePing(t *testing.T) {
	t.Parallel()
	inbox := NewInbox("sess")
	flow := NewFlowController(DefaultBucketConfigs)

	var mu sync.Mutex
	var pings []PingPayload
	cfg := testPingConfig()
	cfg.Delays = PingDelays{
		Initial:    30 * time.Millisecond,
		Max:        100 * time.Millisecond,
		ResetAfter: 200 * time.Millisecond,
	}

	pe := NewPingEmitter(inbox, cfg, flow, nil,
		func(_ string, p PingPayload) error {
			mu.Lock()
			pings = append(pings, p)
			mu.Unlock()
			return nil
		},
		nil, nil,
	)
	defer pe.Stop()

	// Fire 100 events with the same fingerprint in rapid succession.
	for i := 0; i < 100; i++ {
		inbox.Ingest(makeEntry("fp-burst", SeverityError))
	}

	// Wait well past Max delay.
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	n := len(pings)
	var totalCount int
	for _, p := range pings {
		totalCount += p.Summary.Error
	}
	mu.Unlock()

	if n == 0 {
		t.Fatal("no pings emitted from burst")
	}
	if n > 5 {
		t.Errorf("burst of 100 same-fp should produce ≤5 pings, got %d", n)
	}
}
