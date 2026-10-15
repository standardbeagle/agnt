package daemon

import (
	"net"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// sendMessageToOverlay built a fresh keep-alive transport per call and never
// closed it, so every SESSION SEND and incident PTY ping left an idle
// connection open on the overlay — each one holding a slot of the overlay
// listener's connection cap until its idle timeout.
func TestSendMessageToOverlay_DoesNotLeaveConnectionsOpen(t *testing.T) {
	sock := filepath.Join(shortTempDir(t), "ov.sock")
	ln, err := net.Listen("unix", sock)
	require.NoError(t, err)

	var open atomic.Int64
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
		ConnState: func(_ net.Conn, state http.ConnState) {
			switch state {
			case http.StateNew:
				open.Add(1)
			case http.StateClosed, http.StateHijacked:
				open.Add(-1)
			}
		},
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	d := &Daemon{}
	const calls = 5
	for i := 0; i < calls; i++ {
		require.NoError(t, d.sendMessageToOverlay(sock, "hello"))
	}

	require.Eventually(t, func() bool { return open.Load() == 0 }, 5*time.Second, 20*time.Millisecond,
		"every overlay connection must be closed once its message is delivered (still open: %d)", open.Load())
}
