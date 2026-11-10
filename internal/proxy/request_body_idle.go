package proxy

import (
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/standardbeagle/agnt/internal/httpcaps"
)

// requestBodyIdleTimeout bounds how long one read of a streamed request body
// may wait for bytes. A var so tests can shorten it.
var requestBodyIdleTimeout = httpcaps.DefaultReadTimeout

// idleDeadlineBody gives a streamed request body (chunked, unknown length, or
// too large to capture) an idle read deadline. The dev proxy's listener has no
// whole-request read deadline, because it streams, so without this a client
// that starts an upload and stops sending held the handler and a connection
// slot forever. Each Read must make progress within the timeout; a slow but
// steady upload of any total length is unaffected.
//
// The deadline is cleared when the body ends or is closed: left armed, it
// would fire on the server's background read once the response is streaming
// and cancel the request.
type idleDeadlineBody struct {
	body    io.ReadCloser
	rc      *http.ResponseController
	timeout time.Duration
	closed  atomic.Bool
}

// newIdleDeadlineBody wraps body, or returns it unchanged when the writer
// cannot take a read deadline.
func newIdleDeadlineBody(body io.ReadCloser, w http.ResponseWriter, timeout time.Duration) io.ReadCloser {
	rc := http.NewResponseController(w)
	if rc.SetReadDeadline(time.Time{}) != nil {
		return body
	}
	return &idleDeadlineBody{body: body, rc: rc, timeout: timeout}
}

func (b *idleDeadlineBody) Read(p []byte) (int, error) {
	if !b.closed.Load() {
		_ = b.rc.SetReadDeadline(time.Now().Add(b.timeout))
	}
	n, err := b.body.Read(p)
	if err != nil {
		// EOF, a timeout, or a read after Close: every error path disarms,
		// so a Read racing Close cannot leave the deadline armed.
		_ = b.rc.SetReadDeadline(time.Time{})
	}
	return n, err
}

func (b *idleDeadlineBody) Close() error {
	b.closed.Store(true)
	err := b.body.Close()
	_ = b.rc.SetReadDeadline(time.Time{})
	return err
}
