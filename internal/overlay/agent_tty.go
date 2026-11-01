package overlay

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// AgentTTY is the one way into the agent's terminal. Typed keys, injected
// messages, Enter retries and initial stdin all write through it, so writes
// never interleave, each one is bounded in time, and the last bytes in each
// direction are kept for a dump (TTYRecorder).
//
// The time bound used to be a write deadline set by one writer and never
// cleared. A deadline belongs to the file, not the write: five seconds after
// any injected message, every typed key failed with "i/o timeout" and went
// nowhere, while injected messages, which set a fresh deadline, still arrived.
// Here the deadline is set before each write and cleared after it, under the
// same lock, and a failed write is reported, never only logged.
type AgentTTY struct {
	backend PtyReadWriter

	mu sync.Mutex // serializes writes, deadline included

	rec *TTYRecorder

	// writeTimeout bounds one write; agentWriteTimeout unless a test
	// shortens it.
	writeTimeout time.Duration

	// onWriteError is told about the first failed write of a streak; a
	// successful write ends the streak. It is a callback because the
	// reporting surface (overlay notification, session log) lives above.
	onWriteError atomic.Pointer[func(source string, err error)]
	failing      atomic.Bool
}

// agentWriteTimeout bounds one write to the agent's terminal, so an agent
// that stopped reading (Ctrl+Z, wedged) cannot hold every writer forever.
const agentWriteTimeout = 5 * time.Second

// NewAgentTTY wraps backend. Every write to the agent's terminal must go
// through the result.
func NewAgentTTY(backend PtyReadWriter) *AgentTTY {
	return &AgentTTY{backend: backend, rec: NewTTYRecorder(ttyRecordBytes), writeTimeout: agentWriteTimeout}
}

// SetWriteTimeoutForTest shortens the per-write bound.
func (t *AgentTTY) SetWriteTimeoutForTest(d time.Duration) { t.writeTimeout = d }

// Recorder returns the recorder of bytes in and out of the agent's terminal.
func (t *AgentTTY) Recorder() *TTYRecorder { return t.rec }

// SetWriteErrorHandler sets who is told when writes start failing.
func (t *AgentTTY) SetWriteErrorHandler(fn func(source string, err error)) {
	t.onWriteError.Store(&fn)
}

// Read reads the agent's output and records it.
func (t *AgentTTY) Read(p []byte) (int, error) {
	n, err := t.backend.Read(p)
	if n > 0 {
		t.rec.Record(TTYOut, "agent", p[:n])
	}
	return n, err
}

// Write writes on behalf of an unnamed source. Prefer WriteFrom.
func (t *AgentTTY) Write(p []byte) (int, error) { return t.WriteFrom("unnamed", p) }

// From returns a writer that tags every write with source.
func (t *AgentTTY) From(source string) io.Writer { return taggedWriter{t, source} }

// WriteTagged writes p to w, tagged with source when w is an AgentTTY.
func WriteTagged(w io.Writer, source string, p []byte) (int, error) {
	if t, ok := w.(*AgentTTY); ok {
		return t.WriteFrom(source, p)
	}
	return w.Write(p)
}

type taggedWriter struct {
	t      *AgentTTY
	source string
}

func (w taggedWriter) Write(p []byte) (int, error) { return w.t.WriteFrom(w.source, p) }

type writeDeadliner interface{ SetWriteDeadline(time.Time) error }

// WriteFrom writes p in one piece, tagged with source in the recording.
func (t *AgentTTY) WriteFrom(source string, p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	t.mu.Lock()
	dl, hasDeadline := t.backend.(writeDeadliner)
	if hasDeadline && dl.SetWriteDeadline(time.Now().Add(t.writeTimeout)) != nil {
		hasDeadline = false // the backend cannot time out a write
	}
	n, err := t.backend.Write(p)
	if hasDeadline {
		_ = dl.SetWriteDeadline(time.Time{})
	}
	t.mu.Unlock()

	t.rec.Record(TTYIn, source, p[:n])
	if err != nil {
		t.rec.Record(TTYIn, source+"!failed", p[n:])
		if errors.Is(err, os.ErrDeadlineExceeded) {
			err = fmt.Errorf("agent did not read its input for %s: %w", t.writeTimeout, err)
		}
		if t.failing.CompareAndSwap(false, true) {
			if fn := t.onWriteError.Load(); fn != nil {
				(*fn)(source, err)
			}
		}
		return n, err
	}
	t.failing.Store(false)
	return n, nil
}

// TTYDirection says which way recorded bytes went.
type TTYDirection byte

const (
	TTYIn  TTYDirection = '>' // to the agent
	TTYOut TTYDirection = '<' // from the agent
)

// ttyRecordBytes is how much of each direction the recorder keeps.
const ttyRecordBytes = 64 << 10

// TTYEvent is one recorded write or read.
type TTYEvent struct {
	At     time.Time
	Dir    TTYDirection
	Source string
	Data   []byte
}

// TTYRecorder keeps the most recent bytes written to and read from the
// agent's terminal, up to a byte budget per direction, oldest dropped first.
// It lives in memory only and is read by the overlay's /tty endpoint on the
// session's uid-scoped socket. It holds what the user typed.
type TTYRecorder struct {
	mu     sync.Mutex
	budget int
	in     ttyRing
	out    ttyRing
}

type ttyRing struct {
	events []TTYEvent
	bytes  int
}

// NewTTYRecorder keeps up to budget bytes per direction.
func NewTTYRecorder(budget int) *TTYRecorder { return &TTYRecorder{budget: budget} }

// Record copies data into the recording.
func (r *TTYRecorder) Record(dir TTYDirection, source string, data []byte) {
	if len(data) == 0 {
		return
	}
	if len(data) > r.budget {
		data = data[len(data)-r.budget:]
	}
	ev := TTYEvent{At: time.Now(), Dir: dir, Source: source, Data: append([]byte(nil), data...)}
	r.mu.Lock()
	ring := &r.in
	if dir == TTYOut {
		ring = &r.out
	}
	ring.events = append(ring.events, ev)
	ring.bytes += len(ev.Data)
	drop := 0
	for ring.bytes > r.budget {
		ring.bytes -= len(ring.events[drop].Data)
		drop++
	}
	if drop > 0 {
		ring.events = append(ring.events[:0], ring.events[drop:]...)
	}
	r.mu.Unlock()
}

// Events returns both directions merged in time order.
func (r *TTYRecorder) Events() []TTYEvent {
	r.mu.Lock()
	in := append([]TTYEvent(nil), r.in.events...)
	out := append([]TTYEvent(nil), r.out.events...)
	r.mu.Unlock()
	merged := make([]TTYEvent, 0, len(in)+len(out))
	i, j := 0, 0
	for i < len(in) || j < len(out) {
		if j >= len(out) || (i < len(in) && !in[i].At.After(out[j].At)) {
			merged = append(merged, in[i])
			i++
		} else {
			merged = append(merged, out[j])
			j++
		}
	}
	return merged
}

// Dump formats the recording one event per line: time, direction, source,
// and the bytes Go-quoted so escape sequences are visible.
func (r *TTYRecorder) Dump() string {
	var b strings.Builder
	for _, ev := range r.Events() {
		fmt.Fprintf(&b, "%s %c %-14s %q\n", ev.At.Format("15:04:05.000000"), ev.Dir, ev.Source, ev.Data)
	}
	return b.String()
}
