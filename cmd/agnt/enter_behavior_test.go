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

// testSettle and testRetryDelays are the durations the overlay under test is
// configured with. No test waits for them; they are only ever asserted on, as
// the values the overlay asks its clock for.
const testSettle = 50 * time.Millisecond

var testRetryDelays = [3]time.Duration{50 * time.Millisecond, 75 * time.Millisecond, 100 * time.Millisecond}

// afterCall is one o.after(d) request parked in the retry loop's select.
type afterCall struct {
	d  time.Duration
	ch chan time.Time
}

// fire releases the retry, as the wall clock would have at d.
func (c afterCall) fire() { c.ch <- time.Time{} }

// testClock is the overlay's clock, driven by the test rather than by time.
//
// Sleeps return immediately and are recorded. Every after() request is
// published on afters, and — because that send blocks until the test receives
// it — taking one from the channel is proof that the retry loop has drained the
// echo signals and parked in its select. That is the window agent output must
// land in, so the test observes it instead of sleeping toward it.
type testClock struct {
	sleeps chan time.Duration
	afters chan afterCall
	// onSleep runs inside sleep(d), before it returns. Lets a test act at a
	// point the overlay only reaches mid-sleep, e.g. echo during the settle.
	onSleep func(time.Duration)
}

func newTestClock() *testClock {
	return &testClock{
		sleeps: make(chan time.Duration, 16),
		afters: make(chan afterCall),
	}
}

func (c *testClock) sleep(d time.Duration) {
	select {
	case c.sleeps <- d:
	default:
	}
	if c.onSleep != nil {
		c.onSleep(d)
	}
}

func (c *testClock) after(d time.Duration) <-chan time.Time {
	call := afterCall{d: d, ch: make(chan time.Time, 1)}
	c.afters <- call
	return call.ch
}

// awaitRetry blocks until the overlay parks in its retry select, and returns
// the delay it asked for. The timeout is a deadlock guard, not synchronization.
func (c *testClock) awaitRetry(t *testing.T) afterCall {
	t.Helper()
	select {
	case call := <-c.afters:
		return call
	case <-time.After(10 * time.Second):
		t.Fatal("overlay never reached its Enter retry wait")
		return afterCall{}
	}
}

// sleptFor drains the recorded sleep durations.
func (c *testClock) sleptFor() []time.Duration {
	var out []time.Duration
	for {
		select {
		case d := <-c.sleeps:
			out = append(out, d)
		default:
			return out
		}
	}
}

// makeTestOverlay creates an Overlay whose clock the test drives.
func makeTestOverlay(w *recordingWriter) (*Overlay, *testClock) {
	clk := newTestClock()
	o := &Overlay{
		ptmx:             w,
		activityCh:       make(chan struct{}, 1),
		enterSettle:      testSettle,
		enterRetryDelays: testRetryDelays,
		sleepFn:          clk.sleep,
		afterFn:          clk.after,
	}
	return o, clk
}

// awaitDone blocks until typeText returns. The timeout is a deadlock guard.
func awaitDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("typeText never returned")
	}
}

// --- Tests investigating the Enter byte sequence ---

func TestEnterBehavior_ByteSequence(t *testing.T) {
	t.Parallel()
	// FINDING: Enter must be just \r (CR, 0x0D) — NOT \r\n.
	// Ink (Node.js) in raw mode checks `input === '\r'` for the Return key.
	// Through a PTY in raw mode, bytes pass through unchanged, so "\r\n"
	// arrives as the 2-byte string "\r\n" which does NOT match "\r".
	// This was the root cause of Enter not triggering in Claude Code.
	w := &recordingWriter{}
	o, clk := makeTestOverlay(w)

	done := make(chan struct{})
	go func() {
		o.typeText(TypeMessage{Text: "", Enter: true, Instant: true})
		close(done)
	}()

	// The overlay has drained the echo signals and is waiting on retry1.
	clk.awaitRetry(t)
	o.NotifyActivity()
	awaitDone(t, done)

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
	t.Parallel()
	// The key API must also use bare \r for Enter.
	w := &recordingWriter{}
	o, _ := makeTestOverlay(w)

	seq := o.buildKeySequence(KeyMessage{Key: "Enter"})
	assert.Equal(t, "\r", seq, "KeyMessage Enter should produce \\r")

	seq2 := o.buildKeySequence(KeyMessage{Key: "Return"})
	assert.Equal(t, "\r", seq2, "KeyMessage Return should also produce \\r")
}

// --- Tests investigating Enter retry timing ---

func TestEnterBehavior_ImmediateActivity_SingleEnter(t *testing.T) {
	t.Parallel()
	// If the agent responds quickly (activity detected after echo-settle),
	// only 1 Enter is sent.
	w := &recordingWriter{}
	o, clk := makeTestOverlay(w)

	done := make(chan struct{})
	go func() {
		o.typeText(TypeMessage{Text: "hello", Enter: true, Instant: true})
		close(done)
	}()

	// Activity lands in the window between the drain and retry1.
	clk.awaitRetry(t)
	o.NotifyActivity()
	awaitDone(t, done)

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
	t.Parallel()
	// If the agent never responds, 4 Enters are sent (1 initial + 3 retries),
	// each after its configured delay.
	w := &recordingWriter{}
	o, clk := makeTestOverlay(w)

	done := make(chan struct{})
	go func() {
		o.typeText(TypeMessage{Text: "", Enter: true, Instant: true})
		close(done)
	}()

	// Let every retry expire, checking the delay the overlay waited on. Asserting
	// the requested delays rather than elapsed wall time keeps this exact under
	// any load.
	var waited []time.Duration
	for i := 0; i < len(testRetryDelays); i++ {
		call := clk.awaitRetry(t)
		waited = append(waited, call.d)
		assert.Equal(t, i+1, w.countEnters(),
			"retry %d should not have fired before its delay elapsed", i+1)
		call.fire()
	}
	awaitDone(t, done)

	assert.Equal(t, 4, w.countEnters(),
		"with no activity, should send exactly 4 Enters (1 + 3 retries)")
	assert.Equal(t, testRetryDelays[:], waited,
		"retries should back off through the configured delays")
	assert.Equal(t, []time.Duration{testSettle}, clk.sleptFor(),
		"instant mode should sleep only for the echo-settle window")
}

func TestEnterBehavior_RetryTiming(t *testing.T) {
	t.Parallel()
	// The first Enter goes out immediately; each later one waits on the next
	// backoff delay. Asserted as the sequence of waits the overlay performs:
	// [enter] → settle → [enter] → 50ms → [enter] → 75ms → [enter] → 100ms.
	w := &recordingWriter{}
	o, clk := makeTestOverlay(w)

	done := make(chan struct{})
	go func() {
		o.typeText(TypeMessage{Text: "", Enter: true, Instant: true})
		close(done)
	}()

	// The first Enter precedes any wait at all.
	call := clk.awaitRetry(t)
	assert.Equal(t, 1, w.countEnters(), "first Enter is sent before any delay")
	assert.Equal(t, testRetryDelays[0], call.d)
	call.fire()

	for i := 1; i < len(testRetryDelays); i++ {
		call = clk.awaitRetry(t)
		assert.Equal(t, i+1, w.countEnters(), "one Enter per expired delay")
		assert.Equal(t, testRetryDelays[i], call.d, "retry %d delay", i+1)
		call.fire()
	}
	awaitDone(t, done)

	require.Equal(t, 4, w.countEnters(), "expected 4 Enter writes")
}

func TestEnterBehavior_ActivityStopsRetries(t *testing.T) {
	t.Parallel()
	// Activity arriving after the 2nd Enter stops further retries.
	w := &recordingWriter{}
	o, clk := makeTestOverlay(w)

	done := make(chan struct{})
	go func() {
		o.typeText(TypeMessage{Text: "test", Enter: true, Instant: true})
		close(done)
	}()

	// Let retry1 expire (2nd Enter), then signal while retry2 is still waiting.
	clk.awaitRetry(t).fire()
	clk.awaitRetry(t)
	o.NotifyActivity()
	awaitDone(t, done)

	enters := w.countEnters()
	assert.Equal(t, 2, enters,
		"activity after first retry should result in exactly 2 Enters")
}

// --- Tests investigating echo drain behavior ---

func TestEnterBehavior_EchoDrain(t *testing.T) {
	t.Parallel()
	// Activity signals raised during the echo-settle window are the terminal
	// echoing our own text back. They must be discarded, or the overlay mistakes
	// its own echo for the agent answering and never retries a dropped Enter.
	//
	// Proof that the drain happened: with the echo discarded, the retry select
	// has nothing ready and parks until we expire it, producing a 2nd Enter. An
	// undrained echo would satisfy that select instead, and typeText would return
	// after a single Enter — leaving the retry we await below to never arrive.
	w := &recordingWriter{}
	o, clk := makeTestOverlay(w)

	// The echo lands during the settle sleep, i.e. before the drain runs.
	clk.onSleep = func(time.Duration) { o.NotifyActivity() }

	done := make(chan struct{})
	go func() {
		o.typeText(TypeMessage{Text: "msg", Enter: true, Instant: true})
		close(done)
	}()

	// Expire retry1, then wait for the loop to ask for retry2 — that request is
	// proof the 2nd Enter has been written, without timing the goroutine.
	clk.awaitRetry(t).fire()
	clk.awaitRetry(t)
	assert.Equal(t, 2, w.countEnters(),
		"echo signal must be drained, so retry1 fires a 2nd Enter")

	// Now the agent really answers, while retry2 waits.
	o.NotifyActivity()
	awaitDone(t, done)

	assert.Equal(t, 2, w.countEnters(),
		"real activity stops the retries; echo did not count as activity")
}

// --- Tests investigating instant vs typed mode ---

func TestEnterBehavior_InstantMode_WritesFullText(t *testing.T) {
	t.Parallel()
	// In instant mode, text arrives as a single write.
	w := &recordingWriter{}
	o, clk := makeTestOverlay(w)

	done := make(chan struct{})
	go func() {
		o.typeText(TypeMessage{Text: "hello world", Enter: true, Instant: true})
		close(done)
	}()
	clk.awaitRetry(t)
	o.NotifyActivity()
	awaitDone(t, done)

	writes := w.getWrites()
	require.GreaterOrEqual(t, len(writes), 2, "need at least text + enter writes")

	// First write should be the full text
	assert.Equal(t, "hello world", string(writes[0].data),
		"instant mode should write full text in one call")
}

func TestEnterBehavior_TypedMode_CharByChar(t *testing.T) {
	t.Parallel()
	// In typed mode, each character arrives separately with ~10ms delays.
	w := &recordingWriter{}
	o, clk := makeTestOverlay(w)

	done := make(chan struct{})
	go func() {
		o.typeText(TypeMessage{Text: "abc", Enter: true, Instant: false})
		close(done)
	}()
	clk.awaitRetry(t)
	o.NotifyActivity()
	awaitDone(t, done)

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

	// One 10ms pause per character, then the 100ms pre-Enter settle, then the
	// echo settle. Asserting the waits themselves beats timing the goroutine.
	assert.Equal(t, []time.Duration{
		10 * time.Millisecond, 10 * time.Millisecond, 10 * time.Millisecond,
		100 * time.Millisecond, testSettle,
	}, clk.sleptFor(), "typed mode should pause between characters and before Enter")
}

func TestEnterBehavior_TypedMode_PreEnterSettle(t *testing.T) {
	t.Parallel()
	// In typed mode, there's a 100ms settle delay before the first Enter.
	// This is for Ink terminals to process all chars.
	w := &recordingWriter{}
	o, clk := makeTestOverlay(w)

	// Capture the waits the overlay performs before it writes the first Enter.
	var beforeEnter []time.Duration
	clk.onSleep = func(d time.Duration) {
		if w.countEnters() == 0 {
			beforeEnter = append(beforeEnter, d)
		}
	}

	done := make(chan struct{})
	go func() {
		o.typeText(TypeMessage{Text: "x", Enter: true, Instant: false})
		close(done)
	}()
	clk.awaitRetry(t)
	o.NotifyActivity()
	awaitDone(t, done)

	writes := w.getWrites()
	require.GreaterOrEqual(t, len(writes), 2, "should have char and enter writes")
	assert.Equal(t, "x", string(writes[0].data))
	assert.Equal(t, "\r", string(writes[1].data))

	require.Len(t, beforeEnter, 2, "one per-char pause, then the pre-Enter settle")
	assert.Equal(t, 100*time.Millisecond, beforeEnter[1],
		"typed mode should settle 100ms after the last character, before Enter")
}

// --- Tests investigating the ActivityMonitor → activityCh feedback loop ---

func TestEnterBehavior_ActivityMonitorFeedback(t *testing.T) {
	t.Parallel()
	// The ActivityMonitor signals the Overlay's activityCh when output bytes
	// arrive, and that signal must suppress the Enter retries. Full chain:
	//   PTY output → ActivityMonitor.Write() → OnStateChange → NotifyActivity()
	//
	// Agent output has to land in a window the overlay owns: after it drains the
	// echo-triggered signals, before the next retry fires. Awaiting the retry
	// request puts us exactly there — no sleep can, because a sleep races the
	// overlay's own goroutine and loses under load.
	w := &recordingWriter{}
	o, clk := makeTestOverlay(w)

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

	// The retry is parked and waiting; the agent answers. The retry never fires,
	// because nothing ever fires it.
	clk.awaitRetry(t)
	am.Write([]byte("Claude is thinking and producing output here...\n"))
	awaitDone(t, done)

	assert.True(t, activitySignaled.Load(),
		"ActivityMonitor should have fired OnStateChange(Active)")
	assert.Equal(t, 1, w.countEnters(),
		"activity signal should prevent retries — only 1 Enter")
}

func TestEnterBehavior_MinActiveBytes_Threshold(t *testing.T) {
	t.Parallel()
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

	// ActivityMonitor.Write invokes OnStateChange inline, so the transition (or
	// its absence) is settled the moment Write returns — nothing to wait for.

	// Write less than threshold (simulating Enter echo: just "\r" = 1 byte)
	_, err := am.Write([]byte("\r"))
	require.NoError(t, err)

	assert.False(t, triggered.Load(),
		"1 byte (Enter echo) should NOT trigger active state (threshold=10)")
	assert.Equal(t, overlay.ActivityIdle, am.State())

	// Write more to cross the threshold (simulating real agent output)
	_, err = am.Write([]byte("Agent response text"))
	require.NoError(t, err)

	assert.True(t, triggered.Load(),
		"agent output exceeding threshold SHOULD trigger active state")
	assert.Equal(t, overlay.ActivityActive, am.State())
}

// --- Tests investigating text+enter ordering ---

func TestEnterBehavior_TextBeforeEnter(t *testing.T) {
	t.Parallel()
	// Text is always written BEFORE Enter — if Enter arrived first,
	// the agent would process an empty message.
	w := &recordingWriter{}
	o, clk := makeTestOverlay(w)

	done := make(chan struct{})
	go func() {
		o.typeText(TypeMessage{Text: "my prompt text", Enter: true, Instant: true})
		close(done)
	}()
	clk.awaitRetry(t)
	o.NotifyActivity()
	awaitDone(t, done)

	allData := string(w.allBytes())
	textIdx := strings.Index(allData, "my prompt text")
	enterIdx := strings.Index(allData, "\r")

	require.NotEqual(t, -1, textIdx, "text should be present")
	require.NotEqual(t, -1, enterIdx, "enter should be present")
	assert.Less(t, textIdx, enterIdx,
		"text must appear before Enter in the byte stream")
}

func TestEnterBehavior_NoEnter_NoNewline(t *testing.T) {
	t.Parallel()
	// When Enter=false, no \r should be sent at all.
	w := &recordingWriter{}
	o, _ := makeTestOverlay(w)

	o.typeText(TypeMessage{Text: "just text", Enter: false, Instant: true})

	allData := string(w.allBytes())
	assert.Equal(t, "just text", allData,
		"Enter=false should write only the text with no newline")
	assert.Equal(t, 0, w.countEnters())
}

func TestEnterBehavior_EmptyText_WithEnter(t *testing.T) {
	t.Parallel()
	// Empty text + Enter sends just the Enter byte(s).
	w := &recordingWriter{}
	o, clk := makeTestOverlay(w)

	done := make(chan struct{})
	go func() {
		o.typeText(TypeMessage{Text: "", Enter: true, Instant: true})
		close(done)
	}()
	clk.awaitRetry(t)
	o.NotifyActivity()
	awaitDone(t, done)

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
	t.Parallel()
	// activityCh is buffered (cap 1). Multiple signals before consumption
	// don't block. Only one signal is retained.
	w := &recordingWriter{}
	o, _ := makeTestOverlay(w)

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
	t.Parallel()
	// CRITICAL: Verify that Enter is \r NOT \r\n.
	// \r\n through a PTY in raw mode arrives as the 2-byte string "\r\n"
	// at the child process. Ink checks `input === '\r'` which fails for
	// "\r\n" because it's a different string. This was the root cause of
	// Enter not triggering submission in Claude Code.
	w := &recordingWriter{}
	o, clk := makeTestOverlay(w)

	done := make(chan struct{})
	go func() {
		o.typeText(TypeMessage{Text: "test", Enter: true, Instant: true})
		close(done)
	}()
	clk.awaitRetry(t)
	o.NotifyActivity()
	awaitDone(t, done)

	allData := w.allBytes()
	// Should contain \r but NOT \r\n
	assert.Contains(t, string(allData), "\r", "should contain CR")
	assert.NotContains(t, string(allData), "\r\n",
		"MUST NOT contain \\r\\n — Ink receives this as a 2-byte string that doesn't match '\\r'")
}
