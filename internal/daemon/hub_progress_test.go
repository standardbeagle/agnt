package daemon

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	hubclient "github.com/standardbeagle/go-cli-server/client"
	hubpkg "github.com/standardbeagle/go-cli-server/hub"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
	"github.com/stretchr/testify/require"
)

// hubStatusInterval is the hub-side STATUS tick used by these tests. It is a
// hub *input*, not an assertion: every client deadline in this file is sized as
// a multiple of it (or of an observed baseline), never the other way round.
const hubStatusInterval = 10 * time.Millisecond

// minStatusTicksPerDeadline is how many STATUS ticks a client's idle deadline
// must span, and clientIdleDeadline is the deadline that follows from it.
//
// These replace a hardcoded 25ms, which against a 10ms tick left every client
// deadline in this file only ~2 ticks wide: one scheduler stall between ticks
// — routine in the loaded serial suite — then read as "STATUS never arrived"
// and failed on a false premise rather than on a keepalive regression.
// Deriving the deadline from the tick keeps the margin honest at any absolute
// speed, and the tests still fail if the keepalive misses many consecutive
// ticks, which is the actual defect they exist to catch.
const (
	minStatusTicksPerDeadline = 8
	clientIdleDeadline        = minStatusTicksPerDeadline * hubStatusInterval
)

// measureRoundTripBaseline times a trivial, immediately-answered request
// against the live hub under whatever load the machine is currently carrying.
// It is the calibration input for any client idle deadline in this file: a
// hardcoded millisecond budget encodes an assumption about scheduler
// promptness that a loaded host simply outruns, whereas a multiple of an
// observed same-run round trip scales with the actual machine.
// See .claude/rules/testing-timing-assertion-flakes.md.
func measureRoundTripBaseline(t *testing.T, sock string) time.Duration {
	t.Helper()
	probe := hubclient.NewConn(hubclient.WithSocketPath(sock), hubclient.WithTimeout(30*time.Second))
	defer func() { _ = probe.Close() }()

	worst := time.Millisecond
	for i := 0; i < 5; i++ {
		start := time.Now()
		_, err := probe.Request(hubproto.VerbInfo).JSON()
		require.NoError(t, err, "baseline probe request failed")
		if d := time.Since(start); d > worst {
			worst = d
		}
	}
	return worst
}

// TestHubProgressKeepsSilentRequestAlive reproduces the production failure in
// which a healthy, long-running daemon handler emitted no bytes before the
// client's I/O deadline. STATUS frames are transport liveness, not the final
// response, so the request must remain pending until its JSON result arrives.
//
// Load-invariance: the client's idle deadline is derived from an observed
// same-run round-trip baseline rather than a fixed 25ms, and the handler's
// duration is then derived from that deadline (silentMultiple deadline windows
// wide). So the property under test is a *ratio* that holds at any absolute
// speed: the request must survive many consecutive idle-deadline windows during
// which the handler writes nothing at all. Nothing but a STATUS frame can
// refresh that deadline (client conn.go refreshes only on ResponseStatus), so a
// success here is positive evidence that STATUS frames were received — and the
// observed elapsed time is asserted against the observed deadline to prove the
// request really did outlive multiple windows rather than racing through one.
func TestHubProgressKeepsSilentRequestAlive(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "hub.sock")

	// silentMultiple: how many client idle-deadline windows the handler stays
	// completely silent for. >1 is what gives the test teeth — with STATUS
	// keepalive removed the request dies after the first window.
	const silentMultiple = 6

	var slowFor atomic.Int64 // handler sleep, set once the baseline is known

	h := hubpkg.New(hubpkg.Config{
		SocketPath:   sock,
		MaxClients:   4,
		WriteTimeout: time.Second,
		// Status ticks land many times inside one client deadline window, so
		// losing individual ticks to scheduler drift or a busy response writer
		// cannot starve the deadline refresh.
		StatusInterval: hubStatusInterval,
	})
	require.NoError(t, h.RegisterCommand(hubpkg.CommandDefinition{
		Verb: "SLOW",
		Handler: func(_ context.Context, conn *hubpkg.Connection, _ *hubproto.Command) error {
			time.Sleep(time.Duration(slowFor.Load()))
			return conn.WriteJSON([]byte(`{"result":"done"}`))
		},
	}))
	require.NoError(t, h.Start())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(t, h.Stop(ctx))
	})

	baseline := measureRoundTripBaseline(t, sock)
	// Idle deadline: generous against observed load, but still far shorter than
	// the handler's silence, which is what the assertion turns on.
	//
	// Floored at clientIdleDeadline so a baseline measured while the box was
	// momentarily quiet still leaves many ticks of margin.
	idleDeadline := 8 * baseline
	if idleDeadline < clientIdleDeadline {
		idleDeadline = clientIdleDeadline
	}
	silence := silentMultiple * idleDeadline
	slowFor.Store(int64(silence))
	require.GreaterOrEqual(t, idleDeadline, clientIdleDeadline,
		"idle deadline must span many STATUS ticks or the test is calibrating against tick jitter")

	c := hubclient.NewConn(hubclient.WithSocketPath(sock), hubclient.WithTimeout(idleDeadline))
	t.Cleanup(func() { _ = c.Close() })

	start := time.Now()
	got, err := c.Request("SLOW").JSON()
	elapsed := time.Since(start)
	require.NoError(t, err)
	require.Equal(t, "done", got["result"])

	// Judge from what actually happened, not from the nominal schedule: the
	// result arrived only after the connection sat silent across several of its
	// own idle-deadline windows. Without STATUS refreshing that deadline the
	// read above would have failed instead of reaching this line.
	require.Greater(t, elapsed, 2*idleDeadline,
		"request completed inside ~one idle-deadline window: the STATUS keepalive path was never exercised (baseline=%s, deadline=%s, silence=%s)",
		baseline, idleDeadline, silence)
}

// TestHubProgressDoesNotBlockOtherClients pins the daemon-wide responsiveness
// invariant: one long request may occupy its own connection, but STATUS writes
// and handler execution must not delay an independent control-plane request.
func TestHubProgressDoesNotBlockOtherClients(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "hub.sock")
	release := make(chan struct{})
	started := make(chan struct{})
	var once sync.Once
	h := hubpkg.New(hubpkg.Config{
		SocketPath:     sock,
		MaxClients:     4,
		WriteTimeout:   time.Second,
		StatusInterval: hubStatusInterval,
	})
	require.NoError(t, h.RegisterCommand(hubpkg.CommandDefinition{
		Verb: "BLOCKED",
		Handler: func(ctx context.Context, conn *hubpkg.Connection, _ *hubproto.Command) error {
			once.Do(func() { close(started) })
			select {
			case <-release:
				return conn.WriteOK("released")
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}))
	require.NoError(t, h.Start())
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(t, h.Stop(ctx))
	})

	slow := hubclient.NewConn(hubclient.WithSocketPath(sock), hubclient.WithTimeout(clientIdleDeadline))
	fast := hubclient.NewConn(hubclient.WithSocketPath(sock), hubclient.WithTimeout(250*time.Millisecond))
	t.Cleanup(func() { _ = slow.Close() })
	t.Cleanup(func() { _ = fast.Close() })

	slowDone := make(chan error, 1)
	go func() {
		err := slow.Request("BLOCKED").OK()
		slowDone <- err
	}()
	<-started

	_, err := fast.Request(hubproto.VerbInfo).JSON()
	require.NoError(t, err)

	close(release)
	require.NoError(t, <-slowDone)
}

// TestHubProgressDoesNotContaminateChunkedPayload ensures STATUS remains
// out-of-band even when it is interleaved with application CHUNK frames.
func TestHubProgressDoesNotContaminateChunkedPayload(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "hub.sock")
	h := hubpkg.New(hubpkg.Config{
		SocketPath:     sock,
		MaxClients:     4,
		WriteTimeout:   time.Second,
		StatusInterval: hubStatusInterval,
	})
	require.NoError(t, h.RegisterCommand(hubpkg.CommandDefinition{
		Verb: "STREAM",
		Handler: func(_ context.Context, conn *hubpkg.Connection, _ *hubproto.Command) error {
			if err := conn.WriteChunk([]byte("a")); err != nil {
				return err
			}
			// Must exceed the client's idle deadline, or the gap never crosses
			// a deadline window and STATUS is not exercised at all. Scaled with
			// the deadline rather than hardcoded so widening one cannot
			// silently defang the other.
			time.Sleep(2 * clientIdleDeadline)
			if err := conn.WriteChunk([]byte("b")); err != nil {
				return err
			}
			return conn.WriteEnd()
		},
	}))
	require.NoError(t, h.Start())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(t, h.Stop(ctx))
	})

	c := hubclient.NewConn(hubclient.WithSocketPath(sock), hubclient.WithTimeout(clientIdleDeadline))
	t.Cleanup(func() { _ = c.Close() })

	got, err := c.Request("STREAM").Chunked()
	require.NoError(t, err)
	require.Equal(t, []byte("ab"), got)
}
