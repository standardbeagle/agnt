package proxy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tinyPNGDataURL is a data URL saveScreenshot accepts.
const tinyPNGDataURL = "data:image/png;base64,iVBORw0KGgo="

// captureFiles counts the files under the proxy's screenshot directory.
func captureFiles(t *testing.T, ps *ProxyServer) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(ps.Path, ".agnt", AuditDirName, "screenshots"))
	if os.IsNotExist(err) {
		return 0
	}
	require.NoError(t, err)
	return len(entries)
}

// State-changing control frames from an untrusted connection are refused, each
// judged by the effect it would have had. Every case first shows the effect
// happening for a trusted connection, so "nothing happened" below is the gate.
func TestUntrustedControlFrames_Refused(t *testing.T) {
	binaryShot := append([]byte{1, 'x'}, []byte("png-bytes")...)

	cases := []struct {
		name   string
		send   func(c *wsConnState)
		effect func(t *testing.T, ps *ProxyServer) bool // did the frame take effect?
	}{
		{
			name:   "frame_active",
			send:   func(c *wsConnState) { dispatch(c, &wsMessage{Type: "frame_active", FrameID: "abc123"}) },
			effect: func(_ *testing.T, ps *ProxyServer) bool { return ps.ActiveFrame() == "abc123" },
		},
		{
			name: "chaos_request",
			send: func(c *wsConnState) {
				dispatch(c, &wsMessage{Type: "chaos_request", Data: map[string]interface{}{"request_id": "r1", "action": "enable"}})
			},
			effect: func(_ *testing.T, ps *ProxyServer) bool { return ps.chaosEngine.IsEnabled() },
		},
		{
			name: "screenshot",
			send: func(c *wsConnState) {
				dispatch(c, &wsMessage{Type: "screenshot", Data: map[string]interface{}{"name": "shot", "data": tinyPNGDataURL}})
			},
			effect: func(t *testing.T, ps *ProxyServer) bool { return captureFiles(t, ps) > 0 },
		},
		{
			name: "sketch_capture",
			send: func(c *wsConnState) {
				dispatch(c, &wsMessage{Type: "sketch_capture", Data: map[string]interface{}{
					"id": "sk1", "data": map[string]interface{}{"image": tinyPNGDataURL},
				}})
			},
			effect: func(t *testing.T, ps *ProxyServer) bool { return captureFiles(t, ps) > 0 },
		},
		{
			name:   "binary screenshot frame",
			send:   func(c *wsConnState) { c.handleBinaryFrame(binaryShot) },
			effect: func(t *testing.T, ps *ProxyServer) bool { return captureFiles(t, ps) > 0 },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ps := newGateProxy(t)
			trusted, _ := newGateConn(t, ps, true)
			tc.send(trusted)
			if !tc.effect(t, ps) {
				t.Fatalf("premise broken: a trusted %s must take effect", tc.name)
			}

			ps = newGateProxy(t)
			untrusted, stub := newGateConn(t, ps, false)
			tc.send(untrusted)
			assert.False(t, tc.effect(t, ps), "an untrusted %s must not take effect", tc.name)
			assert.Contains(t, diagnosticEvents(ps), "ws_privileged_frame_refused")
			assert.Empty(t, untrusted.captures, "nothing may be held for a later attachment")

			if tc.name == "chaos_request" {
				// The panel that sent it has a reply slot: it is told why.
				require.Eventually(t, func() bool { return stub.count() == 1 }, 3*time.Second, 5*time.Millisecond)
				reply := stub.frames(t)[0]
				assert.Equal(t, "chaos_response", reply["type"])
				assert.Equal(t, "r1", reply["request_id"])
				assert.Contains(t, reply["error"], "requires a local, tailnet or Cloudflare Access-verified caller")
				assert.NotContains(t, reply, "result")
			}
		})
	}
}

// registerConn puts a real asyncWSWriter in the proxy's connection registry,
// as handleWebSocket does, and returns its per-connection state.
func registerConn(t *testing.T, ps *ProxyServer, connID string, trusted bool) (*wsConnState, *stubWSConn) {
	t.Helper()
	c, stub := newGateConn(t, ps, trusted)
	c.connID = connID
	c.asyncConn.trusted = trusted
	ps.wsConns.Store(connID, c.asyncConn)
	return c, stub
}

func framesOfType(t *testing.T, stub *stubWSConn, frameType string) []map[string]interface{} {
	t.Helper()
	var out []map[string]interface{}
	for _, f := range stub.frames(t) {
		if f["type"] == frameType {
			out = append(out, f)
		}
	}
	return out
}

func executionReply(execID, result string) *wsMessage {
	return &wsMessage{Type: "execution", Data: map[string]interface{}{"exec_id": execID, "result": result}}
}

// An exec is sent only to trusted connections, and only a connection it was
// sent to may answer it. Knowing the exec id is not enough: an untrusted
// connection that guesses it, and a trusted connection that was never a
// target, are both dropped — and the exec stays pending for the real page.
func TestExec_ReplyIsBoundToTheTargetConnection(t *testing.T) {
	ps := newGateProxy(t)
	page, pageStub := registerConn(t, ps, "conn-page", true)
	visitor, visitorStub := registerConn(t, ps, "conn-visitor", false)

	execID, resultCh, err := ps.ExecuteJavaScript("document.title")
	require.NoError(t, err)

	// The code went to the trusted page and not to the visitor.
	require.Eventually(t, func() bool { return len(framesOfType(t, pageStub, "execute")) == 1 },
		3*time.Second, 5*time.Millisecond, "the trusted page must be sent the exec")
	assert.Empty(t, framesOfType(t, visitorStub, "execute"), "an untrusted connection must not be sent the agent's code")

	stillPending := func() bool {
		_, ok := ps.pendingExecs.Load(execID)
		return ok
	}
	noExecutionLogged := func() {
		assert.Empty(t, ps.Logger().Query(LogFilter{Types: []LogEntryType{LogTypeExecution}}),
			"a refused result must not be logged as the page's answer")
	}

	// 1. The visitor echoes the id with a forged result.
	dispatch(visitor, executionReply(execID, "FORGED-BY-VISITOR"))
	require.True(t, stillPending(), "a forged result must not complete the exec")
	noExecutionLogged()

	// 2. A trusted connection the exec was never sent to (it connected later).
	late, _ := registerConn(t, ps, "conn-late", true)
	dispatch(late, executionReply(execID, "FROM-A-NON-TARGET"))
	require.True(t, stillPending(), "a non-target connection must not complete the exec")
	noExecutionLogged()
	assert.Contains(t, diagnosticEvents(ps), "ws_exec_result_refused")

	select {
	case got := <-resultCh:
		t.Fatalf("the waiter received a result nobody was entitled to send: %+v", got)
	default:
	}

	// 3. The page it was sent to answers: delivered.
	dispatch(page, executionReply(execID, "the real title"))
	select {
	case got := <-resultCh:
		require.NotNil(t, got)
		assert.Equal(t, "the real title", got.Result)
	case <-time.After(3 * time.Second):
		t.Fatal("the target connection's result was not delivered")
	}
	assert.False(t, stillPending())
}

// With only untrusted connections there is nobody to run an exec.
func TestExec_NotSentWhenOnlyUntrustedConnections(t *testing.T) {
	ps := newGateProxy(t)
	_, visitorStub := registerConn(t, ps, "conn-visitor", false)

	_, _, err := ps.ExecuteJavaScript("1+1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no connected clients")
	assert.Empty(t, framesOfType(t, visitorStub, "execute"))
}

// The agent's own material — its terminal output, its tool calls, toasts and
// the secret-entry prompt — is broadcast to trusted connections only.
func TestAgentBroadcasts_NotSentToUntrustedConnections(t *testing.T) {
	ps := newGateProxy(t)
	_, pageStub := registerConn(t, ps, "conn-page", true)
	_, visitorStub := registerConn(t, ps, "conn-visitor", false)

	ps.BroadcastOutputPreview([]string{"$ cat .env", "API_KEY=..."}, "")
	ps.BroadcastToolEvent("Bash", "call", "rm -rf build")
	_, err := ps.BroadcastToastOpts(ToastOptions{Type: "info", Title: "Enter secret", Input: "secret", Name: "API_KEY"})
	require.NoError(t, err)

	require.Eventually(t, func() bool { return pageStub.count() == 3 }, 3*time.Second, 5*time.Millisecond,
		"premise: the trusted page receives all three broadcasts")
	assert.Zero(t, visitorStub.count(), "an untrusted connection must receive none of them")
}

// telemetryFrames are the frame types an untrusted connection may still send:
// what a page reports about itself. Each carries the marker text so the test
// can find it.
func telemetryFrames() []*wsMessage {
	event := map[string]interface{}{"event_type": "click", "mutation_type": "added", "value": agentInputMarker}
	return []*wsMessage{
		{Type: "error", Data: map[string]interface{}{"message": "TypeError: " + agentInputMarker}},
		{Type: "custom_log", Data: map[string]interface{}{"level": "error", "message": agentInputMarker}},
		{Type: "performance", Data: map[string]interface{}{"page_title": agentInputMarker}},
		{Type: "interactions", Data: map[string]interface{}{"events": []interface{}{event}}},
		{Type: "mutations", Data: map[string]interface{}{"events": []interface{}{event}}},
		{Type: "walkthrough_event", Data: map[string]interface{}{"event": "step", "message": agentInputMarker}},
		{Type: "element_capture", Data: map[string]interface{}{"id": "e1", "selector": agentInputMarker}},
	}
}

func nonDiagnosticEntries(ps *ProxyServer) []LogEntry {
	var out []LogEntry
	for _, e := range ps.Logger().Query(LogFilter{Limit: 1000}) {
		if e.Type != LogTypeDiagnostic {
			out = append(out, e)
		}
	}
	return out
}

// Telemetry from an untrusted connection is kept — the operator can see it —
// and every entry of it is marked. From a trusted connection nothing is.
func TestTelemetry_MarkedUntrustedByConnection(t *testing.T) {
	frames := telemetryFrames()

	ps := newGateProxy(t)
	untrusted, _ := newGateConn(t, ps, false)
	for _, msg := range frames {
		dispatch(untrusted, msg)
	}
	entries := nonDiagnosticEntries(ps)
	if len(entries) != len(frames) {
		t.Fatalf("premise broken: each of the %d telemetry frames must be recorded, got %d entries", len(frames), len(entries))
	}
	for _, e := range entries {
		assert.Truef(t, e.Untrusted, "%s from an untrusted connection must be marked", e.Type)
		raw, err := json.Marshal(e)
		require.NoError(t, err)
		assert.Containsf(t, string(raw), `"untrusted":true`, "%s must carry the marker on the wire", e.Type)
	}
	// The payloads that travel without their envelope carry it too.
	for _, e := range entries {
		switch e.Type {
		case LogTypeError:
			assert.True(t, e.Error.Untrusted)
		case LogTypeInteraction:
			assert.True(t, e.Interaction.Untrusted)
		case LogTypeMutation:
			assert.True(t, e.Mutation.Untrusted)
		}
	}

	ps = newGateProxy(t)
	trusted, _ := newGateConn(t, ps, true)
	for _, msg := range frames {
		dispatch(trusted, msg)
	}
	entries = nonDiagnosticEntries(ps)
	require.Len(t, entries, len(frames))
	for _, e := range entries {
		assert.Falsef(t, e.Untrusted, "%s from a trusted connection must not be marked", e.Type)
		raw, err := json.Marshal(e)
		require.NoError(t, err)
		assert.NotContains(t, string(raw), "untrusted")
	}
}

// Every frame type the proxy handles, sent by an untrusted connection, leaves
// behind only marked entries (or the refusal diagnostics). A new handler that
// logs through the typed Log* methods instead of record fails here.
func TestUntrustedConnectionEntriesAreAllMarked(t *testing.T) {
	payload := map[string]interface{}{
		"message": agentInputMarker, "level": "error", "name": "n", "data": tinyPNGDataURL,
		"exec_id": "exec-x", "result": agentInputMarker, "request_id": "r", "action": "status",
		"id": "i", "selector": agentInputMarker, "event": "step",
		"events":  []interface{}{map[string]interface{}{"event_type": "click"}},
		"payload": map[string]interface{}{"message": agentInputMarker},
		"value":   "v",
	}
	for msgType := range wsMessageHandlers {
		t.Run(msgType, func(t *testing.T) {
			ps := newGateProxy(t)
			c, _ := newGateConn(t, ps, false)
			dispatch(c, &wsMessage{Type: msgType, Data: payload, FrameID: "abc123"})
			for _, e := range nonDiagnosticEntries(ps) {
				assert.Truef(t, e.Untrusted, "a %s entry recorded for an untrusted %s frame is not marked", e.Type, msgType)
			}
		})
	}
}

// Entries the proxy originates are not browser input and are never marked.
func TestServerOriginatedEntriesAreNotMarked(t *testing.T) {
	ps := newGateProxy(t)
	ps.logger.LogHTTP(HTTPLogEntry{ID: "req-1", Method: "GET", URL: "/api", StatusCode: 500})
	ps.logger.LogDiagnostic(ProxyDiagnostic{Level: DiagnosticError, Category: "proxy", Event: "connection_refused"})
	ps.logger.LogCustom(CustomLog{Level: "info", Message: "[Voice] Transcription session started"})

	entries := ps.Logger().Query(LogFilter{Limit: 1000})
	require.Len(t, entries, 3)
	for _, e := range entries {
		assert.Falsef(t, e.Untrusted, "a server-originated %s entry must not be marked", e.Type)
		raw, err := json.Marshal(e)
		require.NoError(t, err)
		assert.NotContains(t, string(raw), "untrusted")
	}
}

// The registry default is untrusted: a writer nobody stamped is not sent the
// agent's material. (Guards the zero value of asyncWSWriter.trusted.)
func TestConnTrusted_DefaultsToUntrusted(t *testing.T) {
	w := newAsyncWSWriter(&stubWSConn{}, websocket.TextMessage)
	t.Cleanup(func() { _ = w.Close() })
	assert.False(t, connTrusted(w))
	w.trusted = true
	assert.True(t, connTrusted(w))
}
