//go:build !windows

package sshclient

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/testutil/sockdir"
)

// The Forwarder tests below bind and dial unix-domain socket FILES on both
// ends and assert on the file's lifecycle (present after connect, removed on
// disconnect, rebound over a stale file). On native Windows the local end of a
// forward is a named pipe (forward_windows.go rejects anything else), so this
// behaviour does not exist there; the Windows transport has its own tests in
// windows_transport_test.go and forward_windows_compile_test.go.
// startEchoUnixServer stands in for "the remote daemon": a real unix socket
// listener that echoes back whatever it reads, prefixed so round-trip bytes
// are distinguishable per-connection in the concurrency test.
func startEchoUnixServer(t *testing.T, path string) (stop func()) {
	t.Helper()
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listening on echo unix socket %s: %v", path, err)
	}
	done := make(chan struct{})
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				close(done)
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 4096)
				for {
					n, err := c.Read(buf)
					if n > 0 {
						if _, werr := c.Write(buf[:n]); werr != nil {
							return
						}
					}
					if err != nil {
						return
					}
				}
			}(conn)
		}
	}()
	return func() {
		l.Close()
		<-done
	}
}

func TestForwarder_ProxiesBytesRoundTripThroughStreamLocalChannel(t *testing.T) {
	remoteSocketDir := sockdir.Short(t)
	remoteSocketPath := filepath.Join(remoteSocketDir, "daemon.sock")
	stopEcho := startEchoUnixServer(t, remoteSocketPath)
	defer stopEcho()

	fixture := newFixtureServer(t)
	fixture.streamLocalDial = func(socketPath string) (net.Conn, error) {
		return net.Dial("unix", socketPath)
	}
	stopFixture := fixture.serve(t)
	defer stopFixture()

	client := dialFixtureClient(t, fixture)

	localDir := sockdir.Short(t)
	localSocketPath := filepath.Join(localDir, "local.sock")
	fwd, err := NewForwarder(client, remoteSocketPath, localSocketPath)
	if err != nil {
		t.Fatalf("NewForwarder: %v", err)
	}
	go fwd.Serve()
	defer fwd.Close()

	conn, err := net.Dial("unix", localSocketPath)
	if err != nil {
		t.Fatalf("dialing local forward socket: %v", err)
	}
	defer conn.Close()

	msg := []byte("hello daemon\n")
	if _, err := conn.Write(msg); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, len(msg))
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := readFull(conn, buf); err != nil {
		t.Fatalf("read echoed bytes: %v", err)
	}
	if !bytes.Equal(buf, msg) {
		t.Fatalf("echoed bytes = %q, want %q", buf, msg)
	}
}

func TestForwarder_ConcurrentClientsGetIndependentStreams(t *testing.T) {
	remoteSocketDir := sockdir.Short(t)
	remoteSocketPath := filepath.Join(remoteSocketDir, "daemon.sock")
	stopEcho := startEchoUnixServer(t, remoteSocketPath)
	defer stopEcho()

	fixture := newFixtureServer(t)
	fixture.streamLocalDial = func(socketPath string) (net.Conn, error) {
		return net.Dial("unix", socketPath)
	}
	stopFixture := fixture.serve(t)
	defer stopFixture()

	client := dialFixtureClient(t, fixture)

	localDir := sockdir.Short(t)
	localSocketPath := filepath.Join(localDir, "local.sock")
	fwd, err := NewForwarder(client, remoteSocketPath, localSocketPath)
	if err != nil {
		t.Fatalf("NewForwarder: %v", err)
	}
	go fwd.Serve()
	defer fwd.Close()

	const n = 5
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			conn, err := net.Dial("unix", localSocketPath)
			if err != nil {
				errCh <- fmt.Errorf("client %d dial: %w", i, err)
				return
			}
			defer conn.Close()

			msg := []byte(fmt.Sprintf("client-%d-payload", i))
			if _, err := conn.Write(msg); err != nil {
				errCh <- fmt.Errorf("client %d write: %w", i, err)
				return
			}
			buf := make([]byte, len(msg))
			conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			if _, err := readFull(conn, buf); err != nil {
				errCh <- fmt.Errorf("client %d read: %w", i, err)
				return
			}
			if !bytes.Equal(buf, msg) {
				errCh <- fmt.Errorf("client %d echoed %q, want %q (cross-talk between channels)", i, buf, msg)
				return
			}
			errCh <- nil
		}(i)
	}
	for i := 0; i < n; i++ {
		if err := <-errCh; err != nil {
			t.Error(err)
		}
	}
}

func TestForwarder_DisconnectRemovesSocketFile_ReconnectRecreates(t *testing.T) {
	remoteSocketDir := sockdir.Short(t)
	remoteSocketPath := filepath.Join(remoteSocketDir, "daemon.sock")
	stopEcho := startEchoUnixServer(t, remoteSocketPath)
	defer stopEcho()

	fixture := newFixtureServer(t)
	fixture.streamLocalDial = func(socketPath string) (net.Conn, error) {
		return net.Dial("unix", socketPath)
	}
	stopFixture := fixture.serve(t)
	defer stopFixture()

	client := dialFixtureClient(t, fixture)

	localDir := sockdir.Short(t)
	localSocketPath := filepath.Join(localDir, "local.sock")

	fwd, err := NewForwarder(client, remoteSocketPath, localSocketPath)
	if err != nil {
		t.Fatalf("NewForwarder (first connect): %v", err)
	}
	go fwd.Serve()

	if _, err := os.Stat(localSocketPath); err != nil {
		t.Fatalf("expected local socket to exist after first connect: %v", err)
	}

	if err := fwd.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := os.Stat(localSocketPath); !os.IsNotExist(err) {
		t.Fatalf("expected local socket file removed after disconnect, stat err = %v", err)
	}

	// Reconnect: must not fail with "address already in use" against the
	// now-removed socket.
	client2 := dialFixtureClient(t, fixture)
	fwd2, err := NewForwarder(client2, remoteSocketPath, localSocketPath)
	if err != nil {
		t.Fatalf("NewForwarder (reconnect): %v", err)
	}
	defer fwd2.Close()
	go fwd2.Serve()

	conn, err := net.Dial("unix", localSocketPath)
	if err != nil {
		t.Fatalf("dialing local forward socket after reconnect: %v", err)
	}
	conn.Close()
}

func TestForwarder_PauseKeepsSocketBoundAndRejectsNewConnections(t *testing.T) {
	fixture := newFixtureServer(t)
	fixture.streamLocalDial = func(string) (net.Conn, error) {
		return nil, fmt.Errorf("must not dial while paused")
	}
	stopFixture := fixture.serve(t)
	defer stopFixture()

	client := dialFixtureClient(t, fixture)
	localSocketPath := filepath.Join(sockdir.Short(t), "local.sock")
	fwd, err := NewForwarder(client, "/remote.sock", localSocketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer fwd.Close()
	go fwd.Serve()

	paused := fwd.Paused()
	fwd.Pause()
	<-paused
	if _, err := os.Stat(localSocketPath); err != nil {
		t.Fatalf("Pause unbound local socket: %v", err)
	}
	conn, err := net.Dial("unix", localSocketPath)
	if err != nil {
		t.Fatalf("paused listener must remain bound: %v", err)
	}
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1)
	if _, err := conn.Read(buf); err == nil {
		t.Fatal("paused forward accepted a connection into the dead transport")
	} else if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
		t.Fatalf("paused forward timed out instead of failing immediately: %v", err)
	}
}

func TestForwarder_PauseBetweenLocalAndRemoteTrackDrainsRegistry(t *testing.T) {
	remoteSocketPath := filepath.Join(sockdir.Short(t), "daemon.sock")
	stopEcho := startEchoUnixServer(t, remoteSocketPath)
	defer stopEcho()
	fixture := newFixtureServer(t)
	fixture.streamLocalDial = func(path string) (net.Conn, error) { return net.Dial("unix", path) }
	stopFixture := fixture.serve(t)
	defer stopFixture()

	localPath := filepath.Join(sockdir.Short(t), "local.sock")
	fwd, err := NewForwarder(dialFixtureClient(t, fixture), remoteSocketPath, localPath)
	if err != nil {
		t.Fatal(err)
	}
	defer fwd.Close()
	entered := make(chan struct{})
	release := make(chan struct{})
	fwd.beforeRemoteTrack = func() { close(entered); <-release }
	go fwd.Serve()
	conn, err := net.Dial("unix", localPath)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	<-entered
	fwd.Pause()
	close(release)
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	_, readErr := conn.Read(make([]byte, 1))
	if readErr == nil {
		t.Fatal("connection survived Pause")
	}
	if netErr, ok := readErr.(net.Error); ok && netErr.Timeout() {
		t.Fatalf("connection timed out instead of being closed: %v", readErr)
	}
	deadline := time.Now().Add(time.Second)
	for {
		fwd.mu.RLock()
		remaining := len(fwd.conns)
		fwd.mu.RUnlock()
		if remaining == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("tracked connections did not drain: %d", remaining)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestForwarder_StaleSocketFileFromCrashedRun_IsRebound(t *testing.T) {
	remoteSocketDir := sockdir.Short(t)
	remoteSocketPath := filepath.Join(remoteSocketDir, "daemon.sock")
	stopEcho := startEchoUnixServer(t, remoteSocketPath)
	defer stopEcho()

	fixture := newFixtureServer(t)
	fixture.streamLocalDial = func(socketPath string) (net.Conn, error) {
		return net.Dial("unix", socketPath)
	}
	stopFixture := fixture.serve(t)
	defer stopFixture()

	localDir := sockdir.Short(t)
	localSocketPath := filepath.Join(localDir, "local.sock")

	// Simulate a crashed prior run: a listener bound, then killed without
	// unlinking its socket file — SetUnlinkOnClose(false) mirrors what
	// happens when a process dies without running its deferred cleanup:
	// the file is left behind but nothing is listening on it anymore.
	staleListener, err := net.Listen("unix", localSocketPath)
	if err != nil {
		t.Fatalf("creating stale socket file: %v", err)
	}
	staleListener.(*net.UnixListener).SetUnlinkOnClose(false)
	staleListener.Close()

	if _, err := os.Stat(localSocketPath); err != nil {
		t.Fatalf("expected stale socket file to exist: %v", err)
	}

	client := dialFixtureClient(t, fixture)
	fwd, err := NewForwarder(client, remoteSocketPath, localSocketPath)
	if err != nil {
		t.Fatalf("NewForwarder should rebind over a stale socket file, got error: %v", err)
	}
	defer fwd.Close()
	go fwd.Serve()

	conn, err := net.Dial("unix", localSocketPath)
	if err != nil {
		t.Fatalf("dialing rebound local forward socket: %v", err)
	}
	conn.Close()
}

func readFull(conn net.Conn, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := conn.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}
