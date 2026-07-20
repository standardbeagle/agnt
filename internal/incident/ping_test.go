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

	var mu sync.Mutex
	pings := []capturedPing{}

	cfg := testPingConfig()
	pe := NewPingEmitter(inbox, cfg, flow,
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

func TestPing_DuplicateStormCriticalSnapshotConcurrentQuery(t *testing.T) {
	inbox := NewInbox("storm-session")
	flow := NewFlowController(DefaultBucketConfigs)
	pings := make(chan capturedPing, 32)
	pe := NewPingEmitter(inbox, testPingConfig(), flow,
		func(level string, payload PingPayload) error {
			select {
			case pings <- capturedPing{level: level, payload: payload}:
			default:
			}
			return nil
		}, nil, nil)
	defer pe.Stop()
	deltas, cancelDeltas := inbox.Subscribe()
	defer cancelDeltas()

	const fingerprint = "duplicate-storm"
	inbox.Ingest(makeEntry(fingerprint, SeverityWarning))
	<-deltas
	inbox.Ingest(makeEntry(fingerprint, SeverityCritical))
	criticalDelta := <-deltas

	const duplicates = 256
	start := make(chan struct{})
	queryDone := make(chan struct{})
	var writers sync.WaitGroup
	for i := 0; i < duplicates; i++ {
		writers.Add(1)
		go func(i int) {
			defer writers.Done()
			<-start
			severity := SeverityError
			if i%3 == 0 {
				severity = SeverityCritical
			}
			inbox.Ingest(makeEntry(fingerprint, severity))
		}(i)
	}
	for i := 0; i < duplicates; i++ {
		writers.Add(1)
		go func() {
			defer writers.Done()
			<-start
			inbox.MarkRead([]string{fingerprint}, false)
		}()
	}
	go func() {
		defer close(queryDone)
		<-start
		for {
			entries, stats := inbox.Query(QueryFilter{})
			expectedCritical := 1
			if len(entries) == 1 && entries[0].Read {
				expectedCritical = 0
			}
			if len(entries) != 1 || entries[0].Severity != SeverityCritical ||
				stats.Critical != expectedCritical || stats.Error != 0 || stats.Warning != 0 {
				t.Errorf("non-atomic critical routing: entries=%#v stats=%+v", entries, stats)
				return
			}
			select {
			case <-queryDone:
				return
			default:
			}
			if entries[0].Count == duplicates+2 {
				return
			}
		}
	}()

	close(start)
	writers.Wait()
	<-queryDone

	entries, stats := inbox.Query(QueryFilter{})
	if len(entries) != 1 {
		t.Fatalf("entries: got %d, want 1", len(entries))
	}
	if entries[0].Severity != SeverityCritical || entries[0].Count != duplicates+2 {
		t.Fatalf("final entry: severity=%s count=%d, want critical/%d", entries[0].Severity, entries[0].Count, duplicates+2)
	}
	expectedCritical := 1
	if entries[0].Read {
		expectedCritical = 0
	}
	if stats.Critical != expectedCritical || stats.Error != 0 || stats.Warning != 0 {
		t.Fatalf("final routing stats: %+v", stats)
	}
	if criticalDelta.Entry.Count != 2 || criticalDelta.Entry.Severity != SeverityCritical || criticalDelta.Entry.Read {
		t.Fatalf("retained delta mutated: %+v", criticalDelta.Entry)
	}

	deadline := time.After(time.Second)
	for {
		select {
		case ping := <-pings:
			if ping.level == "error" && ping.payload.Summary.Critical == 1 &&
				len(ping.payload.Top) == 1 && ping.payload.Top[0].Severity == SeverityCritical {
				return
			}
		case <-deadline:
			t.Fatal("active pinger never emitted critical routing")
		}
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

	pe := NewPingEmitter(inbox, cfg, flow,
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

	pe := NewPingEmitter(inbox, cfg, flow,
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

	// Wait for the first ping to actually land, then let the burst settle. Sizing
	// a sleep to the coalescer's Max delay races the AfterFunc it waits for: on a
	// loaded machine the timer goroutine has not run yet and no ping exists.
	deadline := time.Now().Add(10 * time.Second)
	for {
		mu.Lock()
		emitted := len(pings)
		mu.Unlock()
		if emitted > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no pings emitted from burst")
		}
		time.Sleep(time.Millisecond)
	}
	// Give any further coalesced pings their chance before counting.
	time.Sleep(4 * cfg.Delays.Max)

	mu.Lock()
	n := len(pings)
	var totalCount int
	for _, p := range pings {
		totalCount += p.Summary.Error
	}
	mu.Unlock()

	if n > 5 {
		t.Errorf("burst of 100 same-fp should produce ≤5 pings, got %d", n)
	}
}

// ── escalation with an empty coalescer ──────────────────────────────────────────

// TestPing_Escalation_EmptyCoalescer_StillEmits pins the fix for the escalated
// branch: it previously only called ForceFlush, which no-ops when no coalesced
// ping is pending. A severity escalation arriving after the coalescer has
// already drained therefore consumed the flow token but emitted nothing —
// silently delayed escalation. The escalation must always produce a ping.
func TestPing_Escalation_EmptyCoalescer_StillEmits(t *testing.T) {
	t.Parallel()
	_, inbox, pings, mu := newTestEmitter(t)

	count := func() int { mu.Lock(); defer mu.Unlock(); return len(*pings) }
	waitFor := func(min int, msg string) {
		deadline := time.After(2 * time.Second)
		for count() < min {
			select {
			case <-deadline:
				t.Fatalf("%s: got %d pings, want >= %d", msg, count(), min)
			case <-time.After(2 * time.Millisecond):
			}
		}
	}

	// First occurrence at warning schedules a coalesced ping; wait for it to
	// fire, which drains the coalescer slot (leaving the coalescer EMPTY).
	inbox.Ingest(makeEntry("fp-esc", SeverityWarning))
	waitFor(1, "initial warning ping")
	base := count()

	// Escalate the same fingerprint warning→error with the coalescer now empty.
	// The old ForceFlush-only path emitted nothing here; a ping must still fire.
	inbox.Ingest(makeEntry("fp-esc", SeverityError))
	waitFor(base+1, "escalation ping with empty coalescer")

	// The escalation ping must reflect the entry now in the error band.
	mu.Lock()
	last := (*pings)[len(*pings)-1]
	mu.Unlock()
	if last.payload.Summary.Error < 1 {
		t.Errorf("escalation ping should report the error entry, got error=%d", last.payload.Summary.Error)
	}
}
