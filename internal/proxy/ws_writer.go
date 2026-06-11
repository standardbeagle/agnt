package proxy

import (
	"encoding/json"
	"sync"
	"sync/atomic"

	"github.com/gorilla/websocket"
)

// wsJSONWriter is the write side a control-plane handler (session/store/voice)
// needs. Both *asyncWSWriter and *websocket.Conn satisfy it, so handlers and
// VoiceSession can accept the serialising async writer in production while
// tests may still pass a bare conn.
type wsJSONWriter interface {
	WriteJSON(v interface{}) error
}

// wsWriter is the minimal interface the proxy uses to push messages to a
// WebSocket client. The concrete implementation is *websocket.Conn, but tests
// can supply a stub without opening a real network connection.
type wsWriter interface {
	WriteMessage(messageType int, data []byte) error
	Close() error
}

// asyncWSWriter wraps a wsWriter and serialises writes through a bounded
// channel so that a slow or blocked subscriber never stalls the broadcast
// loop. Each instance owns one goroutine that drains the channel.
//
// Design constraints:
//   - Channel size 64: enough headroom for a burst of broadcast messages
//     without consuming significant memory per subscriber.
//   - If the channel is full the message is dropped (slow-subscriber
//     isolation). The drop is counted via droppedMsgs.
//   - Close() is idempotent and signals the drain goroutine to exit.
//   - WriteMessage returns an error only when the underlying conn has
//     already been closed; the drain goroutine propagates that by closing
//     the channel and setting the closed flag.
type asyncWSWriter struct {
	conn    wsWriter
	ch      chan []byte
	msgType int

	// writeMu serialises every WriteMessage on the underlying conn — the drain
	// goroutine AND the synchronous control-plane writers (WriteJSON / WriteSync)
	// — so gorilla never sees a concurrent write. gorilla/websocket forbids
	// concurrent writers; without this, broadcast drains race the session/store/
	// voice/capture-ack writers and corrupt the frame stream.
	writeMu sync.Mutex

	once    sync.Once
	closed  atomic.Bool
	dropped atomic.Int64
	wg      sync.WaitGroup
}

const asyncWSWriterBufSize = 64

// newAsyncWSWriter creates an asyncWSWriter and starts its drain goroutine.
// msgType is the WebSocket message type (e.g. websocket.TextMessage) used
// for all sends.
func newAsyncWSWriter(conn wsWriter, msgType int) *asyncWSWriter {
	w := &asyncWSWriter{
		conn:    conn,
		ch:      make(chan []byte, asyncWSWriterBufSize),
		msgType: msgType,
	}
	w.wg.Add(1)
	go w.drain()
	return w
}

// drain is the single writer goroutine. It serialises all outbound writes so
// the underlying conn never receives concurrent WriteMessage calls.
func (w *asyncWSWriter) drain() {
	defer w.wg.Done()
	for msg := range w.ch {
		w.writeMu.Lock()
		err := w.conn.WriteMessage(w.msgType, msg)
		w.writeMu.Unlock()
		if err != nil {
			// Connection gone — mark closed and discard the rest.
			w.closed.Store(true)
			// Drain residual messages without blocking.
			for range w.ch {
				w.dropped.Add(1)
			}
			return
		}
	}
}

// WriteJSON marshals v and writes it synchronously, serialised against the
// drain goroutine via writeMu. Unlike the async broadcast path it never drops:
// control-plane request/replies (session, store, voice, capture_ack) must be
// delivered. Returns an error if the conn is already closed or the write fails.
func (w *asyncWSWriter) WriteJSON(v interface{}) error {
	if w.closed.Load() {
		return errConnClosed
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return w.WriteSync(data)
}

// WriteSync writes pre-marshalled bytes synchronously, serialised against the
// drain goroutine. Used for control-plane frames already in wire form.
func (w *asyncWSWriter) WriteSync(data []byte) error {
	if w.closed.Load() {
		return errConnClosed
	}
	w.writeMu.Lock()
	defer w.writeMu.Unlock()
	return w.conn.WriteMessage(w.msgType, data)
}

// WriteMessage satisfies wsWriter. It enqueues msg for asynchronous delivery.
// Returns an error only when the conn is already known-closed.
func (w *asyncWSWriter) WriteMessage(_ int, data []byte) error {
	if w.closed.Load() {
		return errConnClosed
	}
	// Non-blocking send: drop on full buffer to isolate slow subscribers.
	select {
	case w.ch <- data:
	default:
		w.dropped.Add(1)
	}
	return nil
}

// Close signals the drain goroutine to stop and waits for it to exit.
// Safe to call multiple times.
func (w *asyncWSWriter) Close() error {
	w.once.Do(func() {
		close(w.ch)
		w.wg.Wait()
	})
	return w.conn.Close()
}

// errConnClosed is returned by WriteMessage when the underlying connection has
// already been closed or the drain goroutine detected a write failure.
var errConnClosed = connClosedError("connection closed")

type connClosedError string

func (e connClosedError) Error() string { return string(e) }

// connFromMap extracts the wsWriter stored under a wsConns sync.Map value.
// The map stores *asyncWSWriter entries (registered via handleWebSocket) but
// callers in tests may directly store a plain wsWriter stub.
func connFromMap(value interface{}) wsWriter {
	if aw, ok := value.(*asyncWSWriter); ok {
		return aw
	}
	if w, ok := value.(wsWriter); ok {
		return w
	}
	// Legacy path: bare *websocket.Conn stored directly (should not happen
	// in production after this refactor, but kept for safety).
	if c, ok := value.(*websocket.Conn); ok {
		return c
	}
	return nil
}
