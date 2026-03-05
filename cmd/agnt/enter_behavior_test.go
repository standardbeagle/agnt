package main

import (
	"bytes"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/overlay"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingWriter is a thread-safe writer that records every Write call
// with timestamps, so we can analyze exactly what was written and when.
type recordingWriter struct {
	mu      sync.Mutex
	writes  []writeRecord
	allData bytes.Buffer
}

type writeRecord struct {
	data []byte
	at   time.Time
}

func (w *recordingWriter) Write(p []byte) (n int, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	copied := make([]byte, len(p))
	copy(copied, p)
	w.writes = append(w.writes, writeRecord{data: copied, at: time.Now()})
	w.allData.Write(copied)
	return len(p), nil
}

func (w *recordingWriter) allBytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.allData.Bytes()...)
}

func (w *recordingWriter) getWrites() []writeRecord {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]writeRecord, len(w.writes))
	copy(out, w.writes)
	return out
}

// countEnters counts bare \r (CR) bytes that represent Enter keypresses.
// Uses \r as the Enter byte because Ink (Node.js) in raw mode expects
// exactly "\r" for Return — "\r\n" is a 2-byte string that doesn't match.
func (w *recordingWriter) countEnters() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return strings.Count(w.allData.String(), "\r")
}

// makeTestOverlay creates an Overlay with a recording writer for testing.
func makeTestOverlay(w *recordingWriter) *Overlay {
	return &Overlay{
		ptmx:       w,
		activityCh: make(chan struct{}, 1),
	}
}

// --- Tests investigating the Enter byte sequence ---

func TestEnterBehavior_ByteSequence(t *testing.T) {
	// FINDING: Enter must be just \r (CR, 0x0D) — NOT \r\n.
	// Ink (Node.js) in raw mode checks `input === '\r'` for the Return key.
	// Through a PTY in raw mode, bytes pass through unchanged, so "\r\n"
	// arrives as the 2-byte string "\r\n" which does NOT match "\r".
	// This was the root cause of Enter not triggering in Claude Code.
	w := &recordingWriter{}
	o := makeTestOverlay(w)

	done := make(chan struct{})
	go func() {
		o.typeText(TypeMessage{Text: "", Enter: true, Instant: true})
		close(done)
	}()

	// Signal after echo-settle drain (1.1s)
	time.Sleep(1200 * time.Millisecond)
	o.NotifyActivity()
	<-done

	// Every non-empty write should be exactly \r (bare CR)
	writes := w.getWrites()
	for _, wr := range writes {
		if len(wr.data) > 0 {
			assert.Equal(t, "\r", string(wr.data),
				"each Enter should be \\r (bare CR). Got bytes: %v", wr.data)
		}
	}
	assert.Equal(t, 1, w.countEnters(), "should send 1 Enter before activity signal")
}

func TestEnterBehavior_BuildKeySequence(t *testing.T) {
	// The key API must also use bare \r for Enter.
	w := &recordingWriter{}
	o := makeTestOverlay(w)

	seq := o.buildKeySequence(KeyMessage{Key: "Enter"})
	assert.Equal(t, "\r", seq, "KeyMessage Enter should produce \\r")

	seq2 := o.buildKeySequence(KeyMessage{Key: "Return"})
	assert.Equal(t, "\r", seq2, "KeyMessage Return should also produce \\r")
}

// --- Tests investigating Enter retry timing ---

func TestEnterBehavior_ImmediateActivity_SingleEnter(t *testing.T) {
	// If the agent responds quickly (activity detected after echo-settle),
	// only 1 Enter is sent.
	w := &recordingWriter{}
	o := makeTestOverlay(w)

	done := make(chan struct{})
	go func() {
		o.typeText(TypeMessage{Text: "hello", Enter: true, Instant: true})
		close(done)
	}()

	// Signal activity after the 1.1s echo-settle + drain window
	time.Sleep(1200 * time.Millisecond)
	o.NotifyActivity()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("typeText did not return — stuck in retry loop")
	}

	enters := w.countEnters()
	assert.Equal(t, 1, enters, "with quick activity signal, should send exactly 1 Enter")

	// Verify text + enter in the output
	allData := string(w.allBytes())
	assert.True(t, strings.HasPrefix(allData, "hello"),
		"text should be written before Enter")
	assert.True(t, strings.HasSuffix(allData, "\r"),
		"should end with \\r")
}

func TestEnterBehavior_NoActivity_MaxRetries(t *testing.T) {
	// If the agent never responds, 4 Enters are sent (1 initial + 3 retries).
	// Total wall time: ~1.1s + 1.5s + 2s + 3s = ~7.6s
	w := &recordingWriter{}
	o := makeTestOverlay(w)

	start := time.Now()
	done := make(chan struct{})
	go func() {
		o.typeText(TypeMessage{Text: "", Enter: true, Instant: true})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(12 * time.Second):
		t.Fatal("typeText did not return after max retries")
	}
	elapsed := time.Since(start)

	enters := w.countEnters()
	assert.Equal(t, 4, enters,
		"with no activity, should send exactly 4 Enters (1 + 3 retries)")

	// Total time should be approximately 7.6s (1.1 + 1.5 + 2.0 + 3.0)
	assert.InDelta(t, 7.6, elapsed.Seconds(), 1.0,
		"total elapsed should be ~7.6s, got %v", elapsed)
}

func TestEnterBehavior_RetryTiming(t *testing.T) {
	// Exact delays between Enter retries:
	// [immediate] → 1.1s pause → [retry1] → 1.5s → [retry2] → 2s → [retry3] → 3s
	w := &recordingWriter{}
	o := makeTestOverlay(w)

	done := make(chan struct{})
	go func() {
		o.typeText(TypeMessage{Text: "", Enter: true, Instant: true})
		close(done)
	}()

	<-done

	writes := w.getWrites()
	// Filter to just the \r writes (Enter keypresses)
	var enterTimes []time.Time
	for _, wr := range writes {
		if string(wr.data) == "\r" {
			enterTimes = append(enterTimes, wr.at)
		}
	}

	require.Equal(t, 4, len(enterTimes), "expected 4 Enter writes")

	// Gaps between consecutive enters
	gap1 := enterTimes[1].Sub(enterTimes[0]) // echo settle + first retry: ~2.6s
	gap2 := enterTimes[2].Sub(enterTimes[1]) // second retry delay: ~2s
	gap3 := enterTimes[3].Sub(enterTimes[2]) // third retry delay: ~3s

	assert.InDelta(t, 2.6, gap1.Seconds(), 0.5,
		"gap between enter 1→2 should be ~2.6s (1.1s settle + 1.5s retry)")
	assert.InDelta(t, 2.0, gap2.Seconds(), 0.5,
		"gap between enter 2→3 should be ~2.0s")
	assert.InDelta(t, 3.0, gap3.Seconds(), 0.5,
		"gap between enter 3→4 should be ~3.0s")
}

func TestEnterBehavior_ActivityStopsRetries(t *testing.T) {
	// Activity arriving after the 2nd Enter stops further retries.
	w := &recordingWriter{}
	o := makeTestOverlay(w)

	done := make(chan struct{})
	go func() {
		o.typeText(TypeMessage{Text: "test", Enter: true, Instant: true})
		close(done)
	}()

	// Wait past echo settle (1.1s) + first retry delay (1.5s), then signal.
	time.Sleep(3000 * time.Millisecond)
	o.NotifyActivity()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("typeText should have returned after activity signal")
	}

	enters := w.countEnters()
	assert.Equal(t, 2, enters,
		"activity after first retry should result in exactly 2 Enters")
}

// --- Tests investigating echo drain behavior ---

func TestEnterBehavior_EchoDrain(t *testing.T) {
	// Activity signals during the 1.1s echo-settle window are drained.
	// This prevents text echo from being mistaken for agent output.
	w := &recordingWriter{}
	o := makeTestOverlay(w)

	done := make(chan struct{})
	go func() {
		o.typeText(TypeMessage{Text: "msg", Enter: true, Instant: true})
		close(done)
	}()

	// Simulate echo activity during the 1.1s settle window
	time.Sleep(200 * time.Millisecond)
	o.NotifyActivity() // This should be drained

	// Now signal real activity after the settle window
	time.Sleep(1500 * time.Millisecond) // Past the 1.1s settle
	o.NotifyActivity()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("typeText did not return")
	}

	enters := w.countEnters()
	assert.Equal(t, 1, enters,
		"echo signal should be drained; real signal stops retries at 1 Enter")
}

// --- Tests investigating instant vs typed mode ---

func TestEnterBehavior_InstantMode_WritesFullText(t *testing.T) {
	// In instant mode, text arrives as a single write.
	w := &recordingWriter{}
	o := makeTestOverlay(w)

	done := make(chan struct{})
	go func() {
		o.typeText(TypeMessage{Text: "hello world", Enter: true, Instant: true})
		close(done)
	}()
	time.Sleep(1200 * time.Millisecond)
	o.NotifyActivity()
	<-done

	writes := w.getWrites()
	require.GreaterOrEqual(t, len(writes), 2, "need at least text + enter writes")

	// First write should be the full text
	assert.Equal(t, "hello world", string(writes[0].data),
		"instant mode should write full text in one call")
}

func TestEnterBehavior_TypedMode_CharByChar(t *testing.T) {
	// In typed mode, each character arrives separately with ~10ms delays.
	w := &recordingWriter{}
	o := makeTestOverlay(w)

	done := make(chan struct{})
	start := time.Now()
	go func() {
		o.typeText(TypeMessage{Text: "abc", Enter: true, Instant: false})
		close(done)
	}()
	time.Sleep(1300 * time.Millisecond) // 10ms*3 chars + 100ms settle + 1.1s drain
	o.NotifyActivity()
	<-done
	elapsed := time.Since(start)

	writes := w.getWrites()

	// Should have individual character writes
	var textWrites []string
	for _, wr := range writes {
		s := string(wr.data)
		if s != "\r" {
			textWrites = append(textWrites, s)
		}
	}
	assert.Equal(t, []string{"a", "b", "c"}, textWrites,
		"typed mode should write one character at a time")

	// Should have per-char delay (~10ms each) + 100ms pre-Enter settle
	assert.GreaterOrEqual(t, elapsed.Milliseconds(), int64(100),
		"typed mode should include delays between characters")
}

func TestEnterBehavior_TypedMode_PreEnterSettle(t *testing.T) {
	// In typed mode, there's a 100ms settle delay before the first Enter.
	// This is for Ink terminals to process all chars.
	w := &recordingWriter{}
	o := makeTestOverlay(w)

	done := make(chan struct{})
	go func() {
		o.typeText(TypeMessage{Text: "x", Enter: true, Instant: false})
		close(done)
	}()
	time.Sleep(1300 * time.Millisecond) // 10ms char + 100ms settle + 1.1s drain
	o.NotifyActivity()
	<-done

	writes := w.getWrites()
	var charTime, enterTime time.Time
	for _, wr := range writes {
		if string(wr.data) == "x" {
			charTime = wr.at
		}
		if string(wr.data) == "\r" {
			enterTime = wr.at
			break
		}
	}

	require.False(t, charTime.IsZero(), "should have char write")
	require.False(t, enterTime.IsZero(), "should have enter write")

	gap := enterTime.Sub(charTime)
	assert.GreaterOrEqual(t, gap.Milliseconds(), int64(90),
		"typed mode should have ~100ms settle before Enter, got %v", gap)
}

// --- Tests investigating the ActivityMonitor → activityCh feedback loop ---

func TestEnterBehavior_ActivityMonitorFeedback(t *testing.T) {
	// The ActivityMonitor correctly signals the Overlay's activityCh when
	// output bytes arrive. Full chain:
	//   PTY output → ActivityMonitor.Write() → OnStateChange → NotifyActivity()
	w := &recordingWriter{}
	o := makeTestOverlay(w)

	var activitySignaled atomic.Bool

	var outputBuf bytes.Buffer
	cfg := overlay.ActivityMonitorConfig{
		IdleTimeout:    500 * time.Millisecond,
		MinActiveBytes: 5,
		OnStateChange: func(state overlay.ActivityState) {
			if state == overlay.ActivityActive {
				activitySignaled.Store(true)
				o.NotifyActivity()
			}
		},
	}
	am := overlay.NewActivityMonitor(&outputBuf, cfg)
	defer am.Stop()

	done := make(chan struct{})
	go func() {
		o.typeText(TypeMessage{Text: "prompt", Enter: true, Instant: true})
		close(done)
	}()

	// Simulate agent output arriving after the echo-settle window
	time.Sleep(1300 * time.Millisecond)
	am.Write([]byte("Claude is thinking and producing output here...\n"))

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("typeText should have returned after ActivityMonitor signaled")
	}

	assert.True(t, activitySignaled.Load(),
		"ActivityMonitor should have fired OnStateChange(Active)")
	assert.Equal(t, 1, w.countEnters(),
		"activity signal should prevent retries — only 1 Enter")
}

func TestEnterBehavior_MinActiveBytes_Threshold(t *testing.T) {
	// Small output bursts (<MinActiveBytes) should NOT trigger activity.
	// This prevents the Enter echo from being mistaken for agent output.
	var outputBuf bytes.Buffer
	var triggered atomic.Bool

	cfg := overlay.ActivityMonitorConfig{
		IdleTimeout:    500 * time.Millisecond,
		MinActiveBytes: 10, // Default threshold
		OnStateChange: func(state overlay.ActivityState) {
			if state == overlay.ActivityActive {
				triggered.Store(true)
			}
		},
	}
	am := overlay.NewActivityMonitor(&outputBuf, cfg)
	defer am.Stop()

	// Write less than threshold (simulating Enter echo: just "\r" = 1 byte)
	am.Write([]byte("\r"))
	time.Sleep(50 * time.Millisecond)

	assert.False(t, triggered.Load(),
		"1 byte (Enter echo) should NOT trigger active state (threshold=10)")
	assert.Equal(t, overlay.ActivityIdle, am.State())

	// Write more to cross the threshold (simulating real agent output)
	am.Write([]byte("Agent response text"))
	time.Sleep(50 * time.Millisecond)

	assert.True(t, triggered.Load(),
		"agent output exceeding threshold SHOULD trigger active state")
	assert.Equal(t, overlay.ActivityActive, am.State())
}

// --- Tests investigating text+enter ordering ---

func TestEnterBehavior_TextBeforeEnter(t *testing.T) {
	// Text is always written BEFORE Enter — if Enter arrived first,
	// the agent would process an empty message.
	w := &recordingWriter{}
	o := makeTestOverlay(w)

	done := make(chan struct{})
	go func() {
		o.typeText(TypeMessage{Text: "my prompt text", Enter: true, Instant: true})
		close(done)
	}()
	time.Sleep(1200 * time.Millisecond)
	o.NotifyActivity()
	<-done

	allData := string(w.allBytes())
	textIdx := strings.Index(allData, "my prompt text")
	enterIdx := strings.Index(allData, "\r")

	require.NotEqual(t, -1, textIdx, "text should be present")
	require.NotEqual(t, -1, enterIdx, "enter should be present")
	assert.Less(t, textIdx, enterIdx,
		"text must appear before Enter in the byte stream")
}

func TestEnterBehavior_NoEnter_NoNewline(t *testing.T) {
	// When Enter=false, no \r should be sent at all.
	w := &recordingWriter{}
	o := makeTestOverlay(w)

	o.typeText(TypeMessage{Text: "just text", Enter: false, Instant: true})

	allData := string(w.allBytes())
	assert.Equal(t, "just text", allData,
		"Enter=false should write only the text with no newline")
	assert.Equal(t, 0, w.countEnters())
}

func TestEnterBehavior_EmptyText_WithEnter(t *testing.T) {
	// Empty text + Enter sends just the Enter byte(s).
	w := &recordingWriter{}
	o := makeTestOverlay(w)

	done := make(chan struct{})
	go func() {
		o.typeText(TypeMessage{Text: "", Enter: true, Instant: true})
		close(done)
	}()
	time.Sleep(1200 * time.Millisecond)
	o.NotifyActivity()
	<-done

	assert.Equal(t, 1, w.countEnters(),
		"empty text + Enter + quick activity should send 1 \\r")
	// Verify only \r bytes were written (no text content)
	for _, wr := range w.getWrites() {
		if len(wr.data) > 0 {
			assert.Equal(t, "\r", string(wr.data),
				"with empty text, all non-empty writes should be \\r")
		}
	}
}

// --- Test investigating the activityCh buffering ---

func TestEnterBehavior_ActivityChBuffered(t *testing.T) {
	// activityCh is buffered (cap 1). Multiple signals before consumption
	// don't block. Only one signal is retained.
	w := &recordingWriter{}
	o := makeTestOverlay(w)

	// Signal multiple times — should not block
	o.NotifyActivity()
	o.NotifyActivity()
	o.NotifyActivity()

	// Drain should get exactly one
	select {
	case <-o.activityCh:
	default:
		t.Fatal("should have at least one signal")
	}

	select {
	case <-o.activityCh:
		t.Fatal("should not have a second signal (buffer=1)")
	default:
		// Expected
	}
}

// --- PTY byte verification tests ---

func TestEnterBehavior_NotCRLF(t *testing.T) {
	// CRITICAL: Verify that Enter is \r NOT \r\n.
	// \r\n through a PTY in raw mode arrives as the 2-byte string "\r\n"
	// at the child process. Ink checks `input === '\r'` which fails for
	// "\r\n" because it's a different string. This was the root cause of
	// Enter not triggering submission in Claude Code.
	w := &recordingWriter{}
	o := makeTestOverlay(w)

	done := make(chan struct{})
	go func() {
		o.typeText(TypeMessage{Text: "test", Enter: true, Instant: true})
		close(done)
	}()
	time.Sleep(1200 * time.Millisecond)
	o.NotifyActivity()
	<-done

	allData := w.allBytes()
	// Should contain \r but NOT \r\n
	assert.Contains(t, string(allData), "\r", "should contain CR")
	assert.NotContains(t, string(allData), "\r\n",
		"MUST NOT contain \\r\\n — Ink receives this as a 2-byte string that doesn't match '\\r'")
}
