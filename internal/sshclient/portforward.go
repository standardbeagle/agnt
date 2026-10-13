package sshclient

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"context"

	"github.com/standardbeagle/agnt/internal/daemonclient"
	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/agnt/internal/proxy"
)

// reconcilePeriod is the periodic PROXY LIST cross-check interval, matching
// the daemon's own 30s cache-vs-truth reconciliation cadence (see
// daemon-architecture.md § Reconciliation Model) — event-driven teardown
// (proxy_started/proxy_stopped diagnostics over STREAM-EVENTS) is the
// primary signal; this loop exists only to catch anything a dropped/missed
// event left out of sync, never to drive the common case. A package var
// (not a const) so tests can shrink it and assert the periodic backstop
// path deterministically instead of depending on STREAM-EVENTS latency.
var reconcilePeriod = 30 * time.Second

// nearestPortSearchLimit bounds collision recovery so a machine with a broad
// occupied range fails promptly instead of scanning the entire TCP space.
const nearestPortSearchLimit = 1000

// PortForwardManager keeps a set of local TCP listeners in sync with the
// reverse proxies running on a remote agnt daemon reached over an existing
// SSH connection, so a local browser can hit http://127.0.0.1:<port>
// directly without the developer ever running `ssh -L`. Lifecycle:
//   - reconcileOnce() on Start (and after every event) is the "cache is
//     never trusted alone" doctrine: PROXY LIST is the source of truth,
//     STREAM-EVENTS diagnostics are just a trigger to re-check it sooner
//     than the periodic loop would.
//   - one local net.Listener per remote proxy ID; every accepted conn opens
//     its own direct-tcpip channel (client.SSH.Dial("tcp", ...)) — same
//     one-channel-per-conn model as Forwarder in forward.go.
type PortForwardManager struct {
	lifecycleMu sync.Mutex
	sshClient   *Client
	dclient     *daemonclient.Client
	notify      func(string)
	onChange    func([]Mapping)

	mu          sync.Mutex
	reconcileMu sync.Mutex
	forwards    map[string]*portForward // keyed by remote proxy ID

	cancel     context.CancelFunc
	wg         sync.WaitGroup
	paused     chan struct{}
	reconciled chan struct{}
}

func (m *PortForwardManager) SetOnChange(fn func([]Mapping)) {
	m.mu.Lock()
	m.onChange = fn
	m.mu.Unlock()
}

func (m *PortForwardManager) emitChange() {
	m.mu.Lock()
	fn := m.onChange
	m.mu.Unlock()
	if fn != nil {
		fn(m.Status())
	}
}

// portForward is one remote-proxy-ID's local listener.
type portForward struct {
	proxyID string
	// remoteHost is the address the remote proxy listens on, as the relay
	// must dial it from the remote side (see forwardTarget).
	remoteHost string
	remotePort int
	// notify reports relay failures; dialFailing collapses a run of
	// identical failures into one line.
	notify            func(string)
	dialFailing       atomic.Bool
	localPort         int
	listener          net.Listener
	connMu            sync.Mutex
	conns             map[net.Conn]struct{}
	stopping          bool
	paused            bool
	connWG            sync.WaitGroup
	beforeRemoteTrack func()
}

// NewPortForwardManager builds a manager for the proxies visible on dclient
// (a daemonclient.Client already connected to the remote daemon, typically over
// the forwarded unix socket set up by startDaemonSocketForwarding), dialing
// new streams through sshClient. notify receives one-line human-readable
// status messages (collision remaps, forward up/down) for the caller to
// print or toast — this package never writes to stdout/stderr directly.
func NewPortForwardManager(sshClient *Client, dclient *daemonclient.Client, notify func(string)) *PortForwardManager {
	return &PortForwardManager{
		sshClient:  sshClient,
		dclient:    dclient,
		notify:     notify,
		forwards:   make(map[string]*portForward),
		paused:     make(chan struct{}),
		reconciled: make(chan struct{}),
	}
}

// Start reconciles once immediately (reconcile-on-connect: see
// daemon-architecture.md § Reconciliation Model, "on session connect"),
// then runs the event-driven + periodic reconcile loop in the background
// until ctx is cancelled or Stop is called.
func (m *PortForwardManager) Start(ctx context.Context) {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	m.startLoops(ctx)
}

func (m *PortForwardManager) startLoops(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	m.cancel = cancel

	m.reconcileOnce()
	m.mu.Lock()
	select {
	case <-m.reconciled:
	default:
		close(m.reconciled)
	}
	m.mu.Unlock()

	m.wg.Add(1)
	go m.reconcileLoop(ctx)

	// STREAM-EVENTS registration and a proxy start can cross during session
	// startup. Brief authoritative probes close that subscription race while
	// the normal 30s ticker remains strictly a missed-event backstop.
	m.wg.Add(1)
	go m.startupReconcile(ctx)
}

// Pause retains every local listener but rejects new connections and drains
// relays before the dead SSH transport is closed.
func (m *PortForwardManager) Pause() {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	if m.cancel != nil {
		m.cancel()
		m.wg.Wait()
	}
	m.mu.Lock()
	select {
	case <-m.paused:
	default:
		close(m.paused)
	}
	for _, f := range m.forwards {
		f.pause()
	}
	m.mu.Unlock()
}

// Resume supplies fresh transport clients, enables the retained listeners,
// then performs an authoritative PROXY LIST reconciliation before returning.
func (m *PortForwardManager) Resume(ctx context.Context, sshClient *Client, dclient *daemonclient.Client) {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	m.mu.Lock()
	m.sshClient = sshClient
	m.dclient = dclient
	m.paused = make(chan struct{})
	m.reconciled = make(chan struct{})
	m.mu.Unlock()
	m.startLoops(ctx)
	m.mu.Lock()
	for _, f := range m.forwards {
		f.resume()
	}
	m.mu.Unlock()
}

// Reconciled closes once the current Start/Resume generation has completed
// its authoritative PROXY LIST reconciliation.
func (m *PortForwardManager) Reconciled() <-chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reconciled
}

func (m *PortForwardManager) Paused() <-chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.paused
}

func (m *PortForwardManager) startupReconcile(ctx context.Context) {
	defer m.wg.Done()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			return
		case <-ticker.C:
			m.reconcileOnce()
		}
	}
}

// Stop halts the reconcile loop and tears down every active local listener
// (and drains in-flight connections through each) before returning.
func (m *PortForwardManager) Stop() {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	if m.cancel != nil {
		m.cancel()
	}
	m.wg.Wait()

	m.mu.Lock()
	forwards := make([]*portForward, 0, len(m.forwards))
	for _, f := range m.forwards {
		forwards = append(forwards, f)
	}
	m.mu.Unlock()

	for _, f := range forwards {
		m.retire(f)
	}
	m.emitChange()
}

// retire closes a forward and only then drops it from the registry Status
// reads. The order is the contract: Status feeds `agnt ssh --status` and the
// overlay ports panel, so a forward that disappeared while its listener still
// accepted connections, or while a relay was still copying, would report a
// connection the developer can still be using as already gone.
//
// The delete is guarded by identity rather than id: a forward started again
// under the same proxy id during the close must not be retired by the closing
// one.
func (m *PortForwardManager) retire(f *portForward) {
	f.stop()
	m.mu.Lock()
	if m.forwards[f.proxyID] == f {
		delete(m.forwards, f.proxyID)
	}
	m.mu.Unlock()
}

// Mapping is one active remote->local port forward, returned by Status for
// `agnt ssh --status` and the overlay ports panel.
type Mapping struct {
	ProxyID    string `json:"proxy_id"`
	RemotePort int    `json:"remote_port"`
	LocalPort  int    `json:"local_port"`
	Remapped   bool   `json:"remapped"`
}

// Status returns a snapshot of every currently-forwarded proxy.
func (m *PortForwardManager) Status() []Mapping {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Mapping, 0, len(m.forwards))
	for _, f := range m.forwards {
		out = append(out, Mapping{
			ProxyID:    f.proxyID,
			RemotePort: f.remotePort,
			LocalPort:  f.localPort,
			Remapped:   f.localPort != f.remotePort,
		})
	}
	return out
}

// reconcileLoop reacts to two triggers: any STREAM-EVENTS entry (used only
// as a "something happened, re-check now" nudge — the entry's own content
// is not trusted as the port truth) and the periodic 30s tick as a backstop
// against a missed/dropped event (bus.go's own drop-newest-on-overflow
// policy means an event is not guaranteed delivery).
func (m *PortForwardManager) reconcileLoop(ctx context.Context) {
	defer m.wg.Done()

	events := make(chan struct{}, 1)
	m.mu.Lock()
	dclient := m.dclient
	m.mu.Unlock()
	eventsDone := make(chan struct{})
	go func() {
		defer close(eventsDone)
		m.watchEvents(ctx, dclient, events)
	}()
	defer func() { <-eventsDone }()

	ticker := time.NewTicker(reconcilePeriod)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.reconcileOnce()
		case <-events:
			m.reconcileOnce()
		}
	}
}

// watchEvents subscribes to STREAM-EVENTS with no filter (proxy_started /
// proxy_stopped diagnostics pass the daemon's default gate unfiltered — see
// internal/proxy/server.go and proxy_handler.go) and nudges signal on every
// entry. It never parses entry content for port truth: PROXY LIST via
// reconcileOnce is the sole source of truth (daemon-architecture.md § Data
// Ownership). StreamEvents returning (daemon restart, socket drop) is
// non-fatal here — the periodic ticker in reconcileLoop keeps reconciling
// even with events unavailable; it retries via a short backoff loop.
func (m *PortForwardManager) watchEvents(ctx context.Context, dclient *daemonclient.Client, signal chan<- struct{}) {
	for {
		if ctx.Err() != nil {
			return
		}
		_ = dclient.StreamEvents(ctx, protocol.StreamEventFilter{Global: true}, func(_ proxy.LogEntry) error {
			select {
			case signal <- struct{}{}:
			default:
			}
			return nil
		})
		if ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

// reconcileOnce fetches the authoritative proxy list from the remote daemon
// and starts/stops local listeners to match it exactly.
func (m *PortForwardManager) reconcileOnce() {
	m.reconcileMu.Lock()
	defer m.reconcileMu.Unlock()
	desired, err := m.fetchDesired()
	if err != nil {
		if m.notify != nil {
			m.notify(fmt.Sprintf("agnt ssh: could not reconcile port forwards (PROXY LIST failed: %v)", err))
		}
		return
	}

	m.mu.Lock()
	var toStop []*portForward
	stopping := make(map[string]bool)
	for id, f := range m.forwards {
		target, ok := desired[id]
		if !ok || target != f.target() {
			toStop = append(toStop, f)
			stopping[id] = true
		}
	}
	var toStart []struct {
		id     string
		target forwardTarget
	}
	for id, target := range desired {
		// A forward on its way out counts as absent: a proxy whose remote
		// port changed is stopped and started again in this same pass.
		if _, ok := m.forwards[id]; !ok || stopping[id] {
			toStart = append(toStart, struct {
				id     string
				target forwardTarget
			}{id, target})
		}
	}
	m.mu.Unlock()

	for _, f := range toStop {
		m.retire(f)
		if m.notify != nil {
			m.notify(fmt.Sprintf("agnt ssh: proxy %s stopped, local :%d forward closed", f.proxyID, f.localPort))
		}
	}
	for _, s := range toStart {
		m.startForward(s.id, s.target)
	}
	m.emitChange()
}

// fetchDesired calls PROXY LIST (global — this manager forwards every proxy
// the remote daemon knows about, not just one project's) and extracts
// proxy id -> remote listen address from each entry's listen_addr.
func (m *PortForwardManager) fetchDesired() (map[string]forwardTarget, error) {
	result, err := m.dclient.ProxyList(protocol.DirectoryFilter{Global: true})
	if err != nil {
		return nil, err
	}

	raw, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("re-marshaling PROXY LIST result: %w", err)
	}
	var parsed struct {
		Proxies []struct {
			ID         string `json:"id"`
			ListenAddr string `json:"listen_addr"`
		} `json:"proxies"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("decoding PROXY LIST result: %w", err)
	}

	desired := make(map[string]forwardTarget, len(parsed.Proxies))
	for _, p := range parsed.Proxies {
		target, err := listenAddrTarget(p.ListenAddr)
		if err != nil || p.ID == "" {
			continue
		}
		desired[p.ID] = target
	}
	return desired, nil
}

// forwardTarget is where the relay dials on the remote side to reach a proxy.
type forwardTarget struct {
	Host string
	Port int
}

// listenAddrTarget turns a proxy's listen_addr into the address to dial from
// the remote host itself.
//
// The host matters: a proxy bound with `bind "tailscale"` (or any specific
// address) does not answer on loopback, so dialing 127.0.0.1 regardless of
// listen_addr gave such a proxy a local forward that accepted connections and
// dropped every one. A wildcard or empty host is reachable on loopback, and
// loopback is what it is dialed as.
func listenAddrTarget(addr string) (forwardTarget, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return forwardTarget{}, fmt.Errorf("listen_addr %q: %w", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return forwardTarget{}, fmt.Errorf("listen_addr %q: bad port", addr)
	}
	switch host {
	case "", "0.0.0.0", "::", "localhost":
		host = "127.0.0.1"
	}
	return forwardTarget{Host: host, Port: port}, nil
}

// startForward binds a local listener for remotePort, preferring the same
// port number (Port mapping policy #26 — same-origin URLs/absolute paths
// injected by the proxy stay valid unmodified), falling back to an
// OS-assigned port on collision with a mandatory visible notice (#27 — a
// silent remap would leave stale absolute URLs pointing at the wrong port).
func (m *PortForwardManager) startForward(proxyID string, target forwardTarget) {
	remotePort := target.Port
	localAddr := fmt.Sprintf("127.0.0.1:%d", remotePort)
	listener, err := net.Listen("tcp", localAddr)
	remapped := false
	if err != nil {
		listener, err = listenNearestFreePort(remotePort, nearestPortSearchLimit)
		if err != nil {
			if m.notify != nil {
				m.notify(fmt.Sprintf("agnt ssh: proxy %s (:%d) could not be forwarded locally: %v", proxyID, remotePort, err))
			}
			return
		}
		remapped = true
	}

	localPort := listener.Addr().(*net.TCPAddr).Port
	f := &portForward{
		proxyID:    proxyID,
		remoteHost: target.Host,
		remotePort: remotePort,
		notify:     m.notify,
		localPort:  localPort,
		listener:   listener,
		conns:      make(map[net.Conn]struct{}),
	}

	m.mu.Lock()
	m.forwards[proxyID] = f
	m.mu.Unlock()

	if m.notify != nil {
		if remapped {
			m.notify(fmt.Sprintf("agnt ssh: remote :%d -> local :%d (port %d in use locally) for proxy %s", remotePort, localPort, remotePort, proxyID))
		} else {
			m.notify(fmt.Sprintf("agnt ssh: proxy %s available at http://127.0.0.1:%d", proxyID, localPort))
		}
	}

	go f.serve(func() *Client {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.sshClient
	})
}

// listenNearestFreePort binds candidates by increasing distance from
// remotePort. The bind itself reserves the winning port, avoiding the
// check-then-bind race of probing availability separately. Equal-distance
// ties prefer the higher port, making the mapping deterministic.
func listenNearestFreePort(remotePort, limit int) (net.Listener, error) {
	var lastErr error
	for distance := 1; distance <= limit; distance++ {
		candidates := [2]int{remotePort + distance, remotePort - distance}
		for _, port := range candidates {
			if port < 1 || port > 65535 {
				continue
			}
			listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
			if err == nil {
				return listener, nil
			}
			lastErr = err
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no valid candidate ports")
	}
	return nil, fmt.Errorf("no free local port within %d of :%d: %w", limit, remotePort, lastErr)
}

// serve accepts local connections until the listener is closed by stop(),
// opening one new direct-tcpip channel per connection.
func (f *portForward) serve(sshClient func() *Client) {
	for {
		conn, err := f.listener.Accept()
		if err != nil {
			return
		}
		f.connMu.Lock()
		if f.stopping || f.paused {
			stopping := f.stopping
			f.connMu.Unlock()
			conn.Close()
			if stopping {
				return
			}
			continue
		}
		f.conns[conn] = struct{}{}
		f.connWG.Add(1)
		f.connMu.Unlock()
		go func() {
			defer func() {
				f.connMu.Lock()
				delete(f.conns, conn)
				f.connMu.Unlock()
				f.connWG.Done()
			}()
			f.relay(sshClient(), conn)
		}()
	}
}

// target is the remote address this forward relays to.
func (f *portForward) target() forwardTarget {
	return forwardTarget{Host: f.remoteHost, Port: f.remotePort}
}

func (f *portForward) pause() {
	f.connMu.Lock()
	f.paused = true
	for conn := range f.conns {
		conn.Close()
	}
	f.connMu.Unlock()
	f.connWG.Wait()
}

func (f *portForward) resume() {
	f.connMu.Lock()
	f.paused = false
	f.connMu.Unlock()
}

func (f *portForward) relay(sshClient *Client, conn net.Conn) {
	defer conn.Close()

	remoteAddr := net.JoinHostPort(f.remoteHost, strconv.Itoa(f.remotePort))
	remote, err := sshClient.SSH.Dial("tcp", remoteAddr)
	if err != nil {
		// The browser only sees a dropped connection, so say why. One line
		// per run of failures: a page load opens many connections at once.
		if f.notify != nil && f.dialFailing.CompareAndSwap(false, true) {
			f.notify(fmt.Sprintf("agnt ssh: proxy %s: local :%d cannot reach remote %s: %v", f.proxyID, f.localPort, remoteAddr, err))
		}
		return
	}
	f.dialFailing.Store(false)
	defer remote.Close()
	f.connMu.Lock()
	beforeRemoteTrack := f.beforeRemoteTrack
	f.connMu.Unlock()
	if beforeRemoteTrack != nil {
		beforeRemoteTrack()
	}
	if !f.trackConn(remote) {
		return
	}
	defer f.untrackConn(remote)

	done := make(chan struct{}, 2)
	go func() {
		io.Copy(remote, conn)
		done <- struct{}{}
	}()
	go func() {
		io.Copy(conn, remote)
		done <- struct{}{}
	}()
	<-done
	// A half-closed HTTP or WebSocket peer can leave the opposite copy
	// blocked forever. Close both directions, then join the second copier.
	conn.Close()
	remote.Close()
	<-done
}

func (f *portForward) trackConn(conn net.Conn) bool {
	f.connMu.Lock()
	defer f.connMu.Unlock()
	if f.stopping || f.paused {
		conn.Close()
		return false
	}
	f.conns[conn] = struct{}{}
	return true
}

func (f *portForward) untrackConn(conn net.Conn) {
	f.connMu.Lock()
	delete(f.conns, conn)
	f.connMu.Unlock()
}

// stop closes the listener (unblocking Accept in serve) and waits for every
// in-flight relay goroutine to drain before returning, so Manager.Stop can
// guarantee zero leaked goroutines/listeners (see the goleak test).
func (f *portForward) stop() {
	f.connMu.Lock()
	if f.stopping {
		f.connMu.Unlock()
		return
	}
	f.stopping = true
	f.listener.Close()
	for conn := range f.conns {
		conn.Close()
	}
	f.connMu.Unlock()
	f.connWG.Wait()
}
