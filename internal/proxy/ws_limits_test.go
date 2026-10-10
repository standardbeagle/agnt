package proxy

import (
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWS_OversizedMessageClosesConnection pins the inbound message bound on
// /__devtool_metrics: a frame larger than wsMaxMessageBytes ends the connection
// instead of being buffered.
func TestWS_OversizedMessageClosesConnection(t *testing.T) {
	ps := startWrapProxy(t, newWrapBackend(t))
	conn := dialMetrics(t, ps.ListenAddr)

	require.NoError(t, conn.WriteMessage(websocket.BinaryMessage, make([]byte, wsMaxMessageBytes+1)))

	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			assert.True(t, websocket.IsCloseError(err, websocket.CloseMessageTooBig),
				"server must close with 1009 (message too big), got: %v", err)
			return
		}
	}
}
