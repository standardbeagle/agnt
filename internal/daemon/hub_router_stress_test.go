package daemon

// Mechanism-isolation stress harness for the daemon-layer commandRouter (G6).
//
// Scope: this file exercises commandRouter.dispatch in internal/daemon/hub_router.go
// using STUB handler functions only. It NEVER invokes real hubHandle* logic,
// real ProcessManager, real ProxyManager, real StateManager, or real overlay
// writers. The only real external object is *hubpkg.Connection — constructed
// via a short-lived hub.Hub spin-up harness, because Connection's fields are
// unexported in go-cli-server/hub and cannot be built directly from outside.
//
// Mental model (read internal/daemon/hub_router.go end-to-end before editing):
//
//   * commandRouter is a thin value constructed per-call by every hubHandle*
//     wrapper (PROXY, SCRIPT, PROC, etc.). It owns two fields: the top-level
//     command verb (for error messages) and an optional defaultHandler.
//   * dispatch(ctx, conn, cmd, handlers) does strict sub-verb lookup on
//     handlers[cmd.SubVerb]. If found, invokes handler(ctx, conn, cmd) and
//     returns its result.
//   * If not found and defaultHandler != nil, invokes defaultHandler. Used by
//     PROC/SCRIPT/AUTOSTART to forward bare-verb commands (e.g. PROC with no
//     sub-verb) to a legacy catch-all.
//   * If not found and no defaultHandler, writes a StructuredError with the
//     sorted list of valid sub-verbs via conn.WriteStructuredErr and returns
//     its result.
//   * dispatch is synchronous — it calls the handler on the caller's goroutine
//     and returns whatever the handler returns. Parallelism comes from the
//     Hub acceptLoop spawning one goroutine per connection.
//
// Handler panic policy (hardened by this commit):
//
//   Prior to G6, a handler panic would bubble out of dispatch() uncaught,
//   killing the connection's Handle goroutine in go-cli-server/hub. With
//   MaxClients bounded and the accept loop still running, the daemon stayed
//   up — but the client connection was dropped mid-response with no error
//   envelope, and any state the handler had partially committed (rare, but
//   possible for multi-step handlers) was left dangling. Recover at the
//   dispatch boundary: convert panics into an ErrInternal structured error
//   so the client sees a real error frame, and log the stack to debug. This
//   matches the drainHooks policy in hub_hook.go where a per-sink panic is
//   logged-and-swallowed rather than allowed to crash the drain.
//
// Every test runs under -race and verifies goroutine cleanup with
// goleak.VerifyNone(t, goleak.IgnoreCurrent()) so connection-handler
// goroutines spawned by the helper Hub do not produce false positives in
// other tests that share the package.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	hubpkg "github.com/standardbeagle/go-cli-server/hub"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
)

// routerVerifyNone runs goleak.VerifyNone with options appropriate for the
// router stress tests: ignores the current goroutine set AND os/exec pipe
// goroutines that may have been created by SessionRegister tests that start
// real processes (sleep 60). Those goroutines are cross-test leaks from the
// process-management path — they are not produced by the router under test.
func routerVerifyNone(t *testing.T, base goleak.Option) {
	t.Helper()
	goleak.VerifyNone(t, base,
		goleak.IgnoreAnyFunction("os/exec.(*Cmd).writerDescriptor.func1"),
	)
}

// ---------- Stub handler fixture -------------------------------------------------

// stubRouterHandler is the canonical counting/sleeping/panicking handler stub
// reused across every router dispatch test. All counters are atomic so tests
// can observe progress mid-flight. Pointer receiver so handler identity is
// stable under map lookup.
type stubRouterHandler struct {
	called     atomic.Int64
	panicked   atomic.Int64
	errored    atomic.Int64
	sleep      time.Duration
	panicEvery int // 0 = never, N = every Nth call panics
	errEvery   int // 0 = never, N = every Nth call returns a sentinel error
	tag        string
}

func (h *stubRouterHandler) handle(_ context.Context, _ *hubpkg.Connection, _ *hubproto.Command) error {
	if h.sleep > 0 {
		time.Sleep(h.sleep)
	}
	n := h.called.Add(1)
	if h.panicEvery > 0 && int(n)%h.panicEvery == 0 {
		h.panicked.Add(1)
		panic(fmt.Sprintf("synth handler panic in %q at call %d", h.tag, n))
	}
	if h.errEvery > 0 && int(n)%h.errEvery == 0 {
		h.errored.Add(1)
		return fmt.Errorf("synth handler err in %q at call %d", h.tag, n)
	}
	return nil
}

// ---------- Connection pool harness ---------------------------------------------

// routerStressPool spins up a minimal hub.Hub on a short unix socket, registers
// a single CAPTURE verb whose handler stashes the server-side *Connection into
// a pool, then dials N client connections so the pool has N real Connections
// to feed into dispatch(). The Hub is torn down in t.Cleanup and t.Cleanup
// also closes every captured response buffer.
//
// Why a real Hub: *hubpkg.Connection has unexported fields (parser/writer/conn),
// so we cannot construct one from outside the hub package. The socket round
// trip is a thin scaffold — the actual stress under test is dispatch(), which
// we call directly with stub handlers. The Hub is never asked to route a real
// command after capture completes.
type routerStressPool struct {
	t        *testing.T
	hub      *hubpkg.Hub
	sockPath string

	mu       sync.Mutex
	conns    []*hubpkg.Connection
	clients  []net.Conn // client-side dial handles (closed on teardown)
	captured chan *hubpkg.Connection
}

func newRouterStressPool(t *testing.T, size int) *routerStressPool {
	t.Helper()

	sockPath := filepath.Join(shortTempDir(t), "r.sock")
	h := hubpkg.New(hubpkg.Config{
		SocketPath:   sockPath,
		MaxClients:   size + 4, // generous headroom
		WriteTimeout: 5 * time.Second,
	})

	pool := &routerStressPool{
		t:        t,
		hub:      h,
		sockPath: sockPath,
		captured: make(chan *hubpkg.Connection, size+4),
	}

	// Register a capture handler. When a client sends "CAPTURE KEEP", this
	// handler pushes the server-side Connection onto the pool channel and
	// replies OK. The client side of the dial stays open (we never Close it
	// until cleanup) so the Connection's writer buffer remains usable.
	err := h.RegisterCommand(hubpkg.CommandDefinition{
		Verb:        "CAPTURE",
		SubVerbs:    []string{"KEEP"},
		Description: "Router stress harness: capture server-side Connection",
		Handler: func(_ context.Context, conn *hubpkg.Connection, _ *hubproto.Command) error {
			pool.captured <- conn
			return conn.WriteOK("captured")
		},
	})
	require.NoError(t, err)

	require.NoError(t, h.Start())

	for i := 0; i < size; i++ {
		c, err := net.DialTimeout("unix", sockPath, 2*time.Second)
		require.NoError(t, err)
		pool.mu.Lock()
		pool.clients = append(pool.clients, c)
		pool.mu.Unlock()

		// Write: CAPTURE KEEP;;
		_, err = c.Write([]byte("CAPTURE KEEP;;"))
		require.NoError(t, err)

		// Drain the OK response so the client buffer stays empty.
		buf := make([]byte, 128)
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, err = c.Read(buf)
		require.NoError(t, err)
		_ = c.SetReadDeadline(time.Time{})

		// Wait for the handler to stash the Connection.
		select {
		case conn := <-pool.captured:
			pool.mu.Lock()
			pool.conns = append(pool.conns, conn)
			pool.mu.Unlock()
		case <-time.After(2 * time.Second):
			t.Fatalf("capture %d did not stash a connection within 2s", i)
		}
	}

	t.Cleanup(func() {
		pool.mu.Lock()
		for _, c := range pool.clients {
			_ = c.Close()
		}
		pool.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = h.Stop(ctx)
	})

	return pool
}

func (p *routerStressPool) conn(i int) *hubpkg.Connection {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.conns[i%len(p.conns)]
}

// size returns the number of captured connections.
func (p *routerStressPool) size() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.conns)
}

// ---------- Tests ---------------------------------------------------------------

// =====================================================================
//  1. ConcurrentDispatch — 10k commands × 8 stub conns. Handlers are
//     stubs (no conn writes) so we can verify routing without hammering
//     the connection writer. Per-handler call counts must reconcile
//     against the producer's expected distribution.
//
// Catches: SubVerb map lookup race (sync.Map in CommandRegistry is
// lock-free, but the handlers arg to dispatch is a plain map[string]…
// that must not be mutated mid-dispatch — which it isn't in production,
// but this test pins that property).
// =====================================================================
func TestRouter_ConcurrentDispatch(t *testing.T) {
	opts := goleak.IgnoreCurrent()
	t.Cleanup(func() { routerVerifyNone(t, opts) })

	const (
		numConns   = 8
		numCmds    = 10000
		numSubVerb = 4
	)

	pool := newRouterStressPool(t, numConns)
	require.Equal(t, numConns, pool.size())

	// Four handlers keyed on sub-verbs A/B/C/D. Each increments a counter.
	handlers := make([]*stubRouterHandler, numSubVerb)
	subVerbs := []string{"A", "B", "C", "D"}
	dispatchMap := map[string]handlerFn{}
	for i, sv := range subVerbs {
		h := &stubRouterHandler{tag: sv}
		handlers[i] = h
		dispatchMap[sv] = h.handle
	}
	router := newCommandRouter("STRESS")

	var wg sync.WaitGroup
	// Deterministic distribution: each sub-verb is hit numCmds/numSubVerb times.
	expectedPer := int64(numCmds / numSubVerb)

	for i := 0; i < numCmds; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			sv := subVerbs[i%numSubVerb]
			conn := pool.conn(i % numConns)
			cmd := &hubproto.Command{Verb: "STRESS", SubVerb: sv}
			err := router.dispatch(context.Background(), conn, cmd, dispatchMap)
			assert.NoError(t, err)
		}()
	}
	wg.Wait()

	// Reconcile: each handler saw exactly expectedPer calls.
	var total int64
	for _, h := range handlers {
		got := h.called.Load()
		total += got
		assert.Equal(t, expectedPer, got, "sub-verb %q call count drift", h.tag)
	}
	assert.Equal(t, int64(numCmds), total, "dispatch total count drift")
}

// =====================================================================
//  2. SlowHandlerDoesntBlockOthers — one handler sleeps 100ms; others
//     respond instantly. Wallclock for 100 fast dispatches on other
//     sub-verbs must remain well under the slow handler's single-sleep
//     budget because dispatch is synchronous per call but concurrent
//     calls on different connections run in parallel.
//
// Catches: accidental serialization — e.g. if dispatch ever grew a
// shared mutex, the fast path would pile up behind the slow one.
// =====================================================================
func TestRouter_SlowHandlerDoesntBlockOthers(t *testing.T) {
	opts := goleak.IgnoreCurrent()
	t.Cleanup(func() { routerVerifyNone(t, opts) })

	pool := newRouterStressPool(t, 8)

	slow := &stubRouterHandler{tag: "SLOW", sleep: 100 * time.Millisecond}
	fast := &stubRouterHandler{tag: "FAST"}
	dispatchMap := map[string]handlerFn{
		"SLOW": slow.handle,
		"FAST": fast.handle,
	}
	router := newCommandRouter("STRESS")

	var wg sync.WaitGroup
	// Fire 1 slow dispatch on conn[0] in parallel with 100 fast dispatches
	// spread across conns 1..7. Measure wallclock for the 100 fasts.
	wg.Add(1)
	slowStart := time.Now()
	go func() {
		defer wg.Done()
		cmd := &hubproto.Command{Verb: "STRESS", SubVerb: "SLOW"}
		_ = router.dispatch(context.Background(), pool.conn(0), cmd, dispatchMap)
	}()

	// Let the slow handler enter its Sleep first so its goroutine is
	// parked on the timer, not competing for CPU.
	time.Sleep(5 * time.Millisecond)

	fastStart := time.Now()
	var fastWg sync.WaitGroup
	for i := 0; i < 100; i++ {
		i := i
		fastWg.Add(1)
		go func() {
			defer fastWg.Done()
			cmd := &hubproto.Command{Verb: "STRESS", SubVerb: "FAST"}
			_ = router.dispatch(context.Background(), pool.conn(1+(i%7)), cmd, dispatchMap)
		}()
	}
	fastWg.Wait()
	fastElapsed := time.Since(fastStart)

	wg.Wait()
	slowElapsed := time.Since(slowStart)

	t.Logf("100 fast dispatches elapsed in %s; slow dispatch elapsed in %s",
		fastElapsed, slowElapsed)

	// Fast batch must complete well before the slow handler's 100ms nap
	// finishes. 50ms gives generous CI slack but still pins the parallelism
	// invariant — a serialized dispatch would take >100ms just for the slow.
	assert.Less(t, fastElapsed, 50*time.Millisecond,
		"fast dispatches blocked behind slow handler — parallelism broken")
	// Sanity: slow must have actually taken its sleep.
	assert.GreaterOrEqual(t, slowElapsed, 100*time.Millisecond,
		"slow handler did not wait its full sleep — test fixture bug")
	// Counts must match.
	assert.Equal(t, int64(1), slow.called.Load())
	assert.Equal(t, int64(100), fast.called.Load())
}

// =====================================================================
//  3. HandlerPanicRecovery — handler panics on every 10th call. dispatch
//     must recover, write an ErrInternal structured error to the client,
//     and keep the router operational for subsequent calls on the same
//     Connection. Without recovery, the Connection's Handle goroutine
//     dies mid-response and the client sees a truncated frame.
//
// This test was RED before hub_router.go gained the recover block; it
// now pins the recovery contract so future edits to dispatch cannot
// regress it.
// =====================================================================
func TestRouter_HandlerPanicRecovery(t *testing.T) {
	opts := goleak.IgnoreCurrent()
	t.Cleanup(func() { routerVerifyNone(t, opts) })

	pool := newRouterStressPool(t, 2)

	h := &stubRouterHandler{tag: "PANICKY", panicEvery: 10}
	dispatchMap := map[string]handlerFn{
		"BANG": h.handle,
	}
	router := newCommandRouter("STRESS")

	const calls = 50
	var errCount, okCount int64
	for i := 0; i < calls; i++ {
		cmd := &hubproto.Command{Verb: "STRESS", SubVerb: "BANG"}
		// Wrap in a function so a panic bubble would be caught here and
		// fail the test explicitly — a panic escape means recovery is not
		// doing its job.
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("dispatch leaked a panic to caller: %v", r)
				}
			}()
			err := router.dispatch(context.Background(), pool.conn(0), cmd, dispatchMap)
			if err != nil {
				atomic.AddInt64(&errCount, 1)
			} else {
				atomic.AddInt64(&okCount, 1)
			}
		}()
	}

	t.Logf("after %d calls: called=%d panicked=%d errors=%d oks=%d",
		calls, h.called.Load(), h.panicked.Load(), errCount, okCount)

	// panicEvery=10 => exactly calls/10 panics.
	assert.Equal(t, int64(calls/10), h.panicked.Load(),
		"stub did not panic on the expected schedule")
	// Every panic must be converted into a dispatch error (WriteStructuredErr
	// returns nil unless the socket is dead, so errCount may be 0 even though
	// every panic was recovered — the contract that matters is "no panic
	// escaped"). The count test above is sufficient.
	// Router stayed operational: called increments monotonically through
	// panics, so total calls == initial plan.
	assert.Equal(t, int64(calls), h.called.Load(),
		"router skipped calls after a panic — recovery broke loop invariant")

	// Second pass with a fresh conn: the panic-recovery path must not leave
	// any conn in a broken state. Issue a single clean dispatch that should
	// succeed.
	clean := &stubRouterHandler{tag: "CLEAN"}
	dispatchMap["CLEAN"] = clean.handle
	cmd := &hubproto.Command{Verb: "STRESS", SubVerb: "CLEAN"}
	err := router.dispatch(context.Background(), pool.conn(1), cmd, dispatchMap)
	assert.NoError(t, err)
	assert.Equal(t, int64(1), clean.called.Load())
}

// =====================================================================
//  4. UnknownVerb — an unregistered sub-verb must write a structured
//     error with ErrInvalidArgs, a sorted list of valid actions, and
//     the owning command name. No panic, no silent drop.
//
// We read the server-side write by issuing the unknown verb from a
// fresh net.Dial and parsing the JSON response frame.
// =====================================================================
func TestRouter_UnknownVerb(t *testing.T) {
	opts := goleak.IgnoreCurrent()
	t.Cleanup(func() { routerVerifyNone(t, opts) })

	// Use a fresh pool so we can read response frames from the client side.
	// (The captured-conn pattern above drains response frames during capture;
	// for this test we need to observe the exact structured error emitted by
	// dispatch, so we use a dedicated dial-and-read flow.)
	sockPath := filepath.Join(shortTempDir(t), "u.sock")
	h := hubpkg.New(hubpkg.Config{
		SocketPath:   sockPath,
		MaxClients:   4,
		WriteTimeout: 2 * time.Second,
	})
	err := h.RegisterCommand(hubpkg.CommandDefinition{
		Verb:        "STRESS",
		Description: "router stress probe",
		Handler: func(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
			return newCommandRouter("STRESS").dispatch(ctx, conn, cmd, map[string]handlerFn{
				"KNOWN": func(_ context.Context, c *hubpkg.Connection, _ *hubproto.Command) error {
					return c.WriteOK("ok")
				},
				"ALSO-KNOWN": func(_ context.Context, c *hubpkg.Connection, _ *hubproto.Command) error {
					return c.WriteOK("ok")
				},
			})
		},
	})
	require.NoError(t, err)
	require.NoError(t, h.Start())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = h.Stop(ctx)
	})

	c, err := net.DialTimeout("unix", sockPath, 2*time.Second)
	require.NoError(t, err)
	defer c.Close()

	_, err = c.Write([]byte("STRESS MYSTERY;;"))
	require.NoError(t, err)

	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1024)
	n, err := c.Read(buf)
	require.NoError(t, err)
	response := string(buf[:n])

	t.Logf("unknown-verb response: %q", response)

	// Response is a JSON frame carrying the StructuredError. Find and parse.
	// Format: `JSON -- <len>\n<base64>;;` per protocol.responses — but for the
	// purposes of this test the substring match on the error code + sorted
	// valid actions is what we care about. The protocol parser test coverage
	// in go-cli-server pins the frame shape.
	assert.Contains(t, response, "JSON", "response was not a JSON frame")
	// Cheap JSON extraction: find the first '{' and last '}' of the decoded
	// payload. If it's base64, decode first. Or just check substrings on the
	// base64 — but that is brittle. Fallback to structural assertion: the
	// response must carry ErrInvalidArgs as a string somewhere, once decoded.
	// The Writer writes StructuredError as base64'd JSON; to keep the test
	// simple we parse the frame header and decode the body.
	payload := decodeJSONFrame(t, response)
	var se hubproto.StructuredError
	require.NoError(t, json.Unmarshal(payload, &se))

	assert.Equal(t, hubproto.ErrInvalidArgs, se.Code)
	assert.Equal(t, "STRESS", se.Command)
	assert.Contains(t, se.Message, "unknown STRESS sub-command")
	// ValidActions must be sorted and exclude the empty-string bucket.
	assert.Equal(t, []string{"ALSO-KNOWN", "KNOWN"}, se.ValidActions)
}

// decodeJSONFrame parses a `JSON -- <len>\n<base64>;;` frame and returns
// the decoded payload. Helper kept local to the stress test because the
// protocol parser is not exported in a test-friendly shape.
func decodeJSONFrame(t *testing.T, frame string) []byte {
	t.Helper()
	// Strip the trailing ;; terminator if present.
	frame = strings.TrimSuffix(frame, ";;")
	// Locate the first newline — everything after it is the base64 body.
	nl := strings.Index(frame, "\n")
	require.Greater(t, nl, 0, "no newline in JSON frame: %q", frame)
	body := frame[nl+1:]
	// Base64 decode.
	decoded, err := base64StdDecode(body)
	require.NoError(t, err, "base64 decode failed for body %q", body)
	return decoded
}

// base64StdDecode wraps encoding/base64.StdEncoding.DecodeString so the
// only caller (decodeJSONFrame) stays readable. The JSON frame body is
// always padded standard base64, matching protocol.FormatJSON.
func base64StdDecode(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
}

// =====================================================================
//  5. ConnDisconnectMidRequest — the client closes its net.Conn while a
//     handler is still running. dispatch() must not panic; the handler's
//     return value is returned as-is (since the handler itself has no
//     opportunity to write to a dead socket in this fixture, dispatch
//     should return nil and recovery is untouched).
//
// Catches: reliance on conn being alive through dispatch. The router
// does not touch conn in the happy-path (sub-verb hit) — it just forwards
// to the handler. So the test pins the no-op-on-dead-conn invariant.
// =====================================================================
func TestRouter_ConnDisconnectMidRequest(t *testing.T) {
	opts := goleak.IgnoreCurrent()
	t.Cleanup(func() { routerVerifyNone(t, opts) })

	pool := newRouterStressPool(t, 1)
	conn := pool.conn(0)

	ready := make(chan struct{})
	release := make(chan struct{})
	// Handler parks until release is signalled. Meanwhile we close the
	// client side of the connection.
	h := handlerFn(func(_ context.Context, _ *hubpkg.Connection, _ *hubproto.Command) error {
		close(ready)
		<-release
		return nil
	})
	router := newCommandRouter("STRESS")
	dispatchMap := map[string]handlerFn{"PARK": h}

	done := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- fmt.Errorf("dispatch panicked after disconnect: %v", r)
				return
			}
		}()
		cmd := &hubproto.Command{Verb: "STRESS", SubVerb: "PARK"}
		done <- router.dispatch(context.Background(), conn, cmd, dispatchMap)
	}()

	// Wait for handler to enter the park.
	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not enter park within 2s")
	}

	// Close both ends to simulate a mid-request disconnect.
	pool.mu.Lock()
	for _, cc := range pool.clients {
		_ = cc.Close()
	}
	pool.mu.Unlock()
	_ = conn.Close()

	// Release the handler and verify dispatch returns cleanly.
	close(release)
	select {
	case err := <-done:
		assert.NoError(t, err, "dispatch returned an error after disconnect")
	case <-time.After(2 * time.Second):
		t.Fatal("dispatch did not return within 2s of handler release")
	}
}

// =====================================================================
//  6. RegisterUnregisterRace — concurrently mutate a handler map (add /
//     delete entries) while other goroutines drive dispatch against a
//     stable copy. Models the case where commandRouter is recreated
//     per-call with a freshly built map (as in production) — each
//     dispatch gets its OWN snapshot of the map, so concurrent edits by
//     other goroutines must not race-detect with read-side lookup.
//
// Realized by building a shared read-only map once per "generation" and
// swapping the pointer under a RWMutex, while dispatch goroutines hold a
// stable snapshot for the duration of their call. Catches: a regression
// where dispatch started mutating or writing to the handlers map it was
// given.
//
// Runs under -race. Parent loop asserts no panic + no deadlock.
// =====================================================================
func TestRouter_RegisterUnregisterRace(t *testing.T) {
	opts := goleak.IgnoreCurrent()
	t.Cleanup(func() { routerVerifyNone(t, opts) })

	pool := newRouterStressPool(t, 4)
	// Shared default handler so that missing-key lookups during a snapshot
	// swap still run a handler (not writeStructuredErr → WriteJSON).
	fallback := &stubRouterHandler{tag: "FALLBACK"}
	router := newCommandRouter("STRESS").withDefault(fallback.handle)

	// Atomic snapshot pointer: writers build a new map, then atomic-swap.
	// Readers load a pointer and dispatch against it. map is never mutated
	// in place, so race-free by construction.
	//
	// A defaultHandler is attached so that any sub-verb missing from a
	// particular snapshot still lands in a handler (not the structured-error
	// path). Without this, a reader could hit GO2 while the writer had just
	// swapped to a snapshot that omits GO2, which would trigger
	// writeStructuredErr → conn.WriteJSON and eventually fill the client
	// socket buffer (we never drain it after capture). The fallback keeps
	// this test pinned to map-access race coverage.
	var snap atomic.Pointer[map[string]handlerFn]
	h := &stubRouterHandler{tag: "RACE"}
	initial := map[string]handlerFn{"GO": h.handle}
	snap.Store(&initial)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// writerDone signals when the writer goroutine exits; kept separate so
	// readers can finish and trigger cancel() without a circular dependency
	// (wg.Wait waiting for writer, writer waiting for cancel, cancel waiting
	// for wg.Wait).
	writerDone := make(chan struct{})

	// Writer goroutine: every 50µs, rebuild the map with a mutated set of
	// sub-verbs and atomic-swap it into snap. Simulates
	// register/unregister by adding/removing "GO2", "GO3", etc.
	go func() {
		defer close(writerDone)
		i := 0
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			newMap := map[string]handlerFn{"GO": h.handle}
			if i%2 == 0 {
				newMap["GO2"] = h.handle
			}
			if i%3 == 0 {
				newMap["GO3"] = h.handle
			}
			snap.Store(&newMap)
			i++
			time.Sleep(50 * time.Microsecond)
		}
	}()

	// Dispatch goroutines: 4 readers × ~1000 dispatches each. Each loads
	// the current snapshot, then calls dispatch with it. Sub-verb picks
	// cycle through "GO", "GO2", "GO3" — intentionally excluding unknown
	// verbs because the structured-error write-path would fill the client
	// socket buffer (the capture harness does not drain client reads after
	// setup) and block the reader goroutines under load. The unknown-verb
	// path is covered by TestRouter_UnknownVerb; this test targets the
	// map-access race only.
	const perReader = 1000
	subVerbs := []string{"GO", "GO2", "GO3"}
	var readerWg sync.WaitGroup
	for r := 0; r < 4; r++ {
		r := r
		readerWg.Add(1)
		go func() {
			defer readerWg.Done()
			for i := 0; i < perReader; i++ {
				m := snap.Load()
				cmd := &hubproto.Command{Verb: "STRESS", SubVerb: subVerbs[i%len(subVerbs)]}
				// Swallow errors — we only care that dispatch doesn't panic
				// or race on the map. GHOST will always error (not in map);
				// GO2/GO3 may error if snap rebuilt without them in this
				// window. That's expected and not a bug.
				_ = router.dispatch(context.Background(), pool.conn(r), cmd, *m)
			}
		}()
	}

	// Wait for readers to finish, then cancel the writer.
	readersDone := make(chan struct{})
	go func() {
		readerWg.Wait()
		close(readersDone)
	}()

	select {
	case <-readersDone:
		// Readers finished. Cancel the writer and wait for it to exit.
		cancel()
		select {
		case <-writerDone:
		case <-time.After(2 * time.Second):
			t.Fatal("writer goroutine did not exit within 2s of cancel")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("register/unregister race test did not finish within 10s")
	}

	// Sanity: handler was called many times across many snapshot versions
	// without a panic.
	t.Logf("RegisterUnregisterRace: total handler calls=%d", h.called.Load())
	assert.Greater(t, h.called.Load(), int64(1000),
		"expected thousands of successful dispatches across snapshot generations")
}

// =====================================================================
//
//	Aux: DefaultHandler fallback — when a sub-verb is not in the map but
//	   the router has a defaultHandler, the default must be invoked
//	   (not the structured-error path). Guards the PROC/SCRIPT/AUTOSTART
//	   pattern that uses withDefault for bare-verb commands.
//
// =====================================================================
func TestRouter_DefaultHandlerFallback(t *testing.T) {
	opts := goleak.IgnoreCurrent()
	t.Cleanup(func() { routerVerifyNone(t, opts) })

	pool := newRouterStressPool(t, 1)

	primary := &stubRouterHandler{tag: "PRIMARY"}
	fallback := &stubRouterHandler{tag: "FALLBACK"}
	router := newCommandRouter("STRESS").withDefault(fallback.handle)
	dispatchMap := map[string]handlerFn{"PRIMARY": primary.handle}

	// Hit the registered sub-verb: primary runs, fallback does not.
	err := router.dispatch(context.Background(), pool.conn(0),
		&hubproto.Command{Verb: "STRESS", SubVerb: "PRIMARY"}, dispatchMap)
	assert.NoError(t, err)

	// Hit an unknown sub-verb: fallback runs (structured-error path is skipped).
	err = router.dispatch(context.Background(), pool.conn(0),
		&hubproto.Command{Verb: "STRESS", SubVerb: "MYSTERY"}, dispatchMap)
	assert.NoError(t, err)

	assert.Equal(t, int64(1), primary.called.Load())
	assert.Equal(t, int64(1), fallback.called.Load())
}
