package proxy

import (
	"sync"
	"sync/atomic"

	"github.com/gorilla/websocket"
)

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
		if err := w.conn.WriteMessage(w.msgType, msg); err != nil {
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
