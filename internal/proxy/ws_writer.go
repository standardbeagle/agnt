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

// asyncWSWriter funnels every outbound write through a single drain goroutine,
// so the underlying conn never sees concurrent WriteMessage calls (gorilla
// forbids concurrent writers) — by construction, with no write mutex.
//
// Two lanes feed the drain goroutine:
//   - broadcast (WriteMessage): bounded, non-blocking, drop-on-full. A slow or
//     blocked subscriber never stalls the broadcast loop; drops are counted.
//   - control (WriteSync/WriteJSON): bounded, blocking, never drops. Carries
//     control-plane request/replies (session, store, voice, capture_ack) that
//     must be delivered; the caller waits for the write result.
//
// The drain goroutine prefers the control lane when both are ready, so a
// broadcast burst cannot starve a pending reply.
type asyncWSWriter struct {
	conn    wsWriter
	ch      chan []byte
	control chan wsControlFrame
	msgType int

	done    chan struct{} // closed by Close; unblocks senders and the drain loop
	once    sync.Once
	closed  atomic.Bool
	dropped atomic.Int64
	wg      sync.WaitGroup
}

// wsControlFrame is a control-lane write plus the channel the drain goroutine
// reports the write result on. errCh is buffered so the drain never blocks on
// a caller that has already given up.
type wsControlFrame struct {
	data  []byte
	errCh chan error
}

const (
	asyncWSWriterBufSize     = 64
	asyncWSWriterControlSize = 16
)

// newAsyncWSWriter creates an asyncWSWriter and starts its drain goroutine.
// msgType is the WebSocket message type (e.g. websocket.TextMessage) used
// for all sends.
func newAsyncWSWriter(conn wsWriter, msgType int) *asyncWSWriter {
	w := &asyncWSWriter{
		conn:    conn,
		ch:      make(chan []byte, asyncWSWriterBufSize),
		control: make(chan wsControlFrame, asyncWSWriterControlSize),
		msgType: msgType,
		done:    make(chan struct{}),
	}
	w.wg.Add(1)
	go w.drain()
	return w
}

// drain is the single writer goroutine — the only caller of conn.WriteMessage.
func (w *asyncWSWriter) drain() {
	defer w.wg.Done()
	for {
		// Control lane has priority: serve any pending control frame before
		// taking the next broadcast message.
		select {
		case f := <-w.control:
			w.writeControl(f)
			continue
		default:
		}

		select {
		case f := <-w.control:
			w.writeControl(f)
		case msg := <-w.ch:
			w.writeBroadcast(msg)
		case <-w.done:
			w.flushResidual()
			return
		}
	}
}

func (w *asyncWSWriter) writeControl(f wsControlFrame) {
	if w.closed.Load() {
		f.errCh <- errConnClosed
		return
	}
	err := w.conn.WriteMessage(w.msgType, f.data)
	if err != nil {
		w.closed.Store(true)
	}
	f.errCh <- err
}

func (w *asyncWSWriter) writeBroadcast(msg []byte) {
	if w.closed.Load() {
		w.dropped.Add(1)
		return
	}
	if err := w.conn.WriteMessage(w.msgType, msg); err != nil {
		// Connection gone — subsequent frames are discarded (broadcast) or
		// failed (control) until Close.
		w.closed.Store(true)
	}
}

// flushResidual empties both lanes after shutdown so no control caller is
// left waiting on a result.
func (w *asyncWSWriter) flushResidual() {
	for {
		select {
		case f := <-w.control:
			f.errCh <- errConnClosed
		case <-w.ch:
			w.dropped.Add(1)
		default:
			return
		}
	}
}

// WriteJSON marshals v and writes it via the control lane: delivered in order,
// never dropped, result reported to the caller. Returns an error if the conn
// is already closed or the write fails.
func (w *asyncWSWriter) WriteJSON(v interface{}) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return w.WriteSync(data)
}

// WriteSync writes pre-marshalled bytes via the control lane and waits for the
// write result. Used for control-plane frames already in wire form.
func (w *asyncWSWriter) WriteSync(data []byte) error {
	if w.closed.Load() {
		return errConnClosed
	}
	f := wsControlFrame{data: data, errCh: make(chan error, 1)}
	select {
	case w.control <- f:
	case <-w.done:
		return errConnClosed
	}
	select {
	case err := <-f.errCh:
		return err
	case <-w.done:
		return errConnClosed
	}
}

// WriteMessage satisfies wsWriter. It enqueues msg on the broadcast lane for
// asynchronous delivery. Returns an error only when the conn is already
// known-closed.
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

// Close stops the drain goroutine, fails any in-flight control writes, and
// closes the underlying conn. Safe to call multiple times.
func (w *asyncWSWriter) Close() error {
	w.once.Do(func() {
		w.closed.Store(true)
		close(w.done)
		w.wg.Wait()
	})
	return w.conn.Close()
}

// errConnClosed is returned by the write methods when the underlying
// connection has already been closed or a previous write failed.
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
