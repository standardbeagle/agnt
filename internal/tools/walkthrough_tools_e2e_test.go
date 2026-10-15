package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeWalkthroughBrowser connects to the proxy's control WebSocket as the
// local browser and answers every exec with reply, the text the browser-side
// exec snippet would return.
func fakeWalkthroughBrowser(t *testing.T, listenAddr, reply string) {
	t.Helper()
	u := url.URL{Scheme: "ws", Host: listenAddr, Path: "/__devtool_metrics"}
	header := http.Header{"Origin": []string{"http://" + listenAddr}}
	conn, _, err := websocket.DefaultDialer.Dial(u.String(), header)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	go func() {
		for {
			var msg map[string]interface{}
			if err := conn.ReadJSON(&msg); err != nil {
				return
			}
			if msg["type"] != "execute" {
				continue
			}
			_ = conn.WriteJSON(map[string]interface{}{
				"type": "execution",
				"data": map[string]interface{}{"exec_id": msg["id"], "result": reply},
			})
		}
	}()
}

// The browser-side walkthrough API refuses an invalid script (or a missing
// host frame) by returning {error: ...}, which reaches the tool inside the
// exec result. The tool reported that as success:true; it must be an IsError
// result carrying the browser's error.
func TestWalkthroughTool_BrowserErrorIsAnError(t *testing.T) {
	// No t.Parallel(): starts a real daemon and proxy.
	dt, _ := attachTestDaemon(t)
	require.NoError(t, dt.ensureConnected())

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html><body>app</body></html>"))
	}))
	t.Cleanup(backend.Close)

	started, err := dt.client.ProxyStart("wt", backend.URL, 0, 0, "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = dt.client.ProxyStop("wt") })
	listenAddr := getString(started, "listen_addr")
	require.NotEmpty(t, listenAddr)

	fakeWalkthroughBrowser(t, listenAddr, `{"error":"step 0: advance.type is required"}`)
	require.Eventually(t, func() bool {
		_, err := dt.client.ProxyExec("wt", "1")
		return err == nil
	}, 10*time.Second, 50*time.Millisecond, "the fake browser never connected")

	res, out, err := dt.makeWalkthroughHandler()(context.Background(), nil, WalkthroughInput{
		Action:  "load",
		ProxyID: "wt",
		Script:  map[string]interface{}{"id": "demo", "steps": []interface{}{map[string]interface{}{"title": "a"}}},
	})
	require.NoError(t, err)
	require.NotNil(t, res, "a browser-side refusal must not report success: %+v", out)
	assert.True(t, res.IsError)
	assert.Contains(t, resultText(res), "step 0: advance.type is required")
}
