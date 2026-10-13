package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/standardbeagle/agnt/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// injectCapture is a stand-in agent overlay: it records every message POSTed
// to /inject, which is where SESSION SEND (and a delivered scheduled task)
// puts text bound for the agent's terminal.
type injectCapture struct {
	mu       sync.Mutex
	messages []string
}

func (ic *injectCapture) all() []string {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	return append([]string(nil), ic.messages...)
}

func startInjectCapture(t *testing.T) (*injectCapture, string) {
	t.Helper()
	ic := &injectCapture{}
	socketPath := filepath.Join(shortTempDir(t), "o.sock")
	ln, err := net.Listen("unix", socketPath)
	require.NoError(t, err)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		ic.mu.Lock()
		ic.messages = append(ic.messages, string(body))
		ic.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ic, socketPath
}

// browserAPI drives a proxy's control WebSocket the way scripts/session.js and
// scripts/store.js do: {type, data:{request_id, action, params}} out, a
// {type:"<api>_response", request_id, result|error} frame back.
type browserAPI struct {
	t    *testing.T
	conn *websocket.Conn
	seq  int
}

func dialBrowserAPI(t *testing.T, ps *proxy.ProxyServer) *browserAPI {
	t.Helper()
	u := url.URL{Scheme: "ws", Host: ps.ListenAddr, Path: "/__devtool_metrics"}
	header := http.Header{"Origin": {"http://" + ps.ListenAddr}}
	var conn *websocket.Conn
	require.Eventually(t, func() bool {
		c, _, err := websocket.DefaultDialer.Dial(u.String(), header)
		conn = c
		return err == nil
	}, 5*time.Second, 20*time.Millisecond, "the proxy's control websocket must accept a client")
	t.Cleanup(func() { _ = conn.Close() })
	return &browserAPI{t: t, conn: conn}
}

// call sends one request and returns its result, or the error string the
// server replied with.
func (b *browserAPI) call(api, action string, params map[string]interface{}) (json.RawMessage, string) {
	b.t.Helper()
	b.seq++
	requestID := fmt.Sprintf("%s_%d", api, b.seq)
	require.NoError(b.t, b.conn.WriteJSON(map[string]interface{}{
		"type": api + "_request",
		"data": map[string]interface{}{"request_id": requestID, "action": action, "params": params},
	}))
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		_ = b.conn.SetReadDeadline(deadline)
		_, raw, err := b.conn.ReadMessage()
		require.NoError(b.t, err, "waiting for the %s %s response", api, action)
		var frame struct {
			Type      string          `json:"type"`
			RequestID string          `json:"request_id"`
			Result    json.RawMessage `json:"result"`
			Error     string          `json:"error"`
		}
		if json.Unmarshal(raw, &frame) != nil || frame.Type != api+"_response" || frame.RequestID != requestID {
			continue // broadcasts interleave
		}
		return frame.Result, frame.Error
	}
	b.t.Fatalf("no %s %s response", api, action)
	return nil, ""
}

// ok is call for a request that must succeed.
func (b *browserAPI) ok(api, action string, params map[string]interface{}) map[string]interface{} {
	b.t.Helper()
	result, errMsg := b.call(api, action, params)
	require.Emptyf(b.t, errMsg, "%s %s", api, action)
	var out map[string]interface{}
	require.NoError(b.t, json.Unmarshal(result, &out), "%s %s result: %s", api, action, result)
	return out
}

// refused is call for a request that must be refused; it returns the reason.
func (b *browserAPI) refused(api, action string, params map[string]interface{}) string {
	b.t.Helper()
	result, errMsg := b.call(api, action, params)
	require.NotEmptyf(b.t, errMsg, "%s %s must be refused, got result %s", api, action, result)
	require.Emptyf(b.t, string(result), "a refused %s %s must carry no result", api, action)
	return errMsg
}

// daemonProxy creates a proxy the way every daemon creation path does: through
// the manager, then the wireProxyLogger chokepoint.
func daemonProxy(t *testing.T, d *Daemon, id, projectPath string) *proxy.ProxyServer {
	t.Helper()
	ps, err := d.proxym.Create(context.Background(), proxy.ProxyConfig{
		ID: id, TargetURL: ephemeralTargetURL(t), ListenPort: 0, MaxLogSize: 100, Path: projectPath,
	})
	require.NoError(t, err)
	d.wireProxyLogger(ps)
	<-ps.Ready()
	return ps
}

func proxyDiagnosticEvents(ps *proxy.ProxyServer) []string {
	var events []string
	for _, e := range ps.Logger().Query(proxy.LogFilter{Types: []proxy.LogEntryType{proxy.LogTypeDiagnostic}, Limit: 1000}) {
		if e.Diagnostic != nil {
			events = append(events, e.Diagnostic.Event)
		}
	}
	return events
}

// twoProjects boots a daemon with a session and a proxy in project A, and a
// session (with its own overlay) plus stored data in project B.
type twoProjectFixture struct {
	d            *Daemon
	dirA, dirB   string
	proxyA       *proxy.ProxyServer
	injA, injB   *injectCapture
	codeA, codeB string
}

func newTwoProjectFixture(t *testing.T) *twoProjectFixture {
	t.Helper()
	// No t.Parallel: the daemon binds real listeners.
	d := NewForTest(t, defaultTestDaemonConfig(t))
	f := &twoProjectFixture{
		d:     d,
		dirA:  normalizePath(shortTempDir(t)),
		dirB:  normalizePath(shortTempDir(t)),
		codeA: "claude-aaaa1111",
		codeB: "claude-bbbb2222",
	}
	var sockA, sockB string
	f.injA, sockA = startInjectCapture(t)
	f.injB, sockB = startInjectCapture(t)
	activeSession(t, d.sessionRegistry, f.codeA, f.dirA, sockA)
	activeSession(t, d.sessionRegistry, f.codeB, f.dirB, sockB)
	f.proxyA = daemonProxy(t, d, "proj-a:dev", f.dirA)
	return f
}

// A proxy created by the daemon answers the browser store API, and what it
// reads and writes is its own project's store: every action store.js emits,
// then project B's data proven invisible.
func TestBrowserStoreAPI_RoundTripScopedToProxyProject(t *testing.T) {
	f := newTwoProjectFixture(t)
	api := dialBrowserAPI(t, f.proxyA)

	page := map[string]interface{}{"scope": "page", "scope_key": "http://localhost:3000/app/users"}
	with := func(extra map[string]interface{}) map[string]interface{} {
		out := map[string]interface{}{}
		for k, v := range page {
			out[k] = v
		}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}

	// set → get round trip.
	api.ok("store", "set", with(map[string]interface{}{"key": "draft", "value": map[string]interface{}{"n": 3}}))
	got := api.ok("store", "get", with(map[string]interface{}{"key": "draft"}))
	assert.Equal(t, map[string]interface{}{"n": float64(3)}, got["value"])

	// It landed in project A's store, on disk, under A's path — not B's.
	entry, err := f.d.storem.Get(f.dirA, "page", "http://localhost:3000/app/users", "draft")
	require.NoError(t, err)
	assert.Equal(t, map[string]interface{}{"n": float64(3)}, entry.Value)
	_, err = f.d.storem.Get(f.dirB, "page", "http://localhost:3000/app/users", "draft")
	assert.ErrorIs(t, err, store.ErrNotFound)

	// list / getAll.
	assert.Equal(t, []interface{}{"draft"}, api.ok("store", "list", page)["keys"])
	assert.Contains(t, api.ok("store", "getAll", page)["entries"], "draft")

	// Project B's data, in the same scope and key, is not reachable from A's
	// proxy by any read.
	require.NoError(t, f.d.storem.Set(f.dirB, store.ScopeGlobal, "", "b-only", "project-b-value", nil))
	global := map[string]interface{}{"scope": "global"}
	assert.NotContains(t, api.ok("store", "getAll", global)["entries"], "b-only")
	assert.Empty(t, api.ok("store", "list", global)["keys"])
	api.refused("store", "get", map[string]interface{}{"scope": "global", "key": "b-only"})

	// delete, then clear.
	api.ok("store", "delete", with(map[string]interface{}{"key": "draft"}))
	api.refused("store", "get", with(map[string]interface{}{"key": "draft"}))
	api.ok("store", "set", with(map[string]interface{}{"key": "again", "value": "x"}))
	api.ok("store", "clear", page)
	assert.Empty(t, api.ok("store", "list", page)["keys"])

	// B's store was never touched by any of it.
	bEntry, err := f.d.storem.Get(f.dirB, store.ScopeGlobal, "", "b-only")
	require.NoError(t, err)
	assert.Equal(t, "project-b-value", bEntry.Value)
}

// Every action session.js emits, scoped to the proxy's project: project B's
// session and task are invisible and unaddressable from project A's proxy.
func TestBrowserSessionAPI_ScopedToProxyProject(t *testing.T) {
	f := newTwoProjectFixture(t)
	api := dialBrowserAPI(t, f.proxyA)

	// list: A's session only, in the trimmed browser view.
	listed := api.ok("session", "list", map[string]interface{}{"global": false})
	sessions := listed["sessions"].([]interface{})
	require.Len(t, sessions, 1)
	only := sessions[0].(map[string]interface{})
	assert.Equal(t, f.codeA, only["code"])
	assert.NotContains(t, only, "overlay_path", "the browser view must not carry the overlay socket path")
	assert.NotContains(t, only, "owner_pid")
	assert.Equal(t, false, listed["global"])

	// get: A's by code; B's code is "not found", like a code that does not exist.
	assert.Equal(t, f.codeA, api.ok("session", "get", map[string]interface{}{"code": f.codeA})["code"])
	otherProject := api.refused("session", "get", map[string]interface{}{"code": f.codeB})
	unknown := api.refused("session", "get", map[string]interface{}{"code": "claude-00000000"})
	assert.Contains(t, otherProject, "not found in this proxy's project")
	assert.Contains(t, unknown, "not found in this proxy's project")

	// send: reaches A's overlay; a send addressed to B's session is refused
	// and B's overlay receives nothing.
	sent := api.ok("session", "send", map[string]interface{}{"code": f.codeA, "message": "hello from the page"})
	assert.Equal(t, true, sent["success"])
	assert.Equal(t, []string{"hello from the page"}, f.injA.all())
	api.refused("session", "send", map[string]interface{}{"code": f.codeB, "message": "cross-project"})
	assert.Empty(t, f.injB.all(), "a message must never reach another project's agent")

	// schedule: for A's session; refused for B's, and nothing is queued for it.
	scheduled := api.ok("session", "schedule", map[string]interface{}{"code": f.codeA, "duration": "1h", "message": "later"})
	taskA := scheduled["task_id"].(string)
	require.NotEmpty(t, taskA)
	api.refused("session", "schedule", map[string]interface{}{"code": f.codeB, "duration": "1h", "message": "cross-project"})
	assert.Empty(t, f.d.scheduler.ListTasks(f.dirB, false), "no task may be scheduled into another project")

	// tasks: A's task only, even with one pending in B.
	taskB, err := f.d.scheduler.Schedule(f.codeB, time.Hour, "b's own", f.dirB)
	require.NoError(t, err)
	tasks := api.ok("session", "tasks", map[string]interface{}{})["tasks"].([]interface{})
	require.Len(t, tasks, 1)
	assert.Equal(t, taskA, tasks[0].(map[string]interface{})["id"])

	// cancel: B's task is refused and stays pending; A's is cancelled.
	api.refused("session", "cancel", map[string]interface{}{"task_id": taskB.ID})
	stillThere, ok := f.d.scheduler.GetTask(taskB.ID)
	require.True(t, ok)
	assert.Equal(t, TaskStatusPending, stillThere.Status(), "another project's task must not be cancellable")
	api.ok("session", "cancel", map[string]interface{}{"task_id": taskA})
}

// Browser-supplied global scope is refused — not narrowed to the project — on
// both list and tasks, and the refusal is reported.
func TestBrowserSessionAPI_GlobalScopeRefused(t *testing.T) {
	f := newTwoProjectFixture(t)
	api := dialBrowserAPI(t, f.proxyA)

	for _, action := range []string{"list", "tasks"} {
		reason := api.refused("session", action, map[string]interface{}{"global": true})
		assert.Containsf(t, reason, "global scope is not available", "session %s", action)
	}
	assert.Contains(t, proxyDiagnosticEvents(f.proxyA), "ws_scope_refused")
}

// An untrusted caller — here, any connection made once a quick tunnel shares
// the proxy's listener — is refused both APIs before anything is read,
// written or sent.
func TestBrowserSessionStoreAPI_UntrustedCallerRefused(t *testing.T) {
	f := newTwoProjectFixture(t)
	f.proxyA.SetPublicURL("https://abc.trycloudflare.com")
	api := dialBrowserAPI(t, f.proxyA)

	reason := api.refused("store", "set", map[string]interface{}{"scope": "global", "key": "k", "value": "v"})
	assert.Contains(t, reason, "requires a local, tailnet or Cloudflare Access-verified caller")
	_, err := f.d.storem.Get(f.dirA, store.ScopeGlobal, "", "k")
	assert.ErrorIs(t, err, store.ErrNotFound, "an untrusted caller's write must not reach the store")

	reason = api.refused("session", "send", map[string]interface{}{"code": f.codeA, "message": "from the tunnel"})
	assert.Contains(t, reason, "requires a local, tailnet or Cloudflare Access-verified caller")
	assert.Empty(t, f.injA.all(), "an untrusted caller's message must not reach the agent")

	api.refused("session", "list", map[string]interface{}{})
	api.refused("session", "schedule", map[string]interface{}{"code": f.codeA, "duration": "1h", "message": "x"})
	assert.Empty(t, f.d.scheduler.ListTasks(f.dirA, false))
	assert.Contains(t, proxyDiagnosticEvents(f.proxyA), "ws_privileged_frame_refused")
}

// With no session running for the proxy's project, session operations fail
// closed with a clear message. They never fall through to another project's
// session.
func TestBrowserSessionAPI_NoProjectSessionFailsClosed(t *testing.T) {
	f := newTwoProjectFixture(t)
	orphan := daemonProxy(t, f.d, "proj-c:dev", normalizePath(shortTempDir(t)))
	api := dialBrowserAPI(t, orphan)

	for action, params := range map[string]map[string]interface{}{
		"list":     {},
		"tasks":    {},
		"get":      {"code": f.codeA},
		"send":     {"code": f.codeA, "message": "x"},
		"schedule": {"code": f.codeA, "duration": "1h", "message": "x"},
	} {
		reason := api.refused("session", action, params)
		assert.Containsf(t, reason, "no agnt session is running for this proxy's project", "session %s", action)
	}
	assert.Empty(t, f.injA.all())
	assert.Empty(t, f.d.scheduler.ListTasks(f.dirA, false))
}

// Secret entries live in the project's global store scope. The browser API
// may read their masked refs; it may not mint, replace, delete or clear them
// — env injection hands every secret to managed processes at spawn.
func TestBrowserStoreAPI_CannotTouchSecrets(t *testing.T) {
	f := newTwoProjectFixture(t)
	const secretValue = "sk-live-must-not-leak-9876"
	require.NoError(t, f.d.storem.Set(f.dirA, store.ScopeGlobal, "", "API_KEY", secretValue, map[string]any{
		store.MetaSecret:      true,
		store.MetaFingerprint: store.Fingerprint(secretValue),
	}))
	api := dialBrowserAPI(t, f.proxyA)
	global := func(extra map[string]interface{}) map[string]interface{} {
		out := map[string]interface{}{"scope": "global"}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}

	// Reads are masked, on both read surfaces.
	result, errMsg := api.call("store", "get", global(map[string]interface{}{"key": "API_KEY"}))
	require.Empty(t, errMsg)
	assert.NotContains(t, string(result), secretValue)
	result, errMsg = api.call("store", "getAll", global(nil))
	require.Empty(t, errMsg)
	assert.NotContains(t, string(result), secretValue)

	// Writes that would touch the secret, or mint a new one, are refused.
	api.refused("store", "set", global(map[string]interface{}{"key": "API_KEY", "value": "attacker"}))
	api.refused("store", "delete", global(map[string]interface{}{"key": "API_KEY"}))
	api.refused("store", "clear", global(nil))
	api.refused("store", "set", global(map[string]interface{}{
		"key": "DATABASE_URL", "value": "attacker", "metadata": map[string]interface{}{"secret": true},
	}))

	entry, err := f.d.storem.Get(f.dirA, store.ScopeGlobal, "", "API_KEY")
	require.NoError(t, err)
	assert.Equal(t, secretValue, entry.Value, "the secret must be unchanged")
	_, err = f.d.storem.Get(f.dirA, store.ScopeGlobal, "", "DATABASE_URL")
	assert.ErrorIs(t, err, store.ErrNotFound, "no secret may be minted from the browser")
}
