package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newGateProxy builds an unstarted proxy on a loopback listen address, rooted
// in a temp project so captures land outside the repo.
func newGateProxy(t *testing.T) *ProxyServer {
	t.Helper()
	ps, err := NewProxyServer(ProxyConfig{
		ID: "gate", TargetURL: "http://localhost:3000", ListenPort: 8123,
		MaxLogSize: 100, Path: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { ps.pageTracker.Stop(); ps.StopChaos() })
	return ps
}

// newGateConn returns per-connection state over an in-memory writer, with
// handler goroutines run inline.
func newGateConn(t *testing.T, ps *ProxyServer, trusted bool) (*wsConnState, *stubWSConn) {
	t.Helper()
	stub := &stubWSConn{}
	writer := newAsyncWSWriter(stub, websocket.TextMessage)
	t.Cleanup(func() { _ = writer.Close() })
	return &wsConnState{
		ps:           ps,
		asyncConn:    writer,
		connID:       "conn-test",
		captures:     make(map[string]string),
		spawnHandler: func(fn func()) { fn() },
		trusted:      trusted,
		id:           "metric-1",
		timestamp:    time.Now(),
	}, stub
}

// frames decodes every frame written to the stub conn.
func (s *stubWSConn) frames(t *testing.T) []map[string]interface{} {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]map[string]interface{}, 0, len(s.msgs))
	for _, raw := range s.msgs {
		var f map[string]interface{}
		require.NoError(t, json.Unmarshal(raw, &f))
		out = append(out, f)
	}
	return out
}

func diagnosticEvents(ps *ProxyServer) []string {
	var events []string
	for _, e := range ps.Logger().Query(LogFilter{Types: []LogEntryType{LogTypeDiagnostic}, Limit: 1000}) {
		if e.Diagnostic != nil {
			events = append(events, e.Diagnostic.Event)
		}
	}
	return events
}

// controlCallerTrusted follows the dev OIDC caller classification: the
// listener and connection decide, and a header can only take trust away.
func TestControlCallerTrusted(t *testing.T) {
	local := func() *http.Request {
		return httptest.NewRequest(http.MethodGet, "http://localhost:8123/__devtool_metrics", nil)
	}

	t.Run("local request on the loopback listener", func(t *testing.T) {
		assert.True(t, newGateProxy(t).controlCallerTrusted(local()))
	})
	t.Run("relayed by a reverse proxy", func(t *testing.T) {
		r := local()
		r.Header.Set("X-Forwarded-For", "203.0.113.9")
		assert.False(t, newGateProxy(t).controlCallerTrusted(r))
	})
	t.Run("addressed to a public hostname", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "http://abc.trycloudflare.com/__devtool_metrics", nil)
		assert.False(t, newGateProxy(t).controlCallerTrusted(r))
	})
	t.Run("quick tunnel bound to the listener", func(t *testing.T) {
		ps := newGateProxy(t)
		ps.SetPublicURL("https://abc.trycloudflare.com")
		assert.False(t, ps.controlCallerTrusted(local()),
			"with a tunnel sharing the listener a loopback Host proves nothing")
	})
	t.Run("named tunnel ingress without Access", func(t *testing.T) {
		r := local()
		r = r.WithContext(context.WithValue(r.Context(), tunnelledKey{}, true))
		assert.False(t, newGateProxy(t).controlCallerTrusted(r))
	})
	t.Run("named tunnel ingress with a verified Access identity", func(t *testing.T) {
		r := local()
		ctx := context.WithValue(r.Context(), tunnelledKey{}, true)
		ctx = context.WithValue(ctx, accessIdentityKey{}, AccessIdentity{Email: "dev@example.com"})
		assert.True(t, newGateProxy(t).controlCallerTrusted(r.WithContext(ctx)))
	})
}

// An untrusted connection's secret_submit is refused before the sink: the
// sink is never called (the control), the browser is told, the agent gets a
// diagnostic, and no "secret stored" panel message is fabricated.
func TestSecretSubmit_RefusedForUntrustedCaller(t *testing.T) {
	ps := newGateProxy(t)
	var sinkCalls atomic.Int32
	ps.SetSecretSink(func(string, string) error { sinkCalls.Add(1); return nil })

	const value = "sk-live-do-not-store-1234"
	c, stub := newGateConn(t, ps, false)
	c.handleSecretSubmit(&wsMessage{Type: "secret_submit", Data: map[string]interface{}{"name": "API_KEY", "value": value}})

	assert.Zero(t, sinkCalls.Load(), "the secret sink must not be reached for an untrusted caller")
	frames := stub.frames(t)
	require.Len(t, frames, 1)
	assert.Equal(t, "secret_ack", frames[0]["type"])
	assert.Equal(t, false, frames[0]["ok"])
	assert.Equal(t, privilegedRefusalMessage, frames[0]["error"])
	assert.Contains(t, diagnosticEvents(ps), "ws_privileged_frame_refused")
	assert.Empty(t, ps.Logger().Query(LogFilter{Types: []LogEntryType{LogTypePanelMessage}}),
		"a refused submission must not be announced to the agent as a stored secret")
	for _, e := range ps.Logger().Query(LogFilter{Limit: 1000}) {
		raw, err := json.Marshal(e)
		require.NoError(t, err)
		assert.NotContains(t, string(raw), value)
	}

	// The same frame on a trusted connection reaches the sink.
	trusted, _ := newGateConn(t, ps, true)
	trusted.handleSecretSubmit(&wsMessage{Type: "secret_submit", Data: map[string]interface{}{"name": "API_KEY", "value": value}})
	assert.EqualValues(t, 1, sinkCalls.Load())
}

// countingSessionClient is a SessionClient that records how often it was
// built and called, embedding the interface so only the methods a test needs
// are implemented.
type countingSessionClient struct {
	SessionClient
	calls *atomic.Int32
}

func (c countingSessionClient) Close() error { return nil }
func (c countingSessionClient) StoreSet(protocol.StoreSetRequest) error {
	c.calls.Add(1)
	return nil
}
func (c countingSessionClient) SessionList(protocol.DirectoryFilter) (map[string]interface{}, error) {
	c.calls.Add(1)
	return map[string]interface{}{"sessions": []interface{}{}}, nil
}
func (c countingSessionClient) StoreGetAll(protocol.StoreGetAllRequest) (map[string]interface{}, error) {
	c.calls.Add(1)
	return map[string]interface{}{"blob": strings.Repeat("x", maxControlResponseBytes)}, nil
}

// installCountingClient wires a counting client and returns its counters:
// clients built, and calls made on them.
func installCountingClient(ps *ProxyServer) (built, calls *atomic.Int32) {
	built, calls = &atomic.Int32{}, &atomic.Int32{}
	ps.SetSessionClientFactory(func() (SessionClient, error) {
		built.Add(1)
		return countingSessionClient{calls: calls}, nil
	})
	return built, calls
}

func controlRequest(api, action string, params map[string]interface{}) *wsMessage {
	return &wsMessage{Type: api + "_request", Data: map[string]interface{}{
		"request_id": "r1", "action": action, "params": params,
	}}
}

// session_request and store_request are refused to an untrusted connection
// before a client is even built: nothing is read, written or sent.
func TestSessionAndStoreRequest_RefusedForUntrustedCaller(t *testing.T) {
	ps := newGateProxy(t)
	built, calls := installCountingClient(ps)

	c, stub := newGateConn(t, ps, false)
	c.handleSessionRequestMsg(controlRequest("session", "list", nil))
	c.handleStoreRequestMsg(controlRequest("store", "set", map[string]interface{}{"scope": "global", "key": "k", "value": "v"}))

	assert.Zero(t, built.Load(), "no backend client may be built for an untrusted caller")
	assert.Zero(t, calls.Load())
	frames := stub.frames(t)
	require.Len(t, frames, 2)
	for i, want := range []string{"session_response", "store_response"} {
		assert.Equal(t, want, frames[i]["type"])
		assert.Equal(t, "r1", frames[i]["request_id"])
		assert.Equal(t, privilegedRefusalMessage, frames[i]["error"])
		assert.NotContains(t, frames[i], "result")
	}
	assert.Contains(t, diagnosticEvents(ps), "ws_privileged_frame_refused")

	// The same requests on a trusted connection reach the backend.
	trusted, _ := newGateConn(t, ps, true)
	trusted.handleSessionRequestMsg(controlRequest("session", "list", nil))
	trusted.handleStoreRequestMsg(controlRequest("store", "set", map[string]interface{}{"scope": "global", "key": "k", "value": "v"}))
	assert.EqualValues(t, 2, calls.Load())
}

// A browser request for global scope is refused before the backend, and the
// filter the backend does receive never carries one.
func TestSessionRequest_GlobalScopeRefusedBeforeBackend(t *testing.T) {
	ps := newGateProxy(t)
	built, calls := installCountingClient(ps)
	c, stub := newGateConn(t, ps, true)

	c.handleSessionRequestMsg(controlRequest("session", "list", map[string]interface{}{"global": true}))
	c.handleSessionRequestMsg(controlRequest("session", "tasks", map[string]interface{}{"global": true}))
	c.handleStoreRequestMsg(controlRequest("store", "getAll", map[string]interface{}{"global": true, "scope": "global"}))

	assert.Zero(t, built.Load(), "a global-scope request must not reach the backend")
	assert.Zero(t, calls.Load())
	for _, f := range stub.frames(t) {
		assert.Equal(t, globalScopeRefusal, f["error"])
	}
	assert.Contains(t, diagnosticEvents(ps), "ws_scope_refused")
}

// Store requests are bounded before the backend sees them, and one reply is
// bounded on the way out.
func TestStoreRequest_Bounds(t *testing.T) {
	ps := newGateProxy(t)
	_, calls := installCountingClient(ps)
	c, stub := newGateConn(t, ps, true)

	big := func(n int) string { return strings.Repeat("a", n) }
	oversized := []map[string]interface{}{
		{"scope": "global", "key": big(maxStoreKeyBytes + 1), "value": "v"},
		{"scope": "page", "scope_key": big(maxStoreScopeKeyBytes + 1), "key": "k", "value": "v"},
		{"scope": "global", "key": "k", "value": big(maxStoreValueBytes)},
		{"scope": "global", "key": "k", "value": "v", "metadata": map[string]interface{}{"note": big(maxStoreMetadataBytes)}},
	}
	for _, params := range oversized {
		c.handleStoreRequestMsg(controlRequest("store", "set", params))
	}
	assert.Zero(t, calls.Load(), "an over-limit store request must not reach the backend")
	frames := stub.frames(t)
	require.Len(t, frames, len(oversized))
	for _, f := range frames {
		assert.Contains(t, f["error"], "byte limit")
	}

	// A message over the session limit is refused the same way.
	c.handleSessionRequestMsg(controlRequest("session", "send", map[string]interface{}{"code": "c", "message": big(maxSessionMessageBytes + 1)}))
	assert.Zero(t, calls.Load())

	// An over-cap result is refused whole, never truncated.
	c.handleStoreRequestMsg(controlRequest("store", "getAll", map[string]interface{}{"scope": "global"}))
	frames = stub.frames(t)
	last := frames[len(frames)-1]
	assert.Contains(t, last["error"], "over the")
	assert.NotContains(t, last, "result")
}

// fakeDeepgramSession returns a VoiceSession over a real WebSocket to a local
// stand-in, so Close has a connection to close.
func fakeDeepgramSession(t *testing.T, browser wsJSONWriter) *VoiceSession {
	t.Helper()
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return &VoiceSession{id: "v", browserConn: browser, deepgramConn: conn, keepAliveDone: make(chan struct{})}
}

func voiceClosed(vs *VoiceSession) bool {
	vs.mu.Lock()
	defer vs.mu.Unlock()
	return vs.closed
}

// stubVoiceSessions replaces the Deepgram dial for one test.
func stubVoiceSessions(t *testing.T, fn func(string, wsJSONWriter, DeepgramConfig) (*VoiceSession, error)) {
	t.Helper()
	prev := newVoiceSession
	newVoiceSession = fn
	t.Cleanup(func() { newVoiceSession = prev })
}

// voice_start spends the operator's Deepgram key, so an untrusted connection
// never gets as far as the dial.
func TestVoiceStart_RefusedForUntrustedCaller(t *testing.T) {
	ps := newGateProxy(t)
	var dials atomic.Int32
	stubVoiceSessions(t, func(string, wsJSONWriter, DeepgramConfig) (*VoiceSession, error) {
		dials.Add(1)
		return nil, assert.AnError
	})

	c, stub := newGateConn(t, ps, false)
	c.handleVoiceStart(&wsMessage{Type: "voice_start", Data: map[string]interface{}{}})

	assert.Zero(t, dials.Load(), "no Deepgram session may be opened for an untrusted caller")
	frames := stub.frames(t)
	require.Len(t, frames, 1)
	assert.Equal(t, "voice_error", frames[0]["type"])
	assert.Equal(t, privilegedRefusalMessage, frames[0]["error"])
	_, stored := ps.voiceSessions.Load(c.connID)
	assert.False(t, stored)
}

// A second voice_start closes the session it replaces, and audio that arrived
// while a dial was in flight is held as audio rather than parsed as a
// screenshot frame.
func TestVoiceStart_ReplacesAndClosesPreviousSession(t *testing.T) {
	ps := newGateProxy(t)
	c, _ := newGateConn(t, ps, true)

	first := fakeDeepgramSession(t, c.asyncConn)
	second := fakeDeepgramSession(t, c.asyncConn)
	next := []*VoiceSession{first, second}
	var mu sync.Mutex
	stubVoiceSessions(t, func(string, wsJSONWriter, DeepgramConfig) (*VoiceSession, error) {
		mu.Lock()
		defer mu.Unlock()
		vs := next[0]
		next = next[1:]
		return vs, nil
	})

	c.handleVoiceStart(&wsMessage{Type: "voice_start", Data: map[string]interface{}{}})
	got, ok := ps.voiceSessions.Load(c.connID)
	require.True(t, ok)
	require.Same(t, first, got.(*VoiceSession))

	c.handleVoiceStart(&wsMessage{Type: "voice_start", Data: map[string]interface{}{}})
	got, ok = ps.voiceSessions.Load(c.connID)
	require.True(t, ok)
	assert.Same(t, second, got.(*VoiceSession))
	assert.True(t, voiceClosed(first), "the replaced voice session must be closed")
	assert.False(t, voiceClosed(second))

	// While a dial is pending, a binary frame is audio: it is held, and no
	// screenshot file is written for it.
	c.voiceMu.Lock()
	c.voicePending++
	c.voiceMu.Unlock()
	c.handleBinaryFrame([]byte{4, 'a', 'b', 'c', 'd', 0x01, 0x02})
	c.voiceMu.Lock()
	held := len(c.voiceBacklog)
	c.voiceMu.Unlock()
	assert.Equal(t, 1, held)
	assert.Empty(t, c.captures, "audio awaiting a voice session must not be saved as a screenshot")
}

// screenshotFrame builds a binary capture frame the way the browser does:
// [1 byte: idLen][id][PNG bytes].
func screenshotFrame(id string) []byte {
	frame := append([]byte{byte(len(id))}, id...)
	frame = append(frame, pngSignature...)
	return append(frame, "IHDR-and-the-rest"...)
}

// installVoiceSession runs voice_start with the dial stubbed to return vs.
func installVoiceSession(t *testing.T, c *wsConnState, vs *VoiceSession) {
	t.Helper()
	stubVoiceSessions(t, func(string, wsJSONWriter, DeepgramConfig) (*VoiceSession, error) { return vs, nil })
	c.handleVoiceStart(&wsMessage{Type: "voice_start", Data: map[string]interface{}{}})
	got, ok := c.ps.voiceSessions.Load(c.connID)
	require.True(t, ok)
	require.Same(t, vs, got.(*VoiceSession))
}

// Binary frames are routed by content: a frame carrying the PNG signature is
// a screenshot even while dictating (it used to be fed to Deepgram and lost),
// and audio with no session to take it is dropped, never saved as a .png.
func TestBinaryFrame_RoutedByPNGSignature(t *testing.T) {
	ps := newGateProxy(t)
	c, _ := newGateConn(t, ps, true)

	// Stray audio, no voice session: dropped. Raw PCM whose first byte reads
	// as a plausible id length is the case that used to be written to disk.
	c.handleBinaryFrame([]byte{3, 'p', 'c', 'm', 0x40, 0x50, 0x60, 0x70, 0x80, 0x90, 0xa0})
	assert.Empty(t, c.captures, "audio must never be saved as a screenshot")
	assert.Zero(t, captureFiles(t, ps))

	// A screenshot taken while a voice session is live is still saved.
	installVoiceSession(t, c, fakeDeepgramSession(t, c.asyncConn))
	c.handleBinaryFrame(screenshotFrame("shot1"))
	assert.Contains(t, c.captures, "shot1", "a screenshot during dictation must be saved, not sent to Deepgram")
	assert.Equal(t, 1, captureFiles(t, ps))
}

// When Deepgram drops the connection the session closes itself; it must leave
// the registry then (by identity), not linger as a dead session that later
// frames are fed to.
func TestVoiceSession_RetiredWhenDeepgramDrops(t *testing.T) {
	ps := newGateProxy(t)
	c, _ := newGateConn(t, ps, true)

	first := fakeDeepgramSession(t, c.asyncConn)
	installVoiceSession(t, c, first)
	first.Close() // what readDeepgramResponses does when the socket drops

	_, ok := ps.voiceSessions.Load(c.connID)
	assert.False(t, ok, "a session Deepgram dropped must be unregistered")

	// A replacement is not retired by an older session's close.
	second := fakeDeepgramSession(t, c.asyncConn)
	stale := fakeDeepgramSession(t, c.asyncConn)
	installVoiceSession(t, c, stale)
	installVoiceSession(t, c, second) // closes stale, whose hook must not remove second
	got, ok := ps.voiceSessions.Load(c.connID)
	require.True(t, ok)
	assert.Same(t, second, got.(*VoiceSession))
}

// A voice_stop that arrives while the Deepgram dial is in flight stops it: the
// dialled session is closed and never installed, and the stop is acknowledged.
func TestVoiceStop_DuringDialCancelsInstall(t *testing.T) {
	ps := newGateProxy(t)
	c, stub := newGateConn(t, ps, true)

	dialled := fakeDeepgramSession(t, c.asyncConn)
	stubVoiceSessions(t, func(string, wsJSONWriter, DeepgramConfig) (*VoiceSession, error) {
		// The read loop handles voice_stop while this dial is still running.
		c.handleVoiceStop(&wsMessage{Type: "voice_stop"})
		return dialled, nil
	})
	c.handleVoiceStart(&wsMessage{Type: "voice_start", Data: map[string]interface{}{}})

	_, ok := ps.voiceSessions.Load(c.connID)
	assert.False(t, ok, "a session stopped mid-dial must not be installed")
	assert.True(t, voiceClosed(dialled), "the dialled session must be closed")
	types := []interface{}{}
	for _, f := range stub.frames(t) {
		types = append(types, f["type"])
	}
	assert.Contains(t, types, "voice_stopped")
	c.voiceMu.Lock()
	defer c.voiceMu.Unlock()
	assert.Zero(t, c.voicePending)
	assert.Empty(t, c.voiceBacklog)
}

// A browser-supplied attachment path is forwarded to the agent only when it
// names a capture this proxy wrote.
func TestOwnedCapturePath(t *testing.T) {
	ps := newGateProxy(t)

	owned, err := ps.savePNGBytes("area-abc", []byte("png"))
	require.NoError(t, err)
	got, ok := ps.ownedCapturePath(owned)
	require.True(t, ok, "a capture the proxy just wrote must be accepted")
	assert.FileExists(t, got)

	shots := filepath.Dir(owned)
	outside := filepath.Join(t.TempDir(), "screenshot-gate-area-abc.png")
	require.NoError(t, os.WriteFile(outside, []byte("x"), 0o600))
	unprefixed := filepath.Join(shots, "notes.txt")
	require.NoError(t, os.WriteFile(unprefixed, []byte("x"), 0o600))
	otherProxy := filepath.Join(shots, "screenshot-other-area-abc.png")
	require.NoError(t, os.WriteFile(otherProxy, []byte("x"), 0o600))
	nested := filepath.Join(shots, "sub")
	require.NoError(t, os.MkdirAll(nested, 0o755))
	nestedFile := filepath.Join(nested, "screenshot-gate-x.png")
	require.NoError(t, os.WriteFile(nestedFile, []byte("x"), 0o600))

	refused := map[string]string{
		"empty":                      "",
		"outside the capture dirs":   outside,
		"no capture prefix":          unprefixed,
		"another proxy's capture":    otherProxy,
		"nested below a capture dir": nestedFile,
		"traversal out of the dir":   filepath.Join(shots, "..", "..", "..", "screenshot-gate-x.png"),
		"does not exist":             filepath.Join(shots, "screenshot-gate-missing.png"),
		"a directory":                shots,
	}
	for name, p := range refused {
		_, ok := ps.ownedCapturePath(p)
		assert.Falsef(t, ok, "%s: %q must be refused", name, p)
	}
}

// panel_message end to end: a foreign path is dropped from everything that is
// forwarded (the attachment's own field and its data map) and the drop is
// reported; a path naming the proxy's own capture is kept.
func TestPanelMessage_DropsForeignAttachmentPath(t *testing.T) {
	ps := newGateProxy(t)
	c, _ := newGateConn(t, ps, true)

	secret := filepath.Join(t.TempDir(), "id_rsa")
	require.NoError(t, os.WriteFile(secret, []byte("PRIVATE"), 0o600))
	owned, err := ps.savePNGBytes("area-ok", []byte("png"))
	require.NoError(t, err)

	c.handlePanelMessage(&wsMessage{Type: "panel_message", Data: map[string]interface{}{
		"payload": map[string]interface{}{
			"message": "look at this",
			"attachments": []interface{}{
				map[string]interface{}{"type": "screenshot", "id": "s1", "filePath": secret},
				map[string]interface{}{"type": "element", "id": "e1", "filePath": secret},
				map[string]interface{}{"type": "screenshot", "id": "s2", "filePath": owned},
			},
		},
	}})

	entries := ps.Logger().Query(LogFilter{Types: []LogEntryType{LogTypePanelMessage}})
	require.Len(t, entries, 1)
	atts := entries[0].PanelMessage.Attachments
	require.Len(t, atts, 3)

	for _, att := range atts[:2] {
		assert.Emptyf(t, att.FilePath, "attachment %s kept a foreign path", att.ID)
		assert.NotContainsf(t, att.Data, "file_path", "attachment %s kept a foreign path in its data", att.ID)
	}
	raw, err := json.Marshal(entries[0])
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "id_rsa", "the foreign path must not reach the agent in any field")

	assert.NotEmpty(t, atts[2].FilePath, "the proxy's own capture must be kept")
	assert.Equal(t, atts[2].FilePath, atts[2].Data["file_path"])
	assert.Contains(t, diagnosticEvents(ps), "attachment_path_refused")
}

// The frame limiter: a burst passes untouched, the frame after it waits, time
// refills it, and a large frame costs in proportion to its size. Driven by
// injected timestamps, never the clock.
func TestWSFrameLimiter(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	l := newWSFrameLimiter(now)
	for i := 0; i < wsFrameBurst; i++ {
		require.Zerof(t, l.reserve(now, 10), "frame %d is inside the burst", i)
	}
	wait := l.reserve(now, 10)
	assert.Equal(t, time.Second/wsFrameRatePerSecond, wait, "the first frame past the burst waits one token's worth")

	// A long idle period refills to the burst and no further.
	now = now.Add(time.Hour)
	for i := 0; i < wsFrameBurst; i++ {
		require.Zero(t, l.reserve(now, 10))
	}
	assert.Positive(t, l.reserve(now, 10))

	// One maximum-size frame spends more than half a burst; two exceed it.
	big := newWSFrameLimiter(now)
	require.Zero(t, big.reserve(now, wsMaxMessageBytes))
	assert.Positive(t, big.reserve(now, wsMaxMessageBytes), "a second 16 MiB frame in the same instant must wait")
}

// blockingWSConn is a wsWriter whose WriteMessage blocks until Close — a peer
// that has stopped reading.
type blockingWSConn struct {
	unblock   chan struct{}
	once      sync.Once
	writing   chan struct{}
	deadlines atomic.Int32
}

func newBlockingWSConn() *blockingWSConn {
	return &blockingWSConn{unblock: make(chan struct{}), writing: make(chan struct{}, 8)}
}

func (b *blockingWSConn) WriteMessage(int, []byte) error {
	b.writing <- struct{}{}
	<-b.unblock
	return websocket.ErrCloseSent
}

func (b *blockingWSConn) Close() error {
	b.once.Do(func() { close(b.unblock) })
	return nil
}

func (b *blockingWSConn) SetWriteDeadline(time.Time) error {
	b.deadlines.Add(1)
	return nil
}

// Close must close the conn before it waits for the drain. With the drain
// parked in a write to a non-reading peer, the old order (wait, then close)
// never returned, and took ProxyServer.Stop with it.
func TestAsyncWSWriter_CloseUnblocksStalledWrite(t *testing.T) {
	conn := newBlockingWSConn()
	w := newAsyncWSWriter(conn, websocket.TextMessage)
	require.NoError(t, w.WriteMessage(websocket.TextMessage, []byte("x")))

	select {
	case <-conn.writing:
	case <-time.After(5 * time.Second):
		t.Fatal("drain never started the write")
	}
	assert.Positive(t, conn.deadlines.Load(), "every frame write must be given a deadline first")

	// A control write queued behind the stalled frame must be released too.
	syncDone := make(chan error, 1)
	go func() { syncDone <- w.WriteSync([]byte("y")) }()

	closed := make(chan struct{})
	go func() { _ = w.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close hung behind a write to a peer that is not reading")
	}
	select {
	case err := <-syncDone:
		assert.Error(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("a pending control write was not released by Close")
	}
	assert.NoError(t, w.Close(), "Close stays safe to repeat")
}
